//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package vm

import (
	"slices"
	"unsafe"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/jit"
)

// Slow paths (JSC's slow path calls). Generated code leaves to Go for an
// instruction it has no machine code for, or for the cases its machine
// code does not handle; Go runs that one instruction as the interpreter
// does (jitSlowPath) and goes back into the generated code at the
// instruction it continues at. Calls work this way too. An instruction
// with a slow path no longer stops a loop or a function from being
// compiled, so that the rest of it runs as machine code.

var offHTMLDDA = int(unsafe.Offsetof(Object{}.htmlDDA))

// slow returns the exit to the slow path of the instruction at pc, after
// which generated code continues at one of resumeAt (the next instruction,
// and a jump's target).
func (c *jitCompiler) slow(pc int, resumeAt ...int) *jit.Label {
	if o := c.opt; o != nil {
		c.opt = nil
		l := c.slow(pc, resumeAt...)
		c.opt = o
		return c.flushTo(o, pc, l)
	}
	l := c.callExits[pc]
	if l == nil {
		l = jit.NewLabel()
		c.callExits[pc] = l
	}
	for _, r := range resumeAt {
		c.resumeAt[r] = true
	}
	c.slowResumes = append(c.slowResumes, resumeAt...)
	return l
}

// objectInstruction compiles the instructions on objects and other values
// that are not numbers; it reports whether op is one of them.
// smallInstruction compiles instructions whose Go slow path stays for the
// cases it does not handle, as JSC's Baseline JIT has them inline: tests
// of a value's tag, the argument count and arguments, and the prototype of
// an ordinary object.
func (c *jitCompiler) smallInstruction(pc int, op bytecode.OpcodeID, in []uint32) bool {
	m := &c.m
	next := pc + op.Length()
	if c.switchInstruction(pc, op, in) {
		return true
	}
	switch op {
	case bytecode.OpIsEmptyID, bytecode.OpIsUndefinedOrNullID:
		var dst, operand bytecode.VirtualRegister
		if op == bytecode.OpIsEmptyID {
			b := bytecode.DecodeIsEmpty(in)
			dst, operand = b.Dst, b.Operand
		} else {
			b := bytecode.DecodeIsUndefinedOrNull(in)
			dst, operand = b.Dst, b.Operand
		}
		yes, no := jit.NewLabel(), jit.NewLabel()
		c.loadValue(jit.S0, jit.S1, operand)
		if op == bytecode.OpIsEmptyID {
			m.Branch64Imm(jit.EQ, jit.S1, uint32(tagEmpty), yes)
		} else {
			m.Branch64Imm(jit.ULE, jit.S1, uint32(tagNull), yes) // undefined is 0
		}
		m.B(no)
		c.storeBranches(dst, yes, no, true)
		return true
	case bytecode.OpArgumentCountID:
		// The frame's argc counts this.
		b := bytecode.DecodeArgumentCount(in)
		m.Load64(jit.S0, rFrame, offFrameArgc)
		m.SubImm64(jit.S0, jit.S0, 1)
		m.CvtS64(0, jit.S0)
		c.storeNum(b.Dst, 0)
		return true
	case bytecode.OpGetArgumentID:
		// Index counts this; past argc the argument is undefined.
		b := bytecode.DecodeGetArgument(in)
		missing, done := jit.NewLabel(), jit.NewLabel()
		m.Load64(jit.S0, rFrame, offFrameArgc)
		m.Branch64Imm(jit.ULE, jit.S0, uint32(b.Index), missing)
		c.loadValue(jit.S0, jit.S1, bytecode.VirtualRegister(bytecode.CallFrameSlotThisArgument+int(b.Index)))
		c.storeValue(b.Dst, jit.S0, jit.S1)
		m.B(done)
		m.Bind(missing)
		m.MovImm(jit.S0, 0)
		m.MovImm(jit.S1, uint64(tagUndefined))
		c.storeValue(b.Dst, jit.S0, jit.S1)
		m.Bind(done)
		return true
	case bytecode.OpEnumeratorNextID:
		// for-in's next key, for an enumeration of an object's own keys
		// from its shape's cache (OwnStructureMode) while the object keeps
		// that shape: the cached name, as propertyNameEnumerator.next; the
		// rest, and the end, in the slow path (uglify, typescript and
		// tagcloud took it 1.1 million times a run).
		b := bytecode.DecodeEnumeratorNext(in)
		slow := c.slow(pc, next)
		if _, ok := c.constant(b.Enumerator); ok {
			m.B(slow)
			return true
		}
		m.Load64(jit.S0, rRegs, c.off(b.Enumerator)+8)
		m.Branch64Imm(jit.NE, jit.S0, uint32(tagCell), slow)
		m.Load64(jit.S0, rRegs, c.off(b.Enumerator)) // *cellBox
		m.Load64(jit.S1, jit.S0, 0)                  // its type word
		m.MovImm(jit.S2, uint64(enumeratorType))
		m.Branch64(jit.NE, jit.S1, jit.S2, slow)
		m.Load64(jit.S0, jit.S0, 8) // *propertyNameEnumerator
		m.Load64(jit.S1, jit.S0, offEnumBase)
		m.BranchZero(jit.S1, slow)
		m.Load64(jit.S2, jit.S0, offEnumPos)
		m.Load64(jit.S3, jit.S0, offEnumNames+8) // len
		m.Branch64(jit.UGE, jit.S2, jit.S3, slow)
		m.Load64(jit.S3, jit.S1, offProps) // the base's shape
		m.Load64(jit.S4, jit.S0, offEnumShape)
		m.Branch64(jit.NE, jit.S3, jit.S4, slow)
		m.Load64(jit.S3, jit.S0, offEnumNames)
		m.AddShift64(jit.S3, jit.S3, jit.S2, 4)
		m.Load64(jit.S4, jit.S3, 0)
		m.Load64(jit.S5, jit.S3, 8)
		m.AddImm64(jit.S2, jit.S2, 1)
		m.Store64(jit.S2, jit.S0, offEnumPos)
		c.storeValue(b.PropertyName, jit.S4, jit.S5)
		return true
	case bytecode.OpEnumeratorGetByValID:
		// for-in's o[k]: the key is a string, looked up like get_by_val's.
		b := bytecode.DecodeEnumeratorGetByVal(in)
		slow := c.slow(pc, next)
		_, cb := c.constant(b.Base)
		_, cp := c.constant(b.PropertyName)
		if cb || cp {
			m.B(slow)
			return true
		}
		done := jit.NewLabel()
		m.Load64(jit.S0, rRegs, c.off(b.Base)+8)
		m.Branch64Imm(jit.NE, jit.S0, uint32(tagObject), slow)
		m.Load64(jit.S0, rRegs, c.off(b.Base))
		m.Load64(jit.S2, rRegs, c.off(b.PropertyName)+8)
		m.Branch64Imm(jit.NE, jit.S2, uint32(tagString), slow)
		c.megaByVal(b.PropertyName, b.Dst, false, done, slow)
		m.Bind(done)
		return true
	case bytecode.OpGetPrototypeOfID:
		// Ordinary objects (and arrays, whose [[GetPrototypeOf]] is the
		// ordinary one); proxies and primitives take the slow path.
		b := bytecode.DecodeGetPrototypeOf(in)
		slow, ordinary, null, done := c.slow(pc, next), jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
		c.loadValue(jit.S0, jit.S1, b.Value)
		m.Branch64Imm(jit.NE, jit.S1, uint32(tagObject), slow)
		m.Load64(jit.S2, jit.S0, offExotic)
		m.BranchZero(jit.S2, ordinary)
		m.MovImm(jit.S3, uint64(arrayObjectItab))
		m.Branch64(jit.NE, jit.S2, jit.S3, slow)
		m.Bind(ordinary)
		m.Load64(jit.S0, jit.S0, offProto)
		m.BranchZero(jit.S0, null)
		m.MovImm(jit.S1, uint64(tagObject))
		m.B(done)
		m.Bind(null)
		m.MovImm(jit.S1, uint64(tagNull))
		m.Bind(done)
		c.storeValue(b.Dst, jit.S0, jit.S1)
		return true
	}
	return false
}

func (c *jitCompiler) objectInstruction(pc int, op bytecode.OpcodeID, in []uint32) bool {
	m := &c.m
	next := pc + op.Length()
	if c.smallInstruction(pc, op, in) {
		return true
	}
	if jitGenericOps[op] || jitColdOps[op] {
		// Every other instruction of the interpreter: Go runs it (JSC's
		// slow_path_* operations), so it does not stop the code around it
		// from being compiled. A switch continues at one of its targets.
		if targets, ok := switchTargets(c.cb, pc, in); ok {
			m.B(c.slow(pc, targets...))
		} else {
			m.B(c.slow(pc, next))
		}
		return true
	}
	switch op {
	case bytecode.OpToThisID:
		// An object is its own this value.
		b := bytecode.DecodeToThis(in)
		if _, ok := c.constant(b.SrcDst); ok {
			m.B(c.slow(pc, next))
			return true
		}
		m.Load64(jit.S1, rRegs, c.off(b.SrcDst)+8)
		m.Branch64Imm(jit.NE, jit.S1, uint32(tagObject), c.slow(pc, next))
		return true
	case bytecode.OpCheckTdzID:
		// The slow path throws.
		b := bytecode.DecodeCheckTdz(in)
		c.loadValue(jit.S0, jit.S1, b.TargetVirtualRegister)
		m.Branch64Imm(jit.EQ, jit.S1, uint32(tagEmpty), c.slow(pc, next))
		return true
	case bytecode.OpNotID:
		// !ToBoolean of any value but BigInts, which take the slow path.
		b := bytecode.DecodeNot(in)
		c.loadValue(jit.S1, jit.S0, b.Operand)
		truthy, falsy, done := jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
		c.toBoolean(truthy, falsy, c.slow(pc, next))
		m.Bind(truthy)
		m.MovImm(jit.S2, 0)
		m.B(done)
		m.Bind(falsy)
		m.MovImm(jit.S2, 1)
		m.Bind(done)
		c.storeBool(b.Dst, jit.S2)
		return true
	case bytecode.OpJeqNullID, bytecode.OpJneqNullID:
		var value bytecode.VirtualRegister
		var label bytecode.BoundLabel
		if op == bytecode.OpJeqNullID {
			b := bytecode.DecodeJeqNull(in)
			value, label = b.Value, b.TargetLabel
		} else {
			b := bytecode.DecodeJneqNull(in)
			value, label = b.Value, b.TargetLabel
		}
		yes, no := jit.NewLabel(), jit.NewLabel()
		c.nullish(value, yes, no)
		if op == bytecode.OpJeqNullID {
			m.Bind(yes)
			m.B(c.target(pc, label))
			m.Bind(no)
		} else {
			m.Bind(no)
			m.B(c.target(pc, label))
			m.Bind(yes)
		}
		return true
	case bytecode.OpEqNullID, bytecode.OpNeqNullID:
		var dst, operand bytecode.VirtualRegister
		if op == bytecode.OpEqNullID {
			b := bytecode.DecodeEqNull(in)
			dst, operand = b.Dst, b.Operand
		} else {
			b := bytecode.DecodeNeqNull(in)
			dst, operand = b.Dst, b.Operand
		}
		yes, no := jit.NewLabel(), jit.NewLabel()
		c.nullish(operand, yes, no)
		c.storeBranches(dst, yes, no, op == bytecode.OpEqNullID)
		return true
	case bytecode.OpJeqPtrID, bytecode.OpJneqPtrID:
		// sameCell: the same pointer, and not a value without one.
		var value, special bytecode.VirtualRegister
		var label bytecode.BoundLabel
		if op == bytecode.OpJeqPtrID {
			b := bytecode.DecodeJeqPtr(in)
			value, special, label = b.Value, b.SpecialPointer, b.TargetLabel
		} else {
			b := bytecode.DecodeJneqPtr(in)
			value, special, label = b.Value, b.SpecialPointer, b.TargetLabel
		}
		c.loadValue(jit.S0, jit.S1, value)
		c.loadValue(jit.S2, jit.S3, special)
		same, differ := jit.NewLabel(), jit.NewLabel()
		m.BranchZero(jit.S0, differ)
		m.Branch64(jit.EQ, jit.S0, jit.S2, same)
		m.B(differ)
		if op == bytecode.OpJeqPtrID {
			m.Bind(same)
			m.B(c.target(pc, label))
			m.Bind(differ)
		} else {
			m.Bind(differ)
			m.B(c.target(pc, label))
			m.Bind(same)
		}
		return true
	case bytecode.OpStricteqID, bytecode.OpNstricteqID:
		var dst, lhs, rhs bytecode.VirtualRegister
		if op == bytecode.OpStricteqID {
			b := bytecode.DecodeStricteq(in)
			dst, lhs, rhs = b.Dst, b.Lhs, b.Rhs
		} else {
			b := bytecode.DecodeNstricteq(in)
			dst, lhs, rhs = b.Dst, b.Lhs, b.Rhs
		}
		equal, notEqual := jit.NewLabel(), jit.NewLabel()
		c.strictEquals(lhs, rhs, equal, notEqual, c.slow(pc, next))
		c.storeBranches(dst, equal, notEqual, op == bytecode.OpStricteqID)
		return true
	case bytecode.OpJstricteqID, bytecode.OpJnstricteqID:
		var lhs, rhs bytecode.VirtualRegister
		var label bytecode.BoundLabel
		if op == bytecode.OpJstricteqID {
			b := bytecode.DecodeJstricteq(in)
			lhs, rhs, label = b.Lhs, b.Rhs, b.TargetLabel
		} else {
			b := bytecode.DecodeJnstricteq(in)
			lhs, rhs, label = b.Lhs, b.Rhs, b.TargetLabel
		}
		equal, notEqual := jit.NewLabel(), jit.NewLabel()
		c.strictEquals(lhs, rhs, equal, notEqual, c.slow(pc, next, pc+int(label)))
		if op == bytecode.OpJstricteqID {
			m.Bind(equal)
			m.B(c.target(pc, label))
			m.Bind(notEqual)
		} else {
			m.Bind(notEqual)
			m.B(c.target(pc, label))
			m.Bind(equal)
		}
		return true
	case bytecode.OpEqID, bytecode.OpNeqID:
		var dst, lhs, rhs bytecode.VirtualRegister
		if op == bytecode.OpEqID {
			b := bytecode.DecodeEq(in)
			dst, lhs, rhs = b.Dst, b.Lhs, b.Rhs
		} else {
			b := bytecode.DecodeNeq(in)
			dst, lhs, rhs = b.Dst, b.Lhs, b.Rhs
		}
		equal, notEqual := jit.NewLabel(), jit.NewLabel()
		c.looseEquals(lhs, rhs, equal, notEqual, c.slow(pc, next))
		c.storeBranches(dst, equal, notEqual, op == bytecode.OpEqID)
		return true
	case bytecode.OpJeqID, bytecode.OpJneqID:
		var lhs, rhs bytecode.VirtualRegister
		var label bytecode.BoundLabel
		if op == bytecode.OpJeqID {
			b := bytecode.DecodeJeq(in)
			lhs, rhs, label = b.Lhs, b.Rhs, b.TargetLabel
		} else {
			b := bytecode.DecodeJneq(in)
			lhs, rhs, label = b.Lhs, b.Rhs, b.TargetLabel
		}
		equal, notEqual := jit.NewLabel(), jit.NewLabel()
		c.looseEquals(lhs, rhs, equal, notEqual, c.slow(pc, next, pc+int(label)))
		if op == bytecode.OpJeqID {
			m.Bind(equal)
			m.B(c.target(pc, label))
			m.Bind(notEqual)
		} else {
			m.Bind(notEqual)
			m.B(c.target(pc, label))
			m.Bind(equal)
		}
		return true
	case bytecode.OpTypeofID:
		b := bytecode.DecodeTypeof(in)
		c.typeOf(b.Dst, b.Value, c.slow(pc, next))
		return true
	case bytecode.OpTypeofIsObjectID, bytecode.OpTypeofIsFunctionID:
		// null and objects that are neither callable nor [[IsHTMLDDA]];
		// callable objects that are not [[IsHTMLDDA]].
		var dst, operand bytecode.VirtualRegister
		if op == bytecode.OpTypeofIsObjectID {
			b := bytecode.DecodeTypeofIsObject(in)
			dst, operand = b.Dst, b.Operand
		} else {
			b := bytecode.DecodeTypeofIsFunction(in)
			dst, operand = b.Dst, b.Operand
		}
		object, function, no := jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
		c.loadValue(jit.S0, jit.S1, operand)
		if op == bytecode.OpTypeofIsObjectID {
			m.Branch64Imm(jit.EQ, jit.S1, uint32(tagNull), object)
		}
		m.Branch64Imm(jit.NE, jit.S1, uint32(tagObject), no)
		m.Load8(jit.S2, jit.S0, offHTMLDDA)
		m.BranchNonZero(jit.S2, no)
		m.Load64(jit.S2, jit.S0, offFn)
		m.BranchNonZero(jit.S2, function)
		m.B(object)
		if op == bytecode.OpTypeofIsObjectID {
			m.Bind(function)
			m.B(no)
			c.storeBranches(dst, object, no, true)
		} else {
			m.Bind(object)
			m.B(no)
			c.storeBranches(dst, function, no, true)
		}
		return true
	case bytecode.OpIsNumberID:
		b := bytecode.DecodeIsNumber(in)
		yes, no := jit.NewLabel(), jit.NewLabel()
		c.loadValue(jit.S0, jit.S1, b.Operand)
		m.Branch64Imm(jit.UGT, jit.S1, maxTag, yes)
		m.B(no)
		c.storeBranches(b.Dst, yes, no, true)
		return true
	case bytecode.OpIsObjectID:
		b := bytecode.DecodeIsObject(in)
		yes, no := jit.NewLabel(), jit.NewLabel()
		c.loadValue(jit.S0, jit.S1, b.Operand)
		m.Branch64Imm(jit.EQ, jit.S1, uint32(tagObject), yes)
		m.B(no)
		c.storeBranches(b.Dst, yes, no, true)
		return true
	case bytecode.OpTypeofIsUndefinedID:
		// undefined, and [[IsHTMLDDA]] objects.
		b := bytecode.DecodeTypeofIsUndefined(in)
		yes, no := jit.NewLabel(), jit.NewLabel()
		c.loadValue(jit.S0, jit.S1, b.Operand)
		m.Branch64Imm(jit.EQ, jit.S1, uint32(tagUndefined), yes)
		m.Branch64Imm(jit.NE, jit.S1, uint32(tagObject), no)
		m.Load8(jit.S2, jit.S0, offHTMLDDA)
		m.BranchNonZero(jit.S2, yes)
		m.B(no)
		c.storeBranches(b.Dst, yes, no, true)
		return true
	case bytecode.OpIsCellWithTypeID:
		// Strings, symbols and BigInts by their tag; the kinds of objects in
		// Go.
		b := bytecode.DecodeIsCellWithType(in)
		var tag uint64
		switch b.Type {
		case bytecode.StringType:
			tag = tagString
		case bytecode.SymbolType:
			tag = tagSymbol
		case bytecode.HeapBigIntType:
			tag = tagBigInt
		default:
			m.B(c.slow(pc, next))
			return true
		}
		yes, no := jit.NewLabel(), jit.NewLabel()
		c.loadValue(jit.S0, jit.S1, b.Operand)
		m.Branch64Imm(jit.EQ, jit.S1, uint32(tag), yes)
		m.B(no)
		c.storeBranches(b.Dst, yes, no, true)
		return true
	case bytecode.OpToStringID:
		b := bytecode.DecodeToString(in)
		c.loadValue(jit.S0, jit.S1, b.Operand)
		m.Branch64Imm(jit.NE, jit.S1, uint32(tagString), c.slow(pc, next))
		c.storeValue(b.Dst, jit.S0, jit.S1)
		return true
	case bytecode.OpGetParentScopeID:
		b := bytecode.DecodeGetParentScope(in)
		c.loadValue(jit.S0, jit.S1, b.Scope)
		m.Load64(jit.S0, jit.S0, offScopeParent)
		m.MovImm(jit.S1, tagScope)
		c.storeValue(b.Dst, jit.S0, jit.S1)
		return true
	case bytecode.OpInstanceofID:
		b := bytecode.DecodeInstanceof(in)
		c.instanceOf(pc, next, b)
		return true
	case bytecode.OpToPrimitiveID:
		// Values other than objects are primitives already.
		b := bytecode.DecodeToPrimitive(in)
		c.loadValue(jit.S0, jit.S1, b.Src)
		m.Branch64Imm(jit.EQ, jit.S1, uint32(tagObject), c.slow(pc, next))
		c.storeValue(b.Dst, jit.S0, jit.S1)
		return true
	case bytecode.OpRetID:
		// The result goes to the frame's ret, and Go (or a calling function's
		// generated code) takes it from there.
		b := bytecode.DecodeRet(in)
		c.loadValue(jit.S0, jit.S1, b.Value)
		m.Store64(jit.S0, rFrame, offFrameRet)
		m.Store64(jit.S1, rFrame, offFrameRet+8)
		m.Return(uint64(pc)<<jitExitBits | jitExitReturn)
		return true
	case bytecode.OpConstructID:
		// new: a direct call of the cached constructor's construct code
		// (jit_calls.go), or Go's.
		if !jit.NativeCalls {
			m.B(c.slow(pc, next))
			return true
		}
		b := bytecode.DecodeConstruct(in)
		c.nativeCall(pc, b.Callee, b.Argc, b.Argv, b.Dst, true)
		return true
	case bytecode.OpTailCallVarargsID:
		// Like call_varargs (the ret after it returns the result); the
		// interpreter makes the proper tail call when the stack is deep.
		m.B(c.slow(pc, next))
		return true
	case bytecode.OpNewArrayID:
		b := bytecode.DecodeNewArray(in)
		slow := c.slow(pc, next)
		if b.Argc > baseVectorLength {
			m.B(slow)
			return true
		}
		c.newSmallArray(b.Dst, b.Argv, int(b.Argc), slow)
		return true
	case bytecode.OpNewArrayWithSizeID:
		b := bytecode.DecodeNewArrayWithSize(in)
		c.newArrayWithSize(b.Dst, b.Length, c.slow(pc, next))
		return true
	case bytecode.OpIteratorNextID:
		c.iteratorNext(bytecode.DecodeIteratorNext(in), c.slow(pc, next))
		return true
	case bytecode.OpNewRegExpID:
		b := bytecode.DecodeNewRegExp(in)
		c.newRegExp(b.Dst, b.Regexp, c.slow(pc, next))
		return true
	case bytecode.OpNewFuncID, bytecode.OpNewFuncExpID:
		var dst, scope bytecode.VirtualRegister
		var exec *bytecode.UnlinkedFunctionExecutable
		if op == bytecode.OpNewFuncID {
			b := bytecode.DecodeNewFunc(in)
			dst, scope, exec = b.Dst, b.Scope, c.cb.u.FunctionDecls[b.FunctionDecl]
		} else {
			b := bytecode.DecodeNewFuncExp(in)
			dst, scope, exec = b.Dst, b.Scope, c.cb.u.FunctionExprs[b.FunctionDecl]
		}
		c.newClosure(exec, dst, scope, c.slow(pc, next))
		return true
	case bytecode.OpCreateLexicalEnvironmentID:
		b := bytecode.DecodeCreateLexicalEnvironment(in)
		c.newScope(b.Dst, b.Scope, b.SymbolTable, b.InitialValue, c.slow(pc, next))
		return true
	case bytecode.OpCallVarargsID:
		// f.apply(this, arguments) without an arguments object: a direct
		// call with the frame's own arguments (raytrace's Class.create
		// constructors made 1.6 million a run through Go); other
		// argument lists take the slow path.
		b := bytecode.DecodeCallVarargs(in)
		if v, ok := c.constant(b.Arguments); ok && v == forwardedArguments && b.FirstVarArg == 0 && jit.NativeCalls && c.inline == nil {
			c.nativeCallFrom(pc, b.Callee, b.ThisValue, 0, b.Dst, true, true)
			return true
		}
		m.B(c.slow(pc, next))
		return true
	case bytecode.OpStrcatID,
		bytecode.OpCreateDirectArgumentsID,
		bytecode.OpGetFromArgumentsID, bytecode.OpPutToArgumentsID:
		// Always the slow path: allocation, calls through
		// Function.prototype.apply and arguments objects.
		m.B(c.slow(pc, next))
		return true
	}
	return false
}

func boolWord(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// typeOf compiles typeof: the result string by the value's tag; for
// objects by [[IsHTMLDDA]] and whether they are callable. Engine-internal
// values take the slow path.
func (c *jitCompiler) typeOf(dst, value bytecode.VirtualRegister, slow *jit.Label) {
	m := &c.m
	done := jit.NewLabel()
	c.loadValue(jit.S0, jit.S1, value)
	result := func(name string) *jit.Label {
		l := jit.NewLabel()
		c.cold = append(c.cold, func() {
			m.Bind(l)
			m.MovImm(jit.S2, uint64(uintptr(unsafe.Pointer(typeofStrings[name]))))
			m.B(done)
		})
		return l
	}
	number, object := result("number"), result("object")
	m.Branch64Imm(jit.UGT, jit.S1, maxTag, number)
	m.Branch64Imm(jit.EQ, jit.S1, uint32(tagUndefined), result("undefined"))
	m.Branch64Imm(jit.EQ, jit.S1, uint32(tagNull), object)
	m.Branch64Imm(jit.EQ, jit.S1, uint32(tagTrue), result("boolean"))
	m.Branch64Imm(jit.EQ, jit.S1, uint32(tagFalse), result("boolean"))
	m.Branch64Imm(jit.EQ, jit.S1, uint32(tagString), result("string"))
	m.Branch64Imm(jit.EQ, jit.S1, uint32(tagSymbol), result("symbol"))
	m.Branch64Imm(jit.EQ, jit.S1, uint32(tagBigInt), result("bigint"))
	m.Branch64Imm(jit.NE, jit.S1, uint32(tagObject), slow)
	m.Load8(jit.S2, jit.S0, offHTMLDDA)
	m.BranchNonZero(jit.S2, result("undefined"))
	m.Load64(jit.S2, jit.S0, offFn)
	m.BranchNonZero(jit.S2, result("function"))
	m.B(object)
	m.Bind(done)
	m.MovImm(jit.S3, uint64(tagString))
	c.storeValue(dst, jit.S2, jit.S3)
}

// storeBranches binds yes and no and stores to dst whether yes was taken
// (when is true) or whether no was (when is false).
func (c *jitCompiler) storeBranches(dst bytecode.VirtualRegister, yes, no *jit.Label, when bool) {
	m := &c.m
	done := jit.NewLabel()
	m.Bind(yes)
	m.MovImm(jit.S2, boolWord(when))
	m.B(done)
	m.Bind(no)
	m.MovImm(jit.S2, boolWord(!when))
	m.Bind(done)
	c.storeBool(dst, jit.S2)
}

// nullish branches to yes if register r is undefined, null or an
// [[IsHTMLDDA]] object (isNullishOrHTMLDDA), else to no.
func (c *jitCompiler) nullish(r bytecode.VirtualRegister, yes, no *jit.Label) {
	m := &c.m
	c.loadValue(jit.S0, jit.S1, r)
	m.Branch64Imm(jit.EQ, jit.S1, uint32(tagUndefined), yes)
	m.Branch64Imm(jit.EQ, jit.S1, uint32(tagNull), yes)
	m.Branch64Imm(jit.NE, jit.S1, uint32(tagObject), no)
	m.Load8(jit.S2, jit.S0, offHTMLDDA)
	m.BranchNonZero(jit.S2, yes)
	m.B(no)
}

// strictEquals branches to equal or notEqual by IsStrictlyEqual of the
// registers lhs and rhs, or to slow for the cases it does not decide (two
// BigInts, and strings stringEquals does not compare, at different
// addresses).
func (c *jitCompiler) strictEquals(lhs, rhs bytecode.VirtualRegister, equal, notEqual, slow *jit.Label) {
	m := &c.m
	c.loadValue(jit.S0, jit.S1, lhs)
	c.loadValue(jit.S2, jit.S3, rhs)
	lhsTag := jit.NewLabel()
	m.Branch64Imm(jit.ULE, jit.S1, maxTag, lhsTag)
	// lhs is a number: equal only to a number of the same value (NaN is
	// unequal to itself, -0 equal to +0).
	m.Branch64Imm(jit.ULE, jit.S3, maxTag, notEqual)
	m.Xor64(jit.S1, jit.S1, rMask)
	m.Xor64(jit.S3, jit.S3, rMask)
	m.MovToF(0, jit.S1)
	m.MovToF(1, jit.S3)
	m.FBranch(jit.FEQ, 0, 1, equal)
	m.B(notEqual)
	m.Bind(lhsTag)
	m.Branch64Imm(jit.UGT, jit.S3, maxTag, notEqual)
	m.Branch64(jit.NE, jit.S1, jit.S3, notEqual) // different kinds
	c.sameKindEquals(equal, notEqual, slow)
}

// sameKindEquals branches by the equality of the values S0, S1 and S2, S3
// of the same kind (tag), on which loose and strict equality agree.
func (c *jitCompiler) sameKindEquals(equal, notEqual, slow *jit.Label) {
	m := &c.m
	// undefined, null, the same boolean and empty have no pointer; objects
	// and symbols are equal only to themselves.
	m.Branch64(jit.EQ, jit.S0, jit.S2, equal)
	str := jit.NewLabel()
	m.Branch64Imm(jit.EQ, jit.S1, uint32(tagString), str)
	m.Branch64Imm(jit.EQ, jit.S1, uint32(tagBigInt), slow)
	m.B(notEqual)
	c.cold = append(c.cold, func() {
		m.Bind(str)
		c.stringEquals(equal, notEqual, slow)
	})
}

var (
	offStrASCII   = int(unsafe.Offsetof(String{}.data))
	offStrIsASCII = int(unsafe.Offsetof(String{}.isASCII))
	offStrLeft    = int(unsafe.Offsetof(String{}.left))
)

// maxJITStringEquals is the longest strings stringEquals compares; Go's
// memequal is faster for longer ones.
const maxJITStringEquals = 256

// stringEquals branches by the equality of the different strings S0 and
// S2: of different lengths they differ; flat ASCII strings (not ropes) up
// to maxJITStringEquals long are compared here, 8 bytes at a time; others
// take the slow path. Clobbers S4-S9.
func (c *jitCompiler) stringEquals(equal, notEqual, slow *jit.Label) {
	m := &c.m
	m.Load64(jit.S4, jit.S0, offStrLength)
	m.Load64(jit.S5, jit.S2, offStrLength)
	m.Branch64(jit.NE, jit.S4, jit.S5, notEqual)
	m.Branch64Imm(jit.UGT, jit.S4, maxJITStringEquals, slow)
	for _, s := range []jit.R{jit.S0, jit.S2} {
		m.Load64(jit.S6, s, offStrLeft)
		m.BranchNonZero(jit.S6, slow)
		m.Load8(jit.S6, s, offStrIsASCII)
		m.BranchZero(jit.S6, slow)
	}
	m.Load64(jit.S6, jit.S0, offStrASCII)
	m.Load64(jit.S7, jit.S2, offStrASCII)
	words, bytes := jit.NewLabel(), jit.NewLabel()
	m.Bind(words)
	m.Branch64Imm(jit.ULT, jit.S4, 8, bytes)
	m.Load64(jit.S8, jit.S6, 0)
	m.Load64(jit.S9, jit.S7, 0)
	m.Branch64(jit.NE, jit.S8, jit.S9, notEqual)
	m.AddImm64(jit.S6, jit.S6, 8)
	m.AddImm64(jit.S7, jit.S7, 8)
	m.SubImm64(jit.S4, jit.S4, 8)
	m.B(words)
	m.Bind(bytes)
	m.BranchZero(jit.S4, equal)
	m.Load8(jit.S8, jit.S6, 0)
	m.Load8(jit.S9, jit.S7, 0)
	m.Branch64(jit.NE, jit.S8, jit.S9, notEqual)
	m.AddImm64(jit.S6, jit.S6, 1)
	m.AddImm64(jit.S7, jit.S7, 1)
	m.SubImm64(jit.S4, jit.S4, 1)
	m.B(bytes)
}

// looseEquals branches to equal or notEqual by IsLooselyEqual of the
// registers lhs and rhs, or to slow for the cases it does not decide (those
// of strictEquals, and the ones with conversions).
func (c *jitCompiler) looseEquals(lhs, rhs bytecode.VirtualRegister, equal, notEqual, slow *jit.Label) {
	m := &c.m
	// x == 0 and the like: a number constant needs no checks.
	if v, ok := c.constant(lhs); ok && v.IsNumber() {
		lhs, rhs = rhs, lhs
	}
	if v, ok := c.constant(rhs); ok && v.IsNumber() {
		if c.loadNum(0, lhs, slow) && c.loadNum(1, rhs, slow) {
			m.FBranch(jit.FEQ, 0, 1, equal)
			m.B(notEqual)
			return
		}
		m.B(slow)
		return
	}
	c.loadValue(jit.S0, jit.S1, lhs)
	c.loadValue(jit.S2, jit.S3, rhs)
	lhsTag := jit.NewLabel()
	m.Branch64Imm(jit.ULE, jit.S1, maxTag, lhsTag)
	m.Branch64Imm(jit.ULE, jit.S3, maxTag, slow)
	m.Xor64(jit.S1, jit.S1, rMask)
	m.Xor64(jit.S3, jit.S3, rMask)
	m.MovToF(0, jit.S1)
	m.MovToF(1, jit.S3)
	m.FBranch(jit.FEQ, 0, 1, equal)
	m.B(notEqual)
	m.Bind(lhsTag)
	m.Branch64Imm(jit.UGT, jit.S3, maxTag, slow)
	differ := jit.NewLabel()
	m.Branch64(jit.NE, jit.S1, jit.S3, differ)
	c.sameKindEquals(equal, notEqual, slow)
	// Different kinds: undefined and null (tags 0 and 1) equal each other,
	// and no other value but [[IsHTMLDDA]] objects.
	// (x == undefined with an object x: typescript compared so 0.9 million
	// times a run in the slow path.)
	m.Bind(differ)
	lhsNullish, ddaL, ddaR := jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
	m.Branch64Imm(jit.ULE, jit.S1, uint32(tagNull), lhsNullish)
	m.Branch64Imm(jit.UGT, jit.S3, uint32(tagNull), slow)
	m.Branch64Imm(jit.EQ, jit.S1, uint32(tagObject), ddaL)
	m.B(notEqual)
	m.Bind(lhsNullish)
	m.Branch64Imm(jit.ULE, jit.S3, uint32(tagNull), equal)
	m.Branch64Imm(jit.EQ, jit.S3, uint32(tagObject), ddaR)
	m.B(notEqual)
	m.Bind(ddaL)
	m.Load8(jit.S4, jit.S0, offHTMLDDA)
	m.BranchNonZero(jit.S4, equal)
	m.B(notEqual)
	m.Bind(ddaR)
	m.Load8(jit.S4, jit.S2, offHTMLDDA)
	m.BranchNonZero(jit.S4, equal)
	m.B(notEqual)
}

// Process constants for instanceof: the interface words of bound
// functions, and of the objects whose [[GetPrototypeOf]] is not their
// proto field.
var (
	boundFunctionItab = func() uintptr {
		var f functionImpl = (*boundFunction)(nil)
		return (*[2]uintptr)(unsafe.Pointer(&f))[0]
	}()
	proxyObjectItab = func() uintptr {
		var e exoticObject = proxyObject{}
		return (*[2]uintptr)(unsafe.Pointer(&e))[0]
	}()
	moduleNamespaceItab = func() uintptr {
		var e exoticObject = (*moduleNamespace)(nil)
		return (*[2]uintptr)(unsafe.Pointer(&e))[0]
	}()
	offFn = int(unsafe.Offsetof(Object{}.fn))
)

// instanceOf compiles instanceof for the common case: a function that is
// not bound, whose [[Prototype]] is the realm's Function.prototype (whose
// @@hasInstance is OrdinaryHasInstance and can be neither changed nor
// deleted) and which has no own @@hasInstance (the cache's shape: Go fills
// it only then), with its prototype data property through the cache; the
// value's prototype chain is walked here. Everything else takes the slow
// path.
func (c *jitCompiler) instanceOf(pc, next int, b bytecode.OpInstanceof) {
	m := &c.m
	slow := c.slow(pc, next)
	if !c.loadObject(b.Constructor, slow) {
		m.B(slow)
		return
	}
	m.Load64(jit.S4, jit.S0, offFn)
	m.BranchZero(jit.S4, slow)
	m.MovImm(jit.S5, uint64(boundFunctionItab))
	m.Branch64(jit.EQ, jit.S4, jit.S5, slow)
	m.Load64(jit.S4, jit.S0, offProto)
	m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(c.cb.realm.FunctionPrototype))))
	m.Branch64(jit.NE, jit.S4, jit.S5, slow)
	c.ordinaryFor(prototypeKey, slow)
	c.cachedGetInto(&c.cb.instanceofCaches[b.MetadataID], b.Dst, false, slow)
	m.Branch64Imm(jit.NE, jit.S5, uint32(tagObject), slow)
	// S4 = the prototype; walk the value's chain in S0.
	yes, no := jit.NewLabel(), jit.NewLabel()
	c.loadValue(jit.S0, jit.S1, b.Value)
	m.Branch64Imm(jit.NE, jit.S1, uint32(tagObject), no)
	loop, ordinary := jit.NewLabel(), jit.NewLabel()
	m.Bind(loop)
	m.Load64(jit.S6, jit.S0, offExotic)
	m.BranchZero(jit.S6, ordinary)
	m.MovImm(jit.S7, uint64(proxyObjectItab))
	m.Branch64(jit.EQ, jit.S6, jit.S7, slow)
	m.MovImm(jit.S7, uint64(moduleNamespaceItab))
	m.Branch64(jit.EQ, jit.S6, jit.S7, slow)
	m.Bind(ordinary)
	m.Load64(jit.S0, jit.S0, offProto)
	m.BranchZero(jit.S0, no)
	m.Branch64(jit.EQ, jit.S0, jit.S4, yes)
	m.B(loop)
	c.storeBranches(b.Dst, yes, no, true)
}

// fillInstanceofCache fills the cache of an instanceof whose constructor
// generated code can handle (instanceOf) with its prototype property.
func (vm *VM) fillInstanceofCache(cache *getByIdCache, r *Realm, ctor Value) {
	o := ctor.AsObject()
	if o == nil || o.fn == nil || o.proto != r.FunctionPrototype || !icOrdinary(o, prototypeKey) {
		return
	}
	if _, bound := o.fn.(*boundFunction); bound || o.props.find(SymKey(SymbolHasInstance)) >= 0 {
		return
	}
	if cache.shape == o.props.shape && cache.version == o.props.shape.version {
		return
	}
	var c getByIdCache
	vm.fillGetById(&c, o, prototypeKey, false)
	if c.shape != nil && c.depth == 0 && !c.accessor {
		*cache = c
	}
}

// jitSlowOps are the instructions whose machine code takes the slow path
// for the operands it does not handle (instruction's exit); jitSlowPath
// runs them.
var jitSlowOps = func() (set [bytecode.NumberOfBytecodeIDs]bool) {
	for _, op := range []bytecode.OpcodeID{
		bytecode.OpSubID, bytecode.OpMulID, bytecode.OpDivID, bytecode.OpModID, bytecode.OpNegateID,
		bytecode.OpIncID, bytecode.OpDecID, bytecode.OpBitandID, bytecode.OpBitorID, bytecode.OpBitxorID,
		bytecode.OpLshiftID, bytecode.OpRshiftID, bytecode.OpUrshiftID, bytecode.OpBitnotID, bytecode.OpUnsignedID,
		bytecode.OpBelowID, bytecode.OpBeloweqID, bytecode.OpJbelowID, bytecode.OpJbeloweqID,
		bytecode.OpLessID, bytecode.OpLesseqID, bytecode.OpGreaterID, bytecode.OpGreatereqID,
		bytecode.OpJlessID, bytecode.OpJlesseqID, bytecode.OpJgreaterID, bytecode.OpJgreatereqID,
		bytecode.OpJnlessID, bytecode.OpJnlesseqID, bytecode.OpJngreaterID, bytecode.OpJngreatereqID,
		bytecode.OpJtrueID, bytecode.OpJfalseID, bytecode.OpToNumberID, bytecode.OpToNumericID,
		bytecode.OpToPropertyKeyOrNumberID, bytecode.OpResolveScopeID, bytecode.OpGetFromScopeID,
		bytecode.OpPutToScopeID,
	} {
		set[op] = true
	}
	return
}()

// jitColdOps are the instructions of VM.runCold (which neither are hot
// nor change the control flow) whose slow path is runCold itself; catch,
// yield and unreachable are not among them.
var jitColdOps = func() (set [bytecode.NumberOfBytecodeIDs]bool) {
	for _, op := range []bytecode.OpcodeID{
		bytecode.OpAsyncIteratorNextID,
		bytecode.OpAsyncIteratorOpenID,
		bytecode.OpCallDirectEvalID,
		bytecode.OpCheckPrivateBrandID,
		bytecode.OpCreateAsyncGeneratorID,
		bytecode.OpCreateClonedArgumentsID,
		bytecode.OpCreateGeneratorID,
		bytecode.OpCreateScopedArgumentsID,
		bytecode.OpDebugID,
		bytecode.OpDefineAccessorPropertyID,
		bytecode.OpDelByIdID,
		bytecode.OpDelByValID,
		bytecode.OpEnumeratorPutByValID,
		bytecode.OpGetByIdWithThisID,
		bytecode.OpGetByValWithThisID,
		bytecode.OpGetInternalFieldID,
		bytecode.OpGetPrototypeOfID,
		bytecode.OpHasPrivateBrandID,
		bytecode.OpHasPrivateNameID,
		bytecode.OpHasStructureWithFlagsID,
		bytecode.OpIsBigIntID,
		bytecode.OpIsBooleanID,
		bytecode.OpIsConstructorID,
		bytecode.OpIteratorCloseCheckID,
		bytecode.OpNewArrayWithSpeciesID,
		bytecode.OpNewArrayWithSpreadID,
		bytecode.OpNewAsyncFunctionGeneratorID,
		bytecode.OpNewGeneratorID,
		bytecode.OpNewPromiseID,
		bytecode.OpPowID,
		bytecode.OpPushWithScopeID,
		bytecode.OpPutByIdWithThisID,
		bytecode.OpPutByValWithThisID,
		bytecode.OpPutGetterByIdID,
		bytecode.OpPutGetterByValID,
		bytecode.OpPutGetterSetterByIdID,
		bytecode.OpPutInternalFieldID,
		bytecode.OpPutSetterByIdID,
		bytecode.OpPutSetterByValID,
		bytecode.OpResolveScopeForHoistingFuncDeclInEvalID,
		bytecode.OpSetFunctionNameID,
		bytecode.OpSetPrivateBrandID,
		bytecode.OpSpreadID,
		bytecode.OpSuperConstructID,
		bytecode.OpSuperConstructVarargsID,
		bytecode.OpToPropertyKeyID,
	} {
		set[op] = true
	}
	return
}()

// switchTargets returns the pcs a switch instruction at pc may jump to
// (its jump table's and its default), or false for other instructions.
func switchTargets(cb *CodeBlock, pc int, in []uint32) ([]int, bool) {
	var targets []int
	add := func(off int32) {
		if off != 0 {
			targets = append(targets, pc+int(off))
		}
	}
	simple := func(t *bytecode.SimpleJumpTable) {
		add(t.DefaultOffset)
		for i, off := range t.BranchOffsets {
			if t.IsList && i%2 == 0 {
				continue // the keys of a list
			}
			add(off)
		}
	}
	switch bytecode.OpcodeID(in[0]) {
	case bytecode.OpSwitchImmID:
		simple(&cb.u.SwitchJumpTables[bytecode.DecodeSwitchImm(in).TableIndex])
	case bytecode.OpSwitchCharID:
		simple(&cb.u.SwitchJumpTables[bytecode.DecodeSwitchChar(in).TableIndex])
	case bytecode.OpSwitchStringID:
		t := &cb.u.StringSwitchJumpTables[bytecode.DecodeSwitchString(in).TableIndex]
		add(t.DefaultOffset)
		for _, e := range t.OffsetTable {
			add(e.BranchOffset)
		}
	default:
		return nil, false
	}
	return targets, true
}

// jitSlowPath runs the instruction at pc of f as the interpreter does and
// returns the pc to continue at.
func (vm *VM) jitSlowPath(f *Frame, pc int) int {
	cb := f.cb
	r := cb.realm
	in := cb.u.Instructions[pc:]
	op := bytecode.OpcodeID(in[0])
	next := pc + op.Length()
	if _, ops, _, ok := compareJump(in); ok {
		vm.noteUndefinedCompare(f, pc, ops[0], ops[1])
	} else if lhs, rhs, ok := compareOperands(in); ok {
		vm.noteUndefinedCompare(f, pc, lhs, rhs)
	}
	switch op {
	case bytecode.OpTailCallID:
		// An ordinary call while the stack is shallow; deeper, the
		// interpreter makes the proper tail call (runFunction's loop, or
		// finishNative's), so that tail recursion does not grow the stack.
		if vm.depth >= vm.MaxDepth/4 {
			return pc
		}
		b := bytecode.DecodeTailCall(in)
		if jit.NativeCalls {
			vm.fillCallIC(f, pc, *f.ref(b.Callee), int(b.Argc), false)
		}
		this, args := f.callArgs(b.Argv, b.Argc)
		f.set(b.Dst, vm.callValue((*f.ref(b.Callee)), this, args))
	case bytecode.OpCallID, bytecode.OpCallIgnoreResultID:
		if jit.NativeCalls {
			// Cache the callee for generated code to call it directly.
			if op == bytecode.OpCallID {
				b := bytecode.DecodeCall(in)
				vm.fillCallIC(f, pc, *f.ref(b.Callee), int(b.Argc), false)
			} else {
				b := bytecode.DecodeCallIgnoreResult(in)
				vm.fillCallIC(f, pc, *f.ref(b.Callee), int(b.Argc), false)
			}
		}
		return pc + vm.jitCall(f, in)
	case bytecode.OpToThisID:
		b := bytecode.DecodeToThis(in)
		f.set(b.SrcDst, vm.toThis((*f.ref(b.SrcDst)), b.EcmaMode))
	case bytecode.OpCheckTdzID:
		b := bytecode.DecodeCheckTdz(in)
		if (*f.ref(b.TargetVirtualRegister)).IsEmpty() {
			if b.TargetVirtualRegister == bytecode.CallFrameSlotThisArgument {
				vm.throwReferenceError("'super()' must be called in derived constructor before accessing |this| or returning non-object.")
			}
			vm.throwReferenceError("Cannot access uninitialized variable.")
		}
	case bytecode.OpNotID:
		b := bytecode.DecodeNot(in)
		f.set(b.Dst, Bool(!(*f.ref(b.Operand)).ToBoolean()))
	case bytecode.OpStricteqID:
		b := bytecode.DecodeStricteq(in)
		f.set(b.Dst, Bool(StrictEquals((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
	case bytecode.OpNstricteqID:
		b := bytecode.DecodeNstricteq(in)
		f.set(b.Dst, Bool(!StrictEquals((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
	case bytecode.OpJstricteqID:
		b := bytecode.DecodeJstricteq(in)
		if StrictEquals((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpJnstricteqID:
		b := bytecode.DecodeJnstricteq(in)
		if !StrictEquals((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpGetByIdID:
		b := bytecode.DecodeGetById(in)
		base, k := *f.ref(b.Base), f.key(b.Property)
		f.set(b.Dst, vm.getById(&cb.getByIdCaches[b.MetadataID], base, k))
		vm.fillGetterIC(f, pc, base, k)
	case bytecode.OpPutByIdID:
		b := bytecode.DecodePutById(in)
		vm.putById(&cb.putByIdCaches[b.MetadataID], (*f.ref(b.Base)), f.key(b.Property), (*f.ref(b.Value)),
			b.Flags.IsDirect(), b.Flags.ECMAMode().IsStrict())
		vm.fillSlotPools()
	case bytecode.OpGetByValID:
		b := bytecode.DecodeGetByVal(in)
		base, p := *f.ref(b.Base), *f.ref(b.Property)
		f.set(b.Dst, vm.getByVal(base, p))
		vm.fillMegaGetByVal(base, p)
	case bytecode.OpEnumeratorGetByValID:
		b := bytecode.DecodeEnumeratorGetByVal(in)
		base, p := *f.ref(b.Base), *f.ref(b.PropertyName)
		f.set(b.Dst, vm.getByVal(base, p))
		vm.fillMegaGetByVal(base, p)
	case bytecode.OpPutByValID:
		b := bytecode.DecodePutByVal(in)
		base, p := *f.ref(b.Base), *f.ref(b.Property)
		vm.putByVal(base, p, (*f.ref(b.Value)), b.EcmaMode.IsStrict())
		vm.fillMegaPutByVal(base, p)
	case bytecode.OpGetLengthID:
		b := bytecode.DecodeGetLength(in)
		base := *f.ref(b.Base)
		switch {
		case base.IsString():
			f.set(b.Dst, Int(base.StringRef().Length()))
		case base.IsObject() && base.ObjectRef().class == ClassArray:
			if a, ok := base.ObjectRef().exotic.(*arrayObject); ok {
				f.set(b.Dst, Number(float64(a.length)))
				break
			}
			f.set(b.Dst, vm.getByKey(base, PropertyKey{name: lengthIdent}))
		default:
			f.set(b.Dst, vm.getById(&cb.getLengthCaches[b.MetadataID], base, PropertyKey{name: lengthIdent}))
		}
	case bytecode.OpEqID:
		b := bytecode.DecodeEq(in)
		f.set(b.Dst, Bool(vm.IsLooselyEqual((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
	case bytecode.OpNeqID:
		b := bytecode.DecodeNeq(in)
		f.set(b.Dst, Bool(!vm.IsLooselyEqual((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
	case bytecode.OpJeqID:
		b := bytecode.DecodeJeq(in)
		if vm.IsLooselyEqual((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpJneqID:
		b := bytecode.DecodeJneq(in)
		if !vm.IsLooselyEqual((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpAddID:
		b := bytecode.DecodeAdd(in)
		f.set(b.Dst, vm.Add((*f.ref(b.Lhs)), (*f.ref(b.Rhs))))
		if vm.jitStrings != nil && vm.jitStrings.n == 0 {
			vm.fillStringPool() // for appendString
		}
	case bytecode.OpToPrimitiveID:
		b := bytecode.DecodeToPrimitive(in)
		f.set(b.Dst, vm.ToPrimitive((*f.ref(b.Src)), HintDefault))
	case bytecode.OpTypeofID:
		b := bytecode.DecodeTypeof(in)
		f.set(b.Dst, StringValue(typeofStrings[(*f.ref(b.Value)).TypeOf()]))
	case bytecode.OpToStringID:
		b := bytecode.DecodeToString(in)
		f.set(b.Dst, StringValue(vm.ToString((*f.ref(b.Operand)))))
	case bytecode.OpIsCellWithTypeID:
		b := bytecode.DecodeIsCellWithType(in)
		f.set(b.Dst, Bool(isCellWithType((*f.ref(b.Operand)), b.Type)))
	case bytecode.OpInstanceofID:
		b := bytecode.DecodeInstanceof(in)
		ctor := *f.ref(b.Constructor)
		f.set(b.Dst, Bool(vm.InstanceOf((*f.ref(b.Value)), ctor)))
		vm.fillInstanceofCache(&cb.instanceofCaches[b.MetadataID], cb.realm, ctor)
	case bytecode.OpStrcatID:
		b := bytecode.DecodeStrcat(in)
		s := emptyString
		for i := 0; i < int(b.Count); i++ {
			s = Concat(s, vm.ToString(f.getChecked(b.Src-bytecode.VirtualRegister(i))))
		}
		f.set(b.Dst, StringValue(s))
	case bytecode.OpCallVarargsID:
		b := bytecode.DecodeCallVarargs(in)
		if jit.NativeCalls && b.FirstVarArg == 0 && *f.ref(b.Arguments) == forwardedArguments {
			vm.fillCallIC(f, pc, *f.ref(b.Callee), f.argc, false)
		}
		args := vm.varargs(f, (*f.ref(b.Arguments)), b.FirstVarArg)
		f.set(b.Dst, vm.callValue((*f.ref(b.Callee)), thisForCall((*f.ref(b.ThisValue))), args))
	case bytecode.OpCreateLexicalEnvironmentID:
		b := bytecode.DecodeCreateLexicalEnvironment(in)
		table := (*f.ref(b.SymbolTable)).cell().(*bytecode.SymbolTable)
		f.set(b.Dst, cellValue(newLexicalScope((*f.ref(b.Scope)).scope(), table, (*f.ref(b.InitialValue)))))
		if vm.jitScopes != nil && vm.jitScopes.n == 0 {
			vm.fillScopePool() // for newScope in generated code
		}
	case bytecode.OpCreateDirectArgumentsID:
		b := bytecode.DecodeCreateDirectArguments(in)
		f.set(b.Dst, ObjectValue(vm.createDirectArguments(f)))
	case bytecode.OpGetFromArgumentsID:
		b := bytecode.DecodeGetFromArguments(in)
		f.set(b.Dst, (*f.ref(b.Arguments)).ObjectRef().internal.(*argumentsData).getMapped(int(b.Index)))
	case bytecode.OpPutToArgumentsID:
		b := bytecode.DecodePutToArguments(in)
		(*f.ref(b.Arguments)).ObjectRef().internal.(*argumentsData).setMapped(int(b.Index), (*f.ref(b.Value)))
	case bytecode.OpNewObjectID:
		b := bytecode.DecodeNewObject(in)
		f.set(b.Dst, ObjectValue(r.newObjectWithCapacity(r.ObjectPrototype, b.InlineCapacity)))
		vm.refillObjectPool(b.InlineCapacity)
	case bytecode.OpNewArrayID:
		b := bytecode.DecodeNewArray(in)
		values := make([]Value, b.Argc)
		for i := range values {
			values[i] = f.getChecked(b.Argv - bytecode.VirtualRegister(i))
		}
		f.set(b.Dst, ObjectValue(r.NewArray(values...)))
		if vm.jitArrays != nil && vm.jitArrays.n == 0 {
			vm.fillArrayPool() // for newSmallArray
		}
	case bytecode.OpNewFuncID:
		b := bytecode.DecodeNewFunc(in)
		exec := cb.u.FunctionDecls[b.FunctionDecl]
		f.set(b.Dst, ObjectValue(vm.newClosure(f, exec, (*f.ref(b.Scope)).scope())))
		if vm.jitClosures != nil && vm.jitClosures.n == 0 {
			vm.fillClosurePool() // for newClosure in generated code
		}
	case bytecode.OpNewFuncExpID:
		b := bytecode.DecodeNewFuncExp(in)
		exec := cb.u.FunctionExprs[b.FunctionDecl]
		f.set(b.Dst, ObjectValue(vm.newClosure(f, exec, (*f.ref(b.Scope)).scope())))
		if vm.jitClosures != nil && vm.jitClosures.n == 0 {
			vm.fillClosurePool() // for newClosure in generated code
		}
	case bytecode.OpCreateThisID:
		b := bytecode.DecodeCreateThis(in)
		f.set(b.Dst, ObjectValue(vm.createThis(&cb.createThisCaches[b.MetadataID], (*f.ref(b.Callee)).ObjectRef(), b.InlineCapacity)))
		vm.refillObjectPool(thisCapacity(b.InlineCapacity))
	case bytecode.OpConstructID:
		b := bytecode.DecodeConstruct(in)
		if jit.NativeCalls {
			vm.fillCallIC(f, pc, *f.ref(b.Callee), int(b.Argc), true)
		}
		newTarget, args := f.callArgs(b.Argv, b.Argc)
		f.set(b.Dst, vm.constructValue((*f.ref(b.Callee)), args, newTarget.AsObject()))
	case bytecode.OpNewRegExpID:
		b := bytecode.DecodeNewRegExp(in)
		re := f.get(b.Regexp).cell().(*bytecode.RegExp)
		f.set(b.Dst, ObjectValue(vm.newRegExpFromLiteral(re)))
		if vm.jitRegExps != nil && vm.jitRegExps.n == 0 {
			vm.fillRegExpPool() // for newRegExp in generated code
		}
	case bytecode.OpTailCallVarargsID:
		if vm.depth >= vm.MaxDepth/4 {
			return pc // the interpreter makes the proper tail call
		}
		b := bytecode.DecodeTailCallVarargs(in)
		args := slices.Clone(vm.varargs(f, (*f.ref(b.Arguments)), b.FirstVarArg))
		f.set(b.Dst, vm.callValue((*f.ref(b.Callee)), thisForCall((*f.ref(b.ThisValue))), args))
	case bytecode.OpIteratorNextID:
		return vm.jitSlowGeneric(f, pc, in, op)
	case bytecode.OpNewArrayWithSizeID:
		next := vm.jitSlowGeneric(f, pc, in, op)
		if vm.jitArrays != nil && vm.jitArrays.n == 0 {
			vm.fillArrayPool() // for newArrayWithSize
		}
		return next
	default:
		switch {
		case jitSlowOps[op]:
			return vm.jitSlowArith(f, pc, in, op)
		case jitGenericOps[op]:
			return vm.jitSlowGeneric(f, pc, in, op)
		case jitColdOps[op]:
			vm.runCold(f, op, in)
			return next
		}
		panic("vm: no JIT slow path for " + op.String())
	}
	return next
}

// jitSlowArith is jitSlowPath for jitSlowOps: arithmetic, comparisons,
// conversions and scope accesses, the interpreter's work without its
// fused instructions. It is a function of its own so that jitSlowPath's
// frame, which every slow path pays for, stays small (with these cases in
// it, it grew from 560 to 856 bytes and base64 became 5% slower).
func (vm *VM) jitSlowArith(f *Frame, pc int, in []uint32, op bytecode.OpcodeID) int {
	cb := f.cb
	next := pc + op.Length()
	switch op {
	case bytecode.OpSubID:
		b := bytecode.DecodeSub(in)
		f.set(b.Dst, vm.Arith(opSub, (*f.ref(b.Lhs)), (*f.ref(b.Rhs))))
	case bytecode.OpMulID:
		b := bytecode.DecodeMul(in)
		f.set(b.Dst, vm.Arith(opMul, (*f.ref(b.Lhs)), (*f.ref(b.Rhs))))
	case bytecode.OpDivID:
		b := bytecode.DecodeDiv(in)
		f.set(b.Dst, vm.Arith(opDiv, (*f.ref(b.Lhs)), (*f.ref(b.Rhs))))
	case bytecode.OpModID:
		b := bytecode.DecodeMod(in)
		f.set(b.Dst, vm.Arith(opMod, (*f.ref(b.Lhs)), (*f.ref(b.Rhs))))
	case bytecode.OpNegateID:
		b := bytecode.DecodeNegate(in)
		f.set(b.Dst, vm.Negate((*f.ref(b.Operand))))
	case bytecode.OpIncID:
		b := bytecode.DecodeInc(in)
		f.set(b.SrcDst, vm.Increment((*f.ref(b.SrcDst)), 1))
	case bytecode.OpDecID:
		b := bytecode.DecodeDec(in)
		f.set(b.SrcDst, vm.Increment((*f.ref(b.SrcDst)), -1))
	case bytecode.OpBitandID:
		b := bytecode.DecodeBitand(in)
		f.set(b.Dst, vm.Arith(opBitand, (*f.ref(b.Lhs)), (*f.ref(b.Rhs))))
	case bytecode.OpBitorID:
		b := bytecode.DecodeBitor(in)
		f.set(b.Dst, vm.Arith(opBitor, (*f.ref(b.Lhs)), (*f.ref(b.Rhs))))
	case bytecode.OpBitxorID:
		b := bytecode.DecodeBitxor(in)
		f.set(b.Dst, vm.Arith(opBitxor, (*f.ref(b.Lhs)), (*f.ref(b.Rhs))))
	case bytecode.OpLshiftID:
		b := bytecode.DecodeLshift(in)
		f.set(b.Dst, vm.Arith(opLshift, (*f.ref(b.Lhs)), (*f.ref(b.Rhs))))
	case bytecode.OpRshiftID:
		b := bytecode.DecodeRshift(in)
		f.set(b.Dst, vm.Arith(opRshift, (*f.ref(b.Lhs)), (*f.ref(b.Rhs))))
	case bytecode.OpUrshiftID:
		b := bytecode.DecodeUrshift(in)
		f.set(b.Dst, vm.Arith(opUrshift, (*f.ref(b.Lhs)), (*f.ref(b.Rhs))))
	case bytecode.OpBitnotID:
		b := bytecode.DecodeBitnot(in)
		f.set(b.Dst, vm.BitNot((*f.ref(b.Operand))))
	case bytecode.OpUnsignedID:
		b := bytecode.DecodeUnsigned(in)
		f.set(b.Dst, Number(float64(uint32(vm.ToInt32((*f.ref(b.Operand)))))))
	case bytecode.OpBelowID:
		b := bytecode.DecodeBelow(in)
		f.set(b.Dst, Bool(uint32(vm.ToInt32((*f.ref(b.Lhs)))) < uint32(vm.ToInt32((*f.ref(b.Rhs))))))
	case bytecode.OpBeloweqID:
		b := bytecode.DecodeBeloweq(in)
		f.set(b.Dst, Bool(uint32(vm.ToInt32((*f.ref(b.Lhs)))) <= uint32(vm.ToInt32((*f.ref(b.Rhs))))))
	case bytecode.OpJbelowID:
		b := bytecode.DecodeJbelow(in)
		if uint32(vm.ToInt32((*f.ref(b.Lhs)))) < uint32(vm.ToInt32((*f.ref(b.Rhs)))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpJbeloweqID:
		b := bytecode.DecodeJbeloweq(in)
		if uint32(vm.ToInt32((*f.ref(b.Lhs)))) <= uint32(vm.ToInt32((*f.ref(b.Rhs)))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpLessID:
		b := bytecode.DecodeLess(in)
		f.set(b.Dst, Bool(vm.Less((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
	case bytecode.OpLesseqID:
		b := bytecode.DecodeLesseq(in)
		f.set(b.Dst, Bool(vm.LessEq((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
	case bytecode.OpGreaterID:
		b := bytecode.DecodeGreater(in)
		f.set(b.Dst, Bool(vm.Greater((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
	case bytecode.OpGreatereqID:
		b := bytecode.DecodeGreatereq(in)
		f.set(b.Dst, Bool(vm.GreaterEq((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
	case bytecode.OpJlessID:
		b := bytecode.DecodeJless(in)
		if vm.Less((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpJlesseqID:
		b := bytecode.DecodeJlesseq(in)
		if vm.LessEq((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpJgreaterID:
		b := bytecode.DecodeJgreater(in)
		if vm.Greater((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpJgreatereqID:
		b := bytecode.DecodeJgreatereq(in)
		if vm.GreaterEq((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpJnlessID:
		b := bytecode.DecodeJnless(in)
		if !vm.Less((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpJnlesseqID:
		b := bytecode.DecodeJnlesseq(in)
		if !vm.LessEq((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpJngreaterID:
		b := bytecode.DecodeJngreater(in)
		if !vm.Greater((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpJngreatereqID:
		b := bytecode.DecodeJngreatereq(in)
		if !vm.GreaterEq((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpJtrueID:
		b := bytecode.DecodeJtrue(in)
		if (*f.ref(b.Condition)).ToBoolean() {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpJfalseID:
		b := bytecode.DecodeJfalse(in)
		if !(*f.ref(b.Condition)).ToBoolean() {
			return pc + int(b.TargetLabel)
		}
	case bytecode.OpToNumberID:
		b := bytecode.DecodeToNumber(in)
		v := (*f.ref(b.Operand))
		if !v.IsNumber() {
			v = Number(vm.ToNumber(v))
		}
		f.set(b.Dst, v)
	case bytecode.OpToNumericID:
		b := bytecode.DecodeToNumeric(in)
		f.set(b.Dst, vm.ToNumeric((*f.ref(b.Operand))))
	case bytecode.OpToPropertyKeyOrNumberID:
		b := bytecode.DecodeToPropertyKeyOrNumber(in)
		v := f.get(b.Src)
		if !v.IsString() && !v.IsSymbol() && !v.IsNumber() {
			v = vm.ToPropertyKey(v).Value()
		}
		f.set(b.Dst, v)
	case bytecode.OpResolveScopeID:
		b := bytecode.DecodeResolveScope(in)
		scope := (*f.ref(b.Scope)).scope()
		switch {
		case b.ResolveType == bytecode.ClosureVar || b.ResolveType == bytecode.ResolvedClosureVar:
			f.set(b.Dst, cellValue(skipScopes(scope, b.LocalScopeDepth)))
		case isGlobalResolveType(b.ResolveType):
			f.set(b.Dst, cellValue(vm.resolveGlobalScope(&cb.resolveScopeCaches[b.MetadataID], cb.realm, scope, cb.identifier(b.Var))))
		default:
			f.set(b.Dst, cellValue(vm.resolveScope(scope, cb.identifier(b.Var))))
		}
	case bytecode.OpGetFromScopeID:
		b := bytecode.DecodeGetFromScope(in)
		scope := (*f.ref(b.Scope)).scope()
		switch b.GetPutInfo.ResolveType() {
		case bytecode.ClosureVar, bytecode.ResolvedClosureVar:
			f.set(b.Dst, scope.slots[b.Offset])
		default:
			if scope.kind == scopeGlobalObject && isGlobalResolveType(b.GetPutInfo.ResolveType()) {
				f.set(b.Dst, vm.getGlobalProperty(&cb.getFromScopeCaches[b.MetadataID], scope, f.key(b.Var), b.GetPutInfo.ResolveMode()))
				break
			}
			if scope.kind == scopeLexical || scope.kind == scopeGlobalLexical {
				if slot := cb.getScopeSlotCaches[b.MetadataID].slot(scope, cb.identifier(b.Var), false); slot != nil && !slot.IsEmpty() {
					f.set(b.Dst, *slot)
					break
				}
			}
			f.set(b.Dst, vm.getFromScope(scope, cb.identifier(b.Var), b.GetPutInfo.ResolveMode()))
		}
	case bytecode.OpPutToScopeID:
		b := bytecode.DecodePutToScope(in)
		scope := (*f.ref(b.Scope)).scope()
		switch b.GetPutInfo.ResolveType() {
		case bytecode.ClosureVar, bytecode.ResolvedClosureVar:
			scope.slots[b.Offset] = (*f.ref(b.Value))
		default:
			if scope.kind == scopeGlobalObject && isGlobalResolveType(b.GetPutInfo.ResolveType()) {
				vm.putGlobalProperty(&cb.putToScopeCaches[b.MetadataID], scope, f.key(b.Var), (*f.ref(b.Value)), b.GetPutInfo)
				break
			}
			if scope.kind == scopeLexical ||
				scope.kind == scopeGlobalLexical && !b.GetPutInfo.InitializationMode().IsInitialization() {
				if slot := cb.putScopeSlotCaches[b.MetadataID].slot(scope, cb.identifier(b.Var), true); slot != nil &&
					(scope.kind == scopeLexical || !slot.IsEmpty()) {
					*slot = (*f.ref(b.Value))
					break
				}
			}
			vm.putToScope(scope, cb.identifier(b.Var), (*f.ref(b.Value)), b.GetPutInfo)
		}
	}
	return next
}

// compareOperands returns the operands of less, lesseq, greater or
// greatereq.
func compareOperands(in []uint32) (lhs, rhs bytecode.VirtualRegister, ok bool) {
	switch bytecode.OpcodeID(in[0]) {
	case bytecode.OpLessID:
		b := bytecode.DecodeLess(in)
		return b.Lhs, b.Rhs, true
	case bytecode.OpLesseqID:
		b := bytecode.DecodeLesseq(in)
		return b.Lhs, b.Rhs, true
	case bytecode.OpGreaterID:
		b := bytecode.DecodeGreater(in)
		return b.Lhs, b.Rhs, true
	case bytecode.OpGreatereqID:
		b := bytecode.DecodeGreatereq(in)
		return b.Lhs, b.Rhs, true
	}
	return 0, 0, false
}

// Layouts for enumerator_next's machine code.
var (
	enumeratorType = func() uintptr {
		var x any = (*propertyNameEnumerator)(nil)
		return (*[2]uintptr)(unsafe.Pointer(&x))[0]
	}()
	offEnumBase  = int(unsafe.Offsetof(propertyNameEnumerator{}.base))
	offEnumPos   = int(unsafe.Offsetof(propertyNameEnumerator{}.pos))
	offEnumNames = int(unsafe.Offsetof(propertyNameEnumerator{}.names))
	offEnumShape = int(unsafe.Offsetof(propertyNameEnumerator{}.shape))
)
