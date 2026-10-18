package yarr

// A backtracking matcher over a compiled program, as JSC's Yarr
// ByteCompiler and Interpreter and V8's Irregexp interpreter do: the
// pattern tree is compiled once into instructions, and matching keeps an
// explicit stack of backtrack points and undo records instead of the
// closures of the tree matcher (match.go), which allocated for every
// group, quantifier and continuation. Characters, classes, assertions and
// back references are matched by the tree matcher's methods (atom,
// assertion, backReference), so their semantics are shared; the tree
// matcher stays as the reference for the differential tests and for
// patterns the compiler declines.

// vmEnabled lets tests compare the program matcher with the tree matcher.
var vmEnabled = true

type vmOp uint8

const (
	vmAtom       vmOp = iota // t matched n times (n = a)
	vmAssert                 // assertion t
	vmBackref                // back reference t, once
	vmRepeat                 // t (atom or back reference) min..max times, greedy or lazy
	vmSplit                  // try x, then y
	vmJmp                    // go to x
	vmFail                   // backtrack
	vmFirst                  // unless the unit at pos is in first, go to x (x < 0: fail)
	vmGroupEnter             // regs[a] = pos
	vmGroupExit              // capture a = (regs[b], pos), the other way for a backward t
	vmLoopInit               // regs[a] = 0: no iterations yet
	vmLoopHead               // counter a; body x, exit y
	vmIterStart              // regs[b] = pos; t's captures cleared
	vmIterEnd                // counter a, start regs[b]: empty check, count, go to x (the head)
	vmLook                   // lookaround t: body at x, continue at y
	vmLookEnd                // the body of a lookaround matched
	vmMatch
)

type vmInst struct {
	op       vmOp
	greedy   bool
	t        *patternTerm
	x, y     int32
	a, b     int32
	min, max uint32
	first    *firstSet
}

type vmProgram struct {
	insts []vmInst
	nregs int
}

// vmCompiler builds a program from the pattern tree.
type vmCompiler struct {
	insts []vmInst
	nregs int
	ok    bool
}

func (c *vmCompiler) emit(i vmInst) int32 {
	c.insts = append(c.insts, i)
	return int32(len(c.insts) - 1)
}

func (c *vmCompiler) reg() int32 {
	c.nregs++
	return int32(c.nregs - 1)
}

func (c *vmCompiler) here() int32 { return int32(len(c.insts)) }

// compileProgram compiles p, or returns nil for a pattern the program
// matcher does not handle or would not match faster: one without groups,
// lookarounds and quantifiers other than fixed counts, which the tree
// matcher matches without making closures (a word between two word
// boundaries took 7.9 µs there against 6.3 in the tree matcher).
func compileProgram(p *Pattern) *vmProgram {
	if !needsProgram(p.body) {
		return nil
	}
	c := &vmCompiler{ok: true}
	c.disjunction(p.body)
	c.emit(vmInst{op: vmMatch})
	if !c.ok {
		return nil
	}
	return &vmProgram{insts: c.insts, nregs: c.nregs}
}

// needsProgram reports whether matching d makes closures in the tree
// matcher: it has a group, a lookaround or a quantifier other than a
// fixed count.
func needsProgram(d *patternDisjunction) bool {
	for _, a := range d.alternatives {
		for i := range a.terms {
			t := &a.terms[i]
			switch t.typ {
			case termParenthesesSubpattern, termParentheticalAssertion:
				return true
			}
			if t.quantityType != QuantifierFixedCount {
				return true
			}
		}
	}
	return false
}

// disjunction compiles the alternatives of d, tried in order: each but the
// last behind a split to the next, all joining after the last.
func (c *vmCompiler) disjunction(d *patternDisjunction) {
	if len(d.alternatives) == 0 {
		c.emit(vmInst{op: vmFail})
		return
	}
	var ends []int32
	for i, a := range d.alternatives {
		last := i == len(d.alternatives)-1
		var guard, split int32 = -1, -1
		if a.firstOK {
			guard = c.emit(vmInst{op: vmFirst, first: &a.first})
		}
		if !last {
			split = c.emit(vmInst{op: vmSplit})
			c.insts[split].x = c.here()
		}
		c.terms(a.terms)
		if !last {
			ends = append(ends, c.emit(vmInst{op: vmJmp}))
			c.insts[split].y = c.here()
		}
		if guard >= 0 {
			if last {
				c.insts[guard].x = -1 // the last alternative cannot start here: fail
			} else {
				c.insts[guard].x = c.here() // the next alternative
			}
		}
	}
	for _, e := range ends {
		c.insts[e].x = c.here()
	}
}

func (c *vmCompiler) terms(ts []patternTerm) {
	for i := range ts {
		t := &ts[i]
		switch t.typ {
		case termAssertionBOL, termAssertionEOL, termAssertionBOI, termAssertionEOI, termAssertionWordBoundary:
			c.emit(vmInst{op: vmAssert, t: t})
		case termPatternCharacter, termCharacterClass:
			if t.quantityType != QuantifierFixedCount {
				c.emit(vmInst{op: vmRepeat, t: t, min: t.quantityMinCount, max: t.quantityMaxCount,
					greedy: t.quantityType == QuantifierGreedy})
			} else if t.quantityMaxCount > 0 {
				c.emit(vmInst{op: vmAtom, t: t, a: int32(t.quantityMaxCount)})
			}
		case termNumberedBackReference, termNamedBackReference:
			if t.quantityType != QuantifierFixedCount || t.quantityMaxCount != 1 {
				c.emit(vmInst{op: vmRepeat, t: t, min: t.quantityMinCount, max: t.quantityMaxCount,
					greedy: t.quantityType == QuantifierGreedy})
			} else {
				c.emit(vmInst{op: vmBackref, t: t})
			}
		case termNumberedForwardReference, termNamedForwardReference:
			// matches empty
		case termParenthesesSubpattern:
			if t.parentheses.disjunction == nil {
				c.ok = false
				return
			}
			if t.quantityType == QuantifierFixedCount && t.quantityMaxCount == 1 {
				c.group(t)
			} else {
				c.repeatGroup(t)
			}
		case termParentheticalAssertion:
			if t.parentheses.disjunction == nil {
				c.ok = false
				return
			}
			look := c.emit(vmInst{op: vmLook, t: t})
			c.insts[look].x = c.here()
			c.disjunction(t.parentheses.disjunction)
			c.emit(vmInst{op: vmLookEnd})
			c.insts[look].y = c.here()
		default:
			c.ok = false // DotStarEnclosure
			return
		}
	}
}

// group compiles a group matched once: its captures are set when its
// disjunction has matched, as the tree matcher's group does.
func (c *vmCompiler) group(t *patternTerm) {
	if !t.capture {
		c.disjunction(t.parentheses.disjunction)
		return
	}
	r := c.reg()
	c.emit(vmInst{op: vmGroupEnter, a: r})
	c.disjunction(t.parentheses.disjunction)
	c.emit(vmInst{op: vmGroupExit, t: t, a: int32(2 * t.parentheses.subpatternID), b: r})
}

// repeatGroup compiles a quantified group (RepeatMatcher): every
// iteration starts with the group's captures cleared, and one past the
// minimum may not match empty.
func (c *vmCompiler) repeatGroup(t *patternTerm) {
	if t.quantityMaxCount == 0 {
		return
	}
	counter, start := c.reg(), c.reg()
	c.emit(vmInst{op: vmLoopInit, a: counter})
	head := c.emit(vmInst{op: vmLoopHead, a: counter, min: t.quantityMinCount, max: t.quantityMaxCount,
		greedy: t.quantityType != QuantifierNonGreedy})
	c.insts[head].x = c.here()
	c.emit(vmInst{op: vmIterStart, t: t, b: start})
	c.group(t)
	c.emit(vmInst{op: vmIterEnd, a: counter, b: start, min: t.quantityMinCount, x: head})
	c.insts[head].y = c.here()
}

// Backtrack stack records.
const (
	vfAlt    uint8 = iota // resume at pc with pos
	vfCap                 // restore caps[a], caps[a+1] to b, c
	vfReg                 // restore regs[a] to b
	vfGreedy              // a greedy repeat: positions posStack[a..b], resume at pc
	vfLazy                // a lazy repeat at pos after b iterations, resume at pc
)

type vmFrame struct {
	kind uint8
	pc   int32
	pos  int
	a, b int
	c    int
}

// vmRun runs program prog from position pos; on a match it sets m.end.
func (m *matcher[T]) vmRun(prog *vmProgram, pos int) bool {
	if cap(m.regs) < prog.nregs {
		m.regs = make([]int, prog.nregs)
	}
	m.regs = m.regs[:prog.nregs]
	m.stack = m.stack[:0]
	m.posStack = m.posStack[:0]
	end, ok := m.vmExec(prog, 0, pos)
	if ok {
		m.end = end
	}
	return ok
}

// vmExec runs prog from pc at pos until vmMatch or vmLookEnd (success: the
// position then) or until it backtracks below the stack it started with.
func (m *matcher[T]) vmExec(prog *vmProgram, pc int32, pos int) (int, bool) {
	base := len(m.stack)
	insts := prog.insts
	for {
		in := &insts[pc]
		switch in.op {
		case vmAtom:
			ok := true
			for n := int32(0); n < in.a && ok; n++ {
				pos, ok = m.atom(in.t, pos)
			}
			if ok {
				pc++
				continue
			}
		case vmAssert:
			if m.assertion(in.t, pos) {
				pc++
				continue
			}
		case vmBackref:
			var ok bool
			if pos, ok = m.backReference(in.t, pos); ok {
				pc++
				continue
			}
		case vmRepeat:
			if in.greedy {
				// Record every reachable position, then back off one at a
				// time (the frame keeps them on posStack).
				first := len(m.posStack)
				m.posStack = append(m.posStack, pos)
				for n := uint32(0); n < in.max; n++ {
					next, ok := m.step(in.t, pos)
					if !ok || n >= in.min && next == pos {
						break
					}
					pos = next
					m.posStack = append(m.posStack, pos)
				}
				top := len(m.posStack) - 1
				if top-first < int(in.min) {
					m.posStack = m.posStack[:first]
					break // fail
				}
				if top-first > int(in.min) {
					m.stack = append(m.stack, vmFrame{kind: vfGreedy, pc: pc + 1, a: first + int(in.min), b: top - 1, c: first})
				} else {
					m.posStack = m.posStack[:first]
				}
				pc++
				continue
			}
			// Lazy: the minimum first, then one more on each backtrack.
			ok := true
			for n := uint32(0); n < in.min; n++ {
				var next int
				if next, ok = m.step(in.t, pos); !ok {
					break
				}
				pos = next
			}
			if !ok {
				break
			}
			if in.min < in.max {
				m.stack = append(m.stack, vmFrame{kind: vfLazy, pc: pc, pos: pos, b: int(in.min)})
			}
			pc++
			continue
		case vmSplit:
			m.stack = append(m.stack, vmFrame{kind: vfAlt, pc: in.y, pos: pos})
			pc = in.x
			continue
		case vmJmp:
			pc = in.x
			continue
		case vmFail:
		case vmFirst:
			if !prefiltersEnabled || pos < len(m.in) && in.first.has(uint16(m.in[pos])) {
				pc++
				continue
			}
			if in.x >= 0 {
				pc = in.x
				continue
			}
		case vmGroupEnter:
			m.setReg(int(in.a), pos)
			pc++
			continue
		case vmGroupExit:
			i := int(in.a)
			m.stack = append(m.stack, vmFrame{kind: vfCap, a: i, b: m.caps[i], c: m.caps[i+1]})
			if in.t.matchDirection == Backward {
				m.caps[i], m.caps[i+1] = pos, m.regs[in.b]
			} else {
				m.caps[i], m.caps[i+1] = m.regs[in.b], pos
			}
			pc++
			continue
		case vmLoopInit:
			m.setReg(int(in.a), 0)
			pc++
			continue
		case vmLoopHead:
			n := uint32(m.regs[in.a])
			switch {
			case in.max != quantifyInfinite && n >= in.max:
				pc = in.y
			case n < in.min:
				pc = in.x
			case in.greedy:
				m.stack = append(m.stack, vmFrame{kind: vfAlt, pc: in.y, pos: pos})
				pc = in.x
			default:
				m.stack = append(m.stack, vmFrame{kind: vfAlt, pc: in.x, pos: pos})
				pc = in.y
			}
			continue
		case vmIterStart:
			m.setReg(int(in.b), pos)
			if in.t.containsAnyCaptures() {
				for s := 2 * in.t.parentheses.subpatternID; s < 2*(in.t.parentheses.lastSubpatternID+1); s += 2 {
					if m.caps[s] != -1 || m.caps[s+1] != -1 {
						m.stack = append(m.stack, vmFrame{kind: vfCap, a: int(s), b: m.caps[s], c: m.caps[s+1]})
						m.caps[s], m.caps[s+1] = -1, -1
					}
				}
			}
			pc++
			continue
		case vmIterEnd:
			n := uint32(m.regs[in.a])
			if n >= in.min && pos == m.regs[in.b] {
				break // an iteration past the minimum matched empty
			}
			m.setReg(int(in.a), int(n+1))
			pc = in.x
			continue
		case vmLook:
			// The body is matched once, without backtracking into it; its
			// captures are restored when the stack unwinds past here.
			t := in.t
			if t.containsAnyCaptures() {
				for s := 2 * t.parentheses.subpatternID; s < 2*(t.parentheses.lastSubpatternID+1); s += 2 {
					m.stack = append(m.stack, vmFrame{kind: vfCap, a: int(s), b: m.caps[s], c: m.caps[s+1]})
				}
			}
			mark := len(m.stack)
			_, matched := m.vmExec(prog, in.x, pos) // backtracks no lower than mark
			// Drop the body's backtrack points and undo records: its
			// captures stay until the records pushed above restore them.
			m.stack = m.stack[:mark]
			if matched != t.invert {
				if t.invert {
					m.restoreTo(mark) // a failed negative body left nothing, but be exact
				}
				pc = in.y
				continue
			}
		case vmLookEnd:
			return pos, true
		case vmMatch:
			return pos, true
		}

		// Backtrack: undo records are applied until a resumption point.
		for {
			if m.steps++; m.steps >= m.check {
				m.stepCheck()
			}
			if len(m.stack) <= base {
				return pos, false
			}
			f := &m.stack[len(m.stack)-1]
			switch f.kind {
			case vfAlt:
				pc, pos = f.pc, f.pos
				m.stack = m.stack[:len(m.stack)-1]
			case vfCap:
				m.caps[f.a], m.caps[f.a+1] = f.b, f.c
				m.stack = m.stack[:len(m.stack)-1]
				continue
			case vfReg:
				m.regs[f.a] = f.b
				m.stack = m.stack[:len(m.stack)-1]
				continue
			case vfGreedy:
				pos, pc = m.posStack[f.b], f.pc
				if f.b--; f.b < f.a {
					m.posStack = m.posStack[:f.c]
					m.stack = m.stack[:len(m.stack)-1]
				}
			case vfLazy:
				in := &insts[f.pc]
				n := uint32(f.b)
				if n >= in.max {
					m.stack = m.stack[:len(m.stack)-1]
					continue
				}
				next, ok := m.step(in.t, f.pos)
				if !ok || n >= in.min && next == f.pos {
					m.stack = m.stack[:len(m.stack)-1]
					continue
				}
				f.pos, f.b = next, f.b+1
				pos, pc = next, f.pc+1
				if uint32(f.b) >= in.max {
					m.stack = m.stack[:len(m.stack)-1]
				}
			}
			break
		}
	}
}

// step is one iteration of a repeated atom or back reference.
func (m *matcher[T]) step(t *patternTerm, pos int) (int, bool) {
	if t.typ == termPatternCharacter || t.typ == termCharacterClass {
		return m.atom(t, pos)
	}
	return m.backReference(t, pos)
}

// setReg sets regs[r], recording the old value for backtracking.
func (m *matcher[T]) setReg(r, v int) {
	m.stack = append(m.stack, vmFrame{kind: vfReg, a: r, b: m.regs[r]})
	m.regs[r] = v
}

// restoreTo applies the undo records above stack height h.
func (m *matcher[T]) restoreTo(h int) {
	for len(m.stack) > h {
		f := &m.stack[len(m.stack)-1]
		switch f.kind {
		case vfCap:
			m.caps[f.a], m.caps[f.a+1] = f.b, f.c
		case vfReg:
			m.regs[f.a] = f.b
		}
		m.stack = m.stack[:len(m.stack)-1]
	}
}
