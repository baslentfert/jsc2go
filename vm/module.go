package vm

import (
	"slices"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/bytecompiler"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// ECMAScript modules: Source Text Module Records with the Cyclic Module
// Record algorithms of ES 16.2.1.5 (Link, Evaluate with top-level await),
// synthetic JSON modules, module namespace objects and import(). JSC splits
// this between JSModuleLoader.js, AbstractModuleRecord, CyclicModuleRecord
// and JSModuleRecord; here it follows the spec text directly. Loading is
// synchronous through HostHooks.LoadModule.

type moduleStatus uint8

const (
	moduleUnlinked moduleStatus = iota
	moduleLinking
	moduleLinked
	moduleEvaluating
	moduleEvaluatingAsync
	moduleEvaluated
)

// Values of [[AsyncEvaluationOrder]] besides positive integers.
const (
	asyncOrderUnset = 0
	asyncOrderDone  = -1
)

type moduleKey struct{ name, typ string }

// resolvedBinding is a ResolvedBinding Record; a nil module with ambiguous
// set is AMBIGUOUS, a nil module otherwise is null. name is
// ident.StarNamespacePrivateName for NAMESPACE.
type resolvedBinding struct {
	module    *moduleRecord
	name      ident.Identifier
	ambiguous bool
}

type moduleRecord struct {
	realm *Realm
	name  string // the canonical name the host resolved the module to
	typ   string

	code   *bytecode.UnlinkedCodeBlock
	source parser.SourceCode
	info   *parser.ModuleInfo

	// synthetic is set for JSON modules: a single "default" export.
	synthetic bool
	json      Value

	loaded map[parser.ModuleRequest]*moduleRecord

	status           moduleStatus
	evaluationError  *Exception
	env              *Scope
	namespace        *Object
	importBindings   map[ident.Identifier]resolvedBinding
	dfsIndex         int
	dfsAncestorIndex int
	cycleRoot        *moduleRecord

	hasTLA                   bool
	asyncEvaluationOrder     int64
	topLevelCapability       *Object
	asyncParentModules       []*moduleRecord
	pendingAsyncDependencies int

	// gen holds the generatorified body's state and frame fields.
	gen *Object
}

func (m *moduleRecord) hasImportBinding(name ident.Identifier) bool {
	_, ok := m.importBindings[name]
	return ok
}

func (m *moduleRecord) getImportBinding(vm *VM, name ident.Identifier) (Value, bool) {
	b, ok := m.importBindings[name]
	if !ok {
		return Undefined, false
	}
	return vm.bindingValue(b, name), true
}

// bindingValue reads a resolved binding (GetBindingValue on the target
// module's environment, which throws in the TDZ).
func (vm *VM) bindingValue(b resolvedBinding, accessedAs ident.Identifier) Value {
	m := b.module
	if b.name == ident.StarNamespacePrivateName {
		return ObjectValue(vm.getModuleNamespace(m))
	}
	if m.synthetic {
		return m.json
	}
	if m.env == nil {
		vm.throwReferenceError("Cannot access '%s' before initialization.", accessedAs.Description())
	}
	slot, _, ok := m.env.slotFor(b.name)
	if !ok {
		vm.throwReferenceError("Cannot access '%s' before initialization.", accessedAs.Description())
	}
	if slot.IsEmpty() {
		vm.throwTDZError(accessedAs)
	}
	return *slot
}

// --- Loading -----------------------------------------------------------------------------

// hostLoadImportedModule is HostLoadImportedModule, done synchronously.
func (vm *VM) hostLoadImportedModule(r *Realm, referrer string, req parser.ModuleRequest) *moduleRecord {
	load := vm.HostHooks.LoadModule
	if load == nil {
		vm.throwTypeError("Cannot load module '%s': modules are not supported by this host", req.Specifier)
	}
	src, name, err := load(req.Specifier, referrer)
	if err != nil {
		vm.throwTypeError("Cannot load module '%s': %s", req.Specifier, err.Error())
	}
	key := moduleKey{name, req.Type}
	if m, ok := r.modules[key]; ok {
		return m
	}
	m := vm.parseModule(r, src, name, req.Type)
	if r.modules == nil {
		r.modules = make(map[moduleKey]*moduleRecord)
	}
	r.modules[key] = m
	return m
}

// parseModule is ParseModule (or ParseJSONModule for type "json").
func (vm *VM) parseModule(r *Realm, src, name, typ string) *moduleRecord {
	m := &moduleRecord{realm: r, name: name, typ: typ, loaded: map[parser.ModuleRequest]*moduleRecord{}}
	if typ == "json" || typ == "text" {
		saved := vm.realm
		vm.realm = r
		defer func() { vm.realm = saved }()
		if typ == "json" {
			m.json = vm.jsonParse(NewStringFromGo(src))
		} else {
			m.json = StringValue(NewStringFromGo(src))
		}
		m.synthetic = true
		m.status = moduleEvaluated
		m.cycleRoot = m
		return m
	}
	source := parser.NewSourceCode(parser.NewSourceProvider(src, name, parser.SourceTypeModule))
	source.Provider.Origin = m
	code, info, err := bytecompiler.CompileModuleWithInfo(source, 0)
	if err != nil {
		kind := ErrorKindSyntaxError
		if aerr, ok := err.(*parser.ModuleAnalysisError); ok && aerr.IsTypeError {
			kind = ErrorKindTypeError
		}
		msg := err.Error()
		if perr, ok := err.(*parser.ParserError); ok {
			msg = perr.Message
		}
		panic(&Exception{Value: ObjectValue(r.newError(kind, msg))})
	}
	m.code, m.source, m.info, m.hasTLA = code, source, info, info.HasTLA
	return m
}

// loadRequestedModules is LoadRequestedModules, depth first.
func (vm *VM) loadRequestedModules(m *moduleRecord, visited map[*moduleRecord]bool) {
	if visited[m] || m.synthetic {
		return
	}
	visited[m] = true
	for _, req := range m.info.RequestedModules {
		dep := m.loaded[req]
		if dep == nil {
			dep = vm.hostLoadImportedModule(m.realm, m.name, req)
			m.loaded[req] = dep
		}
		vm.loadRequestedModules(dep, visited)
	}
}

func (m *moduleRecord) importedModule(req parser.ModuleRequest) *moduleRecord { return m.loaded[req] }

// --- Exports -----------------------------------------------------------------------------

type resolveSetEntry struct {
	module *moduleRecord
	name   ident.Identifier
}

// resolveExport is ResolveExport.
func (m *moduleRecord) resolveExport(name ident.Identifier, resolveSet []resolveSetEntry) (resolvedBinding, []resolveSetEntry) {
	if m.synthetic {
		if name == ident.Default {
			return resolvedBinding{module: m, name: name}, resolveSet
		}
		return resolvedBinding{}, resolveSet
	}
	for _, r := range resolveSet {
		if r.module == m && r.name == name {
			return resolvedBinding{}, resolveSet // a circular import request
		}
	}
	resolveSet = append(resolveSet, resolveSetEntry{m, name})
	for _, e := range m.info.LocalExports {
		if e.ExportName == name {
			return resolvedBinding{module: m, name: e.LocalName}, resolveSet
		}
	}
	for _, e := range m.info.IndirectExports {
		if e.ExportName == name {
			imported := m.importedModule(e.Request)
			if e.ImportName == ident.StarNamespacePrivateName {
				return resolvedBinding{module: imported, name: ident.StarNamespacePrivateName}, resolveSet
			}
			return imported.resolveExport(e.ImportName, resolveSet)
		}
	}
	if name == ident.Default {
		return resolvedBinding{}, resolveSet
	}
	var star resolvedBinding
	for _, e := range m.info.StarExports {
		imported := m.importedModule(e.Request)
		var res resolvedBinding
		res, resolveSet = imported.resolveExport(name, resolveSet)
		if res.ambiguous {
			return res, resolveSet
		}
		if res.module == nil {
			continue
		}
		if star.module == nil {
			star = res
		} else if res.module != star.module || res.name != star.name {
			return resolvedBinding{ambiguous: true}, resolveSet
		}
	}
	return star, resolveSet
}

func (m *moduleRecord) resolve(name ident.Identifier) resolvedBinding {
	b, _ := m.resolveExport(name, nil)
	return b
}

// exportedNames is GetExportedNames.
func (m *moduleRecord) exportedNames(exportStarSet []*moduleRecord) ([]ident.Identifier, []*moduleRecord) {
	if m.synthetic {
		return []ident.Identifier{ident.Default}, exportStarSet
	}
	if slices.Contains(exportStarSet, m) {
		return nil, exportStarSet
	}
	exportStarSet = append(exportStarSet, m)
	var names []ident.Identifier
	for _, e := range m.info.LocalExports {
		names = append(names, e.ExportName)
	}
	for _, e := range m.info.IndirectExports {
		names = append(names, e.ExportName)
	}
	for _, e := range m.info.StarExports {
		var starNames []ident.Identifier
		starNames, exportStarSet = m.importedModule(e.Request).exportedNames(exportStarSet)
		for _, n := range starNames {
			if n != ident.Default && !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	return names, exportStarSet
}

// --- Linking -----------------------------------------------------------------------------

// link is Link().
func (vm *VM) link(m *moduleRecord) {
	var stack []*moduleRecord
	exc := vm.Try(func() { vm.innerModuleLinking(m, &stack, 0) })
	if exc != nil {
		for _, s := range stack {
			s.status = moduleUnlinked
			s.env = nil
			s.importBindings = nil
		}
		panic(exc)
	}
}

func (vm *VM) innerModuleLinking(m *moduleRecord, stack *[]*moduleRecord, index int) int {
	if m.status != moduleUnlinked {
		return index
	}
	m.status = moduleLinking
	m.dfsIndex, m.dfsAncestorIndex = index, index
	index++
	*stack = append(*stack, m)
	for _, req := range m.info.RequestedModules {
		required := m.importedModule(req)
		index = vm.innerModuleLinking(required, stack, index)
		if required.status == moduleLinking {
			m.dfsAncestorIndex = min(m.dfsAncestorIndex, required.dfsAncestorIndex)
		}
	}
	vm.initializeEnvironment(m)
	if m.dfsAncestorIndex == m.dfsIndex {
		for {
			required := (*stack)[len(*stack)-1]
			*stack = (*stack)[:len(*stack)-1]
			required.status = moduleLinked
			if required == m {
				break
			}
		}
	}
	return index
}

// initializeEnvironment is InitializeEnvironment (CyclicModuleRecord.cpp).
func (vm *VM) initializeEnvironment(m *moduleRecord) {
	r := m.realm
	saved := vm.realm
	vm.realm = r
	defer func() { vm.realm = saved }()

	for _, e := range m.info.IndirectExports {
		res := m.resolve(e.ExportName)
		switch {
		case res.ambiguous:
			vm.throwSyntaxError("Indirectly exported binding name '%s' cannot be resolved due to ambiguous multiple bindings.", e.ExportName.Description())
		case res.module == nil:
			vm.throwSyntaxError("Indirectly exported binding name '%s' is not found.", e.ExportName.Description())
		}
	}

	cb := r.link(m.code, m.source)
	table := cb.constants[m.code.ModuleEnvironmentSymbolTableConstantRegisterOffset-bytecode.FirstConstantRegisterIndex].cell().(*bytecode.SymbolTable)
	env := newLexicalScope(r.globalLexical, table, Empty)
	env.kind = scopeModule
	env.module = m
	m.env = env
	m.importBindings = map[ident.Identifier]resolvedBinding{}

	for _, in := range m.info.ImportEntries {
		imported := m.importedModule(in.Request)
		if in.IsNamespace() {
			if slot, _, ok := env.slotFor(in.LocalName); ok {
				*slot = ObjectValue(vm.getModuleNamespace(imported))
			}
			continue
		}
		res := imported.resolve(in.ImportName)
		switch {
		case res.ambiguous:
			vm.throwSyntaxError("Importing binding name '%s' cannot be resolved due to ambiguous multiple bindings.", in.ImportName.Description())
		case res.module == nil:
			vm.throwSyntaxError("Importing binding name '%s' is not found.", in.ImportName.Description())
		}
		if res.name == ident.StarNamespacePrivateName {
			vm.getModuleNamespace(res.module) // materialize it now
		}
		m.importBindings[in.LocalName] = res
	}

	for name := range m.code.VarDeclarations.All {
		if slot, _, ok := env.slotFor(name); ok {
			*slot = Undefined
		}
	}
	for _, exec := range m.code.FunctionDecls {
		if slot, _, ok := env.slotFor(exec.Name()); ok {
			f := r.newJSFunction(exec, env)
			f.fn.(*jsFunction).insideOrdinary = !exec.IsArrowFunction()
			*slot = ObjectValue(f)
		}
	}
	if slot, _, ok := env.slotFor(ident.MetaPrivateName); ok {
		*slot = ObjectValue(r.newObject(nil))
	}
}

// --- Evaluation --------------------------------------------------------------------------

// evaluate is Evaluate(); it returns the top-level promise.
func (vm *VM) evaluate(m *moduleRecord) *Object {
	if m.status == moduleEvaluatingAsync || m.status == moduleEvaluated {
		if m.cycleRoot != nil {
			m = m.cycleRoot
		}
		if m.topLevelCapability != nil {
			return m.topLevelCapability
		}
	}
	var stack []*moduleRecord
	promise := vm.realm.newPromise()
	m.topLevelCapability = promise
	exc := vm.Try(func() { vm.innerModuleEvaluation(m, &stack, 0) })
	if exc != nil {
		for _, s := range stack {
			s.status = moduleEvaluated
			s.evaluationError = exc
		}
		vm.rejectPromise(promise, exc.Value)
		return promise
	}
	if m.asyncEvaluationOrder == asyncOrderUnset {
		vm.resolvePromise(promise, Undefined)
	}
	return promise
}

func (vm *VM) innerModuleEvaluation(m *moduleRecord, stack *[]*moduleRecord, index int) int {
	switch m.status {
	case moduleEvaluatingAsync, moduleEvaluated:
		if m.evaluationError != nil {
			panic(m.evaluationError)
		}
		return index
	case moduleEvaluating:
		return index
	}
	m.status = moduleEvaluating
	m.dfsIndex, m.dfsAncestorIndex = index, index
	m.pendingAsyncDependencies = 0
	index++
	*stack = append(*stack, m)
	for _, req := range m.info.RequestedModules {
		required := m.importedModule(req)
		index = vm.innerModuleEvaluation(required, stack, index)
		if required.status == moduleEvaluating {
			m.dfsAncestorIndex = min(m.dfsAncestorIndex, required.dfsAncestorIndex)
		} else {
			required = required.cycleRoot
			if required.evaluationError != nil {
				panic(required.evaluationError)
			}
		}
		if required.asyncEvaluationOrder > 0 {
			m.pendingAsyncDependencies++
			required.asyncParentModules = append(required.asyncParentModules, m)
		}
	}
	if m.pendingAsyncDependencies > 0 || m.hasTLA {
		vm.asyncEvaluationCount++
		m.asyncEvaluationOrder = vm.asyncEvaluationCount
		if m.pendingAsyncDependencies == 0 {
			vm.executeAsyncModule(m)
		}
	} else {
		vm.executeModule(m, nil)
	}
	if m.dfsAncestorIndex == m.dfsIndex {
		for {
			required := (*stack)[len(*stack)-1]
			*stack = (*stack)[:len(*stack)-1]
			if required.asyncEvaluationOrder == asyncOrderUnset {
				required.status = moduleEvaluated
			} else {
				required.status = moduleEvaluatingAsync
			}
			required.cycleRoot = m
			if required == m {
				break
			}
		}
	}
	return index
}

// executeAsyncModule is ExecuteAsyncModule.
func (vm *VM) executeAsyncModule(m *moduleRecord) {
	promise := m.realm.newPromise()
	vm.promiseThenNative(promise, func(v Value, rejected bool) {
		if rejected {
			vm.asyncModuleExecutionRejected(m, v)
		} else {
			vm.asyncModuleExecutionFulfilled(m)
		}
	})
	vm.executeModule(m, promise)
}

func (vm *VM) gatherAvailableAncestors(m *moduleRecord, execList *[]*moduleRecord) {
	for _, p := range m.asyncParentModules {
		if slices.Contains(*execList, p) || p.cycleRoot.evaluationError != nil {
			continue
		}
		p.pendingAsyncDependencies--
		if p.pendingAsyncDependencies == 0 {
			*execList = append(*execList, p)
			if !p.hasTLA {
				vm.gatherAvailableAncestors(p, execList)
			}
		}
	}
}

func (vm *VM) asyncModuleExecutionFulfilled(m *moduleRecord) {
	if m.status == moduleEvaluated {
		return
	}
	m.asyncEvaluationOrder = asyncOrderDone
	m.status = moduleEvaluated
	if m.topLevelCapability != nil {
		vm.resolvePromise(m.topLevelCapability, Undefined)
	}
	var execList []*moduleRecord
	vm.gatherAvailableAncestors(m, &execList)
	slices.SortFunc(execList, func(a, b *moduleRecord) int {
		return int(a.asyncEvaluationOrder - b.asyncEvaluationOrder)
	})
	for _, p := range execList {
		switch {
		case p.status == moduleEvaluated:
		case p.hasTLA:
			vm.executeAsyncModule(p)
		default:
			if exc := vm.Try(func() { vm.executeModule(p, nil) }); exc != nil {
				vm.asyncModuleExecutionRejected(p, exc.Value)
				continue
			}
			p.asyncEvaluationOrder = asyncOrderDone
			p.status = moduleEvaluated
			if p.topLevelCapability != nil {
				vm.resolvePromise(p.topLevelCapability, Undefined)
			}
		}
	}
}

func (vm *VM) asyncModuleExecutionRejected(m *moduleRecord, err Value) {
	if m.status == moduleEvaluated {
		return
	}
	m.evaluationError = &Exception{Value: err}
	m.status = moduleEvaluated
	m.asyncEvaluationOrder = asyncOrderDone
	if m.topLevelCapability != nil {
		vm.rejectPromise(m.topLevelCapability, err)
	}
	for _, p := range m.asyncParentModules {
		vm.asyncModuleExecutionRejected(p, err)
	}
}

// executeModule is ExecuteModule: synchronously, or for a module with
// top-level await as an async block that settles capability.
func (vm *VM) executeModule(m *moduleRecord, capability *Object) {
	if m.gen == nil {
		m.gen = &Object{realm: m.realm, extra: &objectExtra{fields: makeFields(2)}}
		m.gen.extra.fields[0] = Int(genStateInit)
	}
	if capability == nil {
		vm.runModuleBody(m, Undefined, resumeNormal)
		return
	}
	vm.moduleBodyStep(m, Undefined, resumeNormal, capability)
}

// moduleBodyStep runs the body until it awaits or completes
// (asyncModuleResolveEvaluation in JSMicrotask.cpp).
func (vm *VM) moduleBodyStep(m *moduleRecord, sent Value, mode int32, capability *Object) {
	var result Value
	if exc := vm.Try(func() { result = vm.runModuleBody(m, sent, mode) }); exc != nil {
		vm.rejectPromise(capability, exc.Value)
		return
	}
	if state := m.gen.extra.fields[0]; !state.IsNumber() || int32(state.Float()) == genStateExecuting {
		vm.resolvePromise(capability, Undefined)
		return
	}
	vm.await(result, func(v Value, rejected bool) {
		mode := int32(resumeNormal)
		if rejected {
			mode = resumeThrow
		}
		vm.moduleBodyStep(m, v, mode, capability)
	})
}

// runModuleBody is executeModuleProgram: the body is called with
// (record, state, sentValue, resumeMode, environment) and |this| undefined.
func (vm *VM) runModuleBody(m *moduleRecord, sent Value, mode int32) Value {
	vm.enterCall()
	defer vm.leaveCall()
	saved := vm.realm
	vm.realm = m.realm
	defer func() { vm.realm = saved }()
	cb := m.realm.link(m.code, m.source)
	state := m.gen.extra.fields[0]
	m.gen.extra.fields[0] = Int(genStateExecuting)
	args := []Value{ObjectValue(m.gen), state, sent, Int(int(mode)), cellValue(m.env)}
	return vm.execute(vm.newFrame(cb, nil, m.env, Undefined, args))
}

// --- Namespace objects --------------------------------------------------------------------

// moduleNamespace is a Module Namespace Exotic Object.
type moduleNamespace struct {
	module   *moduleRecord
	exports  []PropertyKey // sorted by code unit order
	bindings map[PropertyKey]resolvedBinding
}

// getModuleNamespace is GetModuleNamespace.
func (vm *VM) getModuleNamespace(m *moduleRecord) *Object {
	if m.namespace != nil {
		return m.namespace
	}
	names, _ := m.exportedNames(nil)
	ns := &moduleNamespace{module: m, bindings: map[PropertyKey]resolvedBinding{}}
	type named struct {
		key PropertyKey
		s   *String
	}
	var unambiguous []named
	for _, name := range names {
		res := m.resolve(name)
		if res.module == nil {
			continue
		}
		k := keyForIdentifier(name)
		ns.bindings[k] = res
		unambiguous = append(unambiguous, named{k, NewString(name.String())})
	}
	slices.SortFunc(unambiguous, func(a, b named) int { return a.s.Compare(b.s) })
	for _, u := range unambiguous {
		ns.exports = append(ns.exports, u.key)
	}
	o := &Object{realm: m.realm, class: ClassModuleNamespace, exotic: ns, props: propertyMap{shape: vm.rootShape}}
	o.putDirect(SymKey(SymbolToStringTag), Str("Module"), 0)
	m.namespace = o
	return o
}

func (ns *moduleNamespace) value(o *Object, k PropertyKey) Value {
	return o.vm().bindingValue(ns.bindings[k], k.Ident())
}

func (ns *moduleNamespace) getPrototypeOf(o *Object) *Object         { return nil }
func (ns *moduleNamespace) setPrototypeOf(o *Object, p *Object) bool { return p == nil }
func (ns *moduleNamespace) isExtensible(o *Object) bool              { return false }
func (ns *moduleNamespace) preventExtensions(o *Object) bool         { return true }
func (ns *moduleNamespace) hasExport(k PropertyKey) bool             { _, ok := ns.bindings[k]; return ok }
func (ns *moduleNamespace) hasProperty(o *Object, k PropertyKey) bool {
	if k.IsSymbol() {
		return o.ordinaryHasProperty(k)
	}
	return ns.hasExport(k)
}

func (ns *moduleNamespace) getOwnProperty(o *Object, k PropertyKey) (Property, bool) {
	if k.IsSymbol() {
		return o.ordinaryGetOwnProperty(k)
	}
	if !ns.hasExport(k) {
		return Property{}, false
	}
	return dataProperty(ns.value(o, k), Writable|Enumerable), true
}

func (ns *moduleNamespace) defineOwnProperty(o *Object, k PropertyKey, d PropertyDescriptor) bool {
	if k.IsSymbol() {
		return o.ordinaryDefineOwnProperty(k, d)
	}
	current, ok := ns.getOwnProperty(o, k)
	if !ok {
		return false
	}
	if d.Has&HasConfigurable != 0 && d.Configurable ||
		d.Has&HasEnumerable != 0 && !d.Enumerable ||
		d.IsAccessorDescriptor() ||
		d.Has&HasWritable != 0 && !d.Writable {
		return false
	}
	if d.Has&HasValue != 0 {
		return SameValue(d.Value, current.Value)
	}
	return true
}

func (ns *moduleNamespace) get(o *Object, k PropertyKey, receiver Value) Value {
	if k.IsSymbol() {
		return o.ordinaryGet(k, receiver)
	}
	if !ns.hasExport(k) {
		return Undefined
	}
	return ns.value(o, k)
}

func (ns *moduleNamespace) set(o *Object, k PropertyKey, v Value, receiver Value) bool { return false }

func (ns *moduleNamespace) delete(o *Object, k PropertyKey) bool {
	if k.IsSymbol() {
		return o.ordinaryDelete(k)
	}
	return !ns.hasExport(k)
}

func (ns *moduleNamespace) ownKeys(o *Object) []PropertyKey {
	keys := slices.Clone(ns.exports)
	for _, k := range o.ordinaryOwnKeys() {
		if k.IsSymbol() {
			keys = append(keys, k)
		}
	}
	return keys
}

// --- Entry points -------------------------------------------------------------------------

// activeOrigin returns what identifies the running script or module: a
// *moduleRecord or a script URL (string), or nil.
func (vm *VM) activeOrigin() any {
	for f := vm.currentFrame; f != nil; f = f.caller {
		if f.cb == nil || f.cb.source.Provider == nil {
			continue
		}
		p := f.cb.source.Provider
		if p.Origin != nil {
			return p.Origin
		}
		if p.URL != "" && p.URL != "eval" {
			return p.URL
		}
	}
	return nil
}

// activeScriptOrModule returns the name of the running script or module
// (the referrer of import()).
func (vm *VM) activeScriptOrModule() string {
	switch o := vm.activeOrigin().(type) {
	case *moduleRecord:
		return o.name
	case string:
		return o
	}
	return ""
}

// importModuleDynamically is EvaluateImportCall / ContinueDynamicImport.
func (vm *VM) importModuleDynamically(specifier, options, deferred Value) Value {
	realm := vm.realm
	promise := realm.newPromise()
	referrer := vm.activeScriptOrModule()
	var req parser.ModuleRequest
	exc := vm.Try(func() {
		req.Specifier = vm.toGoString(specifier)
		if options.IsUndefined() {
			return
		}
		if !options.IsObject() {
			vm.throwTypeError("The second argument of import() must be an object")
		}
		with := options.ObjectRef().GetStr("with")
		if with.IsUndefined() {
			return
		}
		wo := with.AsObject()
		if wo == nil {
			vm.throwTypeError("The 'with' option of import() must be an object")
		}
		var attrs []parser.ImportAttribute
		for _, entry := range vm.enumerableOwnProperties(wo, enumEntries) {
			pair := entry.ObjectRef()
			k, v := pair.getIdx(0), pair.getIdx(1)
			if !v.IsString() {
				vm.throwTypeError("Import attribute values must be strings")
			}
			attrs = append(attrs, parser.ImportAttribute{Key: ident.FromString(vm.toGoString(k)), Value: ident.FromString(vm.toGoString(v))})
		}
		var aerr *parser.ModuleAnalysisError
		if req, aerr = parser.ImportAttributesRequest(req.Specifier, attrs); aerr != nil {
			if aerr.IsTypeError {
				vm.throwTypeError("%s", aerr.Message)
			}
			vm.throwSyntaxError("%s", aerr.Message)
		}
	})
	if exc != nil {
		vm.rejectPromise(promise, exc.Value)
		return ObjectValue(promise)
	}
	vm.continueDynamicImport(realm, referrer, req, promise)
	return ObjectValue(promise)
}

// continueDynamicImport loads, links and evaluates the requested module in a
// job (the host loads asynchronously) and settles promise with its namespace.
func (vm *VM) continueDynamicImport(realm *Realm, referrer string, req parser.ModuleRequest, promise *Object) {
	vm.EnqueueJob(func() {
		saved := vm.realm
		vm.realm = realm
		defer func() { vm.realm = saved }()
		var m *moduleRecord
		var evaluated *Object
		if exc := vm.Try(func() {
			m = vm.hostLoadImportedModule(realm, referrer, req)
			vm.loadRequestedModules(m, map[*moduleRecord]bool{})
			if !m.synthetic {
				vm.link(m)
			}
			evaluated = vm.evaluate(m)
		}); exc != nil {
			vm.rejectPromise(promise, exc.Value)
			return
		}
		vm.promiseThenNative(evaluated, func(v Value, rejected bool) {
			if rejected {
				vm.rejectPromise(promise, v)
				return
			}
			vm.resolvePromise(promise, ObjectValue(vm.getModuleNamespace(m)))
		})
	})
}

// RunModule loads, links and evaluates the module source named name (its
// imports are loaded through HostHooks.LoadModule). It returns the
// evaluation promise; run jobs to settle it.
func (vm *VM) RunModule(source, name string) (promise *Object, err error) {
	r := vm.realm
	exc := vm.Try(func() {
		m := vm.parseModule(r, source, name, "")
		if r.modules == nil {
			r.modules = make(map[moduleKey]*moduleRecord)
		}
		r.modules[moduleKey{name, ""}] = m
		vm.loadRequestedModules(m, map[*moduleRecord]bool{})
		vm.link(m)
		promise = vm.evaluate(m)
	})
	if exc != nil {
		return nil, exc
	}
	return promise, nil
}

// PromiseResult reports the state of a promise: settled, and if so whether
// it was rejected and with which value.
func PromiseResult(p *Object) (settled, rejected bool, v Value) {
	d := promiseOf(p)
	switch d.state {
	case promiseFulfilled:
		return true, false, d.result
	case promiseRejected:
		d.isHandled = true
		return true, true, d.result
	}
	return false, false, Undefined
}

// jsonParse is ParseJSON for JSON modules.
func (vm *VM) jsonParse(text *String) Value {
	v, _ := vm.parseJSON(vm.realm, text, false, true)
	return v
}
