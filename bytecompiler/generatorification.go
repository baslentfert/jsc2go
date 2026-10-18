package bytecompiler

import (
	"slices"
	"sort"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
)

// Port of JavaScriptCore/bytecode/{BytecodeGeneratorification,BytecodeRewriter}.
//
// Generator and async function bodies are compiled as straight-line code
// with op_yield at every suspension point. Generatorification turns them
// into resumable functions: a switch on the generator state at the entry
// jumps to the resume point, each op_yield becomes "save the live locals to
// the generator frame; return", and the code after it restores them.

// --- Rewriter ----------------------------------------------------------------

type insertionPosition int8

const (
	positionEntrypoint            insertionPosition = -2
	positionBefore                insertionPosition = -1
	positionLabelPoint            insertionPosition = 0
	positionAfter                 insertionPosition = 1
	positionOriginalBytecodePoint insertionPosition = 2
)

type insertionPoint struct {
	offset   int
	position insertionPosition
}

func (a insertionPoint) less(b insertionPoint) bool {
	if a.offset == b.offset {
		return a.position < b.position
	}
	return a.offset < b.offset
}

type insertion struct {
	index         insertionPoint
	remove        bool
	includeBranch bool
	removeLength  int
	instructions  []uint32
}

func (i *insertion) length() int {
	if i.remove {
		return i.removeLength
	}
	return len(i.instructions)
}

// bytecodeRewriter collects insertions and removals relative to the
// original instruction offsets and applies them at once, fixing up jumps,
// switch tables, exception handlers and expression info.
type bytecodeRewriter struct {
	g          *Generator
	insertions []insertion
}

// withWriter emits into buf instead of the instruction stream.
func (g *Generator) withWriter(buf *[]uint32, fn func()) {
	prevLastOpcodeID, prevLastInstruction := g.lastOpcodeID, g.lastInstruction
	saved := g.instructions
	g.instructions = *buf
	g.disablePeepholeOptimization()
	g.lastInstruction = len(g.instructions)
	fn()
	*buf = g.instructions
	g.instructions = saved
	g.lastOpcodeID, g.lastInstruction = prevLastOpcodeID, prevLastInstruction
}

// fragment emits instructions with fn and reports whether one of them is a
// branch.
func (r *bytecodeRewriter) fragment(fn func()) ([]uint32, bool) {
	var buf []uint32
	r.g.withWriter(&buf, fn)
	includeBranch := false
	for offset := 0; offset < len(buf); {
		op := bytecode.OpcodeID(buf[offset])
		includeBranch = includeBranch || op.IsBranch()
		offset += op.Length()
	}
	return buf, includeBranch
}

func (r *bytecodeRewriter) insertFragmentBefore(offset int, fn func()) {
	buf, includeBranch := r.fragment(fn)
	r.insertions = append(r.insertions, insertion{index: insertionPoint{offset, positionBefore}, includeBranch: includeBranch, instructions: buf})
}

func (r *bytecodeRewriter) insertFragmentAfter(offset int, fn func()) {
	buf, includeBranch := r.fragment(fn)
	r.insertions = append(r.insertions, insertion{index: insertionPoint{offset, positionAfter}, includeBranch: includeBranch, instructions: buf})
}

func (r *bytecodeRewriter) replaceBytecodeWithFragment(offset int, fn func()) {
	length := bytecode.OpcodeID(r.g.instructions[offset]).Length()
	r.insertions = append(r.insertions, insertion{index: insertionPoint{offset, positionOriginalBytecodePoint}, remove: true, removeLength: length})
	r.insertFragmentAfter(offset, fn)
}

func calculateDifference(insertions []insertion) int {
	result := 0
	for i := range insertions {
		if insertions[i].remove {
			result -= insertions[i].length()
		} else {
			result += insertions[i].length()
		}
	}
	return result
}

// lowerBound returns the index of the first insertion not before p.
func (r *bytecodeRewriter) lowerBound(p insertionPoint) int {
	return sort.Search(len(r.insertions), func(i int) bool { return !r.insertions[i].index.less(p) })
}

// adjustJumpTarget returns the new distance from start to target.
func (r *bytecodeRewriter) adjustJumpTarget(start, target insertionPoint) int {
	if start.less(target) {
		jumpTarget := target.offset
		if s := r.lowerBound(start); s != len(r.insertions) {
			e := r.lowerBound(target)
			jumpTarget += calculateDifference(r.insertions[s:e])
		}
		return jumpTarget - start.offset
	}
	if start == target {
		return 0
	}
	return -r.adjustJumpTarget(target, start)
}

// adjustAbsoluteOffset maps an original offset to its new offset.
func (r *bytecodeRewriter) adjustAbsoluteOffset(offset int) int {
	return r.adjustJumpTarget(insertionPoint{0, positionEntrypoint}, insertionPoint{offset, positionLabelPoint})
}

// adjustJumpTargets updates the jumps of the original instructions.
func (r *bytecodeRewriter) adjustJumpTargets() {
	instructions := r.g.instructions
	current := 0
	for i := 0; i < len(instructions); {
		remove := 0
		for current < len(r.insertions) && r.insertions[current].index.offset == i {
			if r.insertions[current].remove {
				remove += r.insertions[current].length()
			}
			current++
		}
		if remove == 0 {
			op := bytecode.OpcodeID(instructions[i])
			offset := i
			bytecode.UpdateJumpTargets(r.g.codeBlock, instructions[i:i+op.Length()], func(relative int32) int32 {
				return int32(r.adjustJumpTarget(insertionPoint{offset, positionLabelPoint}, insertionPoint{offset + int(relative), positionLabelPoint}))
			})
			i += op.Length()
		} else {
			i += remove
		}
	}
}

// adjustJumpTargetsInFragment converts the absolute original offsets that
// branches in an inserted fragment hold into relative ones.
func (r *bytecodeRewriter) adjustJumpTargetsInFragment(finalOffset int, ins *insertion) {
	for offset := 0; offset < len(ins.instructions); {
		op := bytecode.OpcodeID(ins.instructions[offset])
		if op.IsBranch() {
			bytecodeOffset := finalOffset + offset
			bytecode.UpdateJumpTargets(r.g.codeBlock, ins.instructions[offset:offset+op.Length()], func(label int32) int32 {
				return int32(r.adjustAbsoluteOffset(int(label)) - bytecodeOffset)
			})
		}
		offset += op.Length()
	}
}

func (r *bytecodeRewriter) execute() {
	sort.SliceStable(r.insertions, func(i, j int) bool { return r.insertions[i].index.less(r.insertions[j].index) })
	cb := r.g.codeBlock

	// Jumps first, based on the original offsets.
	r.adjustJumpTargets()

	for i := range cb.ExceptionHandlers {
		h := &cb.ExceptionHandlers[i]
		h.Target = uint32(r.adjustAbsoluteOffset(int(h.Target)))
		h.Start = uint32(r.adjustAbsoluteOffset(int(h.Start)))
		h.End = uint32(r.adjustAbsoluteOffset(int(h.End)))
	}

	for i, pc := range cb.OpProfileControlFlowPCs {
		cb.OpProfileControlFlowPCs[i] = uint32(r.adjustAbsoluteOffset(int(pc)))
	}

	if cb.TypeProfilerExpressionMap != nil {
		adjusted := make(map[uint32][2]uint32, len(cb.TypeProfilerExpressionMap))
		for pc, v := range cb.TypeProfilerExpressionMap {
			adjusted[uint32(r.adjustAbsoluteOffset(int(pc)))] = v
		}
		cb.TypeProfilerExpressionMap = adjusted
	}

	for i := range cb.ExpressionInfo {
		e := &cb.ExpressionInfo[i]
		e.InstPC = uint32(r.adjustAbsoluteOffset(int(e.InstPC)))
	}

	// Then the instructions themselves, back to front.
	instructions := r.g.instructions
	for i := len(r.insertions) - 1; i >= 0; i-- {
		ins := &r.insertions[i]
		at := ins.index.offset
		if ins.remove {
			instructions = slices.Delete(instructions, at, at+ins.length())
			continue
		}
		if ins.includeBranch {
			finalOffset := at + calculateDifference(r.insertions[:i])
			r.adjustJumpTargetsInFragment(finalOffset, ins)
		}
		instructions = slices.Insert(instructions, at, ins.instructions...)
	}
	r.g.instructions = instructions
	r.insertions = nil
}

// --- Generatorification ---------------------------------------------------------

type yieldData struct {
	point    int
	argument bytecode.VirtualRegister
	liveness bytecode.BitVector
}

type generatorFrameData struct {
	point        int
	dst          bytecode.VirtualRegister
	scope        bytecode.VirtualRegister
	symbolTable  bytecode.VirtualRegister
	initialValue bytecode.VirtualRegister
}

type generatorStorage struct {
	identifierIndex uint32
	scopeOffset     bytecode.ScopeOffset
}

func (g *Generator) performGeneratorification() {
	instructions := g.instructions
	cb := g.codeBlock

	enterPoint := 0
	var yields []yieldData
	var frameData *generatorFrameData
	for offset := 0; offset < len(instructions); {
		op := bytecode.OpcodeID(instructions[offset])
		ins := instructions[offset:]
		switch op {
		case bytecode.OpEnterID:
			enterPoint = offset
		case bytecode.OpYieldID:
			y := bytecode.DecodeYield(ins)
			for int(y.YieldPoint) >= len(yields) {
				yields = append(yields, yieldData{})
			}
			yields[y.YieldPoint].point = offset
			yields[y.YieldPoint].argument = y.Argument
		case bytecode.OpCreateGeneratorFrameEnvironmentID:
			b := bytecode.DecodeCreateGeneratorFrameEnvironment(ins)
			frameData = &generatorFrameData{offset, b.Dst, b.Scope, b.SymbolTable, b.InitialValue}
		}
		offset += op.Length()
	}

	// The liveness at each resume point says, conservatively, which
	// registers must be saved and restored.
	graph := bytecode.NewGraph(cb, instructions)
	graph.RunLivenessFixpoint(cb, instructions)
	for i := range yields {
		next := yields[i].point + bytecode.OpYieldID.Length()
		yields[i].liveness = graph.LivenessAtInstruction(cb, instructions, next)
	}

	// Each saved register gets its own slot in the generator frame, named
	// by its local index, so a register resumed earlier need not be saved
	// again.
	var storages []*generatorStorage
	frameSymbolTable := g.generatorFrameSymbolTable
	storageForGeneratorLocal := func(index int) generatorStorage {
		for len(storages) <= index {
			storages = append(storages, nil)
		}
		if s := storages[index]; s != nil {
			return *s
		}
		id := ident.FromNumber(float64(index))
		identifierIndex := uint32(cb.AddIdentifier(id))
		scopeOffset := frameSymbolTable.TakeNextScopeOffset()
		frameSymbolTable.Add(id, bytecode.SymbolTableEntry{Offset: bytecode.VarOffsetForScope(scopeOffset)})
		s := &generatorStorage{identifierIndex, scopeOffset}
		storages[index] = s
		return *s
	}

	rewriter := &bytecodeRewriter{g: g}

	// The global switch on the generator state.
	nextToEnterPoint := enterPoint + bytecode.OpEnterID.Length()
	switchTableIndex := len(cb.SwitchJumpTables)
	state := bytecode.VirtualRegisterForArgumentIncludingThis(generatorArgumentState, 0)
	jumpTable := cb.AddSwitchJumpTable()
	jumpTable.Min = 0
	jumpTable.BranchOffsets = make([]int32, len(yields)+1)
	jumpTable.Add(0, int32(nextToEnterPoint))
	for i, y := range yields {
		jumpTable.Add(int32(i+1), int32(y.point))
	}
	jumpTable.DefaultOffset = int32(nextToEnterPoint)
	rewriter.insertFragmentBefore(nextToEnterPoint, func() {
		bytecode.EmitSwitchImm(g, uint32(switchTableIndex), state)
	})

	scope := bytecode.VirtualRegisterForArgumentIncludingThis(generatorArgumentFrame, 0)
	getPutInfo := bytecode.MakeGetPutInfo(bytecode.DoNotThrowIfNotFound, bytecode.ResolvedClosureVar, bytecode.InitializationModeNotInitialization, g.ecmaMode)
	for _, y := range yields {
		// The save sequence, then return.
		rewriter.insertFragmentBefore(y.point, func() {
			y.liveness.ForEachSetBit(func(index int) {
				operand := bytecode.VirtualRegisterForLocal(index)
				storage := storageForGeneratorLocal(index)
				bytecode.EmitPutToScope(g, scope, storage.identifierIndex, operand, getPutInfo,
					bytecode.SymbolTableOrScopeDepthForSymbolTable(bytecode.VirtualRegister(g.generatorFrameSymbolTableIndex)), uint32(storage.scopeOffset))
			})
			bytecode.EmitRet(g, y.argument)
		})

		// The resume sequence.
		rewriter.replaceBytecodeWithFragment(y.point, func() {
			y.liveness.ForEachSetBit(func(index int) {
				operand := bytecode.VirtualRegisterForLocal(index)
				storage := storageForGeneratorLocal(index)
				bytecode.EmitGetFromScope(g, operand, scope, storage.identifierIndex, getPutInfo, 0, uint32(storage.scopeOffset), g.nextValueProfileIndex())
			})
		})
	}

	if frameData != nil {
		rewriter.replaceBytecodeWithFragment(frameData.point, func() {
			if frameSymbolTable.ScopeSize() == 0 {
				// The generator frame is undefined.
				bytecode.EmitMov(g, frameData.dst, frameData.initialValue)
			} else {
				bytecode.EmitCreateLexicalEnvironment(g, frameData.dst, frameData.scope, frameData.symbolTable, frameData.initialValue)
			}
		})
	}

	rewriter.execute()
}
