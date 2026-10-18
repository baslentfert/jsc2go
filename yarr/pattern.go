package yarr

import (
	"slices"
	"sort"
	"sync/atomic"
)

// The compiled form of a pattern (YarrPattern.h/.cpp): a tree of
// disjunctions, alternatives and terms built by the parser delegate
// YarrPatternConstructor, with frame locations and input positions assigned.

// CharacterRange is an inclusive range of code points.
type CharacterRange struct {
	Begin, End rune
}

type characterClassWidths uint8

const (
	widthsUnknown        characterClassWidths = 0
	widthsHasBMPChars    characterClassWidths = 1
	widthsHasNonBMPChars characterClassWidths = 2
	widthsHasBoth        characterClassWidths = 3
)

// CharacterClass is a set of code points (Latin-1 and other code points kept
// apart, as sorted single matches and ranges) plus, for the v flag, strings.
type CharacterClass struct {
	Strings   [][]rune
	Matches8  []rune
	Ranges8   []CharacterRange
	Matches32 []rune
	Ranges32  []CharacterRange

	widths          characterClassWidths
	anyCharacter    bool
	inCanonicalForm bool
	// latin1 has a bit per Latin-1 character in the class, made with the
	// class (the table JSC's Yarr JIT tests).
	latin1 [4]uint64
}

func (c *CharacterClass) hasNonBMPCharacters() bool { return c.widths&widthsHasNonBMPChars != 0 }
func (c *CharacterClass) hasOneCharacterSize() bool {
	return c.widths == widthsHasBMPChars || c.widths == widthsHasNonBMPChars
}
func (c *CharacterClass) hasOnlyNonBMPCharacters() bool { return c.widths == widthsHasNonBMPChars }
func (c *CharacterClass) hasStrings() bool              { return len(c.Strings) > 0 }
func (c *CharacterClass) hasSingleCharacters() bool {
	return len(c.Matches8) > 0 || len(c.Ranges8) > 0 || len(c.Matches32) > 0 || len(c.Ranges32) > 0
}

// contains is the interpreter's testCharacterClass.
func (c *CharacterClass) contains(ch rune) bool {
	if c.anyCharacter {
		return true
	}
	if isLatin1(ch) {
		return c.latin1[ch>>6]&(1<<(ch&63)) != 0
	}
	matches, ranges := c.Matches32, c.Ranges32
	if len(matches) > 0 {
		i := sort.Search(len(matches), func(i int) bool { return matches[i] >= ch })
		if i < len(matches) && matches[i] == ch {
			return true
		}
	}
	if len(ranges) > 0 {
		i := sort.Search(len(ranges), func(i int) bool { return ranges[i].End >= ch })
		if i < len(ranges) && ranges[i].Begin <= ch {
			return true
		}
	}
	return false
}

// finish completes a class made from its tables.
func (c *CharacterClass) finish() {
	c.widths = characterWidths(c)
	for _, ch := range c.Matches8 {
		c.latin1[ch>>6] |= 1 << (ch & 63)
	}
	for _, r := range c.Ranges8 {
		for ch := r.Begin; ch <= r.End; ch++ {
			c.latin1[ch>>6] |= 1 << (ch & 63)
		}
	}
}

func characterWidths(c *CharacterClass) characterClassWidths {
	w := widthsUnknown
	if len(c.Matches8) > 0 || len(c.Ranges8) > 0 {
		w |= widthsHasBMPChars
	}
	if n := len(c.Matches32); n > 0 {
		if isBMP(c.Matches32[0]) {
			w |= widthsHasBMPChars
		}
		if !isBMP(c.Matches32[n-1]) {
			w |= widthsHasNonBMPChars
		}
	}
	if n := len(c.Ranges32); n > 0 {
		if isBMP(c.Ranges32[0].Begin) {
			w |= widthsHasBMPChars
		}
		if !isBMP(c.Ranges32[n-1].End) {
			w |= widthsHasNonBMPChars
		}
	}
	return w
}

// --- Built-in classes (create_regex_tables) ---------------------------------------------

// builtinClass builds a class from a list of single characters and ranges.
func builtinClass(items ...any) *CharacterClass {
	var ranges []CharacterRange
	for _, it := range items {
		switch v := it.(type) {
		case rune:
			ranges = append(ranges, CharacterRange{v, v})
		case [2]rune:
			ranges = append(ranges, CharacterRange{v[0], v[1]})
		}
	}
	p := &propertyData{ranges: ranges}
	p.finish()
	cc := &CharacterClass{}
	for _, r := range p.ranges {
		lo, hi := r.Begin, r.End
		if lo <= 0xFF {
			h := min(hi, 0xFF)
			if lo == h {
				cc.Matches8 = append(cc.Matches8, lo)
			} else {
				cc.Ranges8 = append(cc.Ranges8, CharacterRange{lo, h})
			}
			if hi <= 0xFF {
				continue
			}
			lo = 0x100
		}
		if lo == hi {
			cc.Matches32 = append(cc.Matches32, lo)
		} else {
			cc.Ranges32 = append(cc.Ranges32, CharacterRange{lo, hi})
		}
	}
	cc.finish()
	return cc
}

var (
	anyCharacterClass = func() *CharacterClass {
		c := &CharacterClass{Ranges8: []CharacterRange{{0, 0xFF}}, Ranges32: []CharacterRange{{0x100, maxCodePoint}},
			widths: widthsHasBoth, anyCharacter: true}
		c.latin1 = [4]uint64{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}
		return c
	}()
	newlineCharacterClass                   = builtinClass('\n', '\r', [2]rune{0x2028, 0x2029})
	digitsCharacterClass                    = builtinClass([2]rune{'0', '9'})
	spacesCharacterClass                    = builtinClass(' ', [2]rune{'\t', '\r'}, rune(0xa0), rune(0x1680), rune(0x202f), rune(0x205f), rune(0x3000), [2]rune{0x2000, 0x200a}, [2]rune{0x2028, 0x2029}, rune(0xfeff))
	wordcharCharacterClass                  = builtinClass('_', [2]rune{'0', '9'}, [2]rune{'A', 'Z'}, [2]rune{'a', 'z'})
	wordUnicodeIgnoreCaseCharCharacterClass = builtinClass('_', [2]rune{'0', '9'}, [2]rune{'A', 'Z'}, [2]rune{'a', 'z'}, rune(0x017f), rune(0x212a))
	nondigitsCharacterClass                 = builtinClass([2]rune{0, '0' - 1}, [2]rune{'9' + 1, maxCodePoint})
	nonspacesCharacterClass                 = builtinClass([2]rune{0, '\t' - 1}, [2]rune{'\r' + 1, ' ' - 1}, [2]rune{' ' + 1, 0x009f}, [2]rune{0x00a1, 0x167f},
		[2]rune{0x1681, 0x1fff}, [2]rune{0x200b, 0x2027}, [2]rune{0x202a, 0x202e}, [2]rune{0x2030, 0x205e},
		[2]rune{0x2060, 0x2fff}, [2]rune{0x3001, 0xfefe}, [2]rune{0xff00, maxCodePoint})
	nonwordcharCharacterClass                  = builtinClass('`', [2]rune{0, '0' - 1}, [2]rune{'9' + 1, 'A' - 1}, [2]rune{'Z' + 1, '_' - 1}, [2]rune{'z' + 1, maxCodePoint})
	nonwordUnicodeIgnoreCaseCharCharacterClass = builtinClass('`', [2]rune{0, '0' - 1}, [2]rune{'9' + 1, 'A' - 1}, [2]rune{'Z' + 1, '_' - 1},
		[2]rune{'z' + 1, 0x017e}, [2]rune{0x0180, 0x2129}, [2]rune{0x212b, maxCodePoint})
)

// --- Terms ------------------------------------------------------------------------------

// QuantifierType is how a term repeats.
type QuantifierType uint8

const (
	QuantifierFixedCount QuantifierType = iota
	QuantifierGreedy
	QuantifierNonGreedy
)

// MatchDirection is Forward (0) or Backward (1, inside lookbehinds).
type MatchDirection uint8

const (
	Forward MatchDirection = iota
	Backward
)

type patternTermType uint8

const (
	termAssertionBOL patternTermType = iota
	termAssertionEOL
	termAssertionBOI
	termAssertionEOI
	termAssertionWordBoundary
	termPatternCharacter
	termCharacterClass
	termNumberedBackReference
	termNamedBackReference
	termNumberedForwardReference
	termNamedForwardReference
	termParenthesesSubpattern
	termParentheticalAssertion
	termDotStarEnclosure
)

// parenthesesInfo is the parentheses part of PatternTerm's union.
type parenthesesInfo struct {
	disjunction      *patternDisjunction
	subpatternID     uint32
	lastSubpatternID uint32
	isCopy           bool
	isTerminal       bool
	isStringList     bool
	isEOLStringList  bool
}

type patternTerm struct {
	typ                        patternTermType
	currentFlags               Flags
	capture                    bool
	invert                     bool
	withOptionalLineTerminator bool
	matchDirection             MatchDirection
	quantityType               QuantifierType
	quantityMinCount           uint32
	quantityMaxCount           uint32

	patternCharacter          rune
	characterClass            *CharacterClass
	backReferenceSubpatternID uint32
	parentheses               parenthesesInfo
	bolAnchor, eolAnchor      bool

	inputPosition uint32
	frameLocation uint32
}

func newCharacterTerm(ch rune, flags Flags, dir MatchDirection) patternTerm {
	return patternTerm{typ: termPatternCharacter, currentFlags: flags, matchDirection: dir, patternCharacter: ch,
		quantityType: QuantifierFixedCount, quantityMinCount: 1, quantityMaxCount: 1}
}

func newClassTerm(cc *CharacterClass, invert bool, flags Flags, dir MatchDirection) patternTerm {
	return patternTerm{typ: termCharacterClass, currentFlags: flags, matchDirection: dir, characterClass: cc, invert: invert,
		quantityType: QuantifierFixedCount, quantityMinCount: 1, quantityMaxCount: 1}
}

func newParenthesesTerm(typ patternTermType, subpatternID uint32, d *patternDisjunction, flags Flags, capture, invert bool, dir MatchDirection) patternTerm {
	return patternTerm{typ: typ, currentFlags: flags, capture: capture, invert: invert, matchDirection: dir,
		parentheses:  parenthesesInfo{disjunction: d, subpatternID: subpatternID},
		quantityType: QuantifierFixedCount, quantityMinCount: 1, quantityMaxCount: 1}
}

func newSimpleTerm(typ patternTermType, flags Flags, invert bool) patternTerm {
	return patternTerm{typ: typ, currentFlags: flags, invert: invert,
		quantityType: QuantifierFixedCount, quantityMinCount: 1, quantityMaxCount: 1}
}

func newBackReferenceTerm(subpatternID uint32, flags Flags) patternTerm {
	t := newSimpleTerm(termNumberedBackReference, flags, false)
	t.backReferenceSubpatternID = subpatternID
	return t
}

func (t *patternTerm) ignoreCase() bool { return t.currentFlags.Has(FlagIgnoreCase) }
func (t *patternTerm) multiline() bool  { return t.currentFlags.Has(FlagMultiline) }
func (t *patternTerm) dotAll() bool     { return t.currentFlags.Has(FlagDotAll) }

func (t *patternTerm) containsAnyCaptures() bool {
	return t.parentheses.lastSubpatternID != 0 && t.parentheses.lastSubpatternID >= t.parentheses.subpatternID
}

func (t *patternTerm) quantify(min, max uint32, typ QuantifierType) {
	t.quantityMinCount = min
	t.quantityMaxCount = max
	t.quantityType = typ
}

type patternAlternative struct {
	terms             []patternTerm
	parent            *patternDisjunction
	minimumSize       uint32
	firstSubpatternID uint32
	lastSubpatternID  uint32
	direction         MatchDirection
	onceThrough       bool
	hasFixedSize      bool
	startsWithBOL     bool
	containsBOL       bool
	isLastAlternative bool

	// first is the set of units a match of a forward alternative starts
	// with, if firstOK (see prefilter.go).
	first   firstSet
	firstOK bool
}

func (a *patternAlternative) lastTerm() *patternTerm { return &a.terms[len(a.terms)-1] }
func (a *patternAlternative) removeLastTerm()        { a.terms = a.terms[:len(a.terms)-1] }

type patternDisjunction struct {
	alternatives  []*patternAlternative
	parent        *patternAlternative
	minimumSize   uint32
	callFrameSize uint32
	hasFixedSize  bool
}

func (d *patternDisjunction) addNewAlternative(firstSubpatternID uint32, dir MatchDirection) *patternAlternative {
	a := &patternAlternative{parent: d, firstSubpatternID: firstSubpatternID, direction: dir}
	d.alternatives = append(d.alternatives, a)
	return a
}

// Pattern is a compiled YarrPattern.
type Pattern struct {
	containsBackreferences    bool
	containsBOL               bool
	containsLookbehinds       bool
	containsUnsignedLength    bool
	containsModifiers         bool
	hasCopiedParenSubexprs    bool
	hasNamedCaptureGroups     bool
	saveInitialStartValue     bool
	flags                     Flags
	endAnchoredFixedSize      uint32
	NumSubpatterns            uint32
	initialStartValueFrameLoc uint32
	NumDuplicateNamedGroups   uint32
	body                      *patternDisjunction
	disjunctions              []*patternDisjunction
	// CaptureGroupNames[i] is the name of capture group i ("" if unnamed).
	CaptureGroupNames []string
	// NamedGroupToParenIndices maps a group name to its subpattern id, or for
	// duplicate names to [duplicateNamedGroupId, subpatternIds...].
	NamedGroupToParenIndices  map[string][]uint32
	duplicateNamedGroupForSub []uint32
	// duplicateGroups[id] lists the subpatterns sharing duplicate name id.
	duplicateGroups [][]uint32

	unicodePropertiesCached map[uint32]*CharacterClass

	// firstUnits, if set, are the only code units a match can start with.
	firstUnits unitSet
	// startSet, if startOK, holds the units a match can start with; a
	// requiredLiteral, if any, occurs in every match, at most
	// literalPrefixMax units after its start (-1: unbounded). See
	// prefilter.go.
	startSet firstSet
	startOK  bool
	// sets, if not nil, is the pattern as alternatives of fixed sequences
	// of code unit sets (setAlternatives): matched without backtracking.
	sets [][]firstSet
	// spares keeps matchers between Execs (match.go).
	spares spares
	// prog is the compiled program (vm.go), made at the second Exec
	// (execs counts them): patterns used once, which some scripts make
	// many of, are matched by the tree matcher without compiling. noProg
	// is set when the pattern is left to the tree matcher.
	prog             atomic.Pointer[vmProgram]
	execs            atomic.Int32
	noProg           atomic.Bool
	requiredLiteral  []uint16
	literalPrefixMax int
}

const endAnchoredFixedSizeNotSet = ^uint32(0)

func (p *Pattern) global() bool        { return p.flags.Has(FlagGlobal) }
func (p *Pattern) ignoreCase() bool    { return p.flags.Has(FlagIgnoreCase) }
func (p *Pattern) multiline() bool     { return p.flags.Has(FlagMultiline) }
func (p *Pattern) sticky() bool        { return p.flags.Has(FlagSticky) }
func (p *Pattern) unicode() bool       { return p.flags.Has(FlagUnicode) }
func (p *Pattern) unicodeSets() bool   { return p.flags.Has(FlagUnicodeSets) }
func (p *Pattern) eitherUnicode() bool { return p.unicode() || p.unicodeSets() }
func (p *Pattern) dotAll() bool        { return p.flags.Has(FlagDotAll) }
func (p *Pattern) compileMode() CompileMode {
	return compileModeFor(p.flags)
}
func (p *Pattern) hasDuplicateNamedCaptureGroups() bool { return p.NumDuplicateNamedGroups != 0 }

// offsetVectorBaseForNamedCaptures is where the duplicate-named-group slots
// start in the output vector.
func (p *Pattern) offsetVectorBaseForNamedCaptures() uint32 { return (p.NumSubpatterns + 1) * 2 }

// OffsetsSize is the size of the output vector a match needs.
func (p *Pattern) OffsetsSize() uint32 {
	return p.offsetVectorBaseForNamedCaptures() + p.NumDuplicateNamedGroups
}

func (p *Pattern) offsetForDuplicateNamedGroupID(id uint32) uint32 {
	return p.offsetVectorBaseForNamedCaptures() + id - 1
}

func (p *Pattern) resetForReparsing() {
	p.NumSubpatterns = 0
	p.initialStartValueFrameLoc = 0
	p.NumDuplicateNamedGroups = 0
	p.containsBackreferences = false
	p.containsBOL = false
	p.containsLookbehinds = false
	p.containsUnsignedLength = false
	p.hasCopiedParenSubexprs = false
	p.hasNamedCaptureGroups = false
	p.saveInitialStartValue = false
	p.unicodePropertiesCached = nil
	p.disjunctions = nil
	p.CaptureGroupNames = nil
	p.NamedGroupToParenIndices = nil
	p.duplicateNamedGroupForSub = nil
}

// unicodeCharacterClassFor returns the (cached) class of a Unicode property.
func (p *Pattern) unicodeCharacterClassFor(id BuiltInCharacterClassID, ignoreCase, invert bool) *CharacterClass {
	ignoreCase = ignoreCase && p.eitherUnicode()
	invert = invert && ignoreCase && !p.unicodeSets()
	key := uint32(id)<<2 | boolBit(ignoreCase)<<1 | boolBit(invert)
	if cc, ok := p.unicodePropertiesCached[key]; ok {
		return cc
	}
	cc := createUnicodeCharacterClassFor(id)
	if ignoreCase {
		if invert {
			cc = invertedCharacterClass(cc, p.compileMode())
		}
		ctor := newCharacterClassConstructor(true, p.compileMode())
		ctor.putUnicodeIgnoreCaseClass(cc)
		cc = ctor.charClass()
		if invert {
			cc = invertedCharacterClass(cc, p.compileMode())
		}
	}
	if p.unicodePropertiesCached == nil {
		p.unicodePropertiesCached = make(map[uint32]*CharacterClass)
	}
	p.unicodePropertiesCached[key] = cc
	return cc
}

func boolBit(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// --- CharacterClassConstructor ----------------------------------------------------------

type characterClassSetOp uint8

const (
	setOpDefault characterClassSetOp = iota
	setOpUnion
	setOpIntersection
	setOpSubtraction
)

type characterClassConstructor struct {
	isCaseInsensitive bool
	anyCharacter      bool
	mayContainStrings bool
	invertedStrings   bool
	setOp             characterClassSetOp
	compileMode       CompileMode
	canonicalMode     canonicalMode

	strings   [][]rune
	matches8  []rune
	ranges8   []CharacterRange
	matches32 []rune
	ranges32  []CharacterRange
}

func newCharacterClassConstructor(caseInsensitive bool, mode CompileMode) *characterClassConstructor {
	cm := canonicalModeUnicode
	if mode == CompileModeLegacy {
		cm = canonicalModeUCS2
	}
	return &characterClassConstructor{isCaseInsensitive: caseInsensitive, compileMode: mode, canonicalMode: cm}
}

func (c *characterClassConstructor) reset() {
	c.strings, c.matches8, c.ranges8, c.matches32, c.ranges32 = nil, nil, nil, nil, nil
	c.setOp = setOpDefault
	c.anyCharacter = false
	c.mayContainStrings = false
	c.invertedStrings = false
}

func (c *characterClassConstructor) combiningSetOp(op characterClassSetOp) { c.setOp = op }

func (c *characterClassConstructor) isUnionSetOp() bool {
	return c.setOp == setOpDefault || c.setOp == setOpUnion
}

func (c *characterClassConstructor) append(other *CharacterClass) {
	if c.setOp != setOpDefault {
		c.performSetOpWithClass(other)
		return
	}
	for _, s := range other.Strings {
		c.strings = append(c.strings, s)
	}
	for _, m := range other.Matches8 {
		c.addSortedIn(&c.matches8, m)
	}
	for _, r := range other.Ranges8 {
		c.addSortedRangeIn(&c.ranges8, r.Begin, r.End)
	}
	for _, m := range other.Matches32 {
		c.addSortedIn(&c.matches32, m)
	}
	for _, r := range other.Ranges32 {
		c.addSortedRangeIn(&c.ranges32, r.Begin, r.End)
	}
	c.mayContainStrings = c.mayContainStrings || other.hasStrings()
}

func (c *characterClassConstructor) appendInverted(other *CharacterClass) {
	addSortedInverted := func(min, max rune, srcMatches []rune, srcRanges []CharacterRange, destMatches *[]rune, destRanges *[]CharacterRange) {
		addMatchOrRange := func(lo, hiPlusOne rune) {
			if lo < hiPlusOne {
				if lo+1 == hiPlusOne {
					c.addSortedIn(destMatches, lo)
				} else {
					c.addSortedRangeIn(destRanges, lo, hiPlusOne-1)
				}
			}
		}
		lo := min
		mi, ri := 0, 0
		if len(srcMatches) == 0 && len(srcRanges) == 0 {
			addMatchOrRange(min, max+1)
			return
		}
		for mi < len(srcMatches) || ri < len(srcRanges) {
			var hiPlusOne, nextLo rune
			if mi < len(srcMatches) && (ri >= len(srcRanges) || srcMatches[mi] < srcRanges[ri].Begin) {
				hiPlusOne = srcMatches[mi]
				nextLo = hiPlusOne + 1
				mi++
			} else {
				hiPlusOne = srcRanges[ri].Begin
				nextLo = srcRanges[ri].End + 1
				ri++
			}
			addMatchOrRange(lo, hiPlusOne)
			lo = nextLo
		}
		addMatchOrRange(lo, max+1)
	}
	if other.hasStrings() {
		c.mayContainStrings = true
		c.invertedStrings = true
	}
	if !c.isUnionSetOp() {
		inverted := &CharacterClass{}
		addSortedInverted(0, 0xFF, other.Matches8, other.Ranges8, &inverted.Matches8, &inverted.Ranges8)
		addSortedInverted(0x100, maxCodePoint, other.Matches32, other.Ranges32, &inverted.Matches32, &inverted.Ranges32)
		c.append(inverted)
		return
	}
	addSortedInverted(0, 0xFF, other.Matches8, other.Ranges8, &c.matches8, &c.ranges8)
	addSortedInverted(0x100, maxCodePoint, other.Matches32, other.Ranges32, &c.matches32, &c.ranges32)
}

func (c *characterClassConstructor) putChar(ch rune) {
	if !c.isUnionSetOp() {
		c.putCharNonUnion(ch)
		return
	}
	if !c.isCaseInsensitive {
		c.addSorted(ch)
		return
	}
	if c.canonicalMode == canonicalModeUCS2 && isASCII(ch) {
		if isASCIIAlpha(ch) {
			c.addSortedIn(&c.matches8, toASCIIUpper(ch))
			c.addSortedIn(&c.matches8, toASCIILower(ch))
		} else {
			c.addSortedIn(&c.matches8, ch)
		}
		return
	}
	info := &canonicalRangeInfoFor(ch, c.canonicalMode)[0]
	if info.typ == CanonicalizeUnique {
		c.addSorted(ch)
	} else {
		c.putUnicodeIgnoreCase(ch, info)
	}
}

func (c *characterClassConstructor) putCharNonUnion(ch rune) {
	var matches8, matches32 []rune
	if c.setOp == setOpIntersection {
		c.strings = nil
	}
	addChar := func(ch rune) {
		if isLatin1(ch) {
			matches8 = append(matches8, ch)
		} else {
			matches32 = append(matches32, ch)
		}
	}
	performOp := func() { c.performSetOpWithMatches(matches8, nil, matches32, nil) }
	if !c.isCaseInsensitive {
		addChar(ch)
		performOp()
		return
	}
	if c.canonicalMode == canonicalModeUCS2 && isASCII(ch) {
		if isASCIIAlpha(ch) {
			addChar(toASCIIUpper(ch))
			addChar(toASCIILower(ch))
		} else {
			addChar(ch)
		}
		performOp()
		return
	}
	info := &canonicalRangeInfoFor(ch, c.canonicalMode)[0]
	switch info.typ {
	case CanonicalizeUnique:
		addChar(ch)
	case CanonicalizeSet:
		for _, x := range canonicalCharacterSetInfo(info.value, c.canonicalMode) {
			addChar(x)
		}
	default:
		pair := getCanonicalPair(info, ch)
		addChar(min(ch, pair))
		addChar(max(ch, pair))
	}
	performOp()
}

func (c *characterClassConstructor) putUnicodeIgnoreCase(ch rune, info *canonicalizationRange) {
	if info.typ == CanonicalizeSet {
		for _, x := range canonicalCharacterSetInfo(info.value, c.canonicalMode) {
			c.addSorted(x)
		}
		return
	}
	c.addSorted(ch)
	c.addSorted(getCanonicalPair(info, ch))
}

func (c *characterClassConstructor) putRange(lo, hi rune) {
	if !c.isCaseInsensitive || c.canonicalMode == canonicalModeUCS2 {
		if isASCII(lo) {
			asciiLo := lo
			asciiHi := min(hi, 0x7F)
			c.addSortedRange(lo, asciiHi)
			if c.isCaseInsensitive {
				if asciiLo <= 'Z' && asciiHi >= 'A' {
					c.addSortedRange(max(asciiLo, 'A')+('a'-'A'), min(asciiHi, 'Z')+('a'-'A'))
				}
				if asciiLo <= 'z' && asciiHi >= 'a' {
					c.addSortedRange(max(asciiLo, 'a')+('A'-'a'), min(asciiHi, 'z')+('A'-'a'))
				}
			}
		}
		if isASCII(hi) {
			return
		}
		lo = max(lo, 0x80)
	}
	c.addSortedRange(lo, hi)
	if !c.isCaseInsensitive {
		return
	}
	infos := canonicalRangeInfoFor(lo, c.canonicalMode)
	for {
		info := &infos[0]
		end := min(info.end, hi)
		switch info.typ {
		case CanonicalizeUnique:
		case CanonicalizeSet:
			for _, x := range canonicalCharacterSetInfo(info.value, c.canonicalMode) {
				c.addSorted(x)
			}
		case CanonicalizeRangeLo:
			c.addSortedRange(lo+info.value, end+info.value)
		case CanonicalizeRangeHi:
			c.addSortedRange(lo-info.value, end-info.value)
		case CanonicalizeAlternatingAligned:
			if lo&1 != 0 {
				c.addSortedRange(lo-1, lo-1)
			}
			if end&1 == 0 {
				c.addSortedRange(end+1, end+1)
			}
		case CanonicalizeAlternatingUnaligned:
			if lo&1 == 0 {
				c.addSortedRange(lo-1, lo-1)
			}
			if end&1 != 0 {
				c.addSortedRange(end+1, end+1)
			}
		}
		if hi == end {
			return
		}
		infos = infos[1:]
		lo = infos[0].begin
	}
}

// putUnicodeIgnoreCaseClass adds every case variant of other's members.
func (c *characterClassConstructor) putUnicodeIgnoreCaseClass(other *CharacterClass) {
	c.strings = append(c.strings, other.Strings...)
	for _, ch := range other.Matches8 {
		c.putChar(ch)
	}
	for _, r := range other.Ranges8 {
		c.putRange(r.Begin, r.End)
	}
	for _, ch := range other.Matches32 {
		c.putChar(ch)
	}
	for _, r := range other.Ranges32 {
		c.putRange(r.Begin, r.End)
	}
	c.mayContainStrings = c.mayContainStrings || other.hasStrings()
}

func (c *characterClassConstructor) atomClassStringDisjunction(strings [][]rune) {
	var utf32Strings [][]rune
	var matches8, matches32 []rune
	sortUTF32Strings(strings)
	addCh := func(ch rune) {
		if isLatin1(ch) {
			matches8 = append(matches8, ch)
		} else {
			matches32 = append(matches32, ch)
		}
	}
	for _, s := range strings {
		if len(s) == 1 {
			ch := s[0]
			if !c.isCaseInsensitive {
				addCh(ch)
				continue
			}
			info := &canonicalRangeInfoFor(ch, c.canonicalMode)[0]
			switch info.typ {
			case CanonicalizeUnique:
				addCh(ch)
			case CanonicalizeSet:
				for _, x := range canonicalCharacterSetInfo(info.value, c.canonicalMode) {
					addCh(x)
				}
			default:
				addCh(ch)
				addCh(getCanonicalPair(info, ch))
			}
			continue
		}
		utf32Strings = append(utf32Strings, s)
	}
	c.performSetOpWithStrings(utf32Strings)
	c.performSetOpWithMatches(matches8, nil, matches32, nil)
}

func (c *characterClassConstructor) invertMatches() {
	if len(c.strings) > 0 {
		c.invertedStrings = true
	}
	c.latin1Invert()
	c.nonLatin1Invert()
}

func (c *characterClassConstructor) performSetOpWith(rhs *characterClassConstructor) {
	c.performSetOpWithStrings(rhs.strings)
	c.performSetOpWithMatches(rhs.matches8, rhs.ranges8, rhs.matches32, rhs.ranges32)
}

func (c *characterClassConstructor) performSetOpWithClass(rhs *CharacterClass) {
	c.performSetOpWithStrings(rhs.Strings)
	c.performSetOpWithMatches(rhs.Matches8, rhs.Ranges8, rhs.Matches32, rhs.Ranges32)
}

func (c *characterClassConstructor) performSetOpWithStrings(rhs [][]rune) {
	if c.compileMode != CompileModeUnicodeSets {
		return
	}
	switch c.setOp {
	case setOpDefault, setOpUnion:
		c.unionStrings(rhs)
	case setOpIntersection:
		c.intersectionStrings(rhs)
	case setOpSubtraction:
		c.subtractionStrings(rhs)
	}
}

func (c *characterClassConstructor) performSetOpWithMatches(m8 []rune, r8 []CharacterRange, m32 []rune, r32 []CharacterRange) {
	if c.compileMode != CompileModeUnicodeSets {
		return
	}
	c.latin1Op(m8, r8)
	sorted := append([]rune(nil), m32...)
	slices.Sort(sorted)
	c.nonLatin1OpSorted(sorted, r32)
}

func (c *characterClassConstructor) hasInvertedStrings() bool { return c.invertedStrings }

// compareUTF32Strings orders longer strings first, then lexically.
func compareUTF32Strings(a, b []rune) int {
	if len(a) > len(b) {
		return -1
	}
	if len(a) < len(b) {
		return 1
	}
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func sortUTF32Strings(s [][]rune) {
	sort.SliceStable(s, func(i, j int) bool { return compareUTF32Strings(s[i], s[j]) < 0 })
}

func (c *characterClassConstructor) charClass() *CharacterClass {
	c.coalesceTables()
	if len(c.strings) > 0 {
		sortUTF32Strings(c.strings)
	}
	cc := &CharacterClass{Strings: c.strings, Matches8: c.matches8, Ranges8: c.ranges8,
		Matches32: c.matches32, Ranges32: c.ranges32, anyCharacter: c.anyCharacter}
	cc.finish()
	c.strings, c.matches8, c.ranges8, c.matches32, c.ranges32 = nil, nil, nil, nil, nil
	c.anyCharacter = false
	return cc
}

func (c *characterClassConstructor) setIsCaseInsensitive(b bool) { c.isCaseInsensitive = b }

func (c *characterClassConstructor) addSorted(ch rune) {
	if isLatin1(ch) {
		c.addSortedIn(&c.matches8, ch)
	} else {
		c.addSortedIn(&c.matches32, ch)
	}
}

func (c *characterClassConstructor) rangesFor(ch rune) *[]CharacterRange {
	if isLatin1(ch) {
		return &c.ranges8
	}
	return &c.ranges32
}

func (c *characterClassConstructor) addSortedIn(matchesPtr *[]rune, ch rune) {
	matches := *matchesPtr
	pos := 0
	rng := len(matches)
	for rng > 0 {
		index := rng >> 1
		val := matches[pos+index] - ch
		switch {
		case val == 0:
			return
		case val > 0:
			if val == 1 {
				lo, hi := ch, ch+1
				matches = slices.Delete(matches, pos+index, pos+index+1)
				if pos+index > 0 && matches[pos+index-1] == ch-1 {
					lo = ch - 1
					matches = slices.Delete(matches, pos+index-1, pos+index)
				}
				*matchesPtr = matches
				c.addSortedRangeIn(c.rangesFor(ch), lo, hi)
				return
			}
			rng = index
		default:
			if val == -1 {
				lo, hi := ch-1, ch
				matches = slices.Delete(matches, pos+index, pos+index+1)
				if pos+index+1 < len(matches) && matches[pos+index+1] == ch+1 {
					hi = ch + 1
					matches = slices.Delete(matches, pos+index+1, pos+index+2)
				}
				*matchesPtr = matches
				c.addSortedRangeIn(c.rangesFor(ch), lo, hi)
				return
			}
			pos += index + 1
			rng -= index + 1
		}
	}
	*matchesPtr = slices.Insert(matches, pos, ch)
}

func (c *characterClassConstructor) addSortedRangeIn(rangesPtr *[]CharacterRange, lo, hi rune) {
	ranges := *rangesPtr
	i := sort.Search(len(ranges), func(i int) bool { return int64(ranges[i].End)+1 >= int64(lo) })
	if i == len(ranges) {
		*rangesPtr = append(ranges, CharacterRange{lo, hi})
		return
	}
	if hi < ranges[i].Begin {
		if hi == ranges[i].Begin-1 {
			ranges[i].Begin = lo
			return
		}
		*rangesPtr = slices.Insert(ranges, i, CharacterRange{lo, hi})
		return
	}
	ranges[i].Begin = min(ranges[i].Begin, lo)
	ranges[i].End = max(ranges[i].End, hi)
	*rangesPtr = mergeRangesFrom(ranges, i)
}

func (c *characterClassConstructor) addSortedRange(lo, hi rune) {
	if lo == hi {
		c.addSorted(lo)
		return
	}
	if isLatin1(lo) {
		latin1Hi := min(hi, 0xFF)
		if lo == latin1Hi {
			c.addSortedIn(&c.matches8, lo)
		} else {
			c.addSortedRangeIn(&c.ranges8, lo, latin1Hi)
		}
		if isLatin1(hi) {
			return
		}
		lo = 0x100
		if lo == hi {
			c.addSortedIn(&c.matches32, hi)
			return
		}
	}
	c.addSortedRangeIn(&c.ranges32, lo, hi)
}

func mergeRangesFrom(ranges []CharacterRange, index int) []CharacterRange {
	next := index + 1
	for next < len(ranges) {
		if ranges[next].Begin <= ranges[index].End+1 {
			ranges[index].End = max(ranges[index].End, ranges[next].End)
			ranges = slices.Delete(ranges, next, next+1)
		} else {
			break
		}
	}
	return ranges
}

func (c *characterClassConstructor) unionStrings(rhs [][]rune) {
	var result [][]rune
	li, ri := 0, 0
	for li < len(c.strings) && ri < len(rhs) {
		cmp := compareUTF32Strings(c.strings[li], rhs[ri])
		if cmp <= 0 {
			result = append(result, c.strings[li])
			li++
			if cmp == 0 {
				ri++
			}
		} else {
			result = append(result, rhs[ri])
			ri++
		}
	}
	result = append(result, c.strings[li:]...)
	result = append(result, rhs[ri:]...)
	c.strings = result
	c.mayContainStrings = len(c.strings) > 0
}

func (c *characterClassConstructor) intersectionStrings(rhs [][]rune) {
	var result [][]rune
	li, ri := 0, 0
	for li < len(c.strings) && ri < len(rhs) {
		cmp := compareUTF32Strings(c.strings[li], rhs[ri])
		switch {
		case cmp == 0:
			result = append(result, c.strings[li])
			li++
			ri++
		case cmp < 0:
			li++
		default:
			ri++
		}
	}
	c.strings = result
	c.mayContainStrings = len(c.strings) > 0
}

func (c *characterClassConstructor) subtractionStrings(rhs [][]rune) {
	var result [][]rune
	li, ri := 0, 0
	for li < len(c.strings) && ri < len(rhs) {
		cmp := compareUTF32Strings(c.strings[li], rhs[ri])
		switch {
		case cmp == 0:
			li++
			ri++
		case cmp < 0:
			result = append(result, c.strings[li])
			li++
		default:
			ri++
		}
	}
	result = append(result, c.strings[li:]...)
	c.strings = result
	c.mayContainStrings = len(c.strings) > 0
}

// rangesFromBits converts a bit set (offset by base) to matches and ranges.
func rangesFromBits(bits []bool, base rune, coalesceWithPrevious bool) ([]rune, []CharacterRange) {
	var matches []rune
	var ranges []CharacterRange
	first := true
	var lo, hi rune
	flush := func() {
		if lo == hi {
			matches = append(matches, lo)
			return
		}
		if coalesceWithPrevious && len(ranges) > 0 && ranges[len(ranges)-1].End+1 == lo {
			ranges[len(ranges)-1].End = hi
			return
		}
		ranges = append(ranges, CharacterRange{lo, hi})
	}
	for i, b := range bits {
		if !b {
			continue
		}
		ch := rune(i) + base
		switch {
		case first:
			lo, hi = ch, ch
			first = false
		case ch == hi+1:
			hi = ch
		default:
			flush()
			lo, hi = ch, ch
		}
	}
	if !first {
		flush()
	}
	return matches, ranges
}

func setBits(bits []bool, base rune, matches []rune, ranges []CharacterRange) {
	for _, m := range matches {
		bits[m-base] = true
	}
	for _, r := range ranges {
		for ch := r.Begin; ch <= r.End; ch++ {
			bits[ch-base] = true
		}
	}
}

func (c *characterClassConstructor) latin1Op(rhsMatches []rune, rhsRanges []CharacterRange) {
	lhs := make([]bool, 0x100)
	rhs := make([]bool, 0x100)
	setBits(lhs, 0, c.matches8, c.ranges8)
	setBits(rhs, 0, rhsMatches, rhsRanges)
	for i := range lhs {
		switch c.setOp {
		case setOpDefault, setOpUnion:
			lhs[i] = lhs[i] || rhs[i]
		case setOpIntersection:
			lhs[i] = lhs[i] && rhs[i]
		case setOpSubtraction:
			lhs[i] = lhs[i] && !rhs[i]
		}
	}
	c.matches8, c.ranges8 = rangesFromBits(lhs, 0, false)
}

func (c *characterClassConstructor) latin1Invert() {
	bits := make([]bool, 0x100)
	setBits(bits, 0, c.matches8, c.ranges8)
	for i := range bits {
		bits[i] = !bits[i]
	}
	c.matches8, c.ranges8 = rangesFromBits(bits, 0, false)
}

func (c *characterClassConstructor) nonLatin1OpSorted(rhsMatches []rune, rhsRanges []CharacterRange) {
	const chunkSize = 2048
	var resultMatches []rune
	var resultRanges []CharacterRange
	lhsBits := make([]bool, chunkSize)
	rhsBits := make([]bool, chunkSize)
	chunkLo := rune(1<<31 - 1)
	if len(c.matches32) > 0 {
		chunkLo = min(chunkLo, c.matches32[0])
	}
	if len(c.ranges32) > 0 {
		chunkLo = min(chunkLo, c.ranges32[0].Begin)
	}
	if len(rhsMatches) > 0 {
		chunkLo = min(chunkLo, rhsMatches[0])
	}
	if len(rhsRanges) > 0 {
		chunkLo = min(chunkLo, rhsRanges[0].Begin)
	}
	lmi, lri, rmi, rri := 0, 0, 0, 0
	lhsHasMore := func() bool { return lmi < len(c.matches32) || lri < len(c.ranges32) }
	rhsHasMore := func() bool { return rmi < len(rhsMatches) || rri < len(rhsRanges) }
	canProduceMore := func() bool {
		switch c.setOp {
		case setOpIntersection:
			return lhsHasMore() && rhsHasMore()
		case setOpSubtraction:
			return lhsHasMore()
		}
		return lhsHasMore() || rhsHasMore()
	}
	fill := func(bits []bool, matches []rune, mi *int, ranges []CharacterRange, ri *int, chunkHi rune) {
		for ; *mi < len(matches); *mi++ {
			ch := matches[*mi]
			if ch > chunkHi {
				break
			}
			bits[ch-chunkLo] = true
		}
		for ; *ri < len(ranges); *ri++ {
			r := ranges[*ri]
			if r.Begin > chunkHi {
				break
			}
			for ch := max(chunkLo, r.Begin); ch <= min(r.End, chunkHi); ch++ {
				bits[ch-chunkLo] = true
			}
			if r.End > chunkHi {
				break
			}
		}
	}
	for canProduceMore() {
		chunkHi := chunkLo + chunkSize - 1
		fill(lhsBits, c.matches32, &lmi, c.ranges32, &lri, chunkHi)
		fill(rhsBits, rhsMatches, &rmi, rhsRanges, &rri, chunkHi)
		for i := range lhsBits {
			switch c.setOp {
			case setOpDefault, setOpUnion:
				lhsBits[i] = lhsBits[i] || rhsBits[i]
			case setOpIntersection:
				lhsBits[i] = lhsBits[i] && rhsBits[i]
			case setOpSubtraction:
				lhsBits[i] = lhsBits[i] && !rhsBits[i]
			}
		}
		m, r := rangesFromBits(lhsBits, chunkLo, true)
		resultMatches = append(resultMatches, m...)
		for _, x := range r {
			if n := len(resultRanges); n > 0 && resultRanges[n-1].End+1 == x.Begin {
				resultRanges[n-1].End = x.End
				continue
			}
			resultRanges = append(resultRanges, x)
		}
		chunkLo = chunkHi + 1
		clear(lhsBits)
		clear(rhsBits)
	}
	c.matches32, c.ranges32 = resultMatches, resultRanges
}

func (c *characterClassConstructor) nonLatin1Invert() {
	saved := c.setOp
	c.setOp = setOpSubtraction
	matches, ranges := c.matches32, c.ranges32
	c.matches32, c.ranges32 = nil, []CharacterRange{{0x100, maxCodePoint}}
	c.nonLatin1OpSorted(matches, ranges)
	c.setOp = saved
}

func coalesceMatchesAndRanges(matches []rune, ranges []CharacterRange) ([]rune, []CharacterRange) {
	mi, ri := 0, 0
	for mi < len(matches) && ri < len(ranges) {
		if ranges[ri].Begin != 0 {
			for mi < len(matches) && matches[mi] < ranges[ri].Begin-1 {
				mi++
			}
			if mi < len(matches) && matches[mi] == ranges[ri].Begin-1 {
				ranges[ri].Begin = matches[mi]
				matches = slices.Delete(matches, mi, mi+1)
			}
		}
		end := mi
		for end < len(matches) && matches[end] < ranges[ri].End+1 {
			end++
		}
		if mi != end {
			matches = slices.Delete(matches, mi, end)
		}
		if mi < len(matches) {
			if matches[mi] > ranges[ri].End+1 {
				ri++
				continue
			}
			if matches[mi] == ranges[ri].End+1 {
				ranges[ri].End = matches[mi]
				matches = slices.Delete(matches, mi, mi+1)
				ranges = mergeRangesFrom(ranges, ri)
			} else {
				mi++
			}
		}
	}
	for i := len(ranges) - 1; i > 0; i-- {
		if ranges[i].Begin == ranges[i-1].End+1 {
			ranges[i-1].End = ranges[i].End
			ranges = slices.Delete(ranges, i, i+1)
		}
	}
	return matches, ranges
}

func (c *characterClassConstructor) coalesceTables() {
	c.matches8, c.ranges8 = coalesceMatchesAndRanges(c.matches8, c.ranges8)
	c.matches32, c.ranges32 = coalesceMatchesAndRanges(c.matches32, c.ranges32)
	if len(c.matches8) == 0 && len(c.matches32) == 0 && len(c.ranges8) == 1 && len(c.ranges32) == 1 &&
		c.ranges8[0] == (CharacterRange{0, 0xFF}) && c.ranges32[0] == (CharacterRange{0x100, maxCodePoint}) {
		c.anyCharacter = true
	}
}

func invertedCharacterClass(cc *CharacterClass, mode CompileMode) *CharacterClass {
	ctor := newCharacterClassConstructor(false, mode)
	ctor.appendInverted(cc)
	return ctor.charClass()
}
