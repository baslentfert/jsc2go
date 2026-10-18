//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package vm

import (
	"maps"
	"os"
	"runtime"
	"slices"
	"strconv"
	"sync/atomic"
	"unsafe"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/jit"
)

// Optimized loops (ARCHITECTURE.md): an innermost loop compiled a second time,
// after its baseline code, with the numbers it computes in registers
// (unboxed doubles in F8-F14) instead of in the frame's registers. The
// baseline loop_hint jumps to the optimized version, which checks the
// types of the registers it keeps at its entry. Anything the optimized
// version does not handle goes to the baseline code of the same
// instruction, after the registers it changed are stored again: it never
// resumes in the optimized version (JSC's DFG exits to the baseline code
// likewise, but through an OSR exit).
//
// The instructions are compiled by the baseline compiler (instruction),
// with c.opt set: loadNum, int32Of and storeNum use the kept registers,
// and every exit, slow path and jump out of the loop goes through a stub
// that stores them first (flushTo). Instructions that read or write
// registers other than through those (generic ones) get the registers they
// read stored before them and the ones they write loaded after them.

// jitOpt turns optimized loops on (JSC2GO_OPT=0 turns them off).
var jitOpt = os.Getenv("JSC2GO_OPT") != "0"

// optCallOps are the instructions that may call other code which an
// optimized loop compiles without storing and loading all its registers
// around them: get_by_id (its getter goes to the baseline code), and
// calls, whose inlined callees and thunks keep F8-F14, while a call of
// other generated code stores and loads them (nativeCall) and stops of
// inlined code store them (inlineExits).
//
// Loops with calls are left out unless JSC2GO_OPTCALLS=1: measured with
// these (min of 6 runs of the dumped scripts), md5 -6.8%, base64 -4%,
// delta-blue -4.1%, mandreel and Box2D -2..-3%, n-body +4.2%,
// 3d-raytrace +3.3% (arguments and results still pass through memory,
// and compiling functions with large inlined callees again costs).
var optCallOps = func() (s [bytecode.NumberOfBytecodeIDs]bool) {
	s[bytecode.OpGetByIdID] = true
	if optCalls {
		s[bytecode.OpCallID], s[bytecode.OpCallIgnoreResultID] = true, true
	}
	return
}()

// optCalls allows loops with calls (JSC2GO_OPTCALLS=1).
var optCalls = os.Getenv("JSC2GO_OPTCALLS") == "1"

// optCompiled counts the optimized loops compiled, optDisabled those
// disabled and compiled again (for tests).
var optCompiled, optDisabled atomic.Int64

// optRegs are the registers numbers are kept in: F8-F14 on amd64 (X15
// is Go's zero register), F8-F31 but F15 (the assembler's dScratch) on
// arm64 (32 of them; Go treats all as caller-saved). The machine code
// uses F0-F7.
var optRegs = func() []jit.F {
	n := 15
	if runtime.GOARCH == "arm64" {
		n = 32
	}
	var r []jit.F
	for f := 8; f < n; f++ {
		if !jit.ReservedF(jit.F(f)) { // arm64's F15 is the assembler's scratch
			r = append(r, jit.F(f))
		}
	}
	return r
}()

// optShare: registers may be shared by values never live together when
// a loop's numbers do not fit (x86, seven registers: navier-stokes
// +6.6%, pbkdf2 +4.9%). Not on arm64: with 23 registers, gaussian-blur's
// loops kept 25-27 numbers shared instead of 23 and were 4-5% slower, and
// sharing in loops that fit made navier-stokes 2.5% slower (Apple M-series).
var optShare = len(optRegs) <= 8

// optHotIterations is how many times a loop runs (counted by its baseline
// code, over all activations) before its code is compiled again with an
// optimized version: compiling every loop twice made the benchmarks that
// mostly compile code (the code-load ones, cdjs, prepack) up to 10-25%
// slower. JSC2GO_OPTHOT sets it (0: every loop at once, for tests).
var optHotIterations = func() uint32 {
	if n, err := strconv.Atoi(os.Getenv("JSC2GO_OPTHOT")); err == nil && n >= 0 {
		return uint32(n)
	}
	return 5000
}()

// optDeoptLimit is how many times an optimized loop may give up to its
// baseline code (a failed check) before it is not entered again.
const optDeoptLimit = 1000

// optLoop is an innermost loop compiled a second time.
type optLoop struct {
	hint, start, end int
	pcs              []int // the loop's instructions
	regs             map[bytecode.VirtualRegister]jit.F
	written          map[bytecode.VirtualRegister]bool // kept and written in the loop
	labels           map[int]*jit.Label                // the optimized version's instructions
	entry, cont      *jit.Label                        // entry check; back in the baseline loop_hint
	stubs            map[[2]any]*jit.Label             // flushTo's stubs
	state            *optState
	// counting: not hot yet; the baseline code counts its iterations
	// (in state.deopts) instead.
	counting bool
	// entryRegs are the registers its entry checks, fails how often each
	// was not a number.
	entryRegs []bytecode.VirtualRegister
	fails     []uint32
	// hidden are kept registers the instruction being compiled writes:
	// optReg does not see them (its code writes them to memory), but
	// stores of the kept registers do.
	hidden map[bytecode.VirtualRegister]bool
}

// optState is an optimized loop's counters, read and written by its code.
type optState struct {
	deopts   uint32
	disabled uint32
}

var (
	offOptDeopts   = int(unsafe.Offsetof(optState{}.deopts))
	offOptDisabled = int(unsafe.Offsetof(optState{}.disabled))
)

// optNative are the instructions the optimized version compiles with the
// kept registers: their operands go through loadNum, loadCompared,
// int32Of or exactInt32, their results through storeNum or storeBool.
var optNative = func() (s [bytecode.NumberOfBytecodeIDs]bool) {
	for _, op := range []bytecode.OpcodeID{bytecode.OpAddID, bytecode.OpSubID, bytecode.OpMulID, bytecode.OpDivID,
		bytecode.OpModID, bytecode.OpNegateID, bytecode.OpIncID, bytecode.OpDecID,
		bytecode.OpBitandID, bytecode.OpBitorID, bytecode.OpBitxorID, bytecode.OpLshiftID, bytecode.OpRshiftID,
		bytecode.OpUrshiftID, bytecode.OpBitnotID, bytecode.OpUnsignedID,
		bytecode.OpLessID, bytecode.OpLesseqID, bytecode.OpGreaterID, bytecode.OpGreatereqID,
		bytecode.OpJlessID, bytecode.OpJlesseqID, bytecode.OpJgreaterID, bytecode.OpJgreatereqID,
		bytecode.OpJnlessID, bytecode.OpJnlesseqID, bytecode.OpJngreaterID, bytecode.OpJngreatereqID,
		bytecode.OpJmpID} {
		s[op] = true
	}
	return
}()

// optAware are the instructions whose code reads kept registers only
// through loadNum, int32Of, exactInt32 or loadValue (and a base, never a
// number): they need no stores before them. What they write is not kept
// while they are compiled (it goes to memory) and is loaded after them,
// checked to be a number.
var optAware = func() (s [bytecode.NumberOfBytecodeIDs]bool) {
	for _, op := range []bytecode.OpcodeID{bytecode.OpGetByValID, bytecode.OpPutByValID, bytecode.OpGetLengthID,
		bytecode.OpGetByIdID, bytecode.OpPutByIdID} {
		s[op] = true
	}
	return
}()

// optNumberResult are the native instructions whose result is a number.
var optNumberResult = func() (s [bytecode.NumberOfBytecodeIDs]bool) {
	for _, op := range []bytecode.OpcodeID{bytecode.OpAddID, bytecode.OpSubID, bytecode.OpMulID, bytecode.OpDivID,
		bytecode.OpModID, bytecode.OpNegateID, bytecode.OpIncID, bytecode.OpDecID,
		bytecode.OpBitandID, bytecode.OpBitorID, bytecode.OpBitxorID, bytecode.OpLshiftID, bytecode.OpRshiftID,
		bytecode.OpUrshiftID, bytecode.OpBitnotID, bytecode.OpUnsignedID} {
		s[op] = true
	}
	return
}()

// uses and defs of the instruction at in.
func (c *jitCompiler) usesDefs(in []uint32) (uses, defs []bytecode.VirtualRegister) {
	op := bytecode.OpcodeID(in[0])
	for cp := 0; cp < op.NumberOfCheckpoints(); cp++ {
		bytecode.ComputeUsesForInstruction(c.cb.u, in, cp, func(r bytecode.VirtualRegister) {
			if !slices.Contains(uses, r) {
				uses = append(uses, r)
			}
		})
		bytecode.ComputeDefsForInstruction(c.cb.u, in, cp, func(r bytecode.VirtualRegister) {
			if !slices.Contains(defs, r) {
				defs = append(defs, r)
			}
		})
	}
	return
}

// planOptLoops chooses the loops to optimize and the registers to keep:
// innermost loops, with locals whose every write in the loop is a number
// (an arithmetic result, a number constant, a kept register moved) and
// registers only read, as operands of arithmetic. The most used get the
// registers.
func (c *jitCompiler) planOptLoops(pcs []int) {
	ins := c.cb.u.Instructions
	type span struct{ start, end int }
	var loops []span
	for _, j := range pcs {
		if t, ok := jumpTarget(j, ins[j:]); ok && t <= j {
			loops = append(loops, span{t, j})
		}
	}
	for _, l := range loops {
		inner := true
		for _, o := range loops {
			if o != l && o.start >= l.start && o.end <= l.end {
				inner = false
			}
		}
		if !inner {
			continue
		}
		hint := -1
		var lpcs []int
		calls := false
		for _, pc := range pcs {
			if pc >= l.start && pc <= l.end {
				if op := bytecode.OpcodeID(ins[pc]); jitClobbersF[op] && !c.optCallOK(ins[pc:]) {
					calls = true
				}
				lpcs = append(lpcs, pc)
				if hint < 0 && bytecode.OpcodeID(ins[pc]) == bytecode.OpLoopHintID {
					hint = pc
				}
			}
		}
		if hint < 0 || c.optLoops[hint] != nil || calls || c.cb.jit.optNever[hint] {
			continue
		}
		written := map[bytecode.VirtualRegister]bool{}
		nonNumber := map[bytecode.VirtualRegister]bool{} // written with what may not be a number
		nonArith := map[bytecode.VirtualRegister]bool{}  // read other than as an arithmetic operand
		count := map[bytecode.VirtualRegister]int{}
		movs := map[bytecode.VirtualRegister][]bytecode.VirtualRegister{}
		spec := map[bytecode.VirtualRegister]bool{} // numbers if only used as such
		for _, pc := range lpcs {
			in := ins[pc:]
			op := bytecode.OpcodeID(in[0])
			uses, defs := c.usesDefs(in)
			base := bytecode.InvalidVirtualRegister
			switch op {
			case bytecode.OpGetByValID:
				base = bytecode.DecodeGetByVal(in).Base
			case bytecode.OpPutByValID:
				base = bytecode.DecodePutByVal(in).Base
			case bytecode.OpGetLengthID:
				base = bytecode.DecodeGetLength(in).Base
			case bytecode.OpGetByIdID:
				base = bytecode.DecodeGetById(in).Base
			case bytecode.OpPutByIdID:
				base = bytecode.DecodePutById(in).Base
			}
			for _, u := range uses {
				count[u]++
				if !optNative[op] && !(optAware[op] && u != base) || op == bytecode.OpJmpID {
					nonArith[u] = true
				}
			}
			for _, d := range defs {
				count[d]++
				written[d] = true
				switch {
				case op == bytecode.OpMovID:
					src := bytecode.DecodeMov(in).Src
					if v, ok := c.constant(src); ok {
						if !v.IsNumber() {
							nonNumber[d] = true
						}
					} else {
						movs[d] = append(movs[d], src)
					}
				case optNumberResult[op]:
				case op == bytecode.OpGetByValID || op == bytecode.OpGetLengthID || op == bytecode.OpGetByIdID:
					spec[d] = true
				default:
					nonNumber[d] = true
				}
			}
		}
		// Registers that were not numbers at the entry of an optimized
		// version before (noteOptLoop).
		for r := range c.cb.jit.optNoKeep[hint] {
			nonNumber[r], nonArith[r] = true, true
		}
		// An element or length is taken for a number if the loop only
		// computes with it.
		for d := range spec {
			if nonArith[d] {
				nonNumber[d] = true
			}
		}
		// Moves of registers that are not kept numbers are not numbers.
		for changed := true; changed; {
			changed = false
			for d, srcs := range movs {
				if nonNumber[d] {
					continue
				}
				for _, s := range srcs {
					if !written[s] && nonArith[s] || written[s] && nonNumber[s] || s.IsArgument() && written[s] {
						nonNumber[d], changed = true, true
						break
					}
				}
			}
		}
		var cands []bytecode.VirtualRegister
		for r, n := range count {
			if n == 0 || r.IsConstant() || r.IsHeader() {
				continue
			}
			if written[r] {
				if r.IsLocal() && !nonNumber[r] {
					cands = append(cands, r)
				}
			} else if !nonArith[r] {
				cands = append(cands, r)
			}
		}
		if len(cands) == 0 {
			continue
		}
		slices.SortFunc(cands, func(a, b bytecode.VirtualRegister) int {
			if count[a] != count[b] {
				return count[b] - count[a]
			}
			return int(a - b)
		})
		// When they do not all fit (optShare), registers that are never
		// live at the same time share one (a temporary lives from its
		// definition to its use): two interfere if both are live before
		// an instruction or defined by it. What is live after it is live
		// before it or defined by it.
		regOf := map[bytecode.VirtualRegister]jit.F{}
		var lives []map[bytecode.VirtualRegister]bool
		for _, pc := range lpcs {
			if len(cands) <= len(optRegs) || !optShare {
				break // (not needed: compile time)
			}
			_, defs := c.usesDefs(ins[pc:])
			s := map[bytecode.VirtualRegister]bool{}
			for _, r := range cands {
				if c.optLive(r, pc) || slices.Contains(defs, r) {
					s[r] = true
				}
			}
			lives = append(lives, s)
		}
		for i, r := range cands {
			if len(cands) <= len(optRegs) || !optShare {
				// One each when they fit, or the most used ones (arm64).
				if i < len(optRegs) {
					regOf[r] = optRegs[i]
				}
				continue
			}
			used := map[jit.F]bool{}
			for _, s := range lives {
				if s[r] {
					for q := range s {
						if f, ok := regOf[q]; ok {
							used[f] = true
						}
					}
				}
			}
			for _, f := range optRegs {
				if !used[f] {
					regOf[r] = f
					break
				}
			}
		}
		o := &optLoop{hint: hint, start: l.start, end: l.end, pcs: lpcs, regs: map[bytecode.VirtualRegister]jit.F{},
			written: map[bytecode.VirtualRegister]bool{}, labels: map[int]*jit.Label{},
			entry: jit.NewLabel(), cont: jit.NewLabel(), stubs: map[[2]any]*jit.Label{}, state: &optState{}}
		for r, f := range regOf {
			o.regs[r] = f
			if written[r] {
				o.written[r] = true
			}
		}
		for _, pc := range lpcs {
			o.labels[pc] = jit.NewLabel()
		}
		if optHotIterations > 0 && !c.cb.jit.optHot[hint] {
			o.counting = true
		}
		if c.optWanted == nil {
			c.optWanted = new(uint32)
		}
		if c.optLoops == nil {
			c.optLoops = map[int]*optLoop{}
		}
		c.optLoops[hint] = o
	}
}

// optLive reports whether local or argument r is live before pc (an
// argument always is).
func (c *jitCompiler) optLive(r bytecode.VirtualRegister, pc int) bool {
	if !r.IsLocal() {
		return true
	}
	if c.optGraph == nil {
		ins := c.cb.u.Instructions
		c.optGraph = bytecode.NewGraph(c.cb.u, ins)
		c.optGraph.RunLivenessFixpoint(c.cb.u, ins)
		c.optLiveAt = map[int]bytecode.BitVector{}
	}
	if pc >= len(c.cb.u.Instructions) {
		return false
	}
	live, ok := c.optLiveAt[pc]
	if !ok {
		live = c.optGraph.LivenessAtInstruction(c.cb.u, c.cb.u.Instructions, pc)
		c.optLiveAt[pc] = live
	}
	return live.Get(r.ToLocal())
}

// flush stores the kept registers the loop writes that are live before pc,
// except skip.
func (c *jitCompiler) optFlush(o *optLoop, pc int, skip []bytecode.VirtualRegister) {
	m := &c.m
	for _, r := range slices.Sorted(maps.Keys(o.written)) {
		if slices.Contains(skip, r) || !c.optLive(r, pc) {
			continue
		}
		m.MovFromF(rTmp, o.regs[r])
		m.Xor64(rTmp, rTmp, rMask)
		c.storeWord(r, rTmp)
	}
}

// flushTo is the label of a stub that stores the kept registers live
// before pc (where the code goes on) and branches to target.
func (c *jitCompiler) flushTo(o *optLoop, pc int, target *jit.Label) *jit.Label {
	key := [2]any{pc, target}
	if l := o.stubs[key]; l != nil {
		return l
	}
	l := jit.NewLabel()
	o.stubs[key] = l
	c.cold = append(c.cold, func() {
		c.m.Bind(l)
		c.optFlush(o, pc, nil)
		if target == c.labels[pc] {
			c.optRefill(pc)
		}
		c.m.B(target)
	})
	return l
}

// optRefill fills the number cache as the baseline code at pc has it
// (the instructions before it in its block filled it): code that goes on
// there from an optimized loop.
func (c *jitCompiler) optRefill(pc int) {
	m := &c.m
	for k, e := range c.fcacheAt[pc] {
		if e.valid {
			m.Load64(rTmp, rRegs, c.off(e.r)+8)
			m.Xor64(rTmp, rTmp, rMask)
			m.MovToF(jitFCache[k], rTmp)
		}
	}
}

// deopt is the label of a stub that gives up on the optimized version at
// pc: it stores the kept registers and goes on in the baseline code of
// the instruction at pc, counting (skip: registers whose memory is
// already current).
func (c *jitCompiler) deopt(o *optLoop, pc int, skip []bytecode.VirtualRegister) *jit.Label {
	key := [2]any{pc, len(skip) > 0}
	if l := o.stubs[key]; l != nil && len(skip) == 0 {
		return l
	}
	l := jit.NewLabel()
	o.stubs[key] = l
	c.cold = append(c.cold, func() {
		m := &c.m
		m.Bind(l)
		c.optFlush(o, pc, skip)
		c.optCount(o)
		c.optRefill(pc)
		m.B(c.labels[pc])
	})
	return l
}

// optCount counts a deopt; at the limit the loop is not entered again.
func (c *jitCompiler) optCount(o *optLoop) {
	m := &c.m
	counted := jit.NewLabel()
	m.MovImm(jit.S0, uint64(uintptr(unsafe.Pointer(o.state))))
	m.Load32(jit.S1, jit.S0, offOptDeopts)
	m.AddImm64(jit.S1, jit.S1, 1)
	m.Store32(jit.S1, jit.S0, offOptDeopts)
	m.Branch32Imm(jit.ULT, jit.S1, optDeoptLimit, counted)
	m.Store32(jit.S1, jit.S0, offOptDisabled)
	// Compiled again (noteOptLoop).
	m.MovImm(jit.S0, uint64(uintptr(unsafe.Pointer(c.optWanted))))
	m.Store32(jit.S1, jit.S0, 0)
	m.Bind(counted)
}

// emitOptLoops compiles the optimized versions; it reports nothing: a loop
// it cannot compile is not entered.
func (c *jitCompiler) emitOptLoops() {
	for _, hint := range slices.Sorted(maps.Keys(c.optLoops)) {
		if o := c.optLoops[hint]; !o.counting {
			c.emitOptLoop(o)
		}
	}
}

func (c *jitCompiler) emitOptLoop(o *optLoop) {
	m := &c.m
	ins := c.cb.u.Instructions
	start, coldLen := m.Len(), len(c.cold)
	c.keepCells = append(c.keepCells, o.state)
	c.opt = o
	ok := true
	// The entry: not disabled, and the kept registers live here numbers.
	m.Bind(o.entry)
	m.MovImm(jit.S0, uint64(uintptr(unsafe.Pointer(o.state))))
	m.Load32(jit.S1, jit.S0, offOptDisabled)
	m.BranchNonZero(jit.S1, o.cont)
	fail := jit.NewLabel()
	afterHint := o.hint + bytecode.OpLoopHintID.Length()
	for _, r := range slices.Sorted(maps.Keys(o.regs)) {
		if o.written[r] && !c.optLive(r, afterHint) {
			continue // written before it is read
		}
		o.entryRegs = append(o.entryRegs, r)
	}
	o.fails = make([]uint32, len(o.entryRegs))
	c.keepCells = append(c.keepCells, o.fails)
	var failRegs []*jit.Label
	for _, r := range o.entryRegs {
		l := jit.NewLabel()
		failRegs = append(failRegs, l)
		m.Load64(rTmp, rRegs, c.off(r)+8)
		m.Branch64Imm(jit.ULE, rTmp, maxTag, l)
		m.Xor64(rTmp, rTmp, rMask)
		m.MovToF(o.regs[r], rTmp)
	}
	m.B(o.labels[afterHint])
	// Which register was not a number, for the next compile.
	for i, l := range failRegs {
		m.Bind(l)
		m.MovImm(jit.S0, uint64(uintptr(unsafe.Pointer(&o.fails[i]))))
		m.Load32(jit.S1, jit.S0, 0)
		m.AddImm64(jit.S1, jit.S1, 1)
		m.Store32(jit.S1, jit.S0, 0)
		m.B(fail)
	}
	m.Bind(fail)
	c.optCount(o)
	m.B(o.cont)
	// Type facts for the registers not kept, per block (the entry is
	// one).
	spec := jitSpec && !c.cb.jit.noSpec
	blockStart := map[int]bool{afterHint: true}
	for _, t := range bytecode.ComputePreciseJumpTargets(c.cb.u, ins) {
		blockStart[t] = true
	}
	clear(c.facts)
	c.fcache = [len(jitFCache)]jitFEntry{}
	startsLen := len(c.optStarts)
	for _, pc := range o.pcs {
		m.Bind(o.labels[pc])
		c.optStarts = append(c.optStarts, optStart{pc, m.Len()})
		if blockStart[pc] {
			clear(c.facts)
			c.fcache = [len(jitFCache)]jitFEntry{}
		}
		c.pending, c.slowResumes, c.factsOn = c.pending[:0], c.slowResumes[:0], spec
		c.pendingF, c.curOp = c.pendingF[:0], bytecode.OpcodeID(ins[pc])
		ok = c.optInstruction(o, pc, ins[pc:])
		c.factsOn = false
		if ok {
			c.applyFacts(pc, ins[pc:], blockStart)
		} else {
			if jitLog {
				println("jit: opt loop not compiled at", pc, bytecode.OpcodeID(ins[pc]).String())
			}
			break
		}
	}
	if ok {
		// Out of the loop at its end.
		next := o.end + bytecode.OpcodeID(ins[o.end]).Length()
		if l := c.labels[next]; l != nil {
			m.B(c.flushTo(o, next, l))
		} else {
			m.B(c.flushTo(o, next, c.leave(next)))
		}
		// Its cold code, with c.opt set.
		for i := coldLen; i < len(c.cold); i++ {
			c.cold[i]()
		}
		c.cold = c.cold[:coldLen]
	}
	c.opt = nil
	if !ok {
		var labels []*jit.Label
		for _, ls := range []map[int]*jit.Label{c.exits, c.leaves, c.callExits, c.fuel, c.labels, c.nestedExits, c.nativeExits, o.labels} {
			for _, l := range ls {
				labels = append(labels, l)
			}
		}
		for _, l := range o.stubs {
			labels = append(labels, l)
		}
		m.Truncate(start, append(labels, o.entry, o.cont)...)
		c.optStarts = c.optStarts[:startsLen]
		c.cold = c.cold[:coldLen]
		m.Bind(o.entry)
		m.B(o.cont)
		return
	}
	optCompiled.Add(1)
	if jitLog {
		println("jit: opt loop at", o.hint, "regs", len(o.regs))
	}
}

// optInstruction compiles the instruction at pc in the optimized version.
func (c *jitCompiler) optInstruction(o *optLoop, pc int, in []uint32) bool {
	m := &c.m
	op := bytecode.OpcodeID(in[0])
	next := pc + op.Length()
	switch {
	case op == bytecode.OpLoopHintID:
		m.Load64(rTmp, rFrame, offFrameFuel)
		m.SubImm64(rTmp, rTmp, 1)
		m.Store64(rTmp, rFrame, offFrameFuel)
		l := c.fuel[next]
		if l == nil {
			l = jit.NewLabel()
			c.fuel[next] = l
		}
		m.BranchZero(rTmp, c.flushTo(o, next, l))
		return true
	case op == bytecode.OpMovID:
		b := bytecode.DecodeMov(in)
		fd, dk := o.regs[b.Dst]
		fs, sk := o.regs[b.Src]
		switch {
		case dk && sk:
			m.FMov(fd, fs)
			return true
		case dk:
			if !c.loadNum(fd, b.Src, c.deopt(o, pc, nil)) {
				return false
			}
			return true
		case sk:
			m.MovFromF(rTmp, fs)
			m.Xor64(rTmp, rTmp, rMask)
			c.storeWord(b.Dst, rTmp)
			return true
		}
	case optNative[op]:
		return c.instruction(pc, in)
	}
	// A generic instruction: what it reads stored before it, what it
	// writes loaded after it. One that may call other code (whose
	// optimized loops use the same registers) gets all of them stored
	// and loaded again.
	uses, defs := c.usesDefs(in)
	clobbers := jitClobbersF[op] && !c.optCallOK(in)
	aware := optAware[op]
	if _, ok := jumpTarget(pc, in); ok {
		for _, d := range defs {
			if _, kept := o.regs[d]; kept || clobbers {
				return false // the loads after it would not run on its jump
			}
		}
	}
	if clobbers {
		c.optFlush(o, pc, nil)
	} else {
		for _, u := range uses {
			if aware && !slices.Contains(defs, u) {
				continue // read from its register
			}
			if f, kept := o.regs[u]; kept && o.written[u] {
				m.MovFromF(rTmp, f)
				m.Xor64(rTmp, rTmp, rMask)
				c.storeWord(u, rTmp)
			}
		}
	}
	var keptDefs []bytecode.VirtualRegister
	for _, d := range defs {
		if _, kept := o.regs[d]; kept {
			keptDefs = append(keptDefs, d)
		}
	}
	// What it writes goes to memory while it is compiled.
	o.hidden = map[bytecode.VirtualRegister]bool{}
	for _, d := range keptDefs {
		o.hidden[d] = true
	}
	coldLen := len(c.cold)
	ok := c.instruction(pc, in)
	if ok && len(keptDefs) > 0 && len(c.cold) > coldLen {
		// Its cold code now, while they are not kept (it writes them
		// too).
		over := jit.NewLabel()
		m.B(over)
		for i := coldLen; i < len(c.cold); i++ {
			c.cold[i]()
		}
		c.cold = c.cold[:coldLen]
		m.Bind(over)
	}
	o.hidden = nil
	if !ok {
		return false
	}
	if len(keptDefs) > 0 || clobbers {
		redo := c.deopt(o, next, keptDefs)
		for _, r := range slices.Sorted(maps.Keys(o.regs)) {
			if !slices.Contains(keptDefs, r) && (!clobbers || !c.optLive(r, next)) {
				continue
			}
			m.Load64(rTmp, rRegs, c.off(r)+8)
			if slices.Contains(keptDefs, r) {
				m.Branch64Imm(jit.ULE, rTmp, maxTag, redo)
			}
			m.Xor64(rTmp, rTmp, rMask)
			m.MovToF(o.regs[r], rTmp)
		}
	}
	return true
}

// optStart is where an optimized version's instruction starts.
type optStart struct{ pc, offset int }

// optReg is the register r is kept in, in the optimized loop being
// compiled.
func (c *jitCompiler) optReg(r bytecode.VirtualRegister) (jit.F, bool) {
	if c.opt == nil || c.opt.hidden[r] {
		return 0, false
	}
	f, ok := c.opt.regs[r]
	return f, ok
}

// optKept reports whether r is kept in a register.
func (c *jitCompiler) optKept(r bytecode.VirtualRegister) bool {
	_, ok := c.optReg(r)
	return ok
}

// optCountHot counts an iteration of a loop that is not hot yet, in its
// baseline loop_hint; at optHotIterations it asks for the code to be
// compiled again (checkReprofile), with an optimized version.
func (c *jitCompiler) optCountHot(o *optLoop) {
	m := &c.m
	c.keepCells = append(c.keepCells, o.state, c.optWanted)
	counted := jit.NewLabel()
	m.MovImm(jit.S0, uint64(uintptr(unsafe.Pointer(o.state))))
	m.Load32(jit.S1, jit.S0, offOptDeopts)
	m.AddImm64(jit.S1, jit.S1, 1)
	m.Store32(jit.S1, jit.S0, offOptDeopts)
	// (From the threshold on, every iteration, until the code is
	// compiled again: at exactly the threshold the loop may have been at
	// its last iteration.)
	m.Branch32Imm(jit.ULT, jit.S1, optHotIterations, counted)
	m.MovImm(jit.S0, uint64(uintptr(unsafe.Pointer(c.optWanted))))
	m.Store32(jit.S1, jit.S0, 0)
	// Out to Go at the next iteration (a fuel exit), where checkReprofile
	// compiles the code again: code called by other generated code
	// (n-body's advance) does not return to Go otherwise, and its loops
	// never got their optimized versions.
	m.MovImm(jit.S1, 1)
	m.Store64(jit.S1, rFrame, offFrameFuel)
	m.Bind(counted)
}

// noteOptLoop notes, when code is compiled again (checkReprofile), what
// loop o learned: that it got hot, or that its optimized version was
// disabled: then the registers its entry found not to be numbers are not
// kept next time, and a loop disabled for another reason, or twice, gets
// no optimized version again (mandreel and Box2D each had a hot loop
// whose entry kept failing, and their baseline loop_hint kept trying it:
// -4%).
func (s *jitState) noteOptLoop(o *optLoop) {
	if o.counting {
		if o.state.deopts >= optHotIterations {
			if s.optHot == nil {
				s.optHot = map[int]bool{}
			}
			s.optHot[o.hint] = true
		}
		return
	}
	if o.state.disabled == 0 {
		return
	}
	if s.optTries == nil {
		s.optTries, s.optNever, s.optNoKeep = map[int]int{}, map[int]bool{}, map[int]map[bytecode.VirtualRegister]bool{}
	}
	s.optTries[o.hint]++
	optDisabled.Add(1)
	entry := false
	for i, r := range o.entryRegs {
		if o.fails[i] > 0 {
			if s.optNoKeep[o.hint] == nil {
				s.optNoKeep[o.hint] = map[bytecode.VirtualRegister]bool{}
			}
			s.optNoKeep[o.hint][r] = true
			entry = true
		}
	}
	if !entry || s.optTries[o.hint] >= 2 {
		s.optNever[o.hint] = true
	}
}

// optFlushVia is optFlush through base instead of rRegs, into m (inlined
// code's, whose rRegs are the callee's registers).
func (c *jitCompiler) optFlushVia(o *optLoop, pc int, base jit.R, m *jit.Masm) {
	for _, r := range slices.Sorted(maps.Keys(o.written)) {
		f, kept := o.regs[r]
		if !kept || !c.optLive(r, pc) {
			continue
		}
		m.MovFromF(rTmp, f)
		m.Xor64(rTmp, rTmp, rMask)
		m.StoreZero64(base, c.off(r))
		m.Store64(rTmp, base, c.off(r)+8)
	}
}

// optReload loads the kept registers live before pc again (after a call
// of other generated code; optFlush stored them before it).
func (c *jitCompiler) optReload(o *optLoop, pc int) {
	m := &c.m
	for _, r := range slices.Sorted(maps.Keys(o.regs)) {
		if o.hidden[r] || !c.optLive(r, pc) {
			continue // (a hidden one is loaded and checked after it)
		}
		m.Load64(rTmp, rRegs, c.off(r)+8)
		m.Xor64(rTmp, rTmp, rMask)
		m.MovToF(o.regs[r], rTmp)
	}
}

// optCallOK reports whether an optimized loop may compile the instruction
// at in, which may call other code, without storing and loading all its
// kept registers around it (optCallOps): with JSC2GO_OPTCALLS=1 every
// call, else a call whose site only saw a builtin with a thunk (n-body's
// Math.sqrt: +17.8% on arm64, +4.2% on x86, while calls of inlined user
// functions made md5 slower on x86).
func (c *jitCompiler) optCallOK(in []uint32) bool {
	op := bytecode.OpcodeID(in[0])
	if optCallOps[op] {
		return true
	}
	switch op {
	case bytecode.OpCallID:
		return thunkCallee(c.cb.callProfiles[bytecode.DecodeCall(in).MetadataID]) != nil
	case bytecode.OpCallIgnoreResultID:
		return thunkCallee(c.cb.callIgnoreProfiles[bytecode.DecodeCallIgnoreResult(in).MetadataID]) != nil
	}
	return false
}
