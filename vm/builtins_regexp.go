package vm

import (
	"fmt"
	"strings"
	"unsafe"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/yarr"
)

// RegExp objects (ES 22.2, RegExpConstructor/RegExpPrototype/RegExpObject),
// matching with package yarr. The legacy static properties (RegExp.$1 etc.)
// follow the proposal-regexp-legacy-features semantics.

// regExpData holds [[OriginalSource]], [[OriginalFlags]] and [[RegExpMatcher]].
type regExpData struct {
	source  *String
	flags   string
	bits    yarr.Flags
	pattern *yarr.Pattern
	// legacy is [[LegacyFeaturesEnabled]].
	legacy bool
}

// regExpStatics are a realm's legacy RegExp static properties: the input
// and capture offsets of the last successful match.
type regExpStatics struct {
	input   *String
	caps    []int
	invalid bool
}

func regExpDataOf(v Value) *regExpData {
	if o := v.AsObject(); o != nil {
		d, _ := o.internal.(*regExpData)
		return d
	}
	return nil
}

var lastIndexKey = StrKey("lastIndex")

// compilePattern compiles (or fetches from the VM's cache) a pattern.
func (vm *VM) compilePattern(source *String, flags yarr.Flags) *yarr.Pattern {
	key := flags.String() + "/" + source.String()
	if p, ok := vm.regexpCache[key]; ok {
		return p
	}
	p, err := yarr.CompilePattern(source.UTF16(), flags)
	if err != yarr.NoError {
		vm.throwSyntaxError("%s", err.Error())
	}
	if vm.regexpCache == nil || len(vm.regexpCache) > 4096 {
		vm.regexpCache = make(map[string]*yarr.Pattern)
	}
	vm.regexpCache[key] = p
	return p
}

// regExpAlloc is RegExpAlloc.
func (vm *VM) regExpAlloc(newTarget *Object) *Object {
	proto := vm.GetPrototypeFromConstructor(newTarget, func(r *Realm) *Object { return r.RegExpPrototype })
	b := vm.realm.newRegExpBlock()
	b.d = regExpData{source: emptyString, legacy: newTarget == vm.realm.RegExpConstructor}
	b.o.proto = proto
	o := &b.o
	o.putDirect(lastIndexKey, Int(0), Writable)
	return o
}

// regExpBlock is a RegExp object, its data and lastIndex in one
// allocation: every evaluation of a regexp literal makes one.
type regExpBlock struct {
	o Object
	d regExpData
	v [1]Value
}

// newRegExpBlock makes a RegExp object without its prototype, data and
// lastIndex (generated code's pool fills them too).
func (r *Realm) newRegExpBlock() *regExpBlock {
	b := new(regExpBlock)
	b.o = Object{realm: r, extensible: true, props: propertyMap{shape: r.vm.rootShape, vals: b.v[:0]},
		class: ClassRegExp, internal: &b.d}
	return b
}

// regExpInitialize is RegExpInitialize.
func (vm *VM) regExpInitialize(o *Object, pattern, flags Value) *Object {
	p := emptyString
	if !pattern.IsUndefined() {
		p = vm.ToString(pattern)
	}
	f := ""
	if !flags.IsUndefined() {
		f = vm.toGoString(flags)
	}
	bits, ok := yarr.ParseFlags(f)
	if !ok {
		vm.throwSyntaxError("Invalid flags supplied to RegExp constructor '%s'", f)
	}
	compiled := vm.compilePattern(p, bits)
	d := o.internal.(*regExpData)
	d.source, d.flags, d.bits, d.pattern = p, f, bits, compiled
	o.SetOrThrow(lastIndexKey, Int(0))
	return o
}

func (vm *VM) regExpCreate(pattern, flags Value) *Object {
	return vm.regExpInitialize(vm.regExpAlloc(vm.realm.RegExpConstructor), pattern, flags)
}

func (vm *VM) newRegExpFromLiteral(re *bytecode.RegExp) *Object {
	o := vm.regExpAlloc(vm.realm.RegExpConstructor)
	bits, _ := yarr.ParseFlags(re.Flags)
	source := NewString(re.Pattern)
	d := o.internal.(*regExpData)
	d.source, d.flags, d.bits, d.pattern = source, re.Flags, bits, vm.compilePattern(source, bits)
	return o
}

func (vm *VM) isRegExp(v Value) bool {
	o := v.AsObject()
	if o == nil {
		return false
	}
	if m := o.Get(SymKey(SymbolMatch), v); !m.IsUndefined() {
		return m.ToBoolean()
	}
	return o.class == ClassRegExp
}

// advanceStringIndex is AdvanceStringIndex.
func advanceStringIndex(s *String, index int64, unicode bool) int64 {
	if !unicode || index+1 >= int64(s.Length()) {
		return index + 1
	}
	_, n := s.CodePointAt(int(index))
	return index + int64(n)
}

var (
	execKey    = StrKey("exec")
	indexKey   = StrKey("index")
	flagsKey   = StrKey("flags")
	sourceKey  = StrKey("source")
	inputKey   = StrKey("input")
	groupsKey  = StrKey("groups")
	indicesKey = StrKey("indices")
)

// regExpPrimordials are RegExp.prototype's built-in exec, flags and flag
// getters, standing in for JSC's RegExp primordial watchpoints: while they
// are in place, reading them on a plain RegExp is unobservable and the
// built-ins may use the RegExp's internal state instead.
type regExpPrimordials struct {
	keys  []PropertyKey
	funcs []*Object // the built-in value (exec) or getter of keys[i]

	// Where keys are in RegExp.prototype's layout, found again when it
	// changes; ok is false if one of them is missing.
	shape   *shape
	version uint32
	slots   []primordialSlot
	ok      bool
}

// primordialSlot is what a slot of RegExp.prototype must hold: the built-in
// function itself, or (for an accessor) the accessor cell found when the
// layout was last seen, whose getter must be the built-in. A replaced cell
// fails the check even with the built-in getter; that only costs the fast
// paths.
type primordialSlot struct {
	index int
	want  unsafe.Pointer
	cell  *Accessor
	get   *Object
}

func (p *regExpPrimordials) add(k PropertyKey, f *Object) {
	p.keys = append(p.keys, k)
	p.funcs = append(p.funcs, f)
}

// intact reports whether proto still holds the built-ins.
func (p *regExpPrimordials) intact(proto *Object) bool {
	m := &proto.props
	if m.shape != p.shape || m.shape.version != p.version {
		p.shape, p.version, p.ok = m.shape, m.shape.version, true
		p.slots = p.slots[:0]
		for j, k := range p.keys {
			i := m.find(k)
			if i < 0 {
				p.ok = false
				break
			}
			s := primordialSlot{index: i, want: unsafe.Pointer(p.funcs[j])}
			if prop := m.at(i); prop.IsAccessor() {
				s.cell, s.get = prop.Accessor(), p.funcs[j]
				s.want = unsafe.Pointer(s.cell)
			}
			p.slots = append(p.slots, s)
		}
	}
	if !p.ok {
		return false
	}
	vals := m.vals
	for i := range p.slots {
		s := &p.slots[i]
		if vals[s.index].p != s.want || s.cell != nil && s.cell.Get != s.get {
			return false
		}
	}
	return true
}

// regExpIsPristine reports whether r is a RegExp whose exec, flags and flag
// getters are the built-ins, with no own properties but lastIndex (JSC's
// hasObservableSideEffectsForRegExp*).
func (vm *VM) regExpIsPristine(r *Object) bool {
	if r.class != ClassRegExp || r.props.len() != 1 {
		return false
	}
	realm := r.realm
	if r.proto != realm.RegExpPrototype {
		return false
	}
	_, ok := r.internal.(*regExpData)
	return ok && realm.regExpPrimordials.intact(r.proto)
}

// regExpFlags is ToString(Get(rx, "flags")).
func (vm *VM) regExpFlags(rx *Object) string {
	if vm.regExpIsPristine(rx) {
		return vm.regExpFlagsString(rx)
	}
	return vm.toGoString(rx.Get(flagsKey, ObjectValue(rx)))
}

// regExpExec is RegExpExec: it calls a user-visible "exec" if there is one.
func (vm *VM) regExpExec(r *Object, s *String) Value {
	if vm.regExpIsPristine(r) {
		return vm.regExpBuiltinExec(r, s)
	}
	exec := r.GetKey(execKey)
	if exec.IsCallable() {
		result := vm.Call(exec, ObjectValue(r), StringValue(s))
		if !result.IsObject() && !result.IsNull() {
			vm.throwTypeError("The result of RegExp exec must be an object or null")
		}
		return result
	}
	if _, ok := r.internal.(*regExpData); !ok || r.class != ClassRegExp {
		vm.throwTypeError("RegExp.prototype.exec requires that |this| be a RegExp object")
	}
	return vm.regExpBuiltinExec(r, s)
}

// match runs the matcher of d on s from lastIndex.
func (vm *VM) match(d *regExpData, s *String, lastIndex int) []int {
	latin1, u16 := s.codeUnits()
	var caps []int
	var err error
	if u16 == nil {
		caps, err = yarr.ExecStop(d.pattern, latin1, lastIndex, &vm.interrupted)
	} else {
		caps, err = yarr.ExecStop(d.pattern, u16, lastIndex, &vm.interrupted)
	}
	if err == yarr.ErrInterrupted {
		panic(ErrInterrupted)
	}
	if err != nil {
		vm.throwRangeError("%s", err.Error())
	}
	return caps
}

// regExpBuiltinExec is RegExpBuiltinExec.
func (vm *VM) regExpBuiltinExec(r *Object, s *String) Value {
	caps := vm.regExpBuiltinMatch(r, s)
	if caps == nil {
		return Null
	}
	return ObjectValue(vm.regExpResult(r.internal.(*regExpData), s, caps))
}

// regExpBuiltinMatch is RegExpBuiltinExec up to building the match array:
// it returns the capture positions, or nil.
func (vm *VM) regExpBuiltinMatch(r *Object, s *String) []int {
	d := r.internal.(*regExpData)
	lastIndex := vm.ToLength(r.Get(lastIndexKey, ObjectValue(r)))
	global, sticky := d.bits.Has(yarr.FlagGlobal), d.bits.Has(yarr.FlagSticky)
	if !global && !sticky {
		lastIndex = 0
	}
	var caps []int
	if lastIndex <= int64(s.Length()) {
		caps = vm.match(d, s, int(lastIndex))
	}
	if caps == nil {
		if global || sticky {
			r.SetOrThrow(lastIndexKey, Int(0))
		}
		return nil
	}
	if global || sticky {
		r.SetOrThrow(lastIndexKey, Int(caps[1]))
	}
	vm.updateLegacyStatics(r, d, s, caps)
	return caps
}

// regExpResult builds the match array (steps 20-36 of RegExpBuiltinExec).
func (vm *VM) regExpResult(d *regExpData, s *String, caps []int) *Object {
	realm := vm.realm
	n := len(caps) / 2
	values := make([]Value, n)
	for i := range n {
		if caps[2*i] >= 0 {
			values[i] = StringValue(s.Substring(caps[2*i], caps[2*i+1]))
		}
	}
	a := realm.NewArray(values...)
	a.CreateDataProperty(indexKey, Int(caps[0]))
	a.CreateDataProperty(inputKey, StringValue(s))
	p := d.pattern
	groups := Undefined
	var groupNames []string
	if len(p.CaptureGroupNames) > 0 {
		g := realm.newObject(nil)
		groups = ObjectValue(g)
		groupNames = make([]string, n)
		matched := map[string]bool{}
		for i := 1; i < n; i++ {
			if i >= len(p.CaptureGroupNames) || p.CaptureGroupNames[i] == "" {
				continue
			}
			name := p.CaptureGroupNames[i]
			// Of duplicate-named groups at most one participates; it wins.
			if matched[name] {
				continue
			}
			if !values[i].IsUndefined() {
				matched[name] = true
			}
			g.CreateDataProperty(StrKey(name), values[i])
			groupNames[i] = name
		}
	}
	a.CreateDataProperty(groupsKey, groups)
	if d.bits.Has(yarr.FlagHasIndices) {
		indices := make([]Value, n)
		for i := range n {
			if caps[2*i] >= 0 {
				indices[i] = ObjectValue(realm.NewArray(Int(caps[2*i]), Int(caps[2*i+1])))
			}
		}
		ia := realm.NewArray(indices...)
		indexGroups := Undefined
		if groupNames != nil {
			g := realm.newObject(nil)
			indexGroups = ObjectValue(g)
			for i := 1; i < n; i++ {
				if name := groupNames[i]; name != "" {
					g.CreateDataProperty(StrKey(name), indices[i])
				}
			}
		}
		ia.CreateDataProperty(groupsKey, indexGroups)
		a.CreateDataProperty(indicesKey, ObjectValue(ia))
	}
	return a
}

func (vm *VM) updateLegacyStatics(r *Object, d *regExpData, s *String, caps []int) {
	if r.realm != vm.realm {
		return
	}
	st := &vm.realm.regExpStatics
	if !d.legacy {
		*st = regExpStatics{invalid: true}
		return
	}
	*st = regExpStatics{input: s, caps: append([]int(nil), caps...)}
}

// regExpFlagsString is the "flags" getter's generic algorithm.
func (vm *VM) regExpFlagsString(r *Object) string {
	var b strings.Builder
	if vm.regExpIsPristine(r) {
		bits := r.internal.(*regExpData).bits
		for _, f := range regExpFlagProps {
			if bits.Has(f.flag) {
				b.WriteByte(f.letter)
			}
		}
		return b.String()
	}
	for _, f := range regExpFlagProps {
		if r.GetStr(f.name).ToBoolean() {
			b.WriteByte(f.letter)
		}
	}
	return b.String()
}

var regExpFlagProps = []struct {
	name   string
	letter byte
	flag   yarr.Flags
}{
	{"hasIndices", 'd', yarr.FlagHasIndices},
	{"global", 'g', yarr.FlagGlobal},
	{"ignoreCase", 'i', yarr.FlagIgnoreCase},
	{"multiline", 'm', yarr.FlagMultiline},
	{"dotAll", 's', yarr.FlagDotAll},
	{"unicode", 'u', yarr.FlagUnicode},
	{"unicodeSets", 'v', yarr.FlagUnicodeSets},
	{"sticky", 'y', yarr.FlagSticky},
}

// escapeRegExpPattern is EscapeRegExpPattern: the source text such that
// `/${source}/${flags}` reparses to an equivalent literal.
func escapeRegExpPattern(src *String) *String {
	if src.Length() == 0 {
		return NewString("(?:)")
	}
	var b stringBuilder
	inClass, escaped := false, false
	changed := false
	for i := 0; i < src.Length(); i++ {
		c := src.At(i)
		switch {
		case escaped:
			escaped = false
			switch c {
			case '\n':
				b.WriteGo("n")
				changed = true
				continue
			case '\r':
				b.WriteGo("r")
				changed = true
				continue
			case 0x2028:
				b.WriteGo("u2028")
				changed = true
				continue
			case 0x2029:
				b.WriteGo("u2029")
				changed = true
				continue
			}
		case c == '\\':
			escaped = true
		case c == '[':
			inClass = true
		case c == ']':
			inClass = false
		case c == '/' && !inClass:
			b.WriteGo("\\/")
			changed = true
			continue
		case c == '\n':
			b.WriteGo("\\n")
			changed = true
			continue
		case c == '\r':
			b.WriteGo("\\r")
			changed = true
			continue
		case c == 0x2028:
			b.WriteGo("\\u2028")
			changed = true
			continue
		case c == 0x2029:
			b.WriteGo("\\u2029")
			changed = true
			continue
		}
		b.WriteUnit(c)
	}
	if !changed {
		return src
	}
	return b.String()
}

// regExpEscape is RegExp.escape's EncodeForRegExpEscape loop.
func regExpEscape(s *String) *String {
	var b stringBuilder
	hex := func(format string, c rune) { b.WriteGo(fmt.Sprintf(format, c)) }
	for i := 0; i < s.Length(); {
		c, n := s.CodePointAt(i)
		switch {
		case i == 0 && (c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z'):
			hex(`\x%02x`, c)
		case strings.ContainsRune(`^$\.*+?()[]{}|/`, c):
			b.WriteUnit('\\')
			b.WriteRune(c)
		case c == '\t':
			b.WriteGo(`\t`)
		case c == '\n':
			b.WriteGo(`\n`)
		case c == '\v':
			b.WriteGo(`\v`)
		case c == '\f':
			b.WriteGo(`\f`)
		case c == '\r':
			b.WriteGo(`\r`)
		case strings.ContainsRune(",-=<>#&!%:;@~'`\"", c):
			hex(`\x%02x`, c)
		case c <= 0xFFFF && isStrWhiteSpaceChar(uint16(c)) || c >= 0xD800 && c <= 0xDFFF:
			if c <= 0xFF {
				hex(`\x%02x`, c)
			} else {
				hex(`\u%04x`, c)
			}
		default:
			b.WriteRune(c)
		}
		i += n
	}
	return b.String()
}

func (r *Realm) initRegExp() {
	vm := r.vm
	proto := r.NewObject()
	r.RegExpPrototype = proto

	ctor := r.constructor("RegExp", 2, func(c *Call) Value {
		pattern, flags := c.Arg(0), c.Arg(1)
		patternIsRegExp := vm.isRegExp(pattern)
		newTarget := c.NewTarget
		if newTarget == nil {
			newTarget = c.Callee
			if patternIsRegExp && flags.IsUndefined() {
				pc := pattern.ObjectRef().Get(constructorKey, ObjectValue(pattern.ObjectRef()))
				if SameValue(ObjectValue(newTarget), pc) {
					return pattern
				}
			}
		}
		var p, f Value
		if d := regExpDataOf(pattern); d != nil && pattern.ObjectRef().class == ClassRegExp {
			p = StringValue(d.source)
			f = flags
			if flags.IsUndefined() {
				f = Str(d.flags)
			}
		} else if patternIsRegExp {
			p = pattern.ObjectRef().Get(sourceKey, ObjectValue(pattern.ObjectRef()))
			f = flags
			if flags.IsUndefined() {
				f = pattern.ObjectRef().Get(flagsKey, ObjectValue(pattern.ObjectRef()))
			}
		} else {
			p, f = pattern, flags
		}
		return ObjectValue(vm.regExpInitialize(vm.regExpAlloc(newTarget), p, f))
	}, proto)
	r.RegExpConstructor = ctor
	r.speciesGetter(ctor)

	r.method(ctor, "escape", 1, func(c *Call) Value {
		if !c.Arg(0).IsString() {
			vm.throwTypeError("RegExp.escape requires a string")
		}
		return StringValue(regExpEscape(c.Arg(0).StringRef()))
	})
	r.initRegExpLegacyStatics(ctor)

	thisRegExp := func(c *Call, name string) (*Object, *regExpData) {
		o := c.This.AsObject()
		if o == nil {
			vm.throwTypeError("RegExp.prototype.%s requires that |this| be an Object", name)
		}
		d, _ := o.internal.(*regExpData)
		if d == nil || o.class != ClassRegExp {
			return o, nil
		}
		return o, d
	}

	r.regExpPrimordials.add(StrKey("exec"), r.method(proto, "exec", 1, func(c *Call) Value {
		o, d := thisRegExp(c, "exec")
		if d == nil {
			vm.throwTypeError("RegExp.prototype.exec requires that |this| be a RegExp object")
		}
		return vm.regExpBuiltinExec(o, vm.ToString(c.Arg(0)))
	}))
	r.method(proto, "test", 1, func(c *Call) Value {
		o, _ := thisRegExp(c, "test")
		return Bool(!vm.regExpExec(o, vm.ToString(c.Arg(0))).IsNull())
	})
	r.method(proto, "toString", 0, func(c *Call) Value {
		o, _ := thisRegExp(c, "toString")
		var b stringBuilder
		b.WriteUnit('/')
		b.WriteString(vm.ToString(o.Get(sourceKey, ObjectValue(o))))
		b.WriteUnit('/')
		b.WriteString(vm.ToString(o.Get(flagsKey, ObjectValue(o))))
		return StringValue(b.String())
	})
	r.method(proto, "compile", 2, func(c *Call) Value {
		o, d := thisRegExp(c, "compile")
		if d == nil {
			vm.throwTypeError("RegExp.prototype.compile requires that |this| be a RegExp object")
		}
		if o.realm != vm.realm || !d.legacy {
			vm.throwTypeError("RegExp.prototype.compile cannot be used on this RegExp")
		}
		pattern, flags := c.Arg(0), c.Arg(1)
		if pd := regExpDataOf(pattern); pd != nil && pattern.ObjectRef().class == ClassRegExp {
			if !flags.IsUndefined() {
				vm.throwTypeError("Cannot supply flags when constructing one RegExp from another")
			}
			pattern, flags = StringValue(pd.source), Str(pd.flags)
		}
		return ObjectValue(vm.regExpInitialize(o, pattern, flags))
	})

	r.regExpPrimordials.add(StrKey("flags"), r.getter(proto, StrKey("flags"), func(c *Call) Value {
		o := c.This.AsObject()
		if o == nil {
			vm.throwTypeError("RegExp.prototype.flags getter called on non-object")
		}
		return Str(vm.regExpFlagsString(o))
	}))
	for _, f := range regExpFlagProps {
		r.regExpPrimordials.add(StrKey(f.name), r.getter(proto, StrKey(f.name), func(c *Call) Value {
			o, d := thisRegExp(c, f.name)
			if d == nil {
				if o == r.RegExpPrototype {
					return Undefined
				}
				vm.throwTypeError("The RegExp.prototype.%s getter can only be called on a RegExp object", f.name)
			}
			return Bool(d.bits.Has(f.flag))
		}))
	}
	r.getter(proto, StrKey("source"), func(c *Call) Value {
		o, d := thisRegExp(c, "source")
		if d == nil {
			if o == r.RegExpPrototype {
				return Str("(?:)")
			}
			vm.throwTypeError("The RegExp.prototype.source getter can only be called on a RegExp object")
		}
		return StringValue(escapeRegExpPattern(d.source))
	})

	thisObject := func(c *Call, name string) *Object {
		o := c.This.AsObject()
		if o == nil {
			vm.throwTypeError("RegExp.prototype.%s requires that |this| be an Object", name)
		}
		return o
	}
	setLastIndex := func(o *Object, i int64) { o.SetOrThrow(lastIndexKey, Number(float64(i))) }
	getLastIndex := func(o *Object) int64 { return vm.ToLength(o.GetKey(lastIndexKey)) }
	matchString := func(result Value) *String { return vm.ToString(result.ObjectRef().Get(IndexKey(0), result)) }

	r.symbolMethod(proto, SymbolMatch, 1, func(c *Call) Value {
		rx := thisObject(c, "[Symbol.match]")
		s := vm.ToString(c.Arg(0))
		flags := vm.regExpFlags(rx)
		if !strings.Contains(flags, "g") {
			return vm.regExpExec(rx, s)
		}
		fullUnicode := strings.ContainsAny(flags, "uv")
		setLastIndex(rx, 0)
		var matches []Value
		for {
			result := vm.regExpExec(rx, s)
			if result.IsNull() {
				if len(matches) == 0 {
					return Null
				}
				return ObjectValue(c.Realm().NewArray(matches...))
			}
			m := matchString(result)
			matches = append(matches, StringValue(m))
			if m.Length() == 0 {
				setLastIndex(rx, advanceStringIndex(s, getLastIndex(rx), fullUnicode))
			}
		}
	}, BuiltinFlags)

	// RegExp String Iterator.
	r.RegExpStringIteratorPrototype = r.newObject(r.IteratorPrototype)
	r.toStringTag(r.RegExpStringIteratorPrototype, "RegExp String Iterator")
	r.method(r.RegExpStringIteratorPrototype, "next", 0, func(c *Call) Value {
		var it *regExpStringIterator
		if o := c.This.AsObject(); o != nil {
			it, _ = o.internal.(*regExpStringIterator)
		}
		if it == nil {
			vm.throwTypeError("%%RegExpStringIteratorPrototype%%.next requires that |this| be a RegExp String Iterator")
		}
		if it.done {
			return c.Realm().iterResult(Undefined, true)
		}
		result := vm.regExpExec(it.matcher, it.s)
		if result.IsNull() {
			it.done = true
			return c.Realm().iterResult(Undefined, true)
		}
		if !it.global {
			it.done = true
			return c.Realm().iterResult(result, false)
		}
		if matchString(result).Length() == 0 {
			setLastIndex(it.matcher, advanceStringIndex(it.s, getLastIndex(it.matcher), it.fullUnicode))
		}
		return c.Realm().iterResult(result, false)
	})

	r.symbolMethod(proto, SymbolMatchAll, 1, func(c *Call) Value {
		rx := thisObject(c, "[Symbol.matchAll]")
		s := vm.ToString(c.Arg(0))
		ctor := vm.SpeciesConstructor(rx, c.Realm().RegExpConstructor)
		flags := vm.ToString(rx.Get(flagsKey, ObjectValue(rx)))
		matcher := vm.Construct(ctor, []Value{ObjectValue(rx), StringValue(flags)}, ctor).ObjectRef()
		setLastIndex(matcher, getLastIndex(rx))
		fl := flags.String()
		it := c.Realm().newObject(c.Realm().RegExpStringIteratorPrototype)
		it.internal = &regExpStringIterator{matcher: matcher, s: s,
			global: strings.Contains(fl, "g"), fullUnicode: strings.ContainsAny(fl, "uv")}
		return ObjectValue(it)
	}, BuiltinFlags)

	r.symbolMethod(proto, SymbolReplace, 2, func(c *Call) Value {
		return vm.regExpReplace(thisObject(c, "[Symbol.replace]"), vm.ToString(c.Arg(0)), c.Arg(1))
	}, BuiltinFlags)

	r.symbolMethod(proto, SymbolSearch, 1, func(c *Call) Value {
		rx := thisObject(c, "[Symbol.search]")
		s := vm.ToString(c.Arg(0))
		previous := rx.GetKey(lastIndexKey)
		if !SameValue(previous, Int(0)) {
			rx.SetOrThrow(lastIndexKey, Int(0))
		}
		result := vm.regExpExec(rx, s)
		if current := rx.GetKey(lastIndexKey); !SameValue(current, previous) {
			rx.SetOrThrow(lastIndexKey, previous)
		}
		if result.IsNull() {
			return Int(-1)
		}
		return result.ObjectRef().Get(indexKey, ObjectValue(result.ObjectRef()))
	}, BuiltinFlags)

	r.symbolMethod(proto, SymbolSplit, 2, func(c *Call) Value {
		rx := thisObject(c, "[Symbol.split]")
		s := vm.ToString(c.Arg(0))
		realm := c.Realm()
		ctor := vm.SpeciesConstructor(rx, realm.RegExpConstructor)
		flags := vm.regExpFlags(rx)
		unicodeMatching := strings.ContainsAny(flags, "uv")
		newFlags := flags
		if !strings.Contains(flags, "y") {
			newFlags += "y"
		}
		splitter := vm.Construct(ctor, []Value{ObjectValue(rx), Str(newFlags)}, ctor).ObjectRef()
		var parts []Value
		lim := uint32(1<<32 - 1)
		if limit := c.Arg(1); !limit.IsUndefined() {
			lim = vm.ToUint32(limit)
		}
		if lim == 0 {
			return ObjectValue(realm.NewArray())
		}
		size := int64(s.Length())
		if size == 0 {
			if !vm.regExpExec(splitter, s).IsNull() {
				return ObjectValue(realm.NewArray())
			}
			return ObjectValue(realm.NewArray(StringValue(s)))
		}
		var p, q int64
		for q < size {
			setLastIndex(splitter, q)
			z := vm.regExpExec(splitter, s)
			if z.IsNull() {
				q = advanceStringIndex(s, q, unicodeMatching)
				continue
			}
			e := min(getLastIndex(splitter), size)
			if e == p {
				q = advanceStringIndex(s, q, unicodeMatching)
				continue
			}
			parts = append(parts, StringValue(s.Substring(int(p), int(q))))
			if uint32(len(parts)) == lim {
				return ObjectValue(realm.NewArray(parts...))
			}
			p = e
			zo := z.ObjectRef()
			n := max(vm.LengthOfArrayLike(zo)-1, 0)
			for i := int64(1); i <= n; i++ {
				parts = append(parts, zo.Get(IndexKey(uint32(i)), z))
				if uint32(len(parts)) == lim {
					return ObjectValue(realm.NewArray(parts...))
				}
			}
			q = p
		}
		parts = append(parts, StringValue(s.Substring(int(p), int(size))))
		return ObjectValue(realm.NewArray(parts...))
	}, BuiltinFlags)
}

type regExpStringIterator struct {
	matcher             *Object
	s                   *String
	global, fullUnicode bool
	done                bool
}

// regExpReplace is RegExp.prototype[Symbol.replace].
func (vm *VM) regExpReplace(rx *Object, s *String, replaceValue Value) Value {
	functional := replaceValue.IsCallable()
	var replaceStr *String
	if !functional {
		replaceStr = vm.ToString(replaceValue)
	}
	if vm.regExpIsPristine(rx) {
		if d := rx.internal.(*regExpData); !d.bits.Has(yarr.FlagSticky) && len(d.pattern.CaptureGroupNames) == 0 {
			return vm.regExpReplaceFast(rx, d, s, replaceValue, replaceStr)
		}
	}
	flags := vm.regExpFlags(rx)
	global := strings.Contains(flags, "g")
	fullUnicode := false
	if global {
		fullUnicode = strings.ContainsAny(flags, "uv")
		rx.SetOrThrow(lastIndexKey, Int(0))
	}
	var results []*Object
	for {
		result := vm.regExpExec(rx, s)
		if result.IsNull() {
			break
		}
		results = append(results, result.ObjectRef())
		if !global {
			break
		}
		if vm.ToString(result.ObjectRef().Get(IndexKey(0), result)).Length() == 0 {
			thisIndex := vm.ToLength(rx.GetKey(lastIndexKey))
			rx.SetOrThrow(lastIndexKey, Number(float64(advanceStringIndex(s, thisIndex, fullUnicode))))
		}
	}
	return vm.replaceWithResults(s, results, replaceValue, replaceStr)
}

// replaceWithResults is the rest of RegExp.prototype[@@replace]: it
// replaces the matches described by the exec results.
func (vm *VM) replaceWithResults(s *String, results []*Object, replaceValue Value, replaceStr *String) Value {
	lengthS := s.Length()
	functional := replaceStr == nil
	var b stringBuilder
	nextSourcePosition := 0
	for _, result := range results {
		rv := ObjectValue(result)
		nCaptures := max(vm.LengthOfArrayLike(result)-1, 0)
		matched := vm.ToString(result.Get(IndexKey(0), rv))
		position := int(max(0, min(vm.ToIntegerOrInfinity(result.Get(indexKey, ObjectValue(result))), float64(lengthS))))
		captures := make([]Value, 0, nCaptures)
		for n := int64(1); n <= nCaptures; n++ {
			capN := result.Get(IndexKey(uint32(n)), rv)
			if !capN.IsUndefined() {
				capN = StringValue(vm.ToString(capN))
			}
			captures = append(captures, capN)
		}
		namedCaptures := result.GetStr("groups")
		var replacement *String
		if functional {
			args := make([]Value, 0, len(captures)+4)
			args = append(args, StringValue(matched))
			args = append(args, captures...)
			args = append(args, Int(position), StringValue(s))
			if !namedCaptures.IsUndefined() {
				args = append(args, namedCaptures)
			}
			replacement = vm.ToString(vm.Call(replaceValue, Undefined, args...))
		} else {
			if !namedCaptures.IsUndefined() {
				namedCaptures = ObjectValue(vm.ToObject(namedCaptures))
			}
			replacement = vm.getSubstitution(matched, s, position, captures, namedCaptures, replaceStr)
		}
		if position >= nextSourcePosition {
			b.WriteString(s.Substring(nextSourcePosition, position))
			b.WriteString(replacement)
			nextSourcePosition = position + matched.Length()
		}
	}
	if nextSourcePosition >= lengthS {
		return StringValue(b.String())
	}
	b.WriteString(s.Substring(nextSourcePosition, lengthS))
	return StringValue(b.String())
}

// regExpReplaceFast is regExpReplace for a pristine, non-sticky RegExp
// without named groups (JSC's replaceUsingRegExpSearch): it matches
// directly instead of going through exec and match arrays, with the same
// observable effects (lastIndex, the legacy statics, then the replacements
// in order).
func (vm *VM) regExpReplaceFast(rx *Object, d *regExpData, s *String, replaceValue Value, replaceStr *String) Value {
	lengthS := s.Length()
	var matches [][]int
	if d.bits.Has(yarr.FlagGlobal) {
		fullUnicode := d.bits.Has(yarr.FlagUnicode) || d.bits.Has(yarr.FlagUnicodeSets)
		rx.SetOrThrow(lastIndexKey, Int(0))
		for pos := 0; pos <= lengthS; {
			caps := vm.match(d, s, pos)
			if caps == nil {
				break
			}
			matches = append(matches, caps)
			pos = caps[1]
			if caps[0] == caps[1] {
				pos = int(advanceStringIndex(s, int64(pos), fullUnicode))
			}
		}
		rx.SetOrThrow(lastIndexKey, Int(0))
		if len(matches) == 0 {
			return StringValue(s)
		}
		vm.updateLegacyStatics(rx, d, s, matches[len(matches)-1])
	} else {
		// Reading lastIndex may run code that recompiles rx.
		pattern, bits := d.pattern, d.bits
		caps := vm.regExpBuiltinMatch(rx, s)
		if caps == nil {
			return StringValue(s)
		}
		if d.pattern != pattern || d.bits != bits {
			return vm.replaceWithResults(s, []*Object{vm.regExpResult(d, s, caps)}, replaceValue, replaceStr)
		}
		matches = append(matches, caps)
	}
	var b stringBuilder
	nextSourcePosition := 0
	for _, caps := range matches {
		position := caps[0]
		matched := s.Substring(position, caps[1])
		captures := make([]Value, len(caps)/2-1)
		for i := range captures {
			if start := caps[2*i+2]; start >= 0 {
				captures[i] = StringValue(s.Substring(start, caps[2*i+3]))
			}
		}
		var replacement *String
		if replaceStr == nil {
			args := make([]Value, 0, len(captures)+3)
			args = append(args, StringValue(matched))
			args = append(args, captures...)
			args = append(args, Int(position), StringValue(s))
			replacement = vm.ToString(vm.Call(replaceValue, Undefined, args...))
		} else {
			replacement = vm.getSubstitution(matched, s, position, captures, Undefined, replaceStr)
		}
		if position >= nextSourcePosition {
			b.WriteString(s.Substring(nextSourcePosition, position))
			b.WriteString(replacement)
			nextSourcePosition = caps[1]
		}
	}
	if nextSourcePosition < lengthS {
		b.WriteString(s.Substring(nextSourcePosition, lengthS))
	}
	return StringValue(b.String())
}

// initRegExpLegacyStatics installs RegExp.$1-$9, input ($_), lastMatch ($&),
// lastParen ($+), leftContext ($`) and rightContext ($').
func (r *Realm) initRegExpLegacyStatics(ctor *Object) {
	vm := r.vm
	statics := func(c *Call) *regExpStatics {
		if c.This.AsObject() != ctor {
			vm.throwTypeError("RegExp legacy static accessor called on an incompatible receiver")
		}
		st := &r.regExpStatics
		if st.invalid {
			vm.throwTypeError("RegExp legacy static properties are not available")
		}
		return st
	}
	capture := func(st *regExpStatics, i int) Value {
		if st.input == nil || 2*i+1 >= len(st.caps) || st.caps[2*i] < 0 {
			return Str("")
		}
		return StringValue(st.input.Substring(st.caps[2*i], st.caps[2*i+1]))
	}
	define := func(get NativeFunc, set NativeFunc, names ...string) {
		for _, name := range names {
			if set != nil {
				r.accessor(ctor, StrKey(name), get, set)
			} else {
				r.getter(ctor, StrKey(name), get)
			}
		}
	}
	setInput := func(c *Call) Value {
		st := statics(c)
		st.input = vm.ToString(c.Arg(0))
		st.caps = nil
		return Undefined
	}
	define(func(c *Call) Value {
		st := statics(c)
		if st.input == nil {
			return Str("")
		}
		return StringValue(st.input)
	}, setInput, "input", "$_")
	define(func(c *Call) Value { return capture(statics(c), 0) }, nil, "lastMatch", "$&")
	define(func(c *Call) Value {
		st := statics(c)
		if n := len(st.caps)/2 - 1; n > 0 {
			return capture(st, n)
		}
		return Str("")
	}, nil, "lastParen", "$+")
	define(func(c *Call) Value {
		st := statics(c)
		if st.input == nil || len(st.caps) == 0 {
			return Str("")
		}
		return StringValue(st.input.Substring(0, st.caps[0]))
	}, nil, "leftContext", "$`")
	define(func(c *Call) Value {
		st := statics(c)
		if st.input == nil || len(st.caps) == 0 {
			return Str("")
		}
		return StringValue(st.input.Substring(st.caps[1], st.input.Length()))
	}, nil, "rightContext", "$'")
	for i := 1; i <= 9; i++ {
		define(func(c *Call) Value { return capture(statics(c), i) }, nil, fmt.Sprintf("$%d", i))
	}
}
