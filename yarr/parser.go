package yarr

import (
	"strings"
	"unicode"
)

// The pattern parser (YarrParser.h). It reports the pattern's structure to
// a delegate; YarrPatternConstructor (constructor.go) builds a Pattern from
// those callbacks.

type createDisjunctionPurpose uint8

const (
	notForNextAlternative createDisjunctionPurpose = iota
	forNextAlternative
)

// delegate receives the parse events (YarrSyntaxCheckable).
type delegate interface {
	escapeDelegate
	assertionBOL()
	assertionEOL()
	atomCharacterClassBegin(invert bool)
	atomCharacterClassAtom(ch rune)
	atomCharacterClassRange(lo, hi rune)
	atomCharacterClassBuiltIn(id BuiltInCharacterClassID, invert bool)
	atomClassStringDisjunction(strings [][]rune)
	atomCharacterClassSetOp(op characterClassSetOp)
	atomCharacterClassPushNested(invert bool)
	atomCharacterClassPopNested(invert bool)
	atomCharacterClassEnd()
	atomParenthesesSubpatternBegin(capture bool, name *string)
	atomParentheticalAssertionBegin(invert bool, dir MatchDirection)
	atomParentheticalModifierBegin(set, unset Flags)
	atomParenthesesEnd()
	quantifyAtom(min, max uint32, greedy bool)
	disjunction(purpose createDisjunctionPurpose)
	abortedDueToError() bool
	abortErrorCode() ErrorCode
	resetForReparsing()
}

// escapeDelegate receives the events parseEscape can produce.
type escapeDelegate interface {
	atomPatternCharacter(ch rune, hyphenIsRange bool)
	atomBuiltInCharacterClass(id BuiltInCharacterClassID, invert bool)
	assertionBOI()
	assertionEOI(withOptionalLineTerminator bool)
	assertionWordBoundary(invert bool)
	atomBackReference(subpatternID uint32)
	atomNamedBackReference(name string)
	atomNamedForwardReference(name string)
}

// notInClass implements the escapeDelegate methods that cannot occur inside
// a character class.
type notInClass struct{}

func (notInClass) assertionBOI()                    { panic("yarr: unreachable") }
func (notInClass) assertionEOI(bool)                { panic("yarr: unreachable") }
func (notInClass) assertionWordBoundary(bool)       { panic("yarr: unreachable") }
func (notInClass) atomBackReference(uint32)         { panic("yarr: unreachable") }
func (notInClass) atomNamedBackReference(string)    { panic("yarr: unreachable") }
func (notInClass) atomNamedForwardReference(string) { panic("yarr: unreachable") }

type parseEscapeMode uint8

const (
	escapeNormal parseEscapeMode = iota
	escapeCharacterClass
	escapeClassSet
	escapeClassStringDisjunction
)

type tokenType uint8

const (
	tokenNotAtom tokenType = iota
	tokenAtom
	tokenLookbehind
	tokenSetDisjunction
	tokenSetDisjunctionMayContainStrings
)

type parenthesesType uint8

const (
	parenSubpattern parenthesesType = iota
	parenAssertion
	parenLookbehindAssertion
)

const (
	errorCodePoint   = rune(-1)
	maxPatternSize   = 1024 * 1024
	maxCapturesCount = (1 << 16) / 2
)

// namedCaptureGroups tracks group names per alternative so that duplicate
// names are allowed only in different alternatives.
type namedCaptureGroups struct {
	all    map[string]bool
	nested []map[string]bool
	active []map[string]bool
}

func newNamedCaptureGroups() *namedCaptureGroups {
	n := &namedCaptureGroups{}
	n.reset()
	return n
}

func (n *namedCaptureGroups) reset() {
	n.all = map[string]bool{}
	n.nested = []map[string]bool{{}}
	n.active = []map[string]bool{{}}
}

func (n *namedCaptureGroups) contains(name string) bool { return n.all[name] }
func (n *namedCaptureGroups) isEmpty() bool             { return len(n.all) == 0 }

func addAll(dst, src map[string]bool) {
	for k := range src {
		dst[k] = true
	}
}

func (n *namedCaptureGroups) nextAlternative() {
	last := len(n.active) - 1
	addAll(n.nested[len(n.nested)-1], n.active[last])
	n.active[last] = map[string]bool{}
	if len(n.active) > 1 {
		addAll(n.active[last], n.active[last-1])
	}
}

func (n *namedCaptureGroups) pushParenthesis() {
	top := map[string]bool{}
	addAll(top, n.active[len(n.active)-1])
	n.nested = append(n.nested, map[string]bool{})
	n.active = append(n.active, top)
}

func (n *namedCaptureGroups) popParenthesis() {
	addAll(n.nested[len(n.nested)-1], n.active[len(n.active)-1])
	addAll(n.active[len(n.active)-2], n.nested[len(n.nested)-1])
	n.nested = n.nested[:len(n.nested)-1]
	n.active = n.active[:len(n.active)-1]
}

// add records name and reports whether it is new in the active alternative.
func (n *namedCaptureGroups) add(name string) bool {
	n.all[name] = true
	active := n.active[len(n.active)-1]
	if active[name] {
		return false
	}
	active[name] = true
	return true
}

type parser struct {
	d                              delegate
	err                            ErrorCode
	data                           []uint16
	index                          int
	compileMode                    CompileMode
	backReferenceLimit             uint32
	numSubpatterns                 uint32
	maxSeenBackReference           uint32
	numCaptures                    uint32
	isNamedForwardReferenceAllowed bool
	allowRegExpBufferBoundaries    bool
	kIdentityEscapeSeen            bool
	parenthesesStack               []parenthesesType
	namedCaptureGroups             *namedCaptureGroups
	forwardReferenceNames          map[string]bool
}

// parse is Yarr::parse.
func parse(d delegate, pattern []uint16, mode CompileMode, backReferenceLimit uint32, isNamedForwardReferenceAllowed bool) ErrorCode {
	p := &parser{d: d, data: pattern, compileMode: mode, backReferenceLimit: backReferenceLimit,
		isNamedForwardReferenceAllowed: isNamedForwardReferenceAllowed,
		namedCaptureGroups:             newNamedCaptureGroups(), forwardReferenceNames: map[string]bool{}}
	if len(pattern) > maxPatternSize {
		return ErrPatternTooLarge
	}
	p.parseTokens()
	if !p.err.hasError() {
		p.handleIllegalReferences()
	}
	return p.err
}

func (p *parser) isLegacy() bool        { return p.compileMode == CompileModeLegacy }
func (p *parser) isUnicode() bool       { return p.compileMode == CompileModeUnicode }
func (p *parser) isUnicodeSets() bool   { return p.compileMode == CompileModeUnicodeSets }
func (p *parser) isEitherUnicode() bool { return p.isUnicode() || p.isUnicodeSets() }

func (p *parser) atEnd() bool          { return p.index == len(p.data) }
func (p *parser) remaining() int       { return len(p.data) - p.index }
func (p *parser) peek() rune           { return rune(p.data[p.index]) }
func (p *parser) consume() rune        { c := rune(p.data[p.index]); p.index++; return c }
func (p *parser) peekIsDigit() bool    { return !p.atEnd() && isASCIIDigit(p.peek()) }
func (p *parser) consumeDigit() uint32 { return uint32(p.consume() - '0') }

func (p *parser) tryConsume(ch rune) bool {
	if p.atEnd() || rune(p.data[p.index]) != ch {
		return false
	}
	p.index++
	return true
}

func (p *parser) consumeNumber() uint32 {
	n := uint64(p.consumeDigit())
	overflow := false
	for p.peekIsDigit() {
		n = n*10 + uint64(p.consumeDigit())
		if n > uint64(^uint32(0)) {
			overflow = true
			n = uint64(^uint32(0))
		}
	}
	if overflow {
		return quantifyInfinite
	}
	return uint32(n)
}

func (p *parser) consumeNumber64() uint64 {
	n := uint64(p.consumeDigit())
	overflow := false
	for p.peekIsDigit() {
		d := uint64(p.consumeDigit())
		if n > (^uint64(0)-d)/10 {
			overflow = true
		}
		n = n*10 + d
	}
	if overflow {
		return quantifyInfinite64
	}
	return n
}

func (p *parser) consumeOctal(count int) rune {
	var octal rune
	for ; count > 0 && octal < 32 && !p.atEnd() && isASCIIOctalDigit(p.peek()); count-- {
		octal = octal*8 + rune(p.consumeDigit())
	}
	return octal
}

func (p *parser) tryConsumeHex(count int) rune {
	state := p.index
	var n rune
	for ; count > 0; count-- {
		if p.atEnd() || !isASCIIHexDigit(p.peek()) {
			p.index = state
			return errorCodePoint
		}
		n = n<<4 | toASCIIHexValue(p.consume())
	}
	return n
}

type unicodeParseContext uint8

const (
	contextPatternCodePoint unicodeParseContext = iota
	contextGroupName
)

func (p *parser) consumePossibleSurrogatePair(ctx unicodeParseContext) rune {
	unicodeOrName := p.isEitherUnicode() || ctx == contextGroupName
	ch := p.consume()
	if isLead(ch) && unicodeOrName && !p.atEnd() {
		state := p.index
		trail := p.consume()
		if isTrail(trail) {
			ch = supplementary(ch, trail)
		} else {
			p.index = state
		}
	}
	return ch
}

func (p *parser) isIdentityEscapeAnError(mode parseEscapeMode, ch rune) bool {
	if p.isEitherUnicode() {
		allowed := "^$\\.*+?()[]{}|/"
		if mode == escapeClassSet || mode == escapeClassStringDisjunction {
			allowed = "^$\\.*+?()[]{}|/&-!#%,:;<=>@`~"
		}
		if isASCII(ch) && !strings.ContainsRune(allowed, ch) || ch == 0 {
			p.err = ErrInvalidIdentityEscape
			return true
		}
	}
	return false
}

// parseEscape parses the escape at '\\' and reports it to d.
func (p *parser) parseEscape(mode parseEscapeMode, d escapeDelegate) tokenType {
	p.consume() // '\\'
	if p.atEnd() {
		p.err = ErrEscapeUnterminated
		return tokenNotAtom
	}
	switch p.peek() {
	case 'b':
		p.consume()
		if mode != escapeNormal {
			d.atomPatternCharacter('\b', false)
		} else {
			d.assertionWordBoundary(false)
			return tokenNotAtom
		}
	case 'B':
		p.consume()
		if mode != escapeNormal {
			if p.isIdentityEscapeAnError(mode, 'B') {
				break
			}
			d.atomPatternCharacter('B', false)
		} else {
			d.assertionWordBoundary(true)
			return tokenNotAtom
		}
	case 'd', 's', 'w', 'D', 'S', 'W':
		c := p.consume()
		if mode == escapeClassStringDisjunction {
			d.atomPatternCharacter(c, false)
			break
		}
		id := map[rune]BuiltInCharacterClassID{'d': DigitClassID, 's': SpaceClassID, 'w': WordClassID,
			'D': DigitClassID, 'S': SpaceClassID, 'W': WordClassID}[c]
		d.atomBuiltInCharacterClass(id, c < 'a')
	case '0':
		p.consume()
		if !p.peekIsDigit() {
			d.atomPatternCharacter(0, false)
			break
		}
		if p.isEitherUnicode() {
			p.err = ErrInvalidOctalEscape
			break
		}
		d.atomPatternCharacter(p.consumeOctal(2), false)
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if mode == escapeNormal {
			state := p.index
			backReference := p.consumeNumber()
			if backReference <= p.backReferenceLimit {
				p.maxSeenBackReference = max(p.maxSeenBackReference, backReference)
				d.atomBackReference(backReference)
				break
			}
			p.index = state
			if p.isEitherUnicode() {
				p.err = ErrInvalidBackreference
				break
			}
		}
		if p.isEitherUnicode() {
			p.err = ErrInvalidOctalEscape
			break
		}
		if p.peek() < '8' {
			d.atomPatternCharacter(p.consumeOctal(3), false)
		} else {
			d.atomPatternCharacter(p.consume(), false)
		}
	case 'f':
		p.consume()
		d.atomPatternCharacter('\f', false)
	case 'n':
		p.consume()
		d.atomPatternCharacter('\n', false)
	case 'r':
		p.consume()
		d.atomPatternCharacter('\r', false)
	case 't':
		p.consume()
		d.atomPatternCharacter('\t', false)
	case 'v':
		p.consume()
		d.atomPatternCharacter('\v', false)
	case 'c':
		state := p.index
		p.consume()
		if !p.atEnd() {
			control := p.consume()
			if isASCIIAlpha(control) {
				d.atomPatternCharacter(control&0x1f, false)
				break
			}
			if p.isEitherUnicode() {
				p.err = ErrInvalidControlLetterEscape
				break
			}
			if mode != escapeNormal && (isASCIIDigit(control) || control == '_') {
				d.atomPatternCharacter(control&0x1f, false)
				break
			}
		}
		if p.isEitherUnicode() {
			p.err = ErrInvalidIdentityEscape
			break
		}
		p.index = state
		d.atomPatternCharacter('\\', false)
	case 'x':
		p.consume()
		x := p.tryConsumeHex(2)
		if x == errorCodePoint {
			if p.isIdentityEscapeAnError(mode, 'x') {
				break
			}
			d.atomPatternCharacter('x', false)
		} else {
			d.atomPatternCharacter(x, false)
		}
	case 'k':
		p.consume()
		state := p.index
		if mode == escapeNormal && p.tryConsume('<') {
			name, ok := p.tryConsumeGroupName()
			if p.err.hasError() {
				break
			}
			if ok {
				if p.namedCaptureGroups.contains(name) {
					d.atomNamedBackReference(name)
					break
				}
				if p.isNamedForwardReferenceAllowed {
					p.forwardReferenceNames[name] = true
					d.atomNamedForwardReference(name)
					break
				}
			}
		}
		p.index = state
		if !p.isIdentityEscapeAnError(mode, 'k') {
			d.atomPatternCharacter('k', false)
			p.kIdentityEscapeSeen = true
		}
	case 'p', 'P':
		escapeChar := p.consume()
		if p.isLegacy() || mode == escapeClassStringDisjunction {
			if p.isIdentityEscapeAnError(mode, escapeChar) {
				break
			}
			d.atomPatternCharacter(escapeChar, false)
			break
		}
		if !p.atEnd() && p.peek() == '{' {
			p.consume()
			id, ok := p.tryConsumeUnicodePropertyExpression()
			if !ok {
				break
			}
			if escapeChar == 'P' && characterClassMayContainStrings(id) {
				p.err = ErrNegatedClassSetMayContainStrings
				break
			}
			d.atomBuiltInCharacterClass(id, escapeChar == 'P')
		} else {
			p.err = ErrInvalidUnicodePropertyExpression
		}
	case 'q':
		escapeChar := p.consume()
		if mode == escapeClassSet {
			if !p.atEnd() && p.peek() == '{' {
				mayContainStrings := p.parseClassStringDisjunction()
				if mayContainStrings {
					return tokenSetDisjunctionMayContainStrings
				}
				return tokenSetDisjunction
			}
			p.err = ErrInvalidUnicodePropertyExpression
		}
		if p.isIdentityEscapeAnError(mode, escapeChar) {
			break
		}
		d.atomPatternCharacter(escapeChar, false)
	case 'u':
		cp := p.tryConsumeUnicodeEscape(contextPatternCodePoint)
		if p.err.hasError() {
			break
		}
		if cp == errorCodePoint {
			cp = 'u'
		}
		d.atomPatternCharacter(cp, false)
	default:
		ch := p.peek()
		if ch == '-' && p.isEitherUnicode() && mode != escapeNormal {
			d.atomPatternCharacter(p.consume(), false)
			break
		}
		if p.isIdentityEscapeAnError(mode, ch) {
			break
		}
		d.atomPatternCharacter(p.consume(), false)
	}
	return tokenAtom
}

func (p *parser) consumeAndCheckIfValidClassSetCharacter() rune {
	ch := p.consumePossibleSurrogatePair(contextPatternCodePoint)
	if ch == 0 {
		p.err = ErrInvalidClassSetCharacter
		return errorCodePoint
	}
	if isASCII(ch) {
		if strings.ContainsRune("()[]{}/|)", ch) {
			p.err = ErrInvalidClassSetCharacter
			return errorCodePoint
		}
		if !p.atEnd() {
			next := p.peek()
			if ch == next && strings.ContainsRune("&!#$%*+,.:;<=>?@^`~", ch) {
				p.err = ErrInvalidClassSetOperation
				return errorCodePoint
			}
		}
	}
	return ch
}

// --- Character classes ---------------------------------------------------------------------

type ccState uint8

const (
	ccEmpty ccState = iota
	ccCachedCharacter
	ccCachedCharacterHyphen
	ccAfterCharacterClass
	ccAfterCharacterClassHyphen
	ccAfterSetRange
	ccAfterSetOperand
	ccAfterSetOperator
)

// characterClassParserDelegate detects ranges in [...] classes.
type characterClassParserDelegate struct {
	notInClass
	d         delegate
	err       *ErrorCode
	isUnicode bool
	state     ccState
	character rune
}

func (c *characterClassParserDelegate) begin(invert bool) { c.d.atomCharacterClassBegin(invert) }

func (c *characterClassParserDelegate) atomPatternCharacter(ch rune, hyphenIsRange bool) {
	switch c.state {
	case ccAfterCharacterClass:
		if hyphenIsRange && ch == '-' {
			c.d.atomCharacterClassAtom('-')
			c.state = ccAfterCharacterClassHyphen
			return
		}
		fallthrough
	case ccEmpty:
		c.character = ch
		c.state = ccCachedCharacter
	case ccCachedCharacter:
		if hyphenIsRange && ch == '-' {
			c.state = ccCachedCharacterHyphen
		} else {
			c.d.atomCharacterClassAtom(c.character)
			c.character = ch
		}
	case ccCachedCharacterHyphen:
		if ch < c.character {
			*c.err = ErrCharacterClassRangeOutOfOrder
			return
		}
		c.d.atomCharacterClassRange(c.character, ch)
		c.state = ccEmpty
	case ccAfterCharacterClassHyphen:
		if c.isUnicode {
			*c.err = ErrCharacterClassRangeInvalid
			return
		}
		c.d.atomCharacterClassAtom(ch)
		c.state = ccEmpty
	}
}

func (c *characterClassParserDelegate) atomBuiltInCharacterClass(id BuiltInCharacterClassID, invert bool) {
	switch c.state {
	case ccCachedCharacter:
		c.d.atomCharacterClassAtom(c.character)
		fallthrough
	case ccEmpty, ccAfterCharacterClass:
		c.d.atomCharacterClassBuiltIn(id, invert)
		c.state = ccAfterCharacterClass
	case ccCachedCharacterHyphen:
		c.d.atomCharacterClassAtom(c.character)
		c.d.atomCharacterClassAtom('-')
		fallthrough
	case ccAfterCharacterClassHyphen:
		if c.isUnicode {
			*c.err = ErrCharacterClassRangeInvalid
			return
		}
		c.d.atomCharacterClassBuiltIn(id, invert)
		c.state = ccEmpty
	}
}

func (c *characterClassParserDelegate) end() {
	switch c.state {
	case ccCachedCharacter:
		c.d.atomCharacterClassAtom(c.character)
	case ccCachedCharacterHyphen:
		c.d.atomCharacterClassAtom(c.character)
		c.d.atomCharacterClassAtom('-')
	}
	c.d.atomCharacterClassEnd()
}

func (p *parser) parseCharacterClass() {
	p.consume() // '['
	c := &characterClassParserDelegate{d: p.d, err: &p.err, isUnicode: p.compileMode == CompileModeUnicode}
	c.begin(p.tryConsume('^'))
	for !p.atEnd() {
		switch p.peek() {
		case ']':
			p.consume()
			c.end()
			return
		case '\\':
			p.parseEscape(escapeCharacterClass, c)
		default:
			c.atomPatternCharacter(p.consumePossibleSurrogatePair(contextPatternCodePoint), true)
		}
		if p.err.hasError() {
			return
		}
	}
	p.err = ErrCharacterClassUnmatched
}

// classSetParserDelegate handles v-flag class sets with nesting and set ops.
type classSetParserDelegate struct {
	notInClass
	d                 delegate
	err               *ErrorCode
	state             ccState
	setOp             characterClassSetOp
	mayContainStrings bool
	inverted          bool
	processingEscape  bool
	character         rune
	nested            []classSetNesting
}

type classSetNesting struct {
	setOp             characterClassSetOp
	mayContainStrings bool
	inverted          bool
}

func (c *classSetParserDelegate) begin(invert bool) {
	c.inverted = invert
	c.d.atomCharacterClassBegin(invert)
}

func (c *classSetParserDelegate) nestedClassBegin(invert bool) {
	c.flushCachedCharacterIfNeeded()
	c.d.atomCharacterClassPushNested(invert)
	c.nested = append(c.nested, classSetNesting{c.setOp, c.mayContainStrings, c.inverted})
	c.setOp = setOpDefault
	c.mayContainStrings = false
	c.inverted = invert
}

func (c *classSetParserDelegate) nestedClassEnd() bool {
	c.flushCachedCharacterIfNeeded()
	c.processingEscape = false
	if c.inverted && c.mayContainStrings {
		*c.err = ErrNegatedClassSetMayContainStrings
	}
	if len(c.nested) == 0 {
		c.end()
		return true
	}
	rhsMayContainStrings := c.mayContainStrings
	last := c.nested[len(c.nested)-1]
	c.nested = c.nested[:len(c.nested)-1]
	c.setOp, c.inverted, c.mayContainStrings = last.setOp, last.inverted, last.mayContainStrings
	c.d.atomCharacterClassPopNested(c.inverted)
	c.state = ccAfterSetOperand
	c.computeMayContainStrings(rhsMayContainStrings)
	return false
}

func (c *classSetParserDelegate) switchFromDefaultOpToUnionOpIfNeeded() {
	if c.setOp == setOpDefault {
		c.setOp = setOpUnion
		c.d.atomCharacterClassSetOp(c.setOp)
	}
}

func (c *classSetParserDelegate) setSubtractOp() {
	if c.state == ccEmpty || c.setOp != setOpDefault && c.setOp != setOpSubtraction {
		*c.err = ErrInvalidClassSetOperation
		return
	}
	c.flushCachedCharacterIfNeeded()
	c.setOp = setOpSubtraction
	c.d.atomCharacterClassSetOp(c.setOp)
	c.state = ccAfterSetOperator
}

func (c *classSetParserDelegate) setIntersectionOp() {
	if c.state == ccEmpty || c.setOp != setOpDefault && c.setOp != setOpIntersection {
		*c.err = ErrInvalidClassSetOperation
		return
	}
	c.flushCachedCharacterIfNeeded()
	c.setOp = setOpIntersection
	c.d.atomCharacterClassSetOp(c.setOp)
	c.state = ccAfterSetOperator
}

func (c *classSetParserDelegate) computeMayContainStrings(rhs bool) {
	switch c.setOp {
	case setOpDefault, setOpUnion:
		c.mayContainStrings = c.mayContainStrings || rhs
	case setOpIntersection:
		c.mayContainStrings = c.mayContainStrings && rhs
	}
}

func (c *classSetParserDelegate) flushCachedCharacterIfNeeded() {
	switch c.state {
	case ccCachedCharacter:
		c.d.atomCharacterClassAtom(c.character)
		c.state = ccEmpty
	case ccCachedCharacterHyphen, ccAfterCharacterClassHyphen:
		*c.err = ErrInvalidClassSetCharacter
	}
}

func (c *classSetParserDelegate) afterSetOperand() {
	c.flushCachedCharacterIfNeeded()
	c.processingEscape = false
	c.state = ccAfterSetOperand
}

func (c *classSetParserDelegate) canTakeSetOperand() bool {
	unionOpActive := c.setOp == setOpDefault || c.setOp == setOpUnion
	switch c.state {
	case ccEmpty, ccAfterSetOperator:
		return true
	case ccCachedCharacter:
		if !unionOpActive {
			return false
		}
		c.flushCachedCharacterIfNeeded()
		return true
	}
	return unionOpActive
}

func (c *classSetParserDelegate) atomPatternCharacter(ch rune, hyphenIsRange bool) {
	unionOpActive := c.setOp == setOpDefault || c.setOp == setOpUnion
	processingEscape := c.processingEscape
	c.processingEscape = false
	processCharacter := func() {
		c.character = ch
		c.state = ccCachedCharacter
	}
	switch c.state {
	case ccAfterCharacterClass:
		if hyphenIsRange && unionOpActive && ch == '-' {
			c.d.atomCharacterClassAtom('-')
			c.state = ccAfterCharacterClassHyphen
			return
		}
		fallthrough
	case ccAfterSetRange:
		c.switchFromDefaultOpToUnionOpIfNeeded()
		fallthrough
	case ccEmpty, ccAfterSetOperator:
		if !processingEscape && ch == '-' {
			*c.err = ErrInvalidClassSetCharacter
			return
		}
		processCharacter()
	case ccCachedCharacter:
		if !unionOpActive {
			*c.err = ErrInvalidClassSetOperation
			return
		}
		if ch == '-' {
			c.state = ccCachedCharacterHyphen
		} else {
			c.d.atomCharacterClassAtom(c.character)
			c.switchFromDefaultOpToUnionOpIfNeeded()
			processCharacter()
		}
	case ccCachedCharacterHyphen:
		if ch < c.character {
			*c.err = ErrCharacterClassRangeOutOfOrder
			return
		}
		c.d.atomCharacterClassRange(c.character, ch)
		c.switchFromDefaultOpToUnionOpIfNeeded()
		c.state = ccAfterSetRange
	case ccAfterCharacterClassHyphen:
		*c.err = ErrCharacterClassRangeInvalid
	case ccAfterSetOperand:
		if !unionOpActive {
			*c.err = ErrInvalidClassSetOperation
		}
		if !processingEscape && ch == '-' {
			*c.err = ErrInvalidClassSetOperation
		} else {
			c.switchFromDefaultOpToUnionOpIfNeeded()
			processCharacter()
		}
	}
}

func (c *classSetParserDelegate) atomBuiltInCharacterClass(id BuiltInCharacterClassID, invert bool) {
	unionOpActive := c.setOp == setOpDefault || c.setOp == setOpUnion
	process := func() {
		c.computeMayContainStrings(characterClassMayContainStrings(id))
		c.d.atomCharacterClassBuiltIn(id, invert)
		c.state = ccAfterCharacterClass
	}
	switch c.state {
	case ccCachedCharacter:
		if !unionOpActive {
			*c.err = ErrInvalidClassSetOperation
			return
		}
		c.d.atomCharacterClassAtom(c.character)
		fallthrough
	case ccAfterSetRange:
		c.switchFromDefaultOpToUnionOpIfNeeded()
		fallthrough
	case ccEmpty, ccAfterCharacterClass, ccAfterSetOperator:
		process()
	case ccCachedCharacterHyphen:
		c.d.atomCharacterClassAtom(c.character)
		c.d.atomCharacterClassAtom('-')
		fallthrough
	case ccAfterCharacterClassHyphen:
		*c.err = ErrCharacterClassRangeInvalid
	case ccAfterSetOperand:
		if !unionOpActive {
			*c.err = ErrInvalidClassSetOperation
		}
		process()
	}
}

func (c *classSetParserDelegate) end() {
	switch c.state {
	case ccCachedCharacter:
		c.d.atomCharacterClassAtom(c.character)
	case ccCachedCharacterHyphen, ccAfterCharacterClassHyphen, ccAfterSetOperator:
		*c.err = ErrInvalidClassSetCharacter
	}
	if c.inverted && c.mayContainStrings {
		*c.err = ErrNegatedClassSetMayContainStrings
	}
	c.d.atomCharacterClassEnd()
}

func (p *parser) parseClassSet() {
	p.consume() // '['
	c := &classSetParserDelegate{d: p.d, err: &p.err}
	c.begin(p.tryConsume('^'))
	processCharacterNormally := func() {
		ch := p.consumeAndCheckIfValidClassSetCharacter()
		if ch == errorCodePoint {
			return
		}
		c.atomPatternCharacter(ch, true)
	}
	for !p.atEnd() {
		switch p.peek() {
		case ']':
			p.consume()
			if c.nestedClassEnd() {
				return
			}
		case '[':
			p.consume()
			c.nestedClassBegin(p.tryConsume('^'))
		case '\\':
			if !c.canTakeSetOperand() {
				p.err = ErrInvalidClassSetOperation
				return
			}
			c.processingEscape = true
			t := p.parseEscape(escapeClassSet, c)
			c.computeMayContainStrings(t == tokenSetDisjunctionMayContainStrings)
			if t == tokenSetDisjunction || t == tokenSetDisjunctionMayContainStrings {
				c.afterSetOperand()
			}
		case '-':
			state := p.index
			p.consume()
			if p.atEnd() {
				p.err = ErrCharacterClassUnmatched
				return
			}
			if p.peek() == '-' {
				p.consume()
				if p.atEnd() || p.peek() == '-' {
					p.err = ErrInvalidClassSetCharacter
					return
				}
				c.setSubtractOp()
				break
			}
			p.index = state
			processCharacterNormally()
		case '&':
			state := p.index
			p.consume()
			if p.atEnd() {
				p.err = ErrCharacterClassUnmatched
				return
			}
			if p.peek() == '&' {
				p.consume()
				if p.atEnd() || p.peek() == '&' {
					p.err = ErrInvalidClassSetCharacter
					return
				}
				c.setIntersectionOp()
				break
			}
			p.index = state
			processCharacterNormally()
		default:
			processCharacterNormally()
		}
		if p.err.hasError() {
			return
		}
	}
	p.err = ErrCharacterClassUnmatched
}

// classStringDisjunctionParserDelegate collects \q{...} strings.
type classStringDisjunctionParserDelegate struct {
	notInClass
	d                 delegate
	mayContainStrings bool
	inProgress        []rune
	strings           [][]rune
}

func (c *classStringDisjunctionParserDelegate) atomPatternCharacter(ch rune, _ bool) {
	c.inProgress = append(c.inProgress, ch)
	if len(c.inProgress) > 1 {
		c.mayContainStrings = true
	}
}

func (c *classStringDisjunctionParserDelegate) atomBuiltInCharacterClass(BuiltInCharacterClassID, bool) {
	panic("yarr: unreachable")
}

func (c *classStringDisjunctionParserDelegate) newAlternative() {
	c.strings = append(c.strings, c.inProgress)
	c.inProgress = nil
}

func (c *classStringDisjunctionParserDelegate) end() {
	c.newAlternative()
	c.d.atomClassStringDisjunction(c.strings)
}

func (p *parser) parseClassStringDisjunction() bool {
	p.consume() // '{'
	c := &classStringDisjunctionParserDelegate{d: p.d}
	for !p.atEnd() {
		switch p.peek() {
		case '}':
			p.consume()
			c.end()
			return c.mayContainStrings
		case '\\':
			p.parseEscape(escapeClassStringDisjunction, c)
		case '|':
			p.consume()
			c.newAlternative()
		case '-':
			p.consume()
			p.err = ErrInvalidClassSetCharacter
			return false
		default:
			ch := p.consumeAndCheckIfValidClassSetCharacter()
			if ch == errorCodePoint {
				return false
			}
			c.atomPatternCharacter(ch, false)
		}
		if p.err.hasError() {
			return false
		}
	}
	p.err = ErrClassStringDisjunctionUnmatched
	return false
}

// --- Parentheses, quantifiers, tokens -------------------------------------------------------

func (p *parser) countCaptures() {
	p.numCaptures++
	if p.numCaptures > maxCapturesCount {
		p.err = ErrTooManyCaptures
	}
}

func (p *parser) parseParenthesesBegin() {
	p.consume() // '('
	typ := parenSubpattern
	isNonCapturingGroup := false
	if p.tryConsume('?') {
		if p.atEnd() {
			p.err = ErrParenthesesTypeInvalid
			return
		}
		switch p.peek() {
		case ':':
			p.consume()
			p.d.atomParenthesesSubpatternBegin(false, nil)
			isNonCapturingGroup = true
		case '=':
			p.consume()
			p.d.atomParentheticalAssertionBegin(false, Forward)
			typ = parenAssertion
		case '!':
			p.consume()
			p.d.atomParentheticalAssertionBegin(true, Forward)
			typ = parenAssertion
		case '<':
			p.consume()
			name, ok := p.tryConsumeGroupName()
			if p.err.hasError() {
				break
			}
			if ok {
				if p.kIdentityEscapeSeen {
					p.err = ErrInvalidNamedBackReference
					break
				}
				if p.namedCaptureGroups.add(name) {
					p.d.atomParenthesesSubpatternBegin(true, &name)
					p.countCaptures()
				} else {
					p.err = ErrDuplicateGroupName
				}
			} else {
				if p.tryConsume('=') {
					p.d.atomParentheticalAssertionBegin(false, Backward)
					typ = parenLookbehindAssertion
					break
				}
				if p.tryConsume('!') {
					p.d.atomParentheticalAssertionBegin(true, Backward)
					typ = parenLookbehindAssertion
					break
				}
				p.err = ErrInvalidGroupName
			}
		case '-', 'i', 'm', 's':
			var set, unset Flags
			hasHitNegation := false
			isNonCapturingGroup = true
			for !p.atEnd() {
				c := p.consume()
				if c == ':' {
					break
				}
				var flag Flags
				switch c {
				case '-':
					if hasHitNegation {
						p.err = ErrInvalidRegularExpressionModifier
					}
					hasHitNegation = true
					continue
				case 'i':
					flag = FlagIgnoreCase
				case 'm':
					flag = FlagMultiline
				case 's':
					flag = FlagDotAll
				default:
					p.err = ErrParenthesesTypeInvalid
					continue
				}
				if hasHitNegation {
					if unset.Has(flag) {
						p.err = ErrInvalidRegularExpressionModifier
					}
					unset |= flag
				} else {
					if set.Has(flag) {
						p.err = ErrInvalidRegularExpressionModifier
					}
					set |= flag
				}
			}
			if p.err.hasError() {
				break
			}
			if set&unset != 0 || set == 0 && unset == 0 {
				p.err = ErrInvalidRegularExpressionModifier
			}
			p.d.atomParentheticalModifierBegin(set, unset)
		default:
			p.err = ErrParenthesesTypeInvalid
		}
	} else {
		p.d.atomParenthesesSubpatternBegin(true, nil)
		p.countCaptures()
	}
	if typ == parenSubpattern && !isNonCapturingGroup {
		p.numSubpatterns++
	}
	p.parenthesesStack = append(p.parenthesesStack, typ)
	p.namedCaptureGroups.pushParenthesis()
}

func (p *parser) parseParenthesesEnd() tokenType {
	p.consume() // ')'
	if len(p.parenthesesStack) == 0 {
		p.err = ErrParenthesesUnmatched
		return tokenNotAtom
	}
	p.d.atomParenthesesEnd()
	p.namedCaptureGroups.popParenthesis()
	typ := p.parenthesesStack[len(p.parenthesesStack)-1]
	p.parenthesesStack = p.parenthesesStack[:len(p.parenthesesStack)-1]
	if typ == parenLookbehindAssertion {
		return tokenLookbehind
	}
	if typ == parenSubpattern || p.isLegacy() {
		return tokenAtom
	}
	return tokenNotAtom
}

func (p *parser) parseQuantifier(last tokenType, min, max uint32) {
	switch last {
	case tokenAtom:
		p.d.quantifyAtom(min, max, !p.tryConsume('?'))
	case tokenLookbehind:
		p.err = ErrCantQuantifyAtom
	default:
		p.err = ErrQuantifierWithoutAtom
	}
}

func (p *parser) parseTokens() {
	last := tokenNotAtom
	for !p.atEnd() {
		switch p.peek() {
		case '|':
			p.consume()
			p.d.disjunction(forNextAlternative)
			last = tokenNotAtom
			p.namedCaptureGroups.nextAlternative()
		case '(':
			p.parseParenthesesBegin()
			last = tokenNotAtom
		case ')':
			last = p.parseParenthesesEnd()
		case '^':
			p.consume()
			p.d.assertionBOL()
			last = tokenNotAtom
		case '$':
			p.consume()
			p.d.assertionEOL()
			last = tokenNotAtom
		case '.':
			p.consume()
			p.d.atomBuiltInCharacterClass(DotClassID, false)
			last = tokenAtom
		case '[':
			if p.isUnicodeSets() {
				p.parseClassSet()
			} else {
				p.parseCharacterClass()
			}
			last = tokenAtom
		case ']', '}':
			if p.isEitherUnicode() {
				p.err = ErrBracketUnmatched
				break
			}
			p.d.atomPatternCharacter(p.consume(), false)
			last = tokenAtom
		case '\\':
			last = p.parseEscape(escapeNormal, p.d)
		case '*':
			p.consume()
			p.parseQuantifier(last, 0, quantifyInfinite)
			last = tokenNotAtom
		case '+':
			p.consume()
			p.parseQuantifier(last, 1, quantifyInfinite)
			last = tokenNotAtom
		case '?':
			p.consume()
			p.parseQuantifier(last, 0, 1)
			last = tokenNotAtom
		case '{':
			state := p.index
			p.consume()
			if p.peekIsDigit() {
				min := p.consumeNumber64()
				max := min
				if p.tryConsume(',') {
					if p.peekIsDigit() {
						max = p.consumeNumber64()
					} else {
						max = quantifyInfinite64
					}
				}
				if p.tryConsume('}') {
					switch {
					case min == quantifyInfinite64:
						p.err = ErrQuantifierTooLarge
					case min <= max:
						p.parseQuantifier(last, uint32(minU64(min, uint64(quantifyInfinite))), uint32(minU64(max, uint64(quantifyInfinite))))
					default:
						p.err = ErrQuantifierOutOfOrder
					}
					last = tokenNotAtom
					break
				}
			}
			if p.isEitherUnicode() {
				p.err = ErrQuantifierIncomplete
				break
			}
			p.index = state
			p.d.atomPatternCharacter(p.consumePossibleSurrogatePair(contextPatternCodePoint), false)
			last = tokenAtom
		default:
			p.d.atomPatternCharacter(p.consumePossibleSurrogatePair(contextPatternCodePoint), false)
			last = tokenAtom
		}
		if p.err.hasError() {
			return
		}
		if p.d.abortedDueToError() {
			p.err = p.d.abortErrorCode()
			return
		}
	}
	if len(p.parenthesesStack) > 0 {
		p.err = ErrMissingParentheses
	}
}

func minU64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func (p *parser) handleIllegalReferences() {
	shouldReparse := false
	if p.maxSeenBackReference > p.numSubpatterns {
		if p.isEitherUnicode() {
			p.err = ErrInvalidBackreference
			return
		}
		p.backReferenceLimit = p.numSubpatterns
		shouldReparse = true
	}
	if p.kIdentityEscapeSeen && !p.namedCaptureGroups.isEmpty() {
		p.err = ErrInvalidNamedBackReference
		return
	}
	if p.containsIllegalNamedForwardReference() {
		if p.isEitherUnicode() || !p.namedCaptureGroups.isEmpty() {
			p.err = ErrInvalidNamedBackReference
			return
		}
		p.isNamedForwardReferenceAllowed = false
		shouldReparse = true
	}
	if shouldReparse {
		p.resetForReparsing()
		p.parseTokens()
	}
}

func (p *parser) containsIllegalNamedForwardReference() bool {
	if len(p.forwardReferenceNames) == 0 {
		return false
	}
	if p.namedCaptureGroups.isEmpty() {
		return true
	}
	for name := range p.forwardReferenceNames {
		if !p.namedCaptureGroups.contains(name) {
			return true
		}
	}
	return false
}

func (p *parser) resetForReparsing() {
	p.d.resetForReparsing()
	p.index = 0
	p.numSubpatterns = 0
	p.maxSeenBackReference = 0
	p.kIdentityEscapeSeen = false
	p.parenthesesStack = nil
	p.namedCaptureGroups.reset()
	p.forwardReferenceNames = map[string]bool{}
}

func (p *parser) tryConsumeUnicodeEscape(ctx unicodeParseContext) rune {
	unicodeOrName := p.isEitherUnicode() || ctx == contextGroupName
	if !p.tryConsume('u') || p.atEnd() {
		if unicodeOrName {
			p.err = ErrInvalidUnicodeEscape
		}
		return errorCodePoint
	}
	if unicodeOrName && p.tryConsume('{') {
		var cp rune
		for {
			if p.atEnd() || !isASCIIHexDigit(p.peek()) {
				p.err = ErrInvalidUnicodeCodePointEscape
				return errorCodePoint
			}
			cp = cp<<4 | toASCIIHexValue(p.consume())
			if cp > maxCodePoint {
				p.err = ErrInvalidUnicodeCodePointEscape
				return errorCodePoint
			}
			if p.atEnd() || p.peek() == '}' {
				break
			}
		}
		if !p.tryConsume('}') {
			p.err = ErrInvalidUnicodeCodePointEscape
			return errorCodePoint
		}
		return cp
	}
	unit := p.tryConsumeHex(4)
	if unit == errorCodePoint {
		if unicodeOrName {
			p.err = ErrInvalidUnicodeEscape
		}
		return errorCodePoint
	}
	if isLead(unit) && unicodeOrName && p.remaining() >= 6 && p.peek() == '\\' {
		state := p.index
		p.consume()
		if p.tryConsume('u') {
			trail := p.tryConsumeHex(4)
			if trail != errorCodePoint && isTrail(trail) {
				return supplementary(unit, trail)
			}
		}
		p.index = state
	}
	return unit
}

func (p *parser) tryConsumeIdentifierCharacter() rune {
	if p.tryConsume('\\') {
		return p.tryConsumeUnicodeEscape(contextGroupName)
	}
	return p.consumePossibleSurrogatePair(contextGroupName)
}

func isIdentifierStart(ch rune) bool {
	if isASCII(ch) {
		return isASCIIAlpha(ch) || ch == '_' || ch == '$'
	}
	return ch >= 0 && unicode.IsLetter(ch)
}

func isIdentifierPart(ch rune) bool {
	if isASCII(ch) {
		return isASCIIAlpha(ch) || ch == '_' || ch == '$' || isASCIIDigit(ch)
	}
	return ch >= 0 && (unicode.IsLetter(ch) || unicode.In(ch, unicode.Mn, unicode.Mc, unicode.Nd, unicode.Pc)) || ch == 0x200C || ch == 0x200D
}

func (p *parser) tryConsumeGroupName() (string, bool) {
	if p.atEnd() {
		return "", false
	}
	state := p.index
	ch := p.tryConsumeIdentifierCharacter()
	if isIdentifierStart(ch) {
		var b []rune
		b = append(b, ch)
		for !p.atEnd() {
			ch = p.tryConsumeIdentifierCharacter()
			if ch == '>' {
				return encodeName(b), true
			}
			if !isIdentifierPart(ch) {
				break
			}
			b = append(b, ch)
		}
	}
	p.index = state
	return "", false
}

// encodeName turns a group name into a string key (WTF-8 for lone
// surrogates, which cannot occur in identifiers anyway).
func encodeName(r []rune) string { return string(r) }

func isUnicodePropertyValueExpressionChar(ch rune) bool {
	return isASCIIAlphanumeric(ch) || ch == '_' || ch == '='
}

func (p *parser) tryConsumeUnicodePropertyExpression() (BuiltInCharacterClassID, bool) {
	if p.atEnd() || !isUnicodePropertyValueExpressionChar(p.peek()) {
		p.err = ErrInvalidUnicodePropertyExpression
		return 0, false
	}
	var expr []byte
	var name string
	foundEquals := false
	errors := 0
	expr = append(expr, byte(p.consume()))
	for !p.atEnd() {
		ch := p.peek()
		if ch == '}' {
			p.consume()
			if errors > 0 {
				p.err = ErrInvalidUnicodePropertyExpression
				return 0, false
			}
			var id BuiltInCharacterClassID
			var ok bool
			if foundEquals {
				id, ok = unicodeMatchPropertyValue(name, string(expr))
			} else {
				id, ok = unicodeMatchProperty(string(expr), p.compileMode)
			}
			if !ok {
				p.err = ErrInvalidUnicodePropertyExpression
			}
			return id, ok
		}
		p.consume()
		switch {
		case ch == '=':
			if !foundEquals {
				foundEquals = true
				name = string(expr)
				expr = expr[:0]
			} else {
				errors++
			}
		case !isUnicodePropertyValueExpressionChar(ch):
			errors++
		default:
			expr = append(expr, byte(ch))
		}
	}
	p.err = ErrInvalidUnicodePropertyExpression
	return 0, false
}
