package yarr

import (
	"errors"
	"sync/atomic"
)

// A backtracking matcher that walks the pattern tree directly, in
// continuation-passing style, instead of JSC's ByteCompiler + Interpreter.
// Its semantics follow the spec's Matcher closures (ES 22.2.2): every term
// receives the continuation for the rest of the pattern, and backtracking is
// simply returning false. Runs of single-character terms are matched with
// loops, so recursion depth grows with the number of group iterations rather
// than with the input length.

// ErrHitLimit is returned when a match needs too many backtracking steps.
var ErrHitLimit = errors.New("regular expression is too complex")

// Input is a string in either Latin-1 or UTF-16 code units.
type Input interface{ ~[]byte | ~[]uint16 }

type cont func(pos int) bool

type matcher[T byte | uint16] struct {
	p       *Pattern
	in      []T
	unicode bool
	caps    []int
	steps   int
	// end is where the match that done accepted ends; done is made once
	// per matcher, which Exec reuses (Pattern.spare8, spare16).
	end  int
	done cont
	// The program matcher's registers and stacks (vm.go), kept with the
	// matcher.
	regs     []int
	stack    []vmFrame
	posStack []int
}

// spares are a pattern's matchers, one per input width, kept between
// Execs: making one and its done closure for every match was the largest
// source of garbage in the regexp benchmark. Taken with an atomic swap,
// so that VMs on other goroutines matching the same pattern each get
// their own.
type spares struct {
	m8  atomic.Pointer[matcher[byte]]
	m16 atomic.Pointer[matcher[uint16]]
}

func takeMatcher[T byte | uint16](p *Pattern) *matcher[T] {
	var m *matcher[T]
	switch any(m).(type) {
	case *matcher[byte]:
		m, _ = any(p.spares.m8.Swap(nil)).(*matcher[T])
	case *matcher[uint16]:
		m, _ = any(p.spares.m16.Swap(nil)).(*matcher[T])
	}
	if m == nil {
		m = &matcher[T]{p: p}
		m.done = func(e int) bool { m.end = e; return true }
	}
	return m
}

func putMatcher[T byte | uint16](p *Pattern, m *matcher[T]) {
	m.in, m.caps = nil, nil // the input and the result are not kept
	if cap(m.stack) > 1<<16 {
		m.stack, m.posStack = nil, nil // a large match's stacks are not kept
	}
	switch x := any(m).(type) {
	case *matcher[byte]:
		p.spares.m8.Store(x)
	case *matcher[uint16]:
		p.spares.m16.Store(x)
	}
}

type hitLimit struct{}

// Exec matches p against input starting at code unit start (the lastIndex),
// trying successive start positions unless the pattern is sticky. It returns
// the capture offsets (pairs of begin/end, -1 for unset), or nil if there is
// no match.
func Exec[T byte | uint16](p *Pattern, input []T, start int) (caps []int, err error) {
	if p.sets != nil && prefiltersEnabled {
		return execSets(p, input, start), nil
	}
	m := takeMatcher[T](p)
	m.in, m.unicode, m.steps, m.end = input, p.eitherUnicode(), 0, -1
	m.caps = make([]int, 2*(p.NumSubpatterns+1))
	defer func() {
		r := recover()
		putMatcher(p, m)
		if r != nil {
			if _, ok := r.(hitLimit); !ok {
				panic(r)
			}
			caps, err = nil, ErrHitLimit
		}
	}()
	anchored := p.sticky() || p.startsWithBOLOnly()
	minSize := int(p.body.minimumSize)
	done := m.done
	var prog *vmProgram
	if vmEnabled {
		prog = p.program()
	}
	litAt := -1 // the next occurrence of the required literal at or after pos
	for pos := start; pos <= len(input); {
		if p.requiredLiteral != nil && !anchored && prefiltersEnabled {
			if litAt < pos {
				if litAt = indexLiteral(input, p.requiredLiteral, pos); litAt < 0 {
					return nil, nil
				}
			}
			if p.literalPrefixMax >= 0 && pos < litAt-p.literalPrefixMax {
				pos = litAt - p.literalPrefixMax
			}
		}
		if p.startOK && !anchored && prefiltersEnabled {
			// Skip to the next position where a match can start.
			for from := pos; pos < len(input); pos++ {
				// In Unicode mode a match never starts inside a surrogate pair.
				if p.startSet.has(uint16(input[pos])) && !(m.unicode && pos > from &&
					isTrail(rune(input[pos])) && isLead(rune(input[pos-1]))) {
					break
				}
			}
		}
		if len(input)-pos < minSize {
			return nil, nil
		}
		for i := range m.caps {
			m.caps[i] = -1
		}
		var matched bool
		if prog != nil {
			matched = m.vmRun(prog, pos)
		} else {
			matched = m.disjunction(p.body, pos, done)
		}
		if matched {
			m.caps[0], m.caps[1] = pos, m.end
			return m.caps, nil
		}
		if anchored {
			break
		}
		pos++
		if m.unicode && pos < len(input) && isTrail(rune(input[pos])) && isLead(rune(input[pos-1])) {
			pos++
		}
	}
	return nil, nil
}

// startsWithBOLOnly reports whether every top-level alternative begins with ^
// in a pattern where ^ can only match at the start of the input.
func (p *Pattern) startsWithBOLOnly() bool {
	if !p.containsBOL || p.multiline() || p.containsModifiers {
		return false
	}
	for _, a := range p.body.alternatives {
		if !a.startsWithBOL {
			return false
		}
	}
	return true
}

func (m *matcher[T]) disjunction(d *patternDisjunction, pos int, k cont) bool {
	for _, a := range d.alternatives {
		if a.firstOK && prefiltersEnabled && (pos >= len(m.in) || !a.first.has(uint16(m.in[pos]))) {
			continue // cannot start here
		}
		if m.terms(a.terms, pos, k) {
			return true
		}
	}
	return false
}

func (m *matcher[T]) terms(ts []patternTerm, pos int, k cont) bool {
	if m.steps++; m.steps > matchLimit {
		panic(hitLimit{})
	}
	for i := range ts {
		t := &ts[i]
		switch t.typ {
		case termAssertionBOL, termAssertionEOL, termAssertionBOI, termAssertionEOI, termAssertionWordBoundary:
			if !m.assertion(t, pos) {
				return false
			}
		case termPatternCharacter, termCharacterClass:
			if t.quantityType != QuantifierFixedCount {
				return m.repeat(t, pos, m.atom, m.restOf(ts[i+1:], k))
			}
			for n := uint32(0); n < t.quantityMaxCount; n++ {
				var ok bool
				if pos, ok = m.atom(t, pos); !ok {
					return false
				}
			}
		case termNumberedBackReference, termNamedBackReference:
			if t.quantityType != QuantifierFixedCount || t.quantityMaxCount != 1 {
				return m.repeat(t, pos, m.backReference, m.restOf(ts[i+1:], k))
			}
			var ok bool
			if pos, ok = m.backReference(t, pos); !ok {
				return false
			}
		case termNumberedForwardReference, termNamedForwardReference:
			// A reference to a group that cannot have matched yet matches empty.
		case termParenthesesSubpattern:
			if t.quantityType == QuantifierFixedCount && t.quantityMaxCount == 1 {
				return m.group(t, pos, m.restOf(ts[i+1:], k))
			}
			return m.repeatGroup(t, t.quantityMinCount, t.quantityMaxCount, pos, m.restOf(ts[i+1:], k))
		case termParentheticalAssertion:
			return m.lookaround(t, pos, m.restOf(ts[i+1:], k))
		case termDotStarEnclosure:
			panic("yarr: DotStarEnclosure is not produced for the tree matcher")
		}
	}
	return k(pos)
}

// --- Characters ---------------------------------------------------------------------------

// read returns the character before or after pos in direction dir and the
// position past it.
func (m *matcher[T]) read(pos int, dir MatchDirection) (rune, int, bool) {
	in := m.in
	if dir == Backward {
		if pos <= 0 {
			return 0, pos, false
		}
		c := rune(in[pos-1])
		if m.unicode && isTrail(c) && pos >= 2 && isLead(rune(in[pos-2])) {
			return supplementary(rune(in[pos-2]), c), pos - 2, true
		}
		return c, pos - 1, true
	}
	if pos >= len(in) {
		return 0, pos, false
	}
	c := rune(in[pos])
	if m.unicode && isLead(c) && pos+1 < len(in) && isTrail(rune(in[pos+1])) {
		return supplementary(c, rune(in[pos+1])), pos + 2, true
	}
	return c, pos + 1, true
}

// atom matches one character of a PatternCharacter or CharacterClass term.
func (m *matcher[T]) atom(t *patternTerm, pos int) (int, bool) {
	c, next, ok := m.read(pos, t.matchDirection)
	if !ok {
		return pos, false
	}
	if t.typ == termPatternCharacter {
		pc := t.patternCharacter
		// Non-unique characters were turned into classes by the constructor;
		// only ASCII ones in legacy mode remain, folding just within ASCII.
		return next, c == pc || t.ignoreCase() && isASCII(c) && isASCIIAlpha(c) && c|0x20 == pc|0x20
	}
	return next, t.characterClass.contains(c) != t.invert
}

func (m *matcher[T]) isWordChar(pos int, t *patternTerm) bool {
	if pos < 0 || pos >= len(m.in) {
		return false
	}
	c := rune(m.in[pos])
	if m.unicode && t.ignoreCase() {
		return wordUnicodeIgnoreCaseCharCharacterClass.contains(c)
	}
	return wordcharCharacterClass.contains(c)
}

func (m *matcher[T]) isNewline(pos int) bool {
	return pos >= 0 && pos < len(m.in) && newlineCharacterClass.contains(rune(m.in[pos]))
}

func (m *matcher[T]) assertion(t *patternTerm, pos int) bool {
	switch t.typ {
	case termAssertionBOL:
		return pos == 0 || t.multiline() && m.isNewline(pos-1)
	case termAssertionEOL:
		return pos == len(m.in) || t.multiline() && m.isNewline(pos)
	case termAssertionBOI:
		return pos == 0
	case termAssertionEOI:
		return pos == len(m.in) || t.withOptionalLineTerminator && pos == len(m.in)-1 && m.isNewline(pos)
	case termAssertionWordBoundary:
		return (m.isWordChar(pos-1, t) != m.isWordChar(pos, t)) != t.invert
	}
	return false
}

// --- Back references --------------------------------------------------------------------

// capturedRange returns the input range of the group a back reference
// refers to; for a duplicate name it is whichever group participated.
func (m *matcher[T]) capturedRange(t *patternTerm) (int, int) {
	sub := t.backReferenceSubpatternID
	if t.typ == termNamedBackReference && m.p.duplicateNamedGroupForSub != nil {
		if dup := m.p.duplicateNamedGroupForSub[sub]; dup != 0 {
			for _, s := range m.p.duplicateGroups[dup] {
				if m.caps[2*s] >= 0 {
					sub = s
					break
				}
			}
		}
	}
	return m.caps[2*sub], m.caps[2*sub+1]
}

func (m *matcher[T]) backReference(t *patternTerm, pos int) (int, bool) {
	begin, end := m.capturedRange(t)
	if begin < 0 || end < 0 {
		return pos, true
	}
	n := end - begin
	from := pos
	if t.matchDirection == Backward {
		from = pos - n
	}
	if from < 0 || from+n > len(m.in) {
		return pos, false
	}
	if !t.ignoreCase() {
		for i := range n {
			if m.in[begin+i] != m.in[from+i] {
				return pos, false
			}
		}
	} else {
		mode := canonicalModeUCS2
		if m.unicode {
			mode = canonicalModeUnicode
		}
		for i, j := begin, from; i < end; {
			a, ni, _ := m.read(i, Forward)
			b, nj, _ := m.read(j, Forward)
			if ni-i != nj-j || ni > end || !areCanonicallyEquivalent(a, b, mode) {
				return pos, false
			}
			i, j = ni, nj
		}
	}
	// In Unicode mode the input is code points: the match may not split a
	// surrogate pair at its outer edge.
	if m.unicode && n > 0 {
		if t.matchDirection == Backward {
			if from > 0 && isLead(rune(m.in[from-1])) && isTrail(rune(m.in[from])) {
				return pos, false
			}
		} else if end := from + n; end < len(m.in) && isTrail(rune(m.in[end])) && isLead(rune(m.in[end-1])) {
			return pos, false
		}
	}
	if t.matchDirection == Backward {
		return from, true
	}
	return pos + n, true
}

// repeat matches a quantified single-step term (character, class or back
// reference), trying iteration counts in greedy or lazy order.
func (m *matcher[T]) repeat(t *patternTerm, pos int, step func(*patternTerm, int) (int, bool), k cont) bool {
	minCount, maxCount := t.quantityMinCount, t.quantityMaxCount
	if t.quantityType == QuantifierNonGreedy {
		for n := uint32(0); ; n++ {
			if n >= minCount && k(pos) {
				return true
			}
			if n == maxCount {
				return false
			}
			next, ok := step(t, pos)
			if !ok || n >= minCount && next == pos {
				return false
			}
			pos = next
		}
	}
	// Greedy: record every reachable position, then back off one at a time.
	positions := []int{pos}
	for n := uint32(0); n < maxCount; n++ {
		next, ok := step(t, pos)
		if !ok || n >= minCount && next == pos {
			break
		}
		pos = next
		positions = append(positions, pos)
	}
	for n := len(positions) - 1; n >= int(minCount); n-- {
		if k(positions[n]) {
			return true
		}
	}
	return false
}

// --- Groups -----------------------------------------------------------------------------

func (m *matcher[T]) group(t *patternTerm, pos int, k cont) bool {
	d := t.parentheses.disjunction
	if !t.capture {
		return m.disjunction(d, pos, k)
	}
	i := 2 * t.parentheses.subpatternID
	return m.disjunction(d, pos, func(p int) bool {
		oldBegin, oldEnd := m.caps[i], m.caps[i+1]
		if t.matchDirection == Backward {
			m.caps[i], m.caps[i+1] = p, pos
		} else {
			m.caps[i], m.caps[i+1] = pos, p
		}
		if k(p) {
			return true
		}
		m.caps[i], m.caps[i+1] = oldBegin, oldEnd
		return false
	})
}

// groupCaptures is the slice of capture offsets owned by a group's subpatterns.
func (m *matcher[T]) groupCaptures(t *patternTerm) []int {
	if !t.containsAnyCaptures() {
		return nil
	}
	return m.caps[2*t.parentheses.subpatternID : 2*(t.parentheses.lastSubpatternID+1)]
}

// repeatGroup is RepeatMatcher: each iteration starts with the group's
// captures cleared, and an iteration past the minimum may not match empty.
func (m *matcher[T]) repeatGroup(t *patternTerm, minCount, maxCount uint32, pos int, k cont) bool {
	if maxCount == 0 {
		return k(pos)
	}
	iterate := func() bool {
		caps := m.groupCaptures(t)
		saved := append([]int(nil), caps...)
		for i := range caps {
			caps[i] = -1
		}
		nextMin, nextMax := minCount, maxCount
		if nextMin > 0 {
			nextMin--
		}
		if nextMax != quantifyInfinite {
			nextMax--
		}
		ok := m.group(t, pos, func(p int) bool {
			if minCount == 0 && p == pos {
				return false
			}
			return m.repeatGroup(t, nextMin, nextMax, p, k)
		})
		if !ok {
			copy(caps, saved)
		}
		return ok
	}
	switch {
	case minCount > 0:
		return iterate()
	case t.quantityType == QuantifierNonGreedy:
		return k(pos) || iterate()
	default:
		return iterate() || k(pos)
	}
}

// lookaround matches (?=…), (?!…), (?<=…) and (?<!…): the body is matched
// once, without backtracking into it from the rest of the pattern.
func (m *matcher[T]) lookaround(t *patternTerm, pos int, k cont) bool {
	caps := m.groupCaptures(t)
	saved := append([]int(nil), caps...)
	matched := m.disjunction(t.parentheses.disjunction, pos, func(int) bool { return true })
	if matched != t.invert {
		if t.invert {
			copy(caps, saved)
		}
		if k(pos) {
			return true
		}
	}
	copy(caps, saved)
	return false
}

// --- Named groups -----------------------------------------------------------------------

// GroupIndex returns the subpattern number to use for a named group after a
// match: for a duplicated name, the group that participated (or the first).
func (p *Pattern) GroupIndex(name string, caps []int) (uint32, bool) {
	ids, ok := p.NamedGroupToParenIndices[name]
	if !ok {
		return 0, false
	}
	if len(ids) == 1 {
		return ids[0], true
	}
	for _, s := range ids[1:] {
		if caps != nil && caps[2*s] >= 0 {
			return s, true
		}
	}
	return ids[1], true
}

func (m *matcher[T]) restOf(ts []patternTerm, k cont) cont {
	return func(p int) bool { return m.terms(ts, p, k) }
}

// unitSet is a small set of code units that can begin a match (Yarr's
// first-character optimization); it lets the search skip positions.
type unitSet []uint16

func (s unitSet) has(c uint16) bool {
	for _, x := range s {
		if x == c {
			return true
		}
	}
	return false
}

// maxFirstUnits bounds the prefilter; patterns with more possible first
// code units are searched position by position.
const maxFirstUnits = 4

// computeFirstUnits sets p.firstUnits when every top-level alternative
// starts with a required pattern character.
func (p *Pattern) computeFirstUnits() {
	if p.minimumSizeZero() {
		return
	}
	var set unitSet
	add := func(c rune) bool {
		u := uint16(c)
		if c > 0xFFFF {
			u = uint16(0xD800 + ((c - 0x10000) >> 10)) // the lead surrogate
		}
		if !set.has(u) {
			set = append(set, u)
		}
		return len(set) <= maxFirstUnits
	}
	for _, a := range p.body.alternatives {
		if len(a.terms) == 0 {
			return
		}
		t := &a.terms[0]
		if t.typ != termPatternCharacter || t.matchDirection != Forward || t.quantityMinCount == 0 {
			return
		}
		c := t.patternCharacter
		if !add(c) {
			return
		}
		if t.ignoreCase() && isASCIIAlpha(c) && !add(c^0x20) {
			return
		}
	}
	p.firstUnits = set
}

func (p *Pattern) minimumSizeZero() bool { return p.body.minimumSize == 0 }
