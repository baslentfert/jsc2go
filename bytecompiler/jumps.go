package bytecompiler

import "github.com/baslentfert/jsc2go/bytecode"

// Jumps and the compare/test-and-jump peephole fusion (BytecodeGenerator.cpp).

func (g *Generator) emitEnter() {
	bytecode.EmitEnter(g)
	// The end of op_enter is a potential target of recursive tail calls, so
	// it must not be fused with what follows.
	g.disablePeepholeOptimization()
}

func (g *Generator) emitLoopHint() {
	bytecode.EmitLoopHint(g)
	g.emitCheckTraps()
}

func (g *Generator) emitJump(target *Label) {
	bytecode.EmitJmp(g, g.bind(target))
}

func (g *Generator) emitCheckTraps() { bytecode.EmitCheckTraps(g) }

// rewind removes the last instruction.
func (g *Generator) rewind() {
	g.disablePeepholeOptimization()
	clear(g.instructions[g.lastInstruction:])
	g.instructions = g.instructions[:g.lastInstruction]
}

// lastOperand returns word i of the last instruction.
func (g *Generator) lastOperand(i int) bytecode.VirtualRegister {
	return bytecode.VirtualRegister(int32(g.instructions[g.lastInstruction+i]))
}

type jumpEmitter func(gen bytecode.Emitter, lhs, rhs bytecode.VirtualRegister, target bytecode.BoundLabel)
type testJumpEmitter func(gen bytecode.Emitter, operand bytecode.VirtualRegister, target bytecode.BoundLabel)

// fuseCompareAndJump replaces "cond = lhs op rhs" followed by a branch on
// cond with a compare-and-jump, if cond is a dead temporary.
func (g *Generator) fuseCompareAndJump(cond *RegisterID, target *Label, emit jumpEmitter, swapOperands bool) bool {
	// Binary compare ops are (dst, lhs, rhs).
	dst, lhs, rhs := g.lastOperand(1), g.lastOperand(2), g.lastOperand(3)
	if cond.VirtualRegister() == dst && cond.IsTemporary() && cond.RefCount() == 0 {
		g.rewind()
		if swapOperands {
			lhs, rhs = rhs, lhs
		}
		emit(g, lhs, rhs, g.bind(target))
		return true
	}
	return false
}

// fuseTestAndJmp is fuseCompareAndJump for unary tests (dst, operand).
func (g *Generator) fuseTestAndJmp(cond *RegisterID, target *Label, emit testJumpEmitter) bool {
	dst, operand := g.lastOperand(1), g.lastOperand(2)
	if cond.VirtualRegister() == dst && cond.IsTemporary() && cond.RefCount() == 0 {
		g.rewind()
		emit(g, operand, g.bind(target))
		return true
	}
	return false
}

func (g *Generator) emitJumpIfTrue(cond *RegisterID, target *Label) {
	if g.canDoPeepholeOptimization() {
		fused := false
		switch g.lastOpcodeID {
		case bytecode.OpLessID:
			fused = g.fuseCompareAndJump(cond, target, bytecode.EmitJless, false)
		case bytecode.OpLesseqID:
			fused = g.fuseCompareAndJump(cond, target, bytecode.EmitJlesseq, false)
		case bytecode.OpGreaterID:
			fused = g.fuseCompareAndJump(cond, target, bytecode.EmitJgreater, false)
		case bytecode.OpGreatereqID:
			fused = g.fuseCompareAndJump(cond, target, bytecode.EmitJgreatereq, false)
		case bytecode.OpEqID:
			fused = g.fuseCompareAndJump(cond, target, bytecode.EmitJeq, false)
		case bytecode.OpStricteqID:
			fused = g.fuseCompareAndJump(cond, target, bytecode.EmitJstricteq, false)
		case bytecode.OpNeqID:
			fused = g.fuseCompareAndJump(cond, target, bytecode.EmitJneq, false)
		case bytecode.OpNstricteqID:
			fused = g.fuseCompareAndJump(cond, target, bytecode.EmitJnstricteq, false)
		case bytecode.OpBelowID:
			fused = g.fuseCompareAndJump(cond, target, bytecode.EmitJbelow, false)
		case bytecode.OpBeloweqID:
			fused = g.fuseCompareAndJump(cond, target, bytecode.EmitJbeloweq, false)
		case bytecode.OpEqNullID:
			fused = target.IsForward() && g.fuseTestAndJmp(cond, target, bytecode.EmitJeqNull)
		case bytecode.OpNeqNullID:
			fused = target.IsForward() && g.fuseTestAndJmp(cond, target, bytecode.EmitJneqNull)
		case bytecode.OpIsUndefinedOrNullID:
			fused = target.IsForward() && g.fuseTestAndJmp(cond, target, bytecode.EmitJundefinedOrNull)
		}
		if fused {
			return
		}
	}
	bytecode.EmitJtrue(g, vr(cond), g.bind(target))
}

func (g *Generator) emitJumpIfFalse(cond *RegisterID, target *Label) {
	if g.canDoPeepholeOptimization() {
		fused := false
		forward := target.IsForward()
		switch g.lastOpcodeID {
		case bytecode.OpLessID:
			fused = forward && g.fuseCompareAndJump(cond, target, bytecode.EmitJnless, false)
		case bytecode.OpLesseqID:
			fused = forward && g.fuseCompareAndJump(cond, target, bytecode.EmitJnlesseq, false)
		case bytecode.OpGreaterID:
			fused = forward && g.fuseCompareAndJump(cond, target, bytecode.EmitJngreater, false)
		case bytecode.OpGreatereqID:
			fused = forward && g.fuseCompareAndJump(cond, target, bytecode.EmitJngreatereq, false)
		case bytecode.OpEqID:
			fused = forward && g.fuseCompareAndJump(cond, target, bytecode.EmitJneq, false)
		case bytecode.OpStricteqID:
			fused = forward && g.fuseCompareAndJump(cond, target, bytecode.EmitJnstricteq, false)
		case bytecode.OpNeqID:
			fused = forward && g.fuseCompareAndJump(cond, target, bytecode.EmitJeq, false)
		case bytecode.OpNstricteqID:
			fused = forward && g.fuseCompareAndJump(cond, target, bytecode.EmitJstricteq, false)
		case bytecode.OpBelowID:
			fused = forward && g.fuseCompareAndJump(cond, target, bytecode.EmitJbeloweq, true)
		case bytecode.OpBeloweqID:
			fused = forward && g.fuseCompareAndJump(cond, target, bytecode.EmitJbelow, true)
		case bytecode.OpNotID:
			fused = g.fuseTestAndJmp(cond, target, bytecode.EmitJtrue)
		case bytecode.OpEqNullID:
			fused = forward && g.fuseTestAndJmp(cond, target, bytecode.EmitJneqNull)
		case bytecode.OpNeqNullID:
			fused = forward && g.fuseTestAndJmp(cond, target, bytecode.EmitJeqNull)
		case bytecode.OpIsUndefinedOrNullID:
			fused = forward && g.fuseTestAndJmp(cond, target, bytecode.EmitJnundefinedOrNull)
		}
		if fused {
			return
		}
	}
	bytecode.EmitJfalse(g, vr(cond), g.bind(target))
}

func (g *Generator) emitJumpIfNotLinkTimeConstant(cond *RegisterID, target *Label, c bytecode.LinkTimeConstant) {
	ptr := g.moveLinkTimeConstant(nil, c)
	bytecode.EmitJneqPtr(g, vr(cond), vr(ptr), g.bind(target))
}

func (g *Generator) emitJumpIfNotFunctionCall(cond *RegisterID, target *Label) {
	g.emitJumpIfNotLinkTimeConstant(cond, target, bytecode.LinkTimeConstantCallFunction)
}

func (g *Generator) emitJumpIfNotFunctionApply(cond *RegisterID, target *Label) {
	g.emitJumpIfNotLinkTimeConstant(cond, target, bytecode.LinkTimeConstantApplyFunction)
}

func (g *Generator) emitJumpIfNotReflectConstruct(cond *RegisterID, target *Label) {
	g.emitJumpIfNotLinkTimeConstant(cond, target, bytecode.LinkTimeConstantReflectConstructFunction)
}

func (g *Generator) emitJumpIfNotEvalFunction(cond *RegisterID, target *Label) {
	g.emitJumpIfNotLinkTimeConstant(cond, target, bytecode.LinkTimeConstantEvalFunction)
}

func (g *Generator) emitJumpIfEmptyPropertyNameEnumerator(cond *RegisterID, target *Label) {
	ptr := g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantEmptyPropertyNameEnumerator)
	bytecode.EmitJeqPtr(g, vr(cond), vr(ptr), g.bind(target))
}

func (g *Generator) emitJumpIfSentinelString(cond *RegisterID, target *Label) {
	ptr := g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantSentinelString)
	bytecode.EmitJeqPtr(g, vr(cond), vr(ptr), g.bind(target))
}

// emitWideJumpIfNotFunctionHasOwnProperty returns the offset of the jump so
// that a for-in loop can rewrite it later.
func (g *Generator) emitWideJumpIfNotFunctionHasOwnProperty(cond *RegisterID, target *Label) int {
	g.emitJumpIfNotLinkTimeConstant(cond, target, bytecode.LinkTimeConstantHasOwnPropertyFunction)
	return g.lastInstruction
}

func (g *Generator) recordHasOwnPropertyInForInLoop(context *forInContext, branchOffset int, genericPath *Label) {
	if !genericPath.IsBound() || genericPath.IsForward() {
		panic("bytecompiler: for-in generic path label must be bound")
	}
	context.addHasOwnPropertyJump(branchOffset, genericPath.Location())
}
