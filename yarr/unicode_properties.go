package yarr

import (
	"bufio"
	"compress/gzip"
	"embed"
	"io"
	"sort"
	"strings"
	"sync"
)

// Unicode property escapes (YarrUnicodeProperties.cpp and the generator
// generateYarrUnicodePropertyTables.py). JSC generates C++ tables at build
// time from the UCD; here the same UCD files (gzipped) are embedded and
// parsed on first use.

//go:embed ucd/*.gz
var ucdFiles embed.FS

func openUCD(name string) io.Reader {
	f, err := ucdFiles.Open("ucd/" + name + ".gz")
	if err != nil {
		panic("yarr: missing UCD file " + name)
	}
	r, err := gzip.NewReader(f)
	if err != nil {
		panic("yarr: corrupt UCD file " + name)
	}
	return r
}

var supportedBinaryProperties = []string{
	"Alphabetic", "Any", "ASCII", "ASCII_Hex_Digit", "Assigned", "Bidi_Control", "Bidi_Mirrored", "Case_Ignorable",
	"Cased", "Changes_When_Casefolded", "Changes_When_Casemapped", "Changes_When_Lowercased", "Changes_When_NFKC_Casefolded",
	"Changes_When_Titlecased", "Changes_When_Uppercased", "Dash", "Default_Ignorable_Code_Point", "Deprecated",
	"Diacritic", "Emoji", "Emoji_Component", "Emoji_Modifier_Base", "Emoji_Modifier", "Emoji_Presentation",
	"Extended_Pictographic", "Extender", "Grapheme_Base", "Grapheme_Extend", "Hex_Digit", "ID_Continue", "ID_Start",
	"Ideographic", "IDS_Binary_Operator", "IDS_Trinary_Operator", "Join_Control", "Logical_Order_Exception", "Lowercase",
	"Math", "Noncharacter_Code_Point", "Pattern_Syntax", "Pattern_White_Space", "Quotation_Mark", "Radical",
	"Regional_Indicator", "Sentence_Terminal", "Soft_Dotted", "Terminal_Punctuation", "Unified_Ideograph", "Uppercase",
	"Variation_Selector", "White_Space", "XID_Continue", "XID_Start",
}

var supportedSequenceProperties = []string{
	"Basic_Emoji", "Emoji_Keycap_Sequence", "RGI_Emoji", "RGI_Emoji_Flag_Sequence", "RGI_Emoji_Modifier_Sequence",
	"RGI_Emoji_Tag_Sequence", "RGI_Emoji_ZWJ_Sequence",
}

// propertyData is one property's set of code points and strings.
type propertyData struct {
	ranges  []CharacterRange // possibly unsorted until finish()
	strings [][]rune
	index   int
}

func (p *propertyData) addRange(lo, hi rune) { p.ranges = append(p.ranges, CharacterRange{lo, hi}) }

// finish sorts and merges the ranges.
func (p *propertyData) finish() {
	sort.Slice(p.ranges, func(i, j int) bool { return p.ranges[i].Begin < p.ranges[j].Begin })
	merged := p.ranges[:0]
	for _, r := range p.ranges {
		if n := len(merged); n > 0 && r.Begin <= merged[n-1].End+1 {
			if r.End > merged[n-1].End {
				merged[n-1].End = r.End
			}
			continue
		}
		merged = append(merged, r)
	}
	p.ranges = merged
}

func (p *propertyData) remove(cp rune) {
	for i, r := range p.ranges {
		if cp < r.Begin || cp > r.End {
			continue
		}
		switch {
		case r.Begin == r.End:
			p.ranges = append(p.ranges[:i], p.ranges[i+1:]...)
		case cp == r.Begin:
			p.ranges[i].Begin++
		case cp == r.End:
			p.ranges[i].End--
		default:
			p.ranges = append(p.ranges[:i+1], p.ranges[i:]...)
			p.ranges[i].End = cp - 1
			p.ranges[i+1].Begin = cp + 1
		}
		return
	}
}

func (p *propertyData) clone() *propertyData {
	c := &propertyData{ranges: append([]CharacterRange(nil), p.ranges...)}
	return c
}

type unicodeProperties struct {
	all             []*propertyData
	generalCategory map[string]int
	binary          map[string]int
	script          map[string]int
	scriptExtension map[string]int
	sequence        map[string]int
}

var (
	propertiesOnce sync.Once
	properties     *unicodeProperties
)

func (u *unicodeProperties) newProperty() *propertyData {
	p := &propertyData{index: len(u.all)}
	u.all = append(u.all, p)
	return p
}

// ucdLines calls f with the semicolon-separated, trimmed fields of each
// data line.
func ucdLines(name string, f func(fields []string)) {
	sc := bufio.NewScanner(openUCD(name))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ";")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		f(fields)
	}
}

func parseCodePoints(s string) (rune, rune) {
	if lo, hi, ok := strings.Cut(s, ".."); ok {
		return parseHex(lo), parseHex(hi)
	}
	c := parseHex(s)
	return c, c
}

func loadUnicodeProperties() {
	u := &unicodeProperties{
		generalCategory: map[string]int{},
		binary:          map[string]int{},
		script:          map[string]int{},
		scriptExtension: map[string]int{},
		sequence:        map[string]int{},
	}

	// Aliases.
	globalAliases := map[string][]string{}
	ucdLines("PropertyAliases.txt", func(f []string) {
		if len(f) < 2 {
			return
		}
		aliases := []string{f[0]}
		aliases = append(aliases, f[2:]...)
		globalAliases[f[1]] = aliases
	})
	gcAliases, gcForAlias := map[string][]string{}, map[string]string{}
	scAliases, scForAlias := map[string][]string{}, map[string]string{}
	ucdLines("PropertyValueAliases.txt", func(f []string) {
		if len(f) < 3 {
			return
		}
		var aliases map[string][]string
		var reverse map[string]string
		switch f[0] {
		case "gc":
			aliases, reverse = gcAliases, gcForAlias
		case "sc":
			aliases, reverse = scAliases, scForAlias
		default:
			return
		}
		all := []string{f[1]}
		all = append(all, f[3:]...)
		aliases[f[2]] = all
		reverse[f[1]] = f[2]
	})
	register := func(m map[string]int, name string, aliases []string, p *propertyData) {
		m[name] = p.index
		for _, a := range aliases {
			if a != "" {
				m[a] = p.index
			}
		}
	}

	// General_Category (plus Any, ASCII and Assigned).
	byCategory := map[string]*propertyData{}
	special := func(name string, lo, hi rune) *propertyData {
		p := u.newProperty()
		if lo <= hi {
			p.addRange(lo, hi)
		}
		byCategory[name] = p
		register(u.generalCategory, name, nil, p)
		return p
	}
	special("Any", 0, maxCodePoint)
	special("ASCII", 0, 0x7F)
	assigned := special("Assigned", 1, 0)
	group := func(alias string) (*propertyData, *propertyData) {
		category := gcForAlias[alias]
		groupName := gcForAlias[alias[:1]]
		get := func(name string) *propertyData {
			if p, ok := byCategory[name]; ok {
				return p
			}
			p := u.newProperty()
			byCategory[name] = p
			register(u.generalCategory, name, gcAliases[name], p)
			return p
		}
		return get(groupName), get(category)
	}
	_, casedLetter := group("LC")
	last := rune(-1)
	addCodePoints := func(alias string, lo, hi rune) {
		if lo-last > 1 {
			g, c := group("Cn")
			g.addRange(last+1, lo-1)
			c.addRange(last+1, lo-1)
		}
		g, c := group(alias)
		g.addRange(lo, hi)
		c.addRange(lo, hi)
		if alias == "Ll" || alias == "Lt" || alias == "Lu" {
			casedLetter.addRange(lo, hi)
		}
		assigned.addRange(lo, hi)
		last = hi
	}
	var firstOfRange rune = -1
	ucdLines("UnicodeData.txt", func(f []string) {
		cp := parseHex(f[0])
		desc, alias := f[1], f[2]
		if firstOfRange >= 0 {
			if strings.HasSuffix(desc, "Last>") {
				addCodePoints(alias, firstOfRange, cp)
			}
			firstOfRange = -1
			return
		}
		if strings.HasSuffix(desc, "First>") {
			firstOfRange = cp
			return
		}
		addCodePoints(alias, cp, cp)
	})
	if last < maxCodePoint {
		g, c := group("Cn")
		g.addRange(last+1, maxCodePoint)
		c.addRange(last+1, maxCodePoint)
	}

	// Binary properties.
	supported := map[string]bool{}
	for _, name := range supportedBinaryProperties {
		supported[name] = true
	}
	binary := map[string]*propertyData{}
	for _, file := range []string{"DerivedBinaryProperties.txt", "DerivedCoreProperties.txt",
		"DerivedNormalizationProps.txt", "PropList.txt", "emoji-data.txt"} {
		ucdLines(file, func(f []string) {
			if len(f) < 2 || !supported[f[1]] {
				return
			}
			p, ok := binary[f[1]]
			if !ok {
				p = u.newProperty()
				binary[f[1]] = p
				register(u.binary, f[1], globalAliases[f[1]], p)
			}
			p.addRange(parseCodePoints(f[0]))
		})
	}

	// Scripts and Script_Extensions.
	scripts := map[string]*propertyData{}
	unknown := u.newProperty()
	var assignedRanges []CharacterRange
	ucdLines("Scripts.txt", func(f []string) {
		p, ok := scripts[f[1]]
		if !ok {
			p = u.newProperty()
			scripts[f[1]] = p
		}
		lo, hi := parseCodePoints(f[0])
		p.addRange(lo, hi)
		assignedRanges = append(assignedRanges, CharacterRange{lo, hi})
	})
	sort.Slice(assignedRanges, func(i, j int) bool { return assignedRanges[i].Begin < assignedRanges[j].Begin })
	lastAssigned := rune(0)
	for _, r := range assignedRanges {
		if r.Begin-lastAssigned > 1 {
			unknown.addRange(lastAssigned+1, r.Begin-1)
		}
		lastAssigned = r.End
	}
	if lastAssigned < maxCodePoint {
		unknown.addRange(lastAssigned+1, maxCodePoint)
	}
	scripts["Unknown"] = unknown
	for name, p := range scripts {
		p.finish()
		register(u.script, name, scAliases[name], p)
	}

	extensions := map[string]*propertyData{}
	copyScript := func(name string) *propertyData {
		var c *propertyData
		if p, ok := scripts[name]; ok {
			c = p.clone()
		} else {
			c = &propertyData{}
		}
		c.index = len(u.all)
		u.all = append(u.all, c)
		extensions[name] = c
		return c
	}
	common := copyScript("Common")
	inherited := copyScript("Inherited")
	ucdLines("ScriptExtensions.txt", func(f []string) {
		lo, hi := parseCodePoints(f[0])
		for _, alias := range strings.Fields(f[1]) {
			name := scForAlias[alias]
			p, ok := extensions[name]
			if !ok {
				p = copyScript(name)
			}
			p.addRange(lo, hi)
		}
		for cp := lo; cp <= hi; cp++ {
			common.remove(cp)
			inherited.remove(cp)
		}
	})
	for name, p := range scripts {
		if _, ok := extensions[name]; !ok {
			extensions[name] = p
		}
	}
	for name, p := range extensions {
		register(u.scriptExtension, name, scAliases[name], p)
	}

	// Sequence properties.
	seqSupported := map[string]bool{}
	for _, name := range supportedSequenceProperties {
		seqSupported[name] = true
	}
	rgi := u.newProperty()
	register(u.sequence, "RGI_Emoji", nil, rgi)
	sequences := map[string]*propertyData{}
	for _, file := range []string{"emoji-sequences.txt", "emoji-zwj-sequences.txt"} {
		ucdLines(file, func(f []string) {
			if len(f) < 2 || !seqSupported[f[1]] {
				return
			}
			p, ok := sequences[f[1]]
			if !ok {
				p = u.newProperty()
				sequences[f[1]] = p
				register(u.sequence, f[1], nil, p)
			}
			if strings.Contains(f[0], " ") {
				var s []rune
				for _, h := range strings.Fields(f[0]) {
					s = append(s, parseHex(h))
				}
				p.strings = append(p.strings, s)
				rgi.strings = append(rgi.strings, s)
				return
			}
			lo, hi := parseCodePoints(f[0])
			p.addRange(lo, hi)
			rgi.addRange(lo, hi)
		})
	}

	for _, p := range u.all {
		p.finish()
		if len(p.strings) > 0 {
			sortUTF32Strings(p.strings)
		}
	}
	properties = u
}

func unicodeProps() *unicodeProperties {
	propertiesOnce.Do(loadUnicodeProperties)
	return properties
}

// unicodeMatchPropertyValue resolves \p{Name=Value}.
func unicodeMatchPropertyValue(name, value string) (BuiltInCharacterClassID, bool) {
	u := unicodeProps()
	var m map[string]int
	switch name {
	case "Script", "sc":
		m = u.script
	case "Script_Extensions", "scx":
		m = u.scriptExtension
	case "General_Category", "gc":
		m = u.generalCategory
	default:
		return 0, false
	}
	i, ok := m[value]
	return BaseUnicodePropertyID + BuiltInCharacterClassID(i), ok
}

// unicodeMatchProperty resolves \p{Value} (a binary property or a general
// category; sequence properties only with the v flag).
func unicodeMatchProperty(value string, mode CompileMode) (BuiltInCharacterClassID, bool) {
	u := unicodeProps()
	i, ok := u.binary[value]
	if !ok {
		i, ok = u.generalCategory[value]
	}
	if !ok && mode == CompileModeUnicodeSets {
		i, ok = u.sequence[value]
	}
	return BaseUnicodePropertyID + BuiltInCharacterClassID(i), ok
}

func characterClassMayContainStrings(id BuiltInCharacterClassID) bool {
	if id < BaseUnicodePropertyID {
		return false
	}
	u := unicodeProps()
	i := int(id - BaseUnicodePropertyID)
	return i < len(u.all) && len(u.all[i].strings) > 0
}

// createUnicodeCharacterClassFor builds the character class of a property.
func createUnicodeCharacterClassFor(id BuiltInCharacterClassID) *CharacterClass {
	p := unicodeProps().all[id-BaseUnicodePropertyID]
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
	for _, s := range p.strings {
		cc.Strings = append(cc.Strings, append([]rune(nil), s...))
	}
	cc.finish()
	cc.inCanonicalForm = len(cc.Strings) > 0
	return cc
}
