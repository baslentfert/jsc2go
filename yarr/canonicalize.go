package yarr

import (
	"bufio"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Case-insensitive matching tables (YarrCanonicalize.h,
// generateYarrCanonicalizeUnicode). The UCS2 table (non-Unicode patterns) is
// JSC's checked-in table; the Unicode table is built from CaseFolding.txt.

type canonicalizationType uint8

const (
	CanonicalizeUnique canonicalizationType = iota
	CanonicalizeSet
	CanonicalizeRangeLo
	CanonicalizeRangeHi
	CanonicalizeAlternatingAligned
	CanonicalizeAlternatingUnaligned
)

type canonicalizationRange struct {
	begin, end, value rune
	typ               canonicalizationType
}

type canonicalMode uint8

const (
	canonicalModeUCS2 canonicalMode = iota
	canonicalModeUnicode
)

var (
	unicodeTablesOnce      sync.Once
	unicodeRangeInfo       []canonicalizationRange
	unicodeCharacterSetInf [][]rune
)

func canonicalCharacterSetInfo(index rune, mode canonicalMode) []rune {
	if mode == canonicalModeUCS2 {
		return ucs2CharacterSetInfo[index]
	}
	unicodeTablesOnce.Do(buildUnicodeCanonicalizationTables)
	return unicodeCharacterSetInf[index]
}

// canonicalRangeInfoFor finds the range containing ch.
func canonicalRangeInfoFor(ch rune, mode canonicalMode) []canonicalizationRange {
	info := ucs2RangeInfo
	if mode == canonicalModeUnicode {
		unicodeTablesOnce.Do(buildUnicodeCanonicalizationTables)
		info = unicodeRangeInfo
	}
	i := sort.Search(len(info), func(i int) bool { return info[i].end >= ch })
	// Return the tail so callers can walk to following ranges (C++: ++info).
	return info[i:]
}

func getCanonicalPair(info *canonicalizationRange, ch rune) rune {
	switch info.typ {
	case CanonicalizeRangeLo:
		return ch + info.value
	case CanonicalizeRangeHi:
		return ch - info.value
	case CanonicalizeAlternatingAligned:
		return ch ^ 1
	case CanonicalizeAlternatingUnaligned:
		return ((ch - 1) ^ 1) + 1
	}
	panic("yarr: getCanonicalPair on a non-pair range")
}

func isCanonicallyUnique(ch rune, mode canonicalMode) bool {
	return canonicalRangeInfoFor(ch, mode)[0].typ == CanonicalizeUnique
}

func areCanonicallyEquivalent(a, b rune, mode canonicalMode) bool {
	info := &canonicalRangeInfoFor(a, mode)[0]
	switch info.typ {
	case CanonicalizeUnique:
		return a == b
	case CanonicalizeSet:
		for _, c := range canonicalCharacterSetInfo(info.value, mode) {
			if c == b {
				return true
			}
		}
		return false
	case CanonicalizeRangeLo:
		return a == b || a+info.value == b
	case CanonicalizeRangeHi:
		return a == b || a-info.value == b
	case CanonicalizeAlternatingAligned:
		return a|1 == b|1
	case CanonicalizeAlternatingUnaligned:
		return (a-1)|1 == (b-1)|1
	}
	return false
}

// buildUnicodeCanonicalizationTables ports generateYarrCanonicalizeUnicode:
// characters are grouped by their simple case folding (C and S lines).
func buildUnicodeCanonicalizationTables() {
	groups := make(map[rune][]rune)
	seen := make(map[rune]bool)
	sc := bufio.NewScanner(openUCD("CaseFolding.txt"))
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		fields := strings.Split(line, ";")
		if len(fields) < 3 {
			continue
		}
		status := strings.TrimSpace(fields[1])
		if status != "C" && status != "S" {
			continue
		}
		code := parseHex(fields[0])
		mapping := parseHex(fields[2])
		seen[code] = true
		groups[mapping] = append(groups[mapping], code)
	}
	for i := rune(0); i <= maxCodePoint; i++ {
		if !seen[i] {
			groups[i] = append(groups[i], i)
		}
	}

	type info struct {
		typ   canonicalizationType
		value rune
	}
	typeInfo := make([]info, maxCodePoint+1)
	mappings := make([]rune, 0, len(groups))
	for m := range groups {
		mappings = append(mappings, m)
	}
	sort.Slice(mappings, func(i, j int) bool { return mappings[i] < mappings[j] })
	var sets [][]rune
	for _, m := range mappings {
		chars := groups[m]
		if len(chars) == 1 {
			typeInfo[chars[0]] = info{CanonicalizeUnique, 0}
			continue
		}
		sort.Slice(chars, func(i, j int) bool { return chars[i] < chars[j] })
		if len(chars) > 2 {
			for _, c := range chars {
				typeInfo[c] = info{CanonicalizeSet, rune(len(sets))}
			}
			sets = append(sets, chars)
			continue
		}
		lo, hi := chars[0], chars[1]
		delta := hi - lo
		if delta == 1 {
			t := CanonicalizeAlternatingAligned
			if lo&1 != 0 {
				t = CanonicalizeAlternatingUnaligned
			}
			typeInfo[lo] = info{t, 0}
			typeInfo[hi] = info{t, 0}
		} else {
			typeInfo[lo] = info{CanonicalizeRangeLo, delta}
			typeInfo[hi] = info{CanonicalizeRangeHi, delta}
		}
	}
	var ranges []canonicalizationRange
	for end := rune(0); end <= maxCodePoint; end++ {
		begin := end
		t := typeInfo[end]
		for end < maxCodePoint && typeInfo[end+1] == t {
			end++
		}
		ranges = append(ranges, canonicalizationRange{begin, end, t.value, t.typ})
	}
	unicodeRangeInfo = ranges
	unicodeCharacterSetInf = sets
}

func parseHex(s string) rune {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 16, 32)
	if err != nil {
		panic("yarr: bad hex in UCD data: " + s)
	}
	return rune(v)
}
