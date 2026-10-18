package vm

import (
	"fmt"
	"weak"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// CodeBlock is an UnlinkedCodeBlock linked to a realm (JSC's CodeBlock,
// minus profiling and JIT state): constants are materialized as values
// and identifiers as property keys.
type CodeBlock struct {
	u      *bytecode.UnlinkedCodeBlock
	realm  *Realm
	source parser.SourceCode

	constants []Value
	keys      []PropertyKey
	// strictMode caches whether the code is strict.
	name string

	// Inline caches, indexed by the instructions' metadata IDs.
	getByIdCaches      []getByIdCache
	getLengthCaches    []getByIdCache
	putByIdCaches      []putByIdCache
	resolveScopeCaches []resolveScopeCache
	getFromScopeCaches []getByIdCache
	putToScopeCaches   []putByIdCache
	getScopeSlotCaches []scopeSlotCache
	putScopeSlotCaches []scopeSlotCache
	createThisCaches   []getByIdCache
	instanceofCaches   []getByIdCache
	// getByValProfiles and putByValProfiles are, per get_by_val and
	// put_by_val, what bases the interpreter saw (byValProfileOf; JSC's
	// ArrayProfile): the JIT puts a typed array path first where a site
	// saw only one kind of typed array.
	getByValProfiles []uint16
	putByValProfiles []uint16
	// callProfiles, callIgnoreProfiles and tailCallProfiles are, per call,
	// call_ignore_result and tail_call, the function the interpreter saw it call (JSC's
	// CallLinkStatus from the LLInt's call link info): the JIT inlines a
	// small one (jit_inline.go).
	callProfiles       []callProfile
	callIgnoreProfiles []callProfile
	tailCallProfiles   []callProfile

	// jit is the tier-up state of the numeric JIT.
	jit jitState

	// evalCache is JSC's DirectEvalCodeCache: the compiled code of this
	// code block's direct evals by call site and source.
	evalCache map[evalCacheKey]evalCode
}

type evalCacheKey struct {
	pc             int
	source         string
	insideOrdinary bool
}

type evalCode struct {
	ucb    *bytecode.UnlinkedCodeBlock
	source parser.SourceCode
}

// maxEvalCacheEntries is DirectEvalCodeCache::maxCacheEntries.
const maxEvalCacheEntries = 64

// arrayBufferConstant is a linked op_new_array_buffer constant.
type arrayBufferConstant struct{ values []Value }

// templateKey identifies a tagged template site in a realm.
type templateKey struct {
	provider  *parser.SourceProvider
	endOffset int
}

// link links ucb (whose source is source) to the realm, caching the
// result: a code block is linked once per realm, which also makes template
// objects unique per site and realm.
func (r *Realm) link(ucb *bytecode.UnlinkedCodeBlock, source parser.SourceCode) *CodeBlock {
	if cb, ok := ucb.Linked.(*CodeBlock); ok && cb.realm == r {
		return cb
	}
	if cb, ok := r.linked[ucb]; ok {
		return cb
	}
	cb := &CodeBlock{u: ucb, realm: r, source: source}
	cb.constants = make([]Value, len(ucb.Constants))
	for i, c := range ucb.Constants {
		cb.constants[i] = r.constantValue(c, source)
	}
	cb.keys = make([]PropertyKey, len(ucb.Identifiers))
	for i, id := range ucb.Identifiers {
		cb.keys[i] = keyForIdentifier(id)
	}
	cb.allocCaches()
	cb.verifyRegisters()
	if ucb.Linked == nil {
		ucb.Linked = cb
	} else {
		r.linked[ucb] = cb // another realm's
	}
	return cb
}

// linkedCode is the code block of exec for kind if it was linked to the
// realm already, else nil.
func (r *Realm) linkedCode(exec *bytecode.UnlinkedFunctionExecutable, kind bytecode.CodeSpecializationKind) *CodeBlock {
	ucb := exec.CodeBlockForCall
	if kind == bytecode.CodeForConstruct {
		ucb = exec.CodeBlockForConstruct
	}
	if ucb == nil {
		return nil
	}
	if cb, ok := ucb.Linked.(*CodeBlock); ok && cb.realm == r {
		return cb
	}
	return r.linked[ucb]
}

// verifyRegisters checks that every register operand addresses a constant
// or a slot of the frames newFrame creates for the code block, the
// invariant that lets Frame.get and Frame.set skip bounds checks.
func (cb *CodeBlock) verifyRegisters() {
	u := cb.u
	ins := u.Instructions
	for pc := 0; pc < len(ins); {
		op := bytecode.OpcodeID(ins[pc])
		for i, kind := range op.OperandKinds() {
			if kind != bytecode.OperandVirtualRegister {
				continue
			}
			r := bytecode.VirtualRegister(int32(ins[pc+1+i]))
			switch {
			case r == bytecode.InvalidVirtualRegister:
			case r >= bytecode.FirstConstantRegisterIndex:
				if int(r)-bytecode.FirstConstantRegisterIndex >= len(cb.constants) {
					panic(fmt.Sprintf("vm: %s at %d uses constant %d of %d", op, pc, int(r)-bytecode.FirstConstantRegisterIndex, len(cb.constants)))
				}
			case int(r) < -u.NumCalleeLocals || int(r) >= bytecode.CallFrameSlotThisArgument+max(u.NumParameters, 1):
				panic(fmt.Sprintf("vm: %s at %d uses register %d outside the frame (%d locals, %d parameters)", op, pc, r, u.NumCalleeLocals, u.NumParameters))
			}
		}
		pc += op.Length()
	}
}

func (r *Realm) constantValue(c bytecode.Constant, source parser.SourceCode) Value {
	switch c := c.(type) {
	case bytecode.Empty:
		return Empty
	case bytecode.Undefined:
		return Undefined
	case bytecode.ForwardedArguments:
		return forwardedArguments
	case bytecode.Null:
		return Null
	case bytecode.Bool:
		return Bool(bool(c))
	case bytecode.Number:
		return Number(c.Value)
	case bytecode.String:
		return Str(string(c))
	case bytecode.BigInt:
		return BigIntValue(c.Value)
	case *bytecode.TemplateObject:
		return ObjectValue(r.templateObject(c, templateKey{source.Provider, c.EndOffset}))
	case *bytecode.ArrayBuffer:
		values := make([]Value, len(c.Values))
		for i, v := range c.Values {
			values[i] = r.constantValue(v, source)
		}
		return cellValue(&arrayBufferConstant{values})
	case *bytecode.SymbolTable:
		return cellValue(c)
	case bytecode.LinkTimeConstant:
		return r.linkTimeConstant(c)
	case *bytecode.RegExp:
		return cellValue(c)
	}
	panic("vm: unknown constant type")
}

// templateObject is GetTemplateObject (13.2.8.4). The realm's table is
// weak: the code blocks of the site hold the object through their
// constants, and once they are gone nothing can ask for it again.
func (r *Realm) templateObject(t *bytecode.TemplateObject, key templateKey) *Object {
	if w, ok := r.templates[key]; ok {
		if o := w.Value(); o != nil {
			return o
		}
	}
	d := t.Descriptor
	raw := make([]Value, len(d.RawStrings))
	for i, s := range d.RawStrings {
		raw[i] = Str(s)
	}
	cooked := make([]Value, len(d.CookedStrings))
	for i, s := range d.CookedStrings {
		if s == nil {
			cooked[i] = Undefined
		} else {
			cooked[i] = Str(*s)
		}
	}
	rawObj := r.NewArray(raw...)
	template := r.NewArray(cooked...)
	rawObj.SetIntegrityLevel(Frozen)
	template.putDirect(StrKey("raw"), ObjectValue(rawObj), 0)
	template.SetIntegrityLevel(Frozen)
	if len(r.templates) >= 2*max(r.templatesPurged, 64) {
		r.templates = purgeWeak(r.templates, func(w weak.Pointer[Object]) bool { return w.Value() != nil })
		r.templatesPurged = len(r.templates)
	}
	r.templates[key] = weak.Make(template)
	return template
}

func (cb *CodeBlock) identifier(i uint32) ident.Identifier { return cb.u.Identifiers[i] }

func (cb *CodeBlock) isStrict() bool {
	return cb.u.LexicallyScopedFeatures&parser.StrictModeLexicallyScopedFeature != 0 || cb.u.Info.ScriptMode == parser.ScriptModeModule
}
