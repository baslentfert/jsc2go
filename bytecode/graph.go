package bytecode

import (
	"math/bits"
	"slices"
	"sort"
)

// Port of JavaScriptCore/bytecode/{PreciseJumpTargets,PreciseJumpTargetsInlines,
// BytecodeBasicBlock,BytecodeGraph,BytecodeLivenessAnalysis*}: control flow
// and register liveness over an instruction stream.

// --- Opcode classes (Opcode.h) -----------------------------------------------

// IsBranch reports whether op may jump.
func (op OpcodeID) IsBranch() bool {
	return jumpTargetOperandIndex[op] != 0 || op == OpSwitchImmID || op == OpSwitchCharID || op == OpSwitchStringID
}

func (op OpcodeID) IsUnconditionalBranch() bool { return op == OpJmpID }
func (op OpcodeID) IsTerminal() bool            { return op == OpRetID || op == OpUnreachableID }
func (op OpcodeID) IsThrow() bool               { return op == OpThrowID || op == OpThrowStaticErrorID }

// jumpTargetOperandIndex[op] is the word index of op's jump offset, or 0.
var jumpTargetOperandIndex = func() (t [NumberOfBytecodeIDs]int) {
	for op := range OpcodeID(NumberOfBytecodeIDs) {
		if i := slices.Index(op.OperandKinds(), OperandBoundLabel); i >= 0 {
			t[op] = i + 1
		}
	}
	return
}()

// JumpTargetOperandIndex returns the word index of op's jump offset, or 0
// if op has no jump operand.
func JumpTargetOperandIndex(op OpcodeID) int { return jumpTargetOperandIndex[op] }

// forEachJumpTargetSlot calls f with a pointer to each relative jump offset
// stored for the instruction ins: its jump operand or the entries of its
// switch table (extract/updateStoredJumpTargetsForInstruction).
func forEachJumpTargetSlot(cb *UnlinkedCodeBlock, ins []uint32, f func(slot *int32)) {
	op := OpcodeID(ins[0])
	if i := jumpTargetOperandIndex[op]; i != 0 {
		target := int32(ins[i])
		f(&target)
		ins[i] = uint32(target)
		return
	}
	switch op {
	case OpSwitchImmID, OpSwitchCharID:
		table := &cb.SwitchJumpTables[ins[1]]
		if table.IsList {
			for i := 0; i < len(table.BranchOffsets); i += 2 {
				f(&table.BranchOffsets[i+1])
			}
		} else {
			for i := len(table.BranchOffsets) - 1; i >= 0; i-- {
				f(&table.BranchOffsets[i])
			}
		}
		f(&table.DefaultOffset)
	case OpSwitchStringID:
		table := &cb.StringSwitchJumpTables[ins[1]]
		keys := make([]string, 0, len(table.OffsetTable))
		for k := range table.OffsetTable {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic order
		for _, k := range keys {
			e := table.OffsetTable[k]
			f(&e.BranchOffset)
			table.OffsetTable[k] = e
		}
		f(&table.DefaultOffset)
	}
}

// ForEachJumpTarget calls f with each relative jump offset of ins.
func ForEachJumpTarget(cb *UnlinkedCodeBlock, ins []uint32, f func(relative int32)) {
	forEachJumpTargetSlot(cb, ins, func(slot *int32) { f(*slot) })
}

// UpdateJumpTargets replaces each relative jump offset of ins with f(old).
func UpdateJumpTargets(cb *UnlinkedCodeBlock, ins []uint32, f func(relative int32) int32) {
	forEachJumpTargetSlot(cb, ins, func(slot *int32) { *slot = f(*slot) })
}

// jumpTargetsForInstruction appends the absolute offsets ins at offset may
// jump to (op_loop_hint counts as a target of itself).
func jumpTargetsForInstruction(cb *UnlinkedCodeBlock, ins []uint32, offset int, out []int) []int {
	ForEachJumpTarget(cb, ins, func(relative int32) { out = append(out, offset+int(relative)) })
	if OpcodeID(ins[0]) == OpLoopHintID {
		out = append(out, offset)
	}
	return out
}

// ComputePreciseJumpTargets returns the sorted, deduplicated offsets that
// can be jumped to, including exception handler boundaries.
func ComputePreciseJumpTargets(cb *UnlinkedCodeBlock, instructions []uint32) []int {
	var out []int
	for i := len(cb.ExceptionHandlers) - 1; i >= 0; i-- {
		h := cb.ExceptionHandlers[i]
		out = append(out, int(h.Target), int(h.Start), int(h.End))
	}
	for offset := 0; offset < len(instructions); {
		op := OpcodeID(instructions[offset])
		out = jumpTargetsForInstruction(cb, instructions[offset:offset+op.Length()], offset, out)
		offset += op.Length()
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// --- Bit vectors ------------------------------------------------------------------

// BitVector is a fixed-size set of small integers (FastBitVector).
type BitVector struct {
	words []uint64
	n     int
}

func NewBitVector(n int) BitVector { return BitVector{words: make([]uint64, (n+63)/64), n: n} }

func (b *BitVector) Len() int             { return b.n }
func (b *BitVector) Get(i int) bool       { return i < b.n && b.words[i/64]&(1<<(i%64)) != 0 }
func (b *BitVector) Set(i int)            { b.words[i/64] |= 1 << (i % 64) }
func (b *BitVector) Clear(i int)          { b.words[i/64] &^= 1 << (i % 64) }
func (b *BitVector) ClearAll()            { clear(b.words) }
func (b *BitVector) Clone() BitVector     { return BitVector{words: slices.Clone(b.words), n: b.n} }
func (b *BitVector) CopyFrom(o BitVector) { copy(b.words, o.words) }

func (b *BitVector) Or(o BitVector) {
	for i, w := range o.words {
		b.words[i] |= w
	}
}

// SetAndCheck sets b to o and reports whether b changed.
func (b *BitVector) SetAndCheck(o BitVector) bool {
	changed := !slices.Equal(b.words, o.words)
	copy(b.words, o.words)
	return changed
}

// ForEachSetBit calls f with each member in increasing order.
func (b *BitVector) ForEachSetBit(f func(int)) {
	for wi, w := range b.words {
		for w != 0 {
			bit := bits.TrailingZeros64(w)
			f(wi*64 + bit)
			w &^= 1 << bit
		}
	}
}

// --- Basic blocks ---------------------------------------------------------------

// BasicBlock is a straight-line run of instructions.
type BasicBlock struct {
	LeaderOffset int
	TotalLength  int
	Index        int
	// Delta holds the lengths of the instructions, in order.
	Delta      []int
	Successors []int
	In, Out    BitVector
	isEntry    bool
	isExit     bool
}

func (b *BasicBlock) IsEntryBlock() bool {
	return b.LeaderOffset == 0 && b.TotalLength == 0 && !b.isExit
}
func (b *BasicBlock) IsExitBlock() bool { return b.isExit }

func (b *BasicBlock) addSuccessor(to *BasicBlock) {
	if !slices.Contains(b.Successors, to.Index) {
		b.Successors = append(b.Successors, to.Index)
	}
}

func (b *BasicBlock) addLength(n int) {
	b.Delta = append(b.Delta, n)
	b.TotalLength += n
}

// Graph is the control flow graph of a code block (BytecodeGraph). Block 0
// is the entry block and the last block the exit block.
type Graph struct {
	Blocks []*BasicBlock
}

// exitLeaderOffset sorts the exit block after every real block.
const exitLeaderOffset = int(^uint(0) >> 1)

// NewGraph computes the basic blocks of instructions.
func NewGraph(cb *UnlinkedCodeBlock, instructions []uint32) *Graph {
	jumpTargets := ComputePreciseJumpTargets(cb, instructions)
	g := &Graph{}
	newBlock := func(leader int) *BasicBlock {
		b := &BasicBlock{LeaderOffset: leader, Index: len(g.Blocks)}
		g.Blocks = append(g.Blocks, b)
		return b
	}

	// The entry block, and the first block.
	entry := newBlock(0)
	current := newBlock(0)
	entry.addSuccessor(current)

	nextInstructionIsLeader := false
	for offset := 0; offset < len(instructions); {
		op := OpcodeID(instructions[offset])
		length := op.Length()
		createdBlock := false
		// A jump target leads its own basic block.
		if _, isTarget := slices.BinarySearch(jumpTargets, offset); nextInstructionIsLeader || op == OpCatchID || isTarget {
			current = newBlock(offset)
			current.addLength(length)
			createdBlock = true
			nextInstructionIsLeader = false
		}
		// The instruction after a branch, return or throw leads a block.
		if op.IsBranch() || op.IsTerminal() || op.IsThrow() {
			nextInstructionIsLeader = true
		}
		if !createdBlock {
			current.addLength(length)
		}
		offset += length
	}

	exit := newBlock(exitLeaderOffset)
	exit.TotalLength = exitLeaderOffset
	exit.isExit = true

	// Link the blocks.
	for i, block := range g.Blocks {
		if block.IsEntryBlock() || block.IsExitBlock() {
			continue
		}
		fallsThrough := true
		for visited := 0; visited < block.TotalLength; {
			offset := block.LeaderOffset + visited
			op := OpcodeID(instructions[offset])
			visited += op.Length()

			// Terminals go to the exit block.
			if op.IsTerminal() {
				block.addSuccessor(exit)
				fallsThrough = false
				break
			}

			// A throw goes to its handler, or exits if there is none. (A
			// finally block has its own catch handler.)
			if op.IsThrow() {
				fallsThrough = false
				handler := cb.HandlerForIndex(uint32(offset), AnyHandler)
				if handler == nil {
					block.addSuccessor(exit)
					break
				}
				for _, other := range g.Blocks {
					if int(handler.Target) == other.LeaderOffset {
						block.addSuccessor(other)
						break
					}
				}
				break
			}

			// A branch goes to its targets.
			if op.IsBranch() {
				targets := jumpTargetsForInstruction(cb, instructions[offset:offset+op.Length()], offset, nil)
				for _, other := range g.Blocks {
					if slices.Contains(targets, other.LeaderOffset) {
						block.addSuccessor(other)
					}
				}
				if op.IsUnconditionalBranch() {
					fallsThrough = false
				}
				break
			}
		}
		if fallsThrough {
			block.addSuccessor(g.Blocks[i+1])
		}
	}
	return g
}

func (g *Graph) blockContainsOffset(b *BasicBlock, offset int) bool {
	return offset >= b.LeaderOffset && offset < b.LeaderOffset+b.TotalLength
}

// FindBasicBlockForOffset returns the block holding the instruction at offset.
func (g *Graph) FindBasicBlockForOffset(offset int) *BasicBlock {
	for _, b := range g.Blocks {
		if !b.IsEntryBlock() && !b.IsExitBlock() && g.blockContainsOffset(b, offset) {
			return b
		}
	}
	return nil
}

// FindBasicBlockWithLeaderOffset returns the block that starts at offset.
func (g *Graph) FindBasicBlockWithLeaderOffset(offset int) *BasicBlock {
	i := sort.Search(len(g.Blocks), func(i int) bool { return g.Blocks[i].LeaderOffset >= offset })
	for ; i < len(g.Blocks) && g.Blocks[i].LeaderOffset == offset; i++ {
		if !g.Blocks[i].IsEntryBlock() {
			return g.Blocks[i]
		}
	}
	return nil
}

// --- Liveness ------------------------------------------------------------------------

// isValidRegisterForLiveness: only locals are tracked; arguments and the
// header are always live.
func isValidRegisterForLiveness(r VirtualRegister) bool { return !r.IsConstant() && r.IsLocal() }

// stepOverInstruction updates out (live after the instruction at offset) to
// the liveness before it.
func (g *Graph) stepOverInstruction(cb *UnlinkedCodeBlock, instructions []uint32, offset int, out *BitVector) {
	op := OpcodeID(instructions[offset])
	ins := instructions[offset : offset+op.Length()]
	for checkpoint := op.NumberOfCheckpoints() - 1; checkpoint >= 0; checkpoint-- {
		ComputeDefsForInstruction(cb, ins, checkpoint, func(r VirtualRegister) {
			if isValidRegisterForLiveness(r) {
				out.Clear(r.ToLocal())
			}
		})
		// Anything live into the exception handler is live here.
		if handler := cb.HandlerForIndex(uint32(offset), AnyHandler); handler != nil {
			if handlerBlock := g.FindBasicBlockWithLeaderOffset(int(handler.Target)); handlerBlock != nil {
				handlerBlock.In.ForEachSetBit(out.Set)
			}
		}
		ComputeUsesForInstruction(cb, ins, checkpoint, func(r VirtualRegister) {
			if isValidRegisterForLiveness(r) {
				out.Set(r.ToLocal())
			}
		})
	}
}

// computeLocalLivenessForInstruction computes the liveness before the
// instruction at targetOffset in block into result; it reports whether
// result changed.
func (g *Graph) computeLocalLivenessForInstruction(cb *UnlinkedCodeBlock, instructions []uint32, block *BasicBlock, targetOffset int, result *BitVector) bool {
	out := block.Out.Clone()
	cursor := block.TotalLength
	for i := len(block.Delta) - 1; i >= 0; i-- {
		cursor -= block.Delta[i]
		offset := block.LeaderOffset + cursor
		if targetOffset > offset {
			break
		}
		g.stepOverInstruction(cb, instructions, offset, &out)
	}
	return result.SetAndCheck(out)
}

// RunLivenessFixpoint computes the In and Out sets of every block.
func (g *Graph) RunLivenessFixpoint(cb *UnlinkedCodeBlock, instructions []uint32) {
	numberOfVariables := cb.NumCalleeLocals
	for _, b := range g.Blocks {
		b.In = NewBitVector(numberOfVariables)
		b.Out = NewBitVector(numberOfVariables)
	}

	newOut := NewBitVector(numberOfVariables)
	for changed := true; changed; {
		changed = false
		for i := len(g.Blocks) - 1; i >= 0; i-- {
			block := g.Blocks[i]
			newOut.ClearAll()
			for _, s := range block.Successors {
				newOut.Or(g.Blocks[s].In)
			}
			block.Out.CopyFrom(newOut)
			if block.IsExitBlock() || block.IsEntryBlock() {
				continue
			}
			if g.computeLocalLivenessForInstruction(cb, instructions, block, block.LeaderOffset, &block.In) {
				changed = true
			}
		}
	}
}

// LivenessAtInstruction returns the locals live before the instruction at
// offset. RunLivenessFixpoint must have run.
func (g *Graph) LivenessAtInstruction(cb *UnlinkedCodeBlock, instructions []uint32, offset int) BitVector {
	block := g.FindBasicBlockForOffset(offset)
	out := NewBitVector(block.Out.Len())
	g.computeLocalLivenessForInstruction(cb, instructions, block, offset, &out)
	return out
}
