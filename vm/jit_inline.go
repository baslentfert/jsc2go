//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package vm

import (
	"os"
	"strconv"
	"unsafe"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/jit"
)

// Inlining of small functions (JSC's DFG inlines them; its baseline JIT
// does not).
//
// A call or call_ignore_result whose site called one small function in the
// interpreter (callProfile) gets that function's code in place, after a
// check that the callee is still that function: the arguments go to the
// registers a call would give the callee (above the caller's, at the VM's
// sp), RRegs points at them while the callee's instructions run, and its
// ret stores the result and goes on after the call. No frame is made and
// no depth counted: that is where a call's time goes.
//
// The callee's instructions are compiled by a second compiler (inline set)
// into the same machine code, with exits of their own. When the inlined
// code needs Go (a slow path, a guard, anything its machine code does not
// handle), a stub makes the frame the call would have made, around the
// registers that are already in place, and stops with the callee's exit
// in it, as a callee's own code does: the caller stops with a nested exit
// (jitExitNested) and Go finishes the callee (finishNative). So the
// inlined code needs no state of its own to resume, at the cost of a slow
// exit, which the choice of callees keeps rare.
//
// The callee must not need its frame in machine code: no loops (their fuel
// is in the frame), calls, arguments, closures or scopes of its own.

// inlineMaxInstructions bounds the callees inlined (bytecode instructions).
const inlineMaxInstructions = 30

// inlineExitLimit is how often an inlined callee may stop before its
// caller is compiled again without inlining it (as JSC's DFG recompiles
// code whose OSR exits are frequent).
const inlineExitLimit = 64

// inlineSite counts the stops of a call's inlined callee.
type inlineSite struct {
	pc    int
	exits uint64
}

// inlineCtx is the state of a second compiler that compiles an inlined
// callee.
type inlineCtx struct {
	caller *jitCompiler
	pc     int // the call's
	callee *Object
	// dstOff is the call's destination in the caller's registers, if it
	// has one; done is where the code goes on after the call.
	dstOff int
	hasDst bool
	done   *jit.Label
	// cs describes the callee's frame for the stubs that make it.
	cs *callCase
	// argc counts this.
	argc uint32
	// site counts the stops of the outermost inlined call; parent is the
	// inlined callee this one is inlined into (nil: the caller's code).
	site   *inlineSite
	parent *inlineCtx
	size   int // the callee's registers
	// depth is 1 for a callee inlined into the caller's code; regsOff
	// the registers between the VM's sp and this callee's.
	depth, regsOff int
}

// inlineMaxBytes bounds the machine code of one inlined call (with the
// callees inlined into it), inlineBudget all a compiled function's: the
// inlined code is the callee's baseline code with its slow paths and the
// stubs that make its frame (3-7 KB a site), and on Apple cores md5's
// inlined calls (md5_cmn with its five small calls inlined into each of
// md5_ff, gg, hh and ii: 23 KB each) pushed the hot code out of the
// instruction cache (-11% there).
// (Experiment: JSC2GO_INLINEBYTES sets the bound per call; none by
// default: on x86 a bound costs gains (richards +32% with none, +23%
// with 16 KB, -2% with 6 KB and 24 KB a function).)
var inlineMaxBytes = func() int {
	if n, err := strconv.Atoi(os.Getenv("JSC2GO_INLINEBYTES")); err == nil && n > 0 {
		return n
	}
	return 1 << 30
}()

const inlineBudget = 1 << 30

// jitInline is false with JSC2GO_INLINE=0: no call is inlined (for A/B
// runs of one binary).
var jitInline = os.Getenv("JSC2GO_INLINE") != "0"

// inlineMaxDepth bounds the chain of callees inlined into each other.
// Three (core_md5's 64 calls of md5_ff, each with md5_cmn and its five
// calls of safe_add and bit_rol inlined) made md5 slower, also in the
// steady state (average iteration 31.1 -> 24.2): far more code, and more
// of it compiled again.
const inlineMaxDepth = 2

// root returns the outermost inlined call of a chain.
func (in *inlineCtx) root() *inlineCtx {
	for in.parent != nil {
		in = in.parent
	}
	return in
}

// chain returns the inlined calls from the outermost to in.
func (in *inlineCtx) chain() []*inlineCtx {
	var l []*inlineCtx
	for ; in != nil; in = in.parent {
		l = append([]*inlineCtx{in}, l...)
	}
	return l
}

// inlineCandidate returns the callee to inline at a site with profile p,
// with its code block, or nil.
func (c *jitCompiler) inlineCandidate(p callProfile) (*Object, *CodeBlock) {
	if c.inline != nil && c.inline.depth >= inlineMaxDepth || !jit.NativeCalls || p.poly || p.callee == nil || p.callee2 != nil {
		return nil, nil
	}
	// Callees inlined into the caller's code may call small functions,
	// which are inlined into them; those may not call.
	calls := c.inline == nil || c.inline.depth < inlineMaxDepth-1
	o := p.callee
	j, ok := o.fn.(*jsFunction)
	if !ok || j.classConstructor || o.realm != c.cb.realm {
		return nil, nil
	}
	cb := j.code[bytecode.CodeForCall]
	if cb == nil {
		cb = o.realm.linkedCode(j.exec, bytecode.CodeForCall)
	}
	if cb == nil || cb.realm != c.cb.realm || len(cb.u.ExceptionHandlers) != 0 ||
		cb.u.CodeType != bytecode.FunctionCode || cb.u.NumCalleeLocals > maxRegs {
		return nil, nil
	}
	ins := cb.u.Instructions
	n := 0
	for pc := 0; pc < len(ins); pc += bytecode.OpcodeID(ins[pc]).Length() {
		op := bytecode.OpcodeID(ins[pc])
		if n++; n > inlineMaxInstructions {
			return nil, nil
		}
		if !inlineableOp(op, calls) && !(op == bytecode.OpCallID && thunkCallee(cb.callProfiles[bytecode.DecodeCall(ins[pc:]).MetadataID]) != nil ||
			op == bytecode.OpCallIgnoreResultID && thunkCallee(cb.callIgnoreProfiles[bytecode.DecodeCallIgnoreResult(ins[pc:]).MetadataID]) != nil ||
			op == bytecode.OpTailCallID && thunkCallee(cb.tailCallProfiles[bytecode.DecodeTailCall(ins[pc:]).MetadataID]) != nil) {
			return nil, nil
		}
		if op == bytecode.OpGetByIdID && callsGetter(&cb.getByIdCaches[bytecode.DecodeGetById(ins[pc:]).MetadataID]) {
			return nil, nil
		}
	}
	return o, cb
}

// callsGetter reports whether get_by_id's cache holds an accessor: the
// site calls a getter, which inlined code cannot (it would stop every
// time; OfflineAssembler's Token.equals stopped in some 50 callers and had
// them all compiled again).
func callsGetter(ic *getByIdCache) bool {
	if ic.shape != nil && ic.accessor {
		return true
	}
	if ic.poly != nil {
		for i := range ic.poly.entries {
			if e := &ic.poly.entries[i]; e.shape != nil && e.accessor {
				return true
			}
		}
	}
	return false
}

// inlineableOp reports whether an inlined callee may have op: not the ones
// whose machine code uses the frame (but enter and ret, which the second
// compiler compiles itself), calls (but, if calls, call and
// call_ignore_result, which must then be inlined too), and the ones that
// would take a slow exit every time.
func inlineableOp(op bytecode.OpcodeID, calls bool) bool {
	switch op {
	case bytecode.OpCallID, bytecode.OpCallIgnoreResultID, bytecode.OpTailCallID:
		return calls
	case bytecode.OpLoopHintID, bytecode.OpArgumentCountID, bytecode.OpGetArgumentID,
		bytecode.OpConstructID,
		bytecode.OpCallVarargsID, bytecode.OpTailCallVarargsID, bytecode.OpConstructVarargsID,
		bytecode.OpCallDirectEvalID, bytecode.OpSuperConstructID, bytecode.OpSuperConstructVarargsID,
		bytecode.OpCreateDirectArgumentsID, bytecode.OpCreateClonedArgumentsID, bytecode.OpCreateScopedArgumentsID,
		bytecode.OpCreateRestID, bytecode.OpCreateLexicalEnvironmentID, bytecode.OpCreateGeneratorFrameEnvironmentID,
		bytecode.OpNewFuncID, bytecode.OpNewFuncExpID, bytecode.OpNewGeneratorFuncID, bytecode.OpNewGeneratorFuncExpID,
		bytecode.OpNewAsyncFuncID, bytecode.OpNewAsyncFuncExpID, bytecode.OpNewAsyncGeneratorFuncID,
		bytecode.OpNewAsyncGeneratorFuncExpID, bytecode.OpGetScopeID, bytecode.OpYieldID,
		bytecode.OpCatchID, bytecode.OpDebugID,
		// Allocations from the VM's pools: an empty pool is a slow path,
		// which in inlined code makes the frame every time (3d-raytrace's
		// vector functions stopped so 850 times a run).
		bytecode.OpNewObjectID, bytecode.OpCreateThisID, bytecode.OpNewArrayID, bytecode.OpNewRegExpID:
		return false
	}
	return true
}

// tryInline compiles, for the call at pc (dst < 0 for call_ignore_result),
// the callee its profile saw in place, if it can: it returns the label of
// the normal call, which the caller compiles there, and the label after
// both; nil, nil if it emitted nothing.
func (c *jitCompiler) tryInline(pc int, p callProfile, callee bytecode.VirtualRegister, argc, argv uint32, dst bytecode.VirtualRegister, hasDst bool) (call, done *jit.Label) {
	if !jitInline || c.inline == nil && c.cb.jit.noInlineAt[pc] {
		return nil, nil
	}
	if p.callee2 == nil {
		return c.tryInlineOne(pc, p, callee, argc, argv, dst, hasDst, nil)
	}
	// Two callees: the first one's code, then, if the callee is not that
	// one, the second's, then the call (ML's matrix accessors: a matrix
	// or its transpose, 21.5 million calls a run).
	p1, p2 := callProfile{callee: p.callee}, callProfile{callee: p.callee2}
	call1, done := c.tryInlineOne(pc, p1, callee, argc, argv, dst, hasDst, nil)
	if call1 == nil {
		return c.tryInlineOne(pc, p2, callee, argc, argv, dst, hasDst, nil)
	}
	c.m.Bind(call1)
	call2, _ := c.tryInlineOne(pc, p2, callee, argc, argv, dst, hasDst, done)
	if call2 == nil {
		call2 = jit.NewLabel() // the call alone after the first
	}
	return call2, done
}

// tryInlineOne is tryInline for the one callee of profile p; done, if not
// nil, is the label after the call to use (shared by the cases of a
// polymorphic site).
func (c *jitCompiler) tryInlineOne(pc int, p callProfile, callee bytecode.VirtualRegister, argc, argv uint32, dst bytecode.VirtualRegister, hasDst bool, sharedDone *jit.Label) (call, done *jit.Label) {
	o, cb := c.inlineCandidate(p)
	if o == nil {
		return nil, nil
	}
	if _, ok := c.constant(callee); ok {
		return nil, nil
	}
	this := bytecode.VirtualRegister(bytecode.CallFrameSlotThisArgument - int(argv))
	if c.off(this) < 0 {
		return nil, nil
	}
	m := &c.m
	start, coldLen := m.Len(), len(c.cold)
	call, done = jit.NewLabel(), sharedDone
	if done == nil {
		done = jit.NewLabel()
	}
	callL := call
	j := o.fn.(*jsFunction)
	u := cb.u
	nl := u.NumCalleeLocals
	size := nl + bytecode.CallFrameSlotThisArgument + max(int(argc), u.NumParameters)
	cs := &callCase{callee: o, exec: j.exec, cb: cb, nl: int64(nl), size: int64(size),
		consts: unsafe.Pointer(unsafe.SliceData(cb.constants)), nvars: int64(u.NumVars),
		extra: int64(max(u.NumParameters-int(argc), 0))}

	// The callee, then what a call checks (directCall): this, the call
	// depth, the chain of generated-code frames and room for the registers.
	m.Load64(jit.S1, rRegs, c.off(callee)+8)
	m.Branch64Imm(jit.NE, jit.S1, uint32(tagObject), call)
	m.Load64(jit.S0, rRegs, c.off(callee))
	m.MovImm(jit.S2, uint64(uintptr(unsafe.Pointer(o))))
	m.Branch64(jit.NE, jit.S0, jit.S2, call)
	thisChecked := jit.NewLabel()
	m.Load64(jit.S9, rRegs, c.off(this)+8)
	m.Branch64Imm(jit.EQ, jit.S9, uint32(tagCell), call)
	m.Branch64Imm(jit.NE, jit.S9, uint32(tagScope), thisChecked)
	m.Load64(jit.S9, rRegs, c.off(this))
	m.Load8(jit.S9, jit.S9, offScopeKind)
	m.Branch32Imm(jit.EQ, jit.S9, uint32(scopeWith), call)
	m.Bind(thisChecked)
	// (Inlined into inlined callees, the stops make all their frames:
	// room for one more of each, and the registers above the outer
	// callees'.)
	levels, below := 1, 0
	if c.inline != nil {
		levels, below = c.inline.depth+1, c.inline.regsOff+c.inline.size
	}
	m.Load64(jit.S3, rFrame, offFrameVM)
	root := c
	if c.inline != nil {
		root = c.inline.root().caller
	} else {
		// Once an activation: room for the inlined calls of the code
		// (inlineRoom), checked in cold code against what they all need.
		check, checked := jit.NewLabel(), jit.NewLabel()
		m.Load64(jit.S4, rFrame, offFrameInlineRoom)
		m.Branch64Imm(jit.EQ, jit.S4, 0, check)
		m.Bind(checked)
		c.cold = append(c.cold, func() {
			m.Bind(check)
			c.inlineActivationCheck(callL)
			m.B(checked)
		})
	}
	oldNeed := root.inlineNeed
	root.inlineNeed = max(root.inlineNeed, below+size)
	m.Load64(jit.S5, jit.S3, offVMSp)
	m.AddImm64(jit.S9, jit.S5, uint32(below+size))
	// The registers above sp hold old values from here (VM.spDirty).
	dirtyOK := jit.NewLabel()
	m.Load64(rTmp, jit.S3, offVMSpDirty)
	m.Branch64(jit.UGE, rTmp, jit.S9, dirtyOK)
	m.Store64(jit.S9, jit.S3, offVMSpDirty)
	m.Bind(dirtyOK)
	// S8 = the callee's registers, S9 = its register 0: the callee, this,
	// the arguments, and undefined for its vars and missing arguments.
	m.Load64(jit.S8, jit.S3, offVMRegStack)
	m.AddShift64(jit.S8, jit.S8, jit.S5, 4)
	if below > 0 {
		m.AddImm64(jit.S8, jit.S8, uint32(below*valueSize))
	}
	m.AddImm64(jit.S9, jit.S8, uint32(nl*valueSize))
	m.Store64(jit.S0, jit.S9, bytecode.CallFrameSlotCallee*valueSize)
	m.Store64(jit.S1, jit.S9, bytecode.CallFrameSlotCallee*valueSize+8)
	thisScope, thisCopied := jit.NewLabel(), jit.NewLabel()
	m.Load64(rTmp, rRegs, c.off(this)+8)
	m.Branch64Imm(jit.EQ, rTmp, uint32(tagScope), thisScope)
	m.Store64(rTmp, jit.S9, bytecode.CallFrameSlotThisArgument*valueSize+8)
	m.Load64(rTmp, rRegs, c.off(this))
	m.Store64(rTmp, jit.S9, bytecode.CallFrameSlotThisArgument*valueSize)
	m.B(thisCopied)
	m.Bind(thisScope)
	m.StoreZero128(jit.S9, bytecode.CallFrameSlotThisArgument*valueSize)
	m.Bind(thisCopied)
	for i := 1; i < int(argc); i++ {
		m.Load64(rTmp, rRegs, c.off(this+bytecode.VirtualRegister(i)))
		m.Store64(rTmp, jit.S9, (bytecode.CallFrameSlotThisArgument+i)*valueSize)
		m.Load64(rTmp, rRegs, c.off(this+bytecode.VirtualRegister(i))+8)
		m.Store64(rTmp, jit.S9, (bytecode.CallFrameSlotThisArgument+i)*valueSize+8)
	}
	for i := 1; i <= u.NumVars; i++ {
		m.StoreZero128(jit.S9, -i*valueSize)
	}
	for i := int(argc); i < u.NumParameters; i++ {
		m.StoreZero128(jit.S9, (bytecode.CallFrameSlotThisArgument+i)*valueSize)
	}
	// The callee's code, on its registers.
	m.Mov(rRegs, jit.S8)

	sub := &jitCompiler{cb: cb, nl: nl, labels: map[int]*jit.Label{},
		exits: map[int]*jit.Label{}, fuel: map[int]*jit.Label{}, leaves: map[int]*jit.Label{},
		callExits: map[int]*jit.Label{}, resumeAt: map[int]bool{}, nestedExits: map[int]*jit.Label{},
		nativeExits: map[int]*jit.Label{}, facts: map[bytecode.VirtualRegister]uint8{},
		resumeChecks: map[int][]jitFact{}, refills: map[int][len(jitFCache)]jitFEntry{},
		inline: &inlineCtx{caller: c, pc: pc, callee: o, dstOff: c.off(dst), hasDst: hasDst, done: done, cs: cs, argc: argc,
			parent: c.inline, size: size, depth: levels, regsOff: below}}
	if c.inline == nil {
		sub.inline.site = &inlineSite{pc: pc}
	} else {
		sub.inline.site = c.inline.root().site
	}
	ins := u.Instructions
	var pcs []int
	for p := 0; p < len(ins); p += bytecode.OpcodeID(ins[p]).Length() {
		pcs = append(pcs, p)
		sub.labels[p] = jit.NewLabel()
	}
	sub.m = c.m
	// Type facts, as in the caller's code: per block of the callee, from
	// what the caller knew about the arguments (on arm64 md5's safe_add
	// checked every operand's tag without them).
	spec := jitSpec && !cb.jit.noSpec && c.factsOn
	blockStart := map[int]bool{}
	for _, t := range bytecode.ComputePreciseJumpTargets(u, ins) {
		blockStart[t] = true
	}
	if spec {
		for i := 0; i < int(argc); i++ {
			if f := c.facts[this+bytecode.VirtualRegister(i)]; f != 0 {
				sub.facts[bytecode.VirtualRegister(bytecode.CallFrameSlotThisArgument+i)] = f
			}
		}
	}
	ok := true
	for _, p := range pcs {
		sub.m.Bind(sub.labels[p])
		if blockStart[p] {
			clear(sub.facts)
			sub.fcache = [len(jitFCache)]jitFEntry{}
		}
		sub.pending, sub.slowResumes, sub.factsOn = sub.pending[:0], sub.slowResumes[:0], spec
		sub.pendingF, sub.curOp = sub.pendingF[:0], bytecode.OpcodeID(ins[p])
		if !sub.instruction(p, ins[p:]) {
			ok = false
			break
		}
		sub.factsOn = false
		sub.applyFacts(p, ins[p:], blockStart)
	}
	if ok {
		for i := 0; i < len(sub.cold); i++ {
			sub.cold[i]()
		}
		sub.inlineExits()
	}
	c.m = sub.m
	if ok && (m.Len()-start > inlineMaxBytes || root.inlineBytes+m.Len()-start > inlineBudget) {
		ok = false
	}
	if !ok {
		// Not inlined: the normal call alone.
		var labels []*jit.Label
		for _, k := range []*jitCompiler{c, sub} {
			for _, ls := range []map[int]*jit.Label{k.exits, k.leaves, k.callExits, k.fuel, k.labels, k.nestedExits, k.nativeExits} {
				for _, l := range ls {
					labels = append(labels, l)
				}
			}
		}
		m.Truncate(start, append(labels, call, done)...)
		c.cold = c.cold[:coldLen]
		root.inlineNeed = oldNeed
		if jitLog {
			println("jit: not inlined", cb.name, "at", pc)
		}
		return nil, nil
	}
	if c.inline == nil {
		c.inlineBytes += m.Len() - start
	}
	c.keepCells = append(c.keepCells, o, cb, cs)
	if c.inline == nil {
		c.inlineSites = append(c.inlineSites, sub.inline.site)
	}
	c.keepCells = append(c.keepCells, sub.keepCells...)
	c.keepKeys = append(c.keepKeys, sub.keepKeys...)
	c.switches = append(c.switches, sub.switches...)
	// The callee wrote only its own registers and dst (applyFacts), but
	// used the number cache's registers.
	c.fcache = [len(jitFCache)]jitFEntry{}
	if jitLog {
		println("jit: inlined", cb.name, "at", pc)
	}
	return call, done
}

// inlineExits emits the exits of an inlined callee's code: each makes the
// callee's frame (inlineStopThunk) with the exit in it and stops the caller as
// a call whose callee's code stopped.
func (c *jitCompiler) inlineExits() {
	m := &c.m
	frame := jit.NewLabel()
	for kind, ls := range []map[int]*jit.Label{jitExitGuard: c.exits, jitExitFuel: c.fuel, jitExitLeave: c.leaves,
		jitExitCall: c.callExits, jitExitNested: c.nestedExits, jitExitNative: c.nativeExits} {
		for pc, l := range ls {
			m.Bind(l)
			m.MovImm(jit.S0, uint64(pc)<<jitExitBits|uint64(kind))
			m.B(frame)
		}
	}
	m.Bind(frame)
	// The stop's frames: inlineStopThunk, with what it needs to know about
	// this site's chain of inlined calls.
	root := c.inline.root()
	if root.caller.inlineStop == nil {
		root.caller.inlineStop = new(uint32)
	}
	chain := c.inline.chain()
	d := &inlineStopDesc{exits: &root.site.exits, stop: root.caller.inlineStop,
		rootPC: uint64(chain[0].pc), rootWord: uint64(chain[0].pc)<<jitExitBits | jitExitNested, n: uint64(len(chain))}
	for k, in := range chain {
		lv := &d.levels[k]
		lv.cs, lv.callee, lv.scope = in.cs, in.callee, in.callee.fn.(*jsFunction).scope
		lv.size, lv.nl, lv.argc = uint64(in.size), uint64(in.cs.nl), uint64(in.argc)
		if k < len(chain)-1 {
			// Waiting at the call of the next one.
			next := chain[k+1].pc
			lv.waitWord, lv.waitPC = uint64(next)<<jitExitBits|jitExitNested, uint64(next)
		}
	}
	c.keepCells = append(c.keepCells, d)
	if rc := root.caller; rc.opt != nil {
		// Inlined into an optimized loop (jit_opt.go): the registers it
		// keeps, stored to the caller's registers (rRegs is the callee's).
		m.Load64(jit.S9, rFrame, offFrameRegs)
		rc.optFlushVia(rc.opt, root.pc, jit.S9, &c.m)
	}
	m.MovImm(jit.S2, uint64(uintptr(unsafe.Pointer(d))))
	m.MovImm(rTmp, uint64(inlineStopThunk))
	m.JumpReg(rTmp)
}

// inlineStopDesc tells inlineStopThunk about a site's chain of inlined
// calls, the outermost first.
type inlineStopDesc struct {
	exits    *uint64 // the site's stops
	stop     *uint32 // the caller's code's inlineStop
	rootPC   uint64  // the caller's call
	rootWord uint64  // a nested exit there
	n        uint64  // levels used
	levels   [inlineMaxDepth]inlineStopLevel
}

type inlineStopLevel struct {
	cs               *callCase
	callee           *Object
	scope            *Scope
	size, nl, argc   uint64
	waitWord, waitPC uint64 // the exit and pc of all but the innermost
}

var (
	offStopExits    = int(unsafe.Offsetof(inlineStopDesc{}.exits))
	offStopStop     = int(unsafe.Offsetof(inlineStopDesc{}.stop))
	offStopRootPC   = int(unsafe.Offsetof(inlineStopDesc{}.rootPC))
	offStopRootWord = int(unsafe.Offsetof(inlineStopDesc{}.rootWord))
	offStopN        = int(unsafe.Offsetof(inlineStopDesc{}.n))
	offStopLevels   = int(unsafe.Offsetof(inlineStopDesc{}.levels))
	sizeStopLevel   = int(unsafe.Sizeof(inlineStopLevel{}))
	offLvCS         = int(unsafe.Offsetof(inlineStopLevel{}.cs))
	offLvCallee     = int(unsafe.Offsetof(inlineStopLevel{}.callee))
	offLvScope      = int(unsafe.Offsetof(inlineStopLevel{}.scope))
	offLvSize       = int(unsafe.Offsetof(inlineStopLevel{}.size))
	offLvNL         = int(unsafe.Offsetof(inlineStopLevel{}.nl))
	offLvArgc       = int(unsafe.Offsetof(inlineStopLevel{}.argc))
	offLvWaitWord   = int(unsafe.Offsetof(inlineStopLevel{}.waitWord))
	offLvWaitPC     = int(unsafe.Offsetof(inlineStopLevel{}.waitPC))
)

// inlineStopThunk is the shared code an inlined call's code jumps to when
// it stops, with S0 = the exit word and S2 = the site's inlineStopDesc
// (one copy instead of some 750 bytes at every site). It counts the stop
// (at inlineExitLimit it asks for the caller to be compiled again without
// inlining there, checkReprofile), makes the frames of the inlined calls,
// the outermost first, around the callees' registers, as directCall makes
// them (their checks passed before the inlined code ran, and nothing
// changed them): the innermost gets the exit, an outer one waits at the
// call of the next (a nested exit there). Then it stops the caller with a
// nested exit at its call.
var inlineStopThunk = func() uintptr {
	var m jit.Masm
	counted := jit.NewLabel()
	m.Load64(jit.S5, jit.S2, offStopExits)
	m.Load64(jit.S6, jit.S5, 0)
	m.AddImm64(jit.S6, jit.S6, 1)
	m.Store64(jit.S6, jit.S5, 0)
	m.Branch64Imm(jit.ULT, jit.S6, inlineExitLimit, counted)
	m.Load64(jit.S5, jit.S2, offStopStop)
	m.MovImm(jit.S6, 1)
	m.Store32(jit.S6, jit.S5, 0)
	m.Bind(counted)
	// S3 = vm, S5 = sp, S8 = the outermost callee's registers, S1 = the
	// frame each one's caller is, S9 = the level, S6 = levels left.
	m.Load64(jit.S3, rFrame, offFrameVM)
	m.Load64(jit.S5, jit.S3, offVMSp)
	m.Load64(jit.S8, jit.S3, offVMRegStack)
	m.AddShift64(jit.S8, jit.S8, jit.S5, 4)
	m.Mov(jit.S1, rFrame)
	m.AddImm64(jit.S9, jit.S2, uint32(offStopLevels))
	m.Load64(jit.S6, jit.S2, offStopN)
	loop, last, stored := jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
	m.Bind(loop)
	// S7 = the frame: vm.jitFrames[jitDepth], then jitDepth+1, depth+1.
	m.Load64(jit.S4, jit.S3, offVMJitDepth)
	m.Load64(jit.S7, jit.S3, offVMJitFrames)
	m.AddShift64(jit.S7, jit.S7, jit.S4, 3)
	m.Load64(jit.S7, jit.S7, 0)
	m.AddImm64(jit.S4, jit.S4, 1)
	m.Store64(jit.S4, jit.S3, offVMJitDepth)
	m.Load64(jit.S4, jit.S3, offVMDepth)
	m.AddImm64(jit.S4, jit.S4, 1)
	m.Store64(jit.S4, jit.S3, offVMDepth)
	m.Load64(jit.S4, jit.S9, offLvCS)
	m.Load64(rTmp, jit.S4, offICConsts)
	m.Store64(rTmp, jit.S7, offFrameConsts)
	m.Load64(rTmp, jit.S4, offICCB)
	m.Store64(rTmp, jit.S7, offFrameCB)
	m.Store64(jit.S8, jit.S7, offFrameRegs)
	m.Load64(rTmp, jit.S9, offLvSize)
	m.Store64(rTmp, jit.S7, offFrameRegs+8)
	m.Store64(rTmp, jit.S7, offFrameRegs+16)
	m.Load64(rTmp, jit.S9, offLvNL)
	m.Store64(rTmp, jit.S7, offFrameNL)
	m.AddShift64(rTmp, jit.S8, rTmp, 4)
	m.Store64(rTmp, jit.S7, offFrameBase)
	m.Load64(rTmp, jit.S9, offLvArgc)
	m.Store64(rTmp, jit.S7, offFrameArgc)
	m.Load64(rTmp, jit.S9, offLvCallee)
	m.Store64(rTmp, jit.S7, offFrameCallee)
	m.Load64(rTmp, jit.S9, offLvScope)
	m.Store64(rTmp, jit.S7, offFrameScope)
	m.Store64(jit.S1, jit.S7, offFrameCaller)
	m.Store64(jit.S5, jit.S7, offFrameStackBase)
	m.MovImm(rTmp, jitFuel)
	m.Store64(rTmp, jit.S7, offFrameFuel)
	m.Store64(jit.S3, jit.S7, offFrameVM)
	m.StoreZero64(jit.S7, offFrameSuspends)
	m.StoreZero64(jit.S7, offFrameInlineRoom)
	m.Branch64Imm(jit.EQ, jit.S6, 1, last)
	m.Load64(rTmp, jit.S9, offLvWaitWord)
	m.Store64(rTmp, jit.S7, offFrameJitExit)
	m.Load64(rTmp, jit.S9, offLvWaitPC)
	m.Store64(rTmp, jit.S7, offFramePC)
	m.B(stored)
	m.Bind(last)
	m.Store64(jit.S0, jit.S7, offFrameJitExit)
	m.Bind(stored)
	// The next one's registers are above these.
	m.Load64(rTmp, jit.S9, offLvSize)
	m.Add64(jit.S5, jit.S5, rTmp)
	m.AddShift64(jit.S8, jit.S8, rTmp, 4)
	m.Mov(jit.S1, jit.S7)
	m.AddImm64(jit.S9, jit.S9, uint32(sizeStopLevel))
	m.SubImm64(jit.S6, jit.S6, 1)
	m.BranchNonZero(jit.S6, loop)
	m.Store64(jit.S5, jit.S3, offVMSp)
	// The caller: its pc is the call's, its registers its own again, and
	// a nested exit there.
	m.Load64(jit.S0, jit.S2, offStopRootPC)
	m.Store64(jit.S0, rFrame, offFramePC)
	m.Load64(rRegs, rFrame, offFrameRegs)
	m.Load64(jit.S0, jit.S2, offStopRootWord)
	m.ReturnReg(jit.S0)
	code, err := jit.NewBytes(m.Code())
	if err != nil {
		return 0
	}
	inlineStopCode = code
	return code.Entry()
}()

var inlineStopCode *jit.Code

// inlineInstruction compiles the instructions an inlined callee's code
// compiles differently; it reports whether op is one of them (ok then
// says whether it was compiled).
func (c *jitCompiler) inlineInstruction(pc int, op bytecode.OpcodeID, in []uint32) (handled, ok bool) {
	m := &c.m
	ic := c.inline
	switch op {
	case bytecode.OpEnterID:
		// The scope register: the callee's scope, a constant here.
		if r := c.cb.u.ScopeRegister; r.IsValid() {
			s := ic.callee.fn.(*jsFunction).scope
			c.keepCells = append(c.keepCells, s)
			m.MovImm(jit.S0, uint64(uintptr(unsafe.Pointer(s))))
			m.MovImm(jit.S1, uint64(tagScope))
			c.storeValue(r, jit.S0, jit.S1)
		}
		return true, true
	case bytecode.OpRetID:
		b := bytecode.DecodeRet(in)
		c.loadValue(jit.S0, jit.S1, b.Value)
		c.inlineReturn()
		return true, true
	case bytecode.OpTailCallID:
		// A call, then its result returned: the callee inlined too (its
		// ret leaves the result in S0, S1, as no destination is given) or
		// its thunk called; anything else stops the inlined code, and the
		// interpreter makes the tail call.
		b := bytecode.DecodeTailCall(in)
		p := c.cb.tailCallProfiles[b.MetadataID]
		next := pc + op.Length()
		if ic.depth < inlineMaxDepth {
			if call, done := c.tryInline(pc, p, b.Callee, b.Argc, b.Argv, 0, false); call != nil {
				m.Bind(call)
				m.B(c.slow(pc, next))
				m.Bind(done)
				c.inlineReturn()
				return true, true
			}
		}
		if !c.inlineThunkCall(pc, p, b.Callee, b.Argc, b.Argv, 0, false) {
			return true, false
		}
		m.Mov(jit.S0, jit.S1)
		m.Mov(jit.S1, jit.S2)
		c.inlineReturn()
		return true, true
	}
	if !inlineableOp(op, ic.depth < inlineMaxDepth) && op != bytecode.OpCallID && op != bytecode.OpCallIgnoreResultID {
		return true, false
	}
	return false, false
}

// inlineActivationCheck sets the frame's inlineRoom to the registers the
// code's inlined calls need (S3 = the VM), after the checks a call makes
// (directCall), for the deepest chain of them, whose stops make that many
// frames; it branches to fail if they do not pass. It keeps S0 and S1.
// The checks hold for the rest of the activation: while its code runs, the
// call depth, the chain and sp stay, and the register stack only grows.
func (c *jitCompiler) inlineActivationCheck(fail *jit.Label) {
	m := &c.m
	m.Load64(jit.S4, jit.S3, offVMDepth)
	m.AddImm64(jit.S4, jit.S4, uint32(inlineMaxDepth-1))
	m.Load64(jit.S9, jit.S3, offVMCallLimit)
	m.Branch64(jit.SGE, jit.S4, jit.S9, fail)
	m.Load64(jit.S4, jit.S3, offVMJitDepth)
	m.Branch64Imm(jit.UGE, jit.S4, uint32(jitMaxDepth-inlineMaxDepth+1), fail)
	m.Load64(jit.S6, jit.S3, offVMJitFrames)
	m.BranchZero(jit.S6, fail)
	m.Load64(jit.S5, jit.S3, offVMSp)
	m.AddImm64(jit.S9, jit.S5, uint32(c.inlineNeed))
	m.Load64(rTmp, jit.S3, offVMRegStack+8) // len
	m.Branch64(jit.UGT, jit.S9, rTmp, fail)
	m.MovImm(jit.S4, uint64(c.inlineNeed))
	m.Store64(jit.S4, rFrame, offFrameInlineRoom)
}

// thunkCallee returns the builtin a call site's profile saw, if it was
// only that one and it has a thunk (jit_thunks.go): inlined code calls
// the thunk, which needs no frame (a parser's small functions call
// charCodeAt: 17.7 million such calls a run in all of JetStream2 kept
// their callers from being inlined).
func thunkCallee(p callProfile) *Object {
	if p.poly || p.callee == nil || p.callee2 != nil {
		return nil
	}
	nf, ok := p.callee.fn.(*nativeFunction)
	if !ok || int(nf.intrinsic) >= len(jitThunks) || jitThunks[nf.intrinsic] == 0 {
		return nil
	}
	return p.callee
}

// inlineThunkCall compiles, in inlined code, the call at pc of the builtin
// its profile saw (thunkCallee) as a call of its thunk; another callee, or
// a case the thunk leaves to the builtin, takes the slow path, which stops
// the inlined code. It reports false if the call cannot be compiled so.
func (c *jitCompiler) inlineThunkCall(pc int, p callProfile, callee bytecode.VirtualRegister, argc, argv uint32, dst bytecode.VirtualRegister, hasDst bool) bool {
	o := thunkCallee(p)
	this := bytecode.VirtualRegister(bytecode.CallFrameSlotThisArgument - int(argv))
	if o == nil || c.off(this) < 0 {
		return false
	}
	if _, ok := c.constant(callee); ok {
		return false
	}
	m := &c.m
	next := pc + bytecode.OpcodeID(c.cb.u.Instructions[pc]).Length()
	slow := c.slow(pc, next)
	c.keepCells = append(c.keepCells, o)
	m.Load64(jit.S1, rRegs, c.off(callee)+8)
	m.Branch64Imm(jit.NE, jit.S1, uint32(tagObject), slow)
	m.Load64(jit.S0, rRegs, c.off(callee))
	m.MovImm(jit.S2, uint64(uintptr(unsafe.Pointer(o))))
	m.Branch64(jit.NE, jit.S0, jit.S2, slow)
	m.AddImm64(jit.S7, rRegs, uint32(c.off(this)))
	m.MovImm(jit.S6, uint64(argc))
	m.MovImm(jit.S9, uint64(jitThunks[o.fn.(*nativeFunction).intrinsic]))
	m.CallThunk(jit.S9)
	m.BranchNonZero(jit.S0, slow)
	if hasDst {
		c.storeValue(dst, jit.S1, jit.S2)
	}
	return true
}

// inlineReturn returns the value in S0, S1 from inlined code: to the
// call's destination, with the caller's registers again, and on after the
// call.
func (c *jitCompiler) inlineReturn() {
	m := &c.m
	ic := c.inline
	if ic.parent == nil {
		m.Load64(rRegs, rFrame, offFrameRegs)
	} else {
		m.SubImm64(rRegs, rRegs, uint32(ic.parent.size*valueSize))
	}
	if ic.hasDst {
		m.Store64(jit.S0, rRegs, ic.dstOff)
		m.Store64(jit.S1, rRegs, ic.dstOff+8)
	}
	m.B(ic.done)
}
