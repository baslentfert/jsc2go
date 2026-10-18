package yarr

// YarrPatternConstructor (YarrPattern.cpp): builds the pattern tree from the
// parser's callbacks, then assigns frame locations and input positions.

type unresolvedForwardReference struct {
	alternative *patternAlternative
	termIndex   int
	namedGroup  *string
}

func (u *unresolvedForwardReference) term() *patternTerm { return &u.alternative.terms[u.termIndex] }

type parenthesisSavedContext struct {
	isModifier bool
	invert     bool
	direction  MatchDirection
	flags      Flags
}

type parenthesisContext struct {
	stack      []parenthesisSavedContext
	depth      int
	isModifier bool
	invert     bool
	direction  MatchDirection
	flags      Flags
}

func (c *parenthesisContext) push() {
	if c.depth > 0 {
		c.stack = append(c.stack, parenthesisSavedContext{c.isModifier, c.invert, c.direction, c.flags})
	}
	c.depth++
	c.isModifier = false
}

func (c *parenthesisContext) pop() {
	c.depth--
	if c.depth > 0 {
		s := c.stack[len(c.stack)-1]
		c.stack = c.stack[:len(c.stack)-1]
		c.isModifier, c.invert, c.direction, c.flags = s.isModifier, s.invert, s.direction, s.flags
	} else {
		c.isModifier, c.invert, c.direction, c.flags = false, false, Forward, 0
	}
}

func (c *parenthesisContext) reset() { *c = parenthesisContext{} }

type patternConstructor struct {
	pattern                       *Pattern
	alternative                   *patternAlternative
	baseCharacterClassConstructor *characterClassConstructor
	current                       *characterClassConstructor
	characterClassStack           []*characterClassConstructor
	forwardReferencesInLookbehind []unresolvedForwardReference
	err                           ErrorCode
	invertCharacterClass          bool
	paren                         parenthesisContext
	initialFlags                  Flags
	flags                         Flags
	depth                         int
}

const maxRecursionDepth = 2000

func newPatternConstructor(p *Pattern, flags Flags) *patternConstructor {
	c := &patternConstructor{pattern: p, initialFlags: flags}
	c.baseCharacterClassConstructor = newCharacterClassConstructor(p.ignoreCase(), p.compileMode())
	c.current = c.baseCharacterClassConstructor
	body := &patternDisjunction{}
	p.body = body
	c.alternative = body.addNewAlternative(1, Forward)
	p.disjunctions = append(p.disjunctions, body)
	c.flags = flags
	c.paren.flags = flags
	return c
}

func (c *patternConstructor) resetForReparsing() {
	c.pattern.resetForReparsing()
	c.baseCharacterClassConstructor.reset()
	c.current = c.baseCharacterClassConstructor
	c.err = NoError
	c.paren.reset()
	c.paren.flags = c.flags
	c.forwardReferencesInLookbehind = nil
	body := &patternDisjunction{}
	c.pattern.body = body
	c.alternative = body.addNewAlternative(1, Forward)
	c.pattern.disjunctions = append(c.pattern.disjunctions, body)
	c.flags = c.initialFlags
}

func (c *patternConstructor) ignoreCase() bool { return c.flags.Has(FlagIgnoreCase) }
func (c *patternConstructor) multiline() bool  { return c.flags.Has(FlagMultiline) }
func (c *patternConstructor) dotAll() bool     { return c.flags.Has(FlagDotAll) }

func (c *patternConstructor) parenDirection() MatchDirection { return c.paren.direction }

func (c *patternConstructor) addCaptureGroupForName(name string, subpatternID uint32) {
	p := c.pattern
	p.hasNamedCaptureGroups = true
	if p.NamedGroupToParenIndices == nil {
		p.NamedGroupToParenIndices = map[string][]uint32{}
	}
	ids, exists := p.NamedGroupToParenIndices[name]
	switch {
	case !exists:
		for uint32(len(p.CaptureGroupNames)) < subpatternID {
			p.CaptureGroupNames = append(p.CaptureGroupNames, "")
		}
		p.CaptureGroupNames = append(p.CaptureGroupNames, name)
		ids = append(ids, subpatternID)
	case len(ids) == 2:
		p.NumDuplicateNamedGroups++
		ids[0] = p.NumDuplicateNamedGroups
	}
	ids = append(ids, subpatternID)
	p.NamedGroupToParenIndices[name] = ids
}

func (c *patternConstructor) tryConvertingForwardReferencesToBackreferences() {
	for i := range c.forwardReferencesInLookbehind {
		u := &c.forwardReferencesInLookbehind[i]
		t := u.term()
		if u.namedGroup != nil {
			ids, ok := c.pattern.NamedGroupToParenIndices[*u.namedGroup]
			if !ok {
				continue
			}
			t.backReferenceSubpatternID = ids[len(ids)-1]
			t.typ = termNamedBackReference
			c.pattern.containsBackreferences = true
		} else if t.backReferenceSubpatternID != 0 && t.backReferenceSubpatternID <= c.pattern.NumSubpatterns {
			t.typ = termNumberedBackReference
			c.pattern.containsBackreferences = true
		}
	}
	c.forwardReferencesInLookbehind = nil
}

func (c *patternConstructor) assertionBOL() {
	if len(c.alternative.terms) == 0 && !c.paren.invert && c.parenDirection() == Forward {
		c.alternative.startsWithBOL = true
		c.alternative.containsBOL = true
		c.pattern.containsBOL = true
	}
	t := newSimpleTerm(termAssertionBOL, c.flags, false)
	t.matchDirection = c.parenDirection()
	c.alternative.terms = append(c.alternative.terms, t)
}

func (c *patternConstructor) assertionEOL() {
	c.alternative.terms = append(c.alternative.terms, newSimpleTerm(termAssertionEOL, c.flags, false))
}

func (c *patternConstructor) assertionBOI() {
	if len(c.alternative.terms) == 0 && !c.paren.invert && c.parenDirection() == Forward {
		c.alternative.startsWithBOL = true
		c.alternative.containsBOL = true
		c.pattern.containsBOL = true
	}
	t := newSimpleTerm(termAssertionBOI, c.flags, false)
	t.matchDirection = c.parenDirection()
	c.alternative.terms = append(c.alternative.terms, t)
}

func (c *patternConstructor) assertionEOI(withOptionalLineTerminator bool) {
	t := newSimpleTerm(termAssertionEOI, c.flags, false)
	t.withOptionalLineTerminator = withOptionalLineTerminator
	c.alternative.terms = append(c.alternative.terms, t)
}

func (c *patternConstructor) assertionWordBoundary(invert bool) {
	c.alternative.terms = append(c.alternative.terms, newSimpleTerm(termAssertionWordBoundary, c.flags, invert))
}

func (c *patternConstructor) atomPatternCharacter(ch rune, _ bool) {
	if !c.ignoreCase() || isASCII(ch) && !c.pattern.eitherUnicode() {
		c.alternative.terms = append(c.alternative.terms, newCharacterTerm(ch, c.flags, c.parenDirection()))
		return
	}
	mode := canonicalModeUCS2
	if c.pattern.eitherUnicode() {
		mode = canonicalModeUnicode
	}
	info := &canonicalRangeInfoFor(ch, mode)[0]
	if info.typ == CanonicalizeUnique {
		c.alternative.terms = append(c.alternative.terms, newCharacterTerm(ch, c.flags, c.parenDirection()))
		return
	}
	c.current.putUnicodeIgnoreCase(ch, info)
	cc := c.current.charClass()
	c.alternative.terms = append(c.alternative.terms, newClassTerm(cc, false, c.flags, c.parenDirection()))
}

func (c *patternConstructor) atomBuiltInCharacterClass(id BuiltInCharacterClassID, invert bool) {
	add := func(cc *CharacterClass, inv bool) {
		c.alternative.terms = append(c.alternative.terms, newClassTerm(cc, inv, c.flags, c.parenDirection()))
	}
	switch id {
	case DigitClassID:
		add(digitsCharacterClass, invert)
	case SpaceClassID:
		add(spacesCharacterClass, invert)
	case WordClassID:
		if c.pattern.eitherUnicode() && c.ignoreCase() {
			add(wordUnicodeIgnoreCaseCharCharacterClass, invert)
		} else {
			add(wordcharCharacterClass, invert)
		}
	case DotClassID:
		if c.dotAll() {
			add(anyCharacterClass, false)
		} else {
			add(newlineCharacterClass, true)
		}
	default:
		cc := c.pattern.unicodeCharacterClassFor(id, c.ignoreCase(), invert)
		if !cc.hasStrings() {
			add(cc, invert)
			return
		}
		c.atomParenthesesSubpatternBegin(false, nil)
		n := 0
		for _, s := range cc.Strings {
			if n > 0 {
				c.disjunction(forNextAlternative)
			}
			for _, ch := range s {
				c.atomPatternCharacter(ch, false)
			}
			n++
		}
		if cc.hasSingleCharacters() {
			if n > 0 {
				c.disjunction(forNextAlternative)
			}
			add(cc, invert)
		}
		c.atomParenthesesEnd()
	}
}

func (c *patternConstructor) atomCharacterClassBegin(invert bool) {
	c.invertCharacterClass = invert
	c.current.setIsCaseInsensitive(c.ignoreCase())
}

func (c *patternConstructor) atomCharacterClassAtom(ch rune)      { c.current.putChar(ch) }
func (c *patternConstructor) atomCharacterClassRange(lo, hi rune) { c.current.putRange(lo, hi) }

func (c *patternConstructor) atomCharacterClassBuiltIn(id BuiltInCharacterClassID, invert bool) {
	switch id {
	case DigitClassID:
		if invert {
			c.current.append(nondigitsCharacterClass)
		} else {
			c.current.append(digitsCharacterClass)
		}
	case SpaceClassID:
		if invert {
			c.current.append(nonspacesCharacterClass)
		} else {
			c.current.append(spacesCharacterClass)
		}
	case WordClassID:
		switch {
		case c.pattern.eitherUnicode() && c.ignoreCase() && invert:
			c.current.append(nonwordUnicodeIgnoreCaseCharCharacterClass)
		case c.pattern.eitherUnicode() && c.ignoreCase():
			c.current.append(wordUnicodeIgnoreCaseCharCharacterClass)
		case invert:
			c.current.append(nonwordcharCharacterClass)
		default:
			c.current.append(wordcharCharacterClass)
		}
	default:
		cc := c.pattern.unicodeCharacterClassFor(id, c.ignoreCase(), invert)
		if !invert {
			c.current.append(cc)
		} else {
			c.current.appendInverted(cc)
		}
	}
}

func (c *patternConstructor) atomClassStringDisjunction(strings [][]rune) {
	c.current.atomClassStringDisjunction(strings)
}

func (c *patternConstructor) atomCharacterClassSetOp(op characterClassSetOp) {
	c.current.combiningSetOp(op)
}

func (c *patternConstructor) atomCharacterClassPushNested(invert bool) {
	nested := newCharacterClassConstructor(c.ignoreCase(), c.pattern.compileMode())
	c.characterClassStack = append(c.characterClassStack, nested)
	c.current = nested
	c.invertCharacterClass = invert
}

func (c *patternConstructor) atomCharacterClassPopNested(invert bool) {
	if len(c.characterClassStack) == 0 {
		return
	}
	if c.invertCharacterClass {
		c.current.invertMatches()
	}
	prior := c.baseCharacterClassConstructor
	if n := len(c.characterClassStack); n > 1 {
		prior = c.characterClassStack[n-2]
	}
	prior.performSetOpWith(c.current)
	c.characterClassStack = c.characterClassStack[:len(c.characterClassStack)-1]
	c.current = prior
	c.invertCharacterClass = invert
}

func (c *patternConstructor) atomCharacterClassEnd() {
	if c.current.hasInvertedStrings() {
		c.err = ErrNegatedClassSetMayContainStrings
		return
	}
	cc := c.current.charClass()
	c.current.reset()
	addTerm := func() {
		if !c.invertCharacterClass && cc.anyCharacter {
			c.alternative.terms = append(c.alternative.terms, newClassTerm(anyCharacterClass, false, c.flags, c.parenDirection()))
			return
		}
		c.alternative.terms = append(c.alternative.terms, newClassTerm(cc, c.invertCharacterClass, c.flags, c.parenDirection()))
	}
	if !cc.hasStrings() {
		addTerm()
		return
	}
	if c.invertCharacterClass {
		c.err = ErrNegatedClassSetMayContainStrings
		return
	}
	c.atomParenthesesSubpatternBegin(false, nil)
	n := 0
	for _, s := range cc.Strings {
		if n > 0 {
			c.disjunction(forNextAlternative)
		}
		for _, ch := range s {
			c.atomPatternCharacter(ch, false)
		}
		n++
	}
	if cc.hasSingleCharacters() {
		if n > 0 {
			c.disjunction(forNextAlternative)
		}
		addTerm()
	}
	c.atomParenthesesEnd()
}

func (c *patternConstructor) atomParenthesesSubpatternBegin(capture bool, name *string) {
	subpatternID := c.pattern.NumSubpatterns + 1
	if capture {
		c.pattern.NumSubpatterns++
		if name != nil {
			c.addCaptureGroupForName(*name, subpatternID)
		}
	}
	d := &patternDisjunction{parent: c.alternative}
	c.alternative.terms = append(c.alternative.terms, newParenthesesTerm(termParenthesesSubpattern, subpatternID, d, c.flags, capture, false, c.parenDirection()))
	c.alternative = d.addNewAlternative(c.pattern.NumSubpatterns, c.parenDirection())
	c.paren.push()
	c.pattern.disjunctions = append(c.pattern.disjunctions, d)
}

func (c *patternConstructor) atomParentheticalAssertionBegin(invert bool, dir MatchDirection) {
	d := &patternDisjunction{parent: c.alternative}
	c.alternative.terms = append(c.alternative.terms, newParenthesesTerm(termParentheticalAssertion, c.pattern.NumSubpatterns+1, d, c.flags, false, invert, dir))
	c.alternative = d.addNewAlternative(c.pattern.NumSubpatterns, dir)
	c.paren.push()
	c.paren.invert = invert
	c.paren.direction = dir
	if dir == Backward {
		c.pattern.containsLookbehinds = true
	}
	c.pattern.disjunctions = append(c.pattern.disjunctions, d)
}

func (c *patternConstructor) atomParentheticalModifierBegin(set, unset Flags) {
	d := &patternDisjunction{parent: c.alternative}
	c.alternative.terms = append(c.alternative.terms, newParenthesesTerm(termParenthesesSubpattern, c.pattern.NumSubpatterns+1, d, c.flags, false, false, c.parenDirection()))
	c.alternative = d.addNewAlternative(c.pattern.NumSubpatterns, c.parenDirection())
	c.paren.push()
	c.pattern.disjunctions = append(c.pattern.disjunctions, d)
	c.paren.isModifier = true
	c.paren.flags = c.flags
	c.flags |= set
	c.flags &^= unset
	c.pattern.containsModifiers = true
}

func (c *patternConstructor) atomParenthesesEnd() {
	parenDisjunction := c.alternative.parent
	c.alternative = c.alternative.parent.parent
	lastTerm := c.alternative.lastTerm()

	numBOL := 0
	for _, a := range parenDisjunction.alternatives {
		if a.startsWithBOL {
			numBOL++
		}
	}
	parenDisjunction.alternatives[len(parenDisjunction.alternatives)-1].isLastAlternative = true
	if numBOL > 0 {
		c.alternative.containsBOL = true
		if numBOL == len(parenDisjunction.alternatives) {
			c.alternative.startsWithBOL = true
		}
	}
	lastTerm.parentheses.lastSubpatternID = c.pattern.NumSubpatterns

	shouldTryConverting := lastTerm.typ == termParentheticalAssertion && len(c.forwardReferencesInLookbehind) > 0 &&
		c.parenDirection() == Backward
	if c.paren.isModifier {
		c.flags = c.paren.flags
	}
	c.paren.pop()
	if shouldTryConverting && c.parenDirection() == Forward {
		c.tryConvertingForwardReferencesToBackreferences()
	}
	if lastTerm.matchDirection == Backward && c.parenDirection() == Forward {
		c.putTermsInMatchOrder(lastTerm.parentheses.disjunction)
	}
}

func (c *patternConstructor) putTermsInMatchOrder(d *patternDisjunction) {
	for _, a := range d.alternatives {
		for i, j := 0, len(a.terms)-1; i < j; i, j = i+1, j-1 {
			a.terms[i], a.terms[j] = a.terms[j], a.terms[i]
		}
		for i := range a.terms {
			t := &a.terms[i]
			if (t.typ == termParenthesesSubpattern || t.typ == termParentheticalAssertion) && t.matchDirection == Backward {
				c.putTermsInMatchOrder(t.parentheses.disjunction)
			}
		}
	}
}

func (c *patternConstructor) atomBackReference(subpatternID uint32) {
	if subpatternID > c.pattern.NumSubpatterns {
		t := newSimpleTerm(termNumberedForwardReference, c.flags, false)
		c.alternative.terms = append(c.alternative.terms, t)
		if c.parenDirection() == Backward {
			lt := c.alternative.lastTerm()
			lt.backReferenceSubpatternID = subpatternID
			lt.matchDirection = c.parenDirection()
			c.forwardReferencesInLookbehind = append(c.forwardReferencesInLookbehind,
				unresolvedForwardReference{c.alternative, len(c.alternative.terms) - 1, nil})
		}
		return
	}
	for alt := c.alternative.parent.parent; alt != nil; alt = alt.parent.parent {
		t := alt.lastTerm()
		if t.typ == termParenthesesSubpattern && t.capture && subpatternID == t.parentheses.subpatternID {
			c.alternative.terms = append(c.alternative.terms, newSimpleTerm(termNumberedForwardReference, c.flags, false))
			return
		}
	}
	t := newBackReferenceTerm(subpatternID, c.flags)
	t.matchDirection = c.parenDirection()
	c.alternative.terms = append(c.alternative.terms, t)
	c.pattern.containsBackreferences = true
}

func (c *patternConstructor) atomNamedBackReference(name string) {
	ids := c.pattern.NamedGroupToParenIndices[name]
	if len(ids) == 2 {
		subpatternID := ids[len(ids)-1]
		for alt := c.alternative.parent.parent; alt != nil; alt = alt.parent.parent {
			t := alt.lastTerm()
			if t.typ == termParenthesesSubpattern && t.capture && subpatternID == t.parentheses.subpatternID {
				c.alternative.terms = append(c.alternative.terms, newSimpleTerm(termNamedForwardReference, c.flags, false))
				return
			}
		}
	}
	if c.parenDirection() == Forward {
		t := newBackReferenceTerm(ids[len(ids)-1], c.flags)
		t.typ = termNamedBackReference
		t.matchDirection = c.parenDirection()
		c.alternative.terms = append(c.alternative.terms, t)
		c.pattern.containsBackreferences = true
		return
	}
	t := newSimpleTerm(termNamedForwardReference, c.flags, false)
	t.matchDirection = c.parenDirection()
	c.alternative.terms = append(c.alternative.terms, t)
	n := name
	c.forwardReferencesInLookbehind = append(c.forwardReferencesInLookbehind,
		unresolvedForwardReference{c.alternative, len(c.alternative.terms) - 1, &n})
}

func (c *patternConstructor) atomNamedForwardReference(name string) {
	c.alternative.terms = append(c.alternative.terms, newSimpleTerm(termNamedForwardReference, c.flags, false))
	if c.parenDirection() == Backward {
		t := c.alternative.lastTerm()
		t.matchDirection = c.parenDirection()
		n := name
		c.forwardReferencesInLookbehind = append(c.forwardReferencesInLookbehind,
			unresolvedForwardReference{c.alternative, len(c.alternative.terms) - 1, &n})
	}
}

// copyDisjunction deep-copies d, optionally dropping BOL-anchored
// alternatives; it returns nil if nothing is left.
func (c *patternConstructor) copyDisjunction(d *patternDisjunction, filterStartsWithBOL bool) *patternDisjunction {
	c.depth++
	defer func() { c.depth-- }()
	if c.depth > maxRecursionDepth {
		c.err = ErrPatternTooLarge
		return nil
	}
	var nd *patternDisjunction
	for _, a := range d.alternatives {
		if filterStartsWithBOL && a.startsWithBOL {
			continue
		}
		terms, ok := c.copyTerms(a, filterStartsWithBOL)
		if !ok {
			continue
		}
		if nd == nil {
			nd = &patternDisjunction{parent: d.parent}
		}
		na := nd.addNewAlternative(a.firstSubpatternID, a.direction)
		na.lastSubpatternID = a.lastSubpatternID
		na.terms = terms
	}
	if c.err.hasError() || nd == nil {
		return nil
	}
	c.pattern.disjunctions = append(c.pattern.disjunctions, nd)
	return nd
}

func parenthesesMustMatch(t *patternTerm) bool { return t.quantityMinCount != 0 && !t.invert }

func (c *patternConstructor) copyTerms(a *patternAlternative, filterStartsWithBOL bool) ([]patternTerm, bool) {
	terms := make([]patternTerm, 0, len(a.terms))
	for i := range a.terms {
		t := &a.terms[i]
		if copied, ok := c.copyTerm(t, filterStartsWithBOL); ok {
			terms = append(terms, copied)
			continue
		}
		if parenthesesMustMatch(t) {
			return nil, false
		}
	}
	return terms, true
}

func (c *patternConstructor) copyTerm(t *patternTerm, filterStartsWithBOL bool) (patternTerm, bool) {
	if t.typ != termParenthesesSubpattern && t.typ != termParentheticalAssertion {
		return *t, true
	}
	if nd := c.copyDisjunction(t.parentheses.disjunction, filterStartsWithBOL && !t.invert && t.matchDirection == Forward); nd != nil {
		cp := *t
		cp.parentheses.disjunction = nd
		c.pattern.hasCopiedParenSubexprs = true
		return cp, true
	}
	return patternTerm{}, false
}

func (c *patternConstructor) quantifyAtom(min, max uint32, greedy bool) {
	qt := QuantifierNonGreedy
	if greedy {
		qt = QuantifierGreedy
	}
	if max == 0 {
		if c.parenDirection() == Backward && len(c.forwardReferencesInLookbehind) > 0 {
			last := &c.forwardReferencesInLookbehind[len(c.forwardReferencesInLookbehind)-1]
			if last.alternative == c.alternative && last.termIndex == len(c.alternative.terms)-1 {
				c.forwardReferencesInLookbehind = c.forwardReferencesInLookbehind[:len(c.forwardReferencesInLookbehind)-1]
			}
		}
		c.alternative.removeLastTerm()
		return
	}
	t := c.alternative.lastTerm()
	if t.typ == termParentheticalAssertion {
		if min == 0 {
			c.alternative.removeLastTerm()
		}
		return
	}
	switch {
	case min == max:
		t.quantify(min, max, QuantifierFixedCount)
	case min == 0 || t.typ == termParenthesesSubpattern && (c.pattern.hasCopiedParenSubexprs || t.matchDirection == Forward):
		t.quantify(min, max, qt)
	case t.matchDirection == Forward:
		t.quantify(min, min, QuantifierFixedCount)
		copied, ok := c.copyTerm(t, false)
		if !ok {
			return
		}
		c.alternative.terms = append(c.alternative.terms, copied)
		rest := max
		if max != quantifyInfinite {
			rest = max - min
		}
		lt := c.alternative.lastTerm()
		lt.quantify(0, rest, qt)
		if lt.typ == termParenthesesSubpattern {
			lt.parentheses.isCopy = true
		}
	default:
		rest := max
		if max != quantifyInfinite {
			rest = max - min
		}
		t.quantify(0, rest, qt)
		if t.typ == termParenthesesSubpattern {
			t.parentheses.isCopy = true
		}
		copied, ok := c.copyTerm(t, false)
		if !ok {
			return
		}
		c.alternative.terms = append(c.alternative.terms, copied)
		lt := c.alternative.lastTerm()
		lt.quantify(min, min, QuantifierFixedCount)
		if lt.typ == termParenthesesSubpattern {
			lt.parentheses.isCopy = false
		}
	}
}

func (c *patternConstructor) disjunction(purpose createDisjunctionPurpose) {
	if purpose == forNextAlternative && c.alternative.parent.parent == nil {
		c.alternative.lastSubpatternID = c.pattern.NumSubpatterns
	}
	c.alternative = c.alternative.parent.addNewAlternative(c.pattern.NumSubpatterns, c.parenDirection())
}

func (c *patternConstructor) abortedDueToError() bool   { return c.err.hasError() }
func (c *patternConstructor) abortErrorCode() ErrorCode { return c.err }

// --- Offsets ---------------------------------------------------------------------------------

func addChecked(a uint64, b uint64) uint64 { return a + b }

func (c *patternConstructor) setupAlternativeOffsets(a *patternAlternative, frameSize uint64, initialInputPosition uint32) (uint64, ErrorCode) {
	c.depth++
	defer func() { c.depth-- }()
	if c.depth > maxRecursionDepth {
		return 0, ErrTooManyDisjunctions
	}
	a.hasFixedSize = true
	inputPosition := uint64(initialInputPosition)
	const limit = uint64(^uint32(0))
	for i := range a.terms {
		t := &a.terms[i]
		switch t.typ {
		case termAssertionBOL, termAssertionEOL, termAssertionBOI, termAssertionEOI, termAssertionWordBoundary:
			t.inputPosition = uint32(inputPosition)
		case termNumberedBackReference, termNamedBackReference:
			t.inputPosition = uint32(inputPosition)
			t.frameLocation = uint32(frameSize)
			frameSize += stackSpaceForBackTrackInfoBackReference
			a.hasFixedSize = false
		case termNumberedForwardReference, termNamedForwardReference:
		case termPatternCharacter:
			t.inputPosition = uint32(inputPosition)
			if t.quantityType != QuantifierFixedCount {
				t.frameLocation = uint32(frameSize)
				frameSize += stackSpaceForBackTrackInfoPatternCharacter
				a.hasFixedSize = false
			} else if c.pattern.eitherUnicode() {
				inputPosition += uint64(t.quantityMaxCount) * uint64(u16Length(t.patternCharacter))
			} else {
				inputPosition += uint64(t.quantityMaxCount)
			}
		case termCharacterClass:
			t.inputPosition = uint32(inputPosition)
			switch {
			case t.quantityType != QuantifierFixedCount:
				t.frameLocation = uint32(frameSize)
				frameSize += stackSpaceForBackTrackInfoCharacterClass
				a.hasFixedSize = false
			case c.pattern.eitherUnicode():
				t.frameLocation = uint32(frameSize)
				frameSize += stackSpaceForBackTrackInfoCharacterClass
				if t.characterClass.hasOneCharacterSize() && !t.invert {
					width := uint64(1)
					if t.characterClass.hasNonBMPCharacters() {
						width = 2
					}
					inputPosition += uint64(t.quantityMaxCount) * width
				} else {
					inputPosition += uint64(t.quantityMaxCount)
					a.hasFixedSize = false
				}
			default:
				inputPosition += uint64(t.quantityMaxCount)
			}
		case termParenthesesSubpattern:
			t.frameLocation = uint32(frameSize)
			switch {
			case t.quantityMaxCount == 1 && !t.parentheses.isCopy:
				frameSize += stackSpaceForBackTrackInfoParenthesesOnce
				fs, err := c.setupDisjunctionOffsets(t.parentheses.disjunction, frameSize, uint32(inputPosition))
				if err.hasError() {
					return 0, err
				}
				frameSize = fs
				if t.quantityType == QuantifierFixedCount {
					inputPosition += uint64(t.parentheses.disjunction.minimumSize)
				}
				t.inputPosition = uint32(inputPosition)
			case t.parentheses.isTerminal:
				frameSize += stackSpaceForBackTrackInfoParenthesesTerminal
				fs, err := c.setupDisjunctionOffsets(t.parentheses.disjunction, frameSize, uint32(inputPosition))
				if err.hasError() {
					return 0, err
				}
				frameSize = fs
				t.inputPosition = uint32(inputPosition)
			default:
				t.inputPosition = uint32(inputPosition)
				frameSize += stackSpaceForBackTrackInfoParentheses
				fs, err := c.setupDisjunctionOffsets(t.parentheses.disjunction, frameSize, uint32(inputPosition))
				if err.hasError() {
					return 0, err
				}
				frameSize = fs
			}
			a.hasFixedSize = false
		case termParentheticalAssertion:
			initial := uint32(0)
			if t.matchDirection == Forward && a.direction == Forward {
				initial = uint32(inputPosition)
			}
			t.inputPosition = uint32(inputPosition)
			t.frameLocation = uint32(frameSize)
			frameSize += stackSpaceForBackTrackInfoParentheticalAssertion
			fs, err := c.setupDisjunctionOffsets(t.parentheses.disjunction, frameSize, initial)
			if err.hasError() {
				return 0, err
			}
			frameSize = fs
		case termDotStarEnclosure:
			a.hasFixedSize = false
			t.inputPosition = initialInputPosition
			c.pattern.initialStartValueFrameLoc = uint32(frameSize)
			frameSize += stackSpaceForDotStarEnclosure
			c.pattern.saveInitialStartValue = true
		}
		if frameSize > limit {
			return 0, ErrFrameTooLarge
		}
		if inputPosition > limit {
			return 0, ErrOffsetTooLarge
		}
	}
	a.minimumSize = uint32(inputPosition) - initialInputPosition
	return frameSize, NoError
}

func (c *patternConstructor) setupDisjunctionOffsets(d *patternDisjunction, initialFrameSize uint64, initialInputPosition uint32) (uint64, ErrorCode) {
	c.depth++
	defer func() { c.depth-- }()
	if c.depth > maxRecursionDepth {
		return 0, ErrTooManyDisjunctions
	}
	if d != c.pattern.body && len(d.alternatives) > 1 {
		initialFrameSize += stackSpaceForBackTrackInfoAlternative
	}
	shareOffsets := d == c.pattern.body
	minimumInputSize := ^uint32(0)
	var maximumFrameSize uint64
	hasFixedSize := true
	perAlternativeInitial := initialFrameSize
	for _, a := range d.alternatives {
		fs, err := c.setupAlternativeOffsets(a, perAlternativeInitial, initialInputPosition)
		if err.hasError() {
			return 0, err
		}
		minimumInputSize = min(minimumInputSize, a.minimumSize)
		maximumFrameSize = max(maximumFrameSize, fs)
		hasFixedSize = hasFixedSize && a.hasFixedSize
		if a.minimumSize > 1<<31-1 {
			c.pattern.containsUnsignedLength = true
		}
		if !shareOffsets {
			perAlternativeInitial = fs
		}
	}
	d.hasFixedSize = hasFixedSize
	d.minimumSize = minimumInputSize
	d.callFrameSize = uint32(maximumFrameSize)
	return maximumFrameSize, NoError
}

func (c *patternConstructor) setupOffsets() ErrorCode {
	_, err := c.setupDisjunctionOffsets(c.pattern.body, 0, 0)
	return err
}

// --- Optimizations ------------------------------------------------------------------------

// checkForTerminalParentheses marks greedy non-capturing (...)* at the end
// of the body as needing no per-iteration backtracking state.
func (c *patternConstructor) checkForTerminalParentheses() {
	alternatives := c.pattern.body.alternatives
	if c.pattern.NumSubpatterns == 0 {
		alternatives[len(alternatives)-1].isLastAlternative = true
	}
	for _, a := range alternatives {
		if len(a.terms) == 0 {
			continue
		}
		t := a.lastTerm()
		if t.typ == termParenthesesSubpattern && t.quantityType == QuantifierGreedy && t.quantityMinCount <= 1 &&
			t.quantityMaxCount == quantifyInfinite && !t.capture && !t.containsAnyCaptures() {
			t.parentheses.isTerminal = true
		}
	}
}

func (c *patternConstructor) recomputeStartsWithBOLIn(d *patternDisjunction) bool {
	c.depth++
	defer func() { c.depth-- }()
	if c.depth > maxRecursionDepth {
		c.err = ErrPatternTooLarge
		return false
	}
	all := true
	for _, a := range d.alternatives {
		startsWithBOL := false
		for i := range a.terms {
			t := &a.terms[i]
			termStarts := false
			switch t.typ {
			case termAssertionBOL, termAssertionBOI:
				termStarts = t.matchDirection == Forward
			case termParenthesesSubpattern, termParentheticalAssertion:
				termStarts = c.recomputeStartsWithBOLIn(t.parentheses.disjunction) && t.matchDirection == Forward && parenthesesMustMatch(t)
			}
			if i == 0 {
				startsWithBOL = termStarts
			}
		}
		a.startsWithBOL = startsWithBOL
		if !startsWithBOL {
			all = false
		}
	}
	return all
}

func (c *patternConstructor) recomputeStartsWithBOL() {
	if c.pattern.containsBOL {
		c.recomputeStartsWithBOLIn(c.pattern.body)
	}
}

func (c *patternConstructor) optimizeBOL() {
	d := c.pattern.body
	if c.pattern.containsModifiers || !c.pattern.containsBOL || c.pattern.multiline() {
		return
	}
	loop := c.copyDisjunction(d, true)
	for _, a := range d.alternatives {
		a.onceThrough = true
	}
	if loop != nil {
		d.alternatives = append(d.alternatives, loop.alternatives...)
		loop.alternatives = nil
	}
}

func (c *patternConstructor) containsCapturingTerms(a *patternAlternative, first, end int) bool {
	for i := first; i < end; i++ {
		t := &a.terms[i]
		if t.capture {
			return true
		}
		if t.typ == termParenthesesSubpattern {
			for _, na := range t.parentheses.disjunction.alternatives {
				if c.containsCapturingTerms(na, 0, len(na.terms)) {
					return true
				}
			}
		}
	}
	return false
}

func (c *patternConstructor) optimizeDotStarWrappedExpressions() {
	alternatives := c.pattern.body.alternatives
	if len(alternatives) != 1 || c.pattern.sticky() {
		return
	}
	dot := newlineCharacterClass
	if c.dotAll() {
		dot = anyCharacterClass
	}
	a := alternatives[0]
	terms := a.terms
	if len(terms) < 3 {
		return
	}
	startsWithBOL, endsWithEOL := false, false
	i := 0
	if terms[i].typ == termAssertionBOL {
		startsWithBOL = true
		i++
	}
	if startsWithBOL && c.dotAll() && c.multiline() {
		return
	}
	first := &terms[i]
	if first.typ != termCharacterClass || first.characterClass != dot || first.quantityMinCount != 0 || first.quantityMaxCount != quantifyInfinite {
		return
	}
	firstExpressionTerm := i + 1
	i = len(terms) - 1
	if terms[i].typ == termAssertionEOL {
		endsWithEOL = true
		i--
	}
	last := &terms[i]
	if last.typ != termCharacterClass || last.characterClass != dot || last.quantityType != QuantifierGreedy ||
		last.quantityMinCount != 0 || last.quantityMaxCount != quantifyInfinite {
		return
	}
	endIndex := i
	if firstExpressionTerm >= endIndex {
		return
	}
	if !c.containsCapturingTerms(a, firstExpressionTerm, endIndex) {
		middle := append([]patternTerm(nil), terms[firstExpressionTerm:endIndex]...)
		enclosure := newSimpleTerm(termDotStarEnclosure, c.flags, false)
		enclosure.bolAnchor = startsWithBOL
		enclosure.eolAnchor = endsWithEOL
		a.terms = append(middle, enclosure)
		a.startsWithBOL = false
		c.pattern.containsBOL = false
	}
}

func (c *patternConstructor) setupNamedCaptures() {
	p := c.pattern
	if !p.hasNamedCaptureGroups {
		return
	}
	for uint32(len(p.CaptureGroupNames)) <= p.NumSubpatterns {
		p.CaptureGroupNames = append(p.CaptureGroupNames, "")
	}
	for name, ids := range p.NamedGroupToParenIndices {
		if len(ids) == 2 {
			p.NamedGroupToParenIndices[name] = ids[1:]
		}
	}
	if p.NumDuplicateNamedGroups > 0 {
		p.duplicateNamedGroupForSub = make([]uint32, p.NumSubpatterns+1)
		p.duplicateGroups = make([][]uint32, p.NumDuplicateNamedGroups+1)
		for _, ids := range p.NamedGroupToParenIndices {
			if len(ids) > 2 {
				p.duplicateGroups[ids[0]] = ids[1:]
				for _, sub := range ids[2:] {
					p.CaptureGroupNames[sub] = p.CaptureGroupNames[ids[1]]
				}
				for _, sub := range ids[1:] {
					p.duplicateNamedGroupForSub[sub] = ids[0]
				}
			}
		}
	}
}

func (c *patternConstructor) computeEndAnchoredFixedSize() {
	p := c.pattern
	if p.sticky() || p.containsModifiers || p.containsBOL || p.containsUnsignedLength || !p.body.hasFixedSize || p.saveInitialStartValue {
		return
	}
	var maximumSize uint32
	for _, a := range p.body.alternatives {
		if !a.hasFixedSize || len(a.terms) == 0 {
			return
		}
		last := &a.terms[len(a.terms)-1]
		if !(last.typ == termAssertionEOL && !last.multiline()) && !(last.typ == termAssertionEOI && !last.withOptionalLineTerminator) {
			return
		}
		maximumSize = max(maximumSize, a.minimumSize)
	}
	p.endAnchoredFixedSize = maximumSize
}

// --- Compilation ----------------------------------------------------------------------------

// CompilePattern parses and compiles pattern (YarrPattern::compile).
func CompilePattern(pattern []uint16, flags Flags) (*Pattern, ErrorCode) {
	p := &Pattern{flags: flags, endAnchoredFixedSize: endAnchoredFixedSizeNotSet}
	c := newPatternConstructor(p, flags)
	err := parse(c, pattern, p.compileMode(), quantifyInfinite, true)
	if c.err.hasError() {
		return nil, c.err
	}
	if err.hasError() {
		return nil, err
	}
	c.recomputeStartsWithBOL()
	c.checkForTerminalParentheses()
	// optimizeDotStarWrappedExpressions and optimizeBOL only serve JSC's bytecode
	// interpreter; the tree matcher (match.go) handles BOL anchoring itself.
	if c.err.hasError() {
		return nil, c.err
	}
	if err := c.setupOffsets(); err.hasError() {
		return nil, err
	}
	c.computeEndAnchoredFixedSize()
	c.setupNamedCaptures()
	p.computeFirstUnits()
	p.computePrefilters()
	return p, NoError
}
