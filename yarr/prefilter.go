package yarr

import (
	"bytes"
	"unsafe"
)

// Prefilters: what the tree matcher can check before trying a pattern or
// an alternative at a position (the checks JSC's Yarr JIT and Irregexp
// make before running the matcher proper).
//
//   - The units a match of an alternative can start with: alternatives that
//     cannot start at a position are skipped without running them, and
//     Exec skips positions no top-level alternative can start at.
//   - A literal every match contains: if the input lacks it there is no
//     match, and a match must start shortly before an occurrence when what
//     precedes the literal has a bounded width.

// prefiltersEnabled is false only in tests comparing with and without them.
var prefiltersEnabled = true

// firstSet is a set of code units: the Latin-1 ones exactly, the others
// all or none.
type firstSet struct {
	bits [4]uint64
	high bool // units from 256 up may start a match
}

func (f *firstSet) addRune(c rune) {
	if c < 256 {
		f.bits[c>>6] |= 1 << (c & 63)
	} else {
		f.high = true
	}
}

func (f *firstSet) union(o *firstSet) {
	for i := range f.bits {
		f.bits[i] |= o.bits[i]
	}
	f.high = f.high || o.high
}

func (f *firstSet) has(u uint16) bool {
	if u < 256 {
		return f.bits[u>>6]&(1<<(u&63)) != 0
	}
	return f.high
}

// sequenceFirst returns the units a match of the forward terms ts can start
// with; ok is false if ts can match empty or is not analyzed.
func sequenceFirst(ts []patternTerm) (set firstSet, ok bool) {
	for i := range ts {
		t := &ts[i]
		if t.matchDirection != Forward {
			return set, false
		}
		var f firstSet
		switch t.typ {
		case termAssertionBOL, termAssertionEOL, termAssertionBOI, termAssertionEOI, termAssertionWordBoundary,
			termNumberedForwardReference, termNamedForwardReference:
			continue // zero width
		case termPatternCharacter:
			c := t.patternCharacter
			if c > 0xFFFF {
				f.high = true // a lead surrogate
			} else {
				f.addRune(c)
			}
			if t.ignoreCase() && isASCIIAlpha(c) {
				f.addRune(c ^ 0x20)
			}
		case termCharacterClass:
			cc := t.characterClass
			if len(cc.Strings) > 0 {
				return set, false
			}
			if t.invert {
				for j := range f.bits {
					f.bits[j] = ^cc.latin1[j]
				}
				f.high = true
			} else if cc.anyCharacter {
				f.bits = [4]uint64{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}
				f.high = true
			} else {
				f.bits = cc.latin1
				f.high = len(cc.Matches32) > 0 || len(cc.Ranges32) > 0
			}
		case termParenthesesSubpattern:
			d := t.parentheses.disjunction
			if d == nil {
				return set, false
			}
			for _, a := range d.alternatives {
				af, ok := sequenceFirst(a.terms)
				if !ok {
					return set, false
				}
				f.union(&af)
			}
		default:
			return set, false
		}
		set.union(&f)
		if t.quantityMinCount > 0 {
			return set, true
		}
	}
	return set, false // can match empty
}

// computePrefilters sets the alternatives' first sets, the pattern's start
// set and its required literal.
func (p *Pattern) computePrefilters() {
	var walk func(d *patternDisjunction)
	walk = func(d *patternDisjunction) {
		for _, a := range d.alternatives {
			if a.direction == Forward {
				a.first, a.firstOK = sequenceFirst(a.terms)
			}
			for i := range a.terms {
				if d := a.terms[i].parentheses.disjunction; d != nil {
					walk(d)
				}
			}
		}
	}
	walk(p.body)

	p.startOK = len(p.body.alternatives) > 0
	for _, a := range p.body.alternatives {
		if !a.firstOK {
			p.startOK = false
			break
		}
		p.startSet.union(&a.first)
	}

	if len(p.body.alternatives) == 1 {
		p.requiredLiteral, p.literalPrefixMax = requiredLiteral(p.body.alternatives[0].terms)
	}
	p.sets = p.setAlternatives()
}

// program returns p's compiled program once p has been executed before,
// or nil.
func (p *Pattern) program() *vmProgram {
	if prog := p.prog.Load(); prog != nil || p.noProg.Load() {
		return prog
	}
	if p.execs.Add(1) < 2 {
		return nil
	}
	prog := compileProgram(p)
	if prog == nil {
		p.noProg.Store(true)
		return nil
	}
	p.prog.Store(prog)
	return prog
}

// setAlternatives returns the pattern as alternatives of sequences of
// exact code unit sets, if it is only that: each term one character or
// character class, exactly once, without groups, assertions or unicode
// mode (/agggtaaa|tttaccct/i, /[cgt]gggtaaa|tttaccc[acg]/i in regex-dna).
// Such a pattern needs no backtracking: at each position the alternatives
// are compared in order, as JSC's Yarr JIT compiles them to straight code.
func (p *Pattern) setAlternatives() [][]firstSet {
	if p.eitherUnicode() || p.NumSubpatterns != 0 || len(p.body.alternatives) == 0 || len(p.body.alternatives) > 16 {
		return nil
	}
	sets := make([][]firstSet, 0, len(p.body.alternatives))
	for _, a := range p.body.alternatives {
		if a.direction != Forward || len(a.terms) > 64 {
			return nil
		}
		seq := make([]firstSet, 0, len(a.terms))
		for i := range a.terms {
			t := &a.terms[i]
			if t.matchDirection != Forward || t.quantityType != QuantifierFixedCount ||
				t.quantityMinCount != 1 || t.quantityMaxCount != 1 {
				return nil
			}
			var f firstSet
			switch t.typ {
			case termPatternCharacter:
				c := t.patternCharacter
				if c >= 256 {
					return nil
				}
				f.addRune(c)
				if t.ignoreCase() && isASCIIAlpha(c) {
					f.addRune(c ^ 0x20) // as atom folds
				}
			case termCharacterClass:
				cc := t.characterClass
				if len(cc.Strings) > 0 || len(cc.Matches32) > 0 || len(cc.Ranges32) > 0 {
					return nil // units from 256 up only partly in the class
				}
				switch {
				case cc.anyCharacter && !t.invert:
					f.bits = [4]uint64{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}
					f.high = true
				case cc.anyCharacter:
					// matches nothing: the empty set
				case t.invert:
					for j := range f.bits {
						f.bits[j] = ^cc.latin1[j]
					}
					f.high = true
				default:
					f.bits = cc.latin1
				}
			default:
				return nil
			}
			seq = append(seq, f)
		}
		sets = append(sets, seq)
	}
	return sets
}

// execSets is Exec for a pattern with sets.
func execSets[T byte | uint16](p *Pattern, input []T, start int) []int {
	anchored := p.sticky()
	for pos := start; pos <= len(input); pos++ {
		if p.startOK && !anchored {
			for pos < len(input) && !p.startSet.has(uint16(input[pos])) {
				pos++
			}
		}
	alts:
		for _, seq := range p.sets {
			if len(seq) > len(input)-pos {
				continue
			}
			for i := range seq {
				if !seq[i].has(uint16(input[pos+i])) {
					continue alts
				}
			}
			return []int{pos, pos + len(seq)}
		}
		if anchored {
			break
		}
	}
	return nil
}

// requiredLiteral returns the longest run of plain characters among terms
// (which every match of them contains) and the most units that can precede
// it in a match (-1 if unbounded).
func requiredLiteral(ts []patternTerm) ([]uint16, int) {
	var best, run []uint16
	bestPrefix, prefix, runPrefix := -1, 0, 0
	for i := range ts {
		t := &ts[i]
		if t.matchDirection != Forward {
			return nil, -1
		}
		if t.typ == termPatternCharacter && !t.ignoreCase() && t.quantityType == QuantifierFixedCount &&
			t.patternCharacter <= 0xFFFF && t.quantityMaxCount <= 16 {
			if len(run) == 0 {
				runPrefix = prefix
			}
			for range t.quantityMaxCount {
				run = append(run, uint16(t.patternCharacter))
			}
			if len(run) > len(best) {
				best, bestPrefix = append([]uint16(nil), run...), runPrefix
			}
		} else {
			run = run[:0]
		}
		if prefix >= 0 {
			if w := t.maxWidth(); w < 0 {
				prefix = -1
			} else {
				prefix += w
			}
		}
	}
	if len(best) < 2 {
		return nil, -1
	}
	return best, bestPrefix
}

// maxWidth is the most units a term matches, or -1 if unbounded.
func (t *patternTerm) maxWidth() int {
	if t.quantityMaxCount == quantifyInfinite {
		return -1
	}
	n := int(t.quantityMaxCount)
	switch t.typ {
	case termAssertionBOL, termAssertionEOL, termAssertionBOI, termAssertionEOI, termAssertionWordBoundary,
		termParentheticalAssertion, termNumberedForwardReference, termNamedForwardReference:
		return 0
	case termPatternCharacter, termCharacterClass:
		return 2 * n // a non-BMP character is two units
	case termParenthesesSubpattern:
		d := t.parentheses.disjunction
		if d == nil {
			return -1
		}
		most := 0
		for _, a := range d.alternatives {
			w := 0
			for i := range a.terms {
				tw := a.terms[i].maxWidth()
				if tw < 0 {
					return -1
				}
				w += tw
			}
			most = max(most, w)
		}
		return most * n
	}
	return -1
}

// indexLiteral returns the first index at or after from where lit occurs in
// input, or -1.
func indexLiteral[T byte | uint16](input []T, lit []uint16, from int) int {
	if from > len(input) {
		return -1
	}
	var zero T
	if unsafe.Sizeof(zero) == 1 {
		b := make([]byte, 0, 16)
		for _, u := range lit {
			if u > 0xFF {
				return -1 // Latin-1 input cannot contain it
			}
			b = append(b, byte(u))
		}
		in := unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(input))), len(input))
		if i := bytes.Index(in[from:], b); i >= 0 {
			return from + i
		}
		return -1
	}
	n := len(lit)
outer:
	for i := from; i+n <= len(input); i++ {
		if uint16(input[i]) != lit[0] {
			continue
		}
		for j := 1; j < n; j++ {
			if uint16(input[i+j]) != lit[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}
