package vm

import (
	"math"
	"strconv"
	"strings"
	"unsafe"
)

// The JSON object (JSONObject.cpp, LiteralParser.cpp).

func (r *Realm) initJSON() {
	vm := r.vm
	j := r.NewObject()
	r.GlobalObject.putDirect(StrKey("JSON"), ObjectValue(j), BuiltinFlags)
	r.toStringTag(j, "JSON")
	r.method(j, "parse", 2, func(c *Call) Value {
		text := vm.ToString(c.Arg(0))
		reviver := c.Arg(1)
		v, rec := vm.parseJSON(c.Realm(), text, reviver.IsCallable(), true)
		if !reviver.IsCallable() {
			return v
		}
		root := c.Realm().NewObject()
		root.CreateDataPropertyOrThrow(StrKey(""), v)
		return vm.internalizeJSONProperty(root, StrKey(""), reviver, rec)
	})
	r.method(j, "stringify", 3, func(c *Call) Value {
		s, ok := vm.jsonStringify(c.Arg(0), c.Arg(1), c.Arg(2))
		if !ok {
			return Undefined
		}
		return StringValue(s)
	})
	r.method(j, "rawJSON", 1, func(c *Call) Value {
		s := vm.ToString(c.Arg(0))
		u := s.UTF16()
		if len(u) == 0 || isJSONSpace(u[0]) || isJSONSpace(u[len(u)-1]) {
			vm.throwSyntaxError("Invalid value for JSON.rawJSON")
		}
		v, _ := vm.parseJSON(c.Realm(), s, false, false)
		if v.IsObject() {
			vm.throwSyntaxError("Invalid value for JSON.rawJSON")
		}
		o := c.Realm().newObject(nil)
		o.internal = &rawJSON{s}
		o.CreateDataPropertyOrThrow(StrKey("rawJSON"), StringValue(s))
		o.SetIntegrityLevel(Frozen)
		return ObjectValue(o)
	})
	r.method(j, "isRawJSON", 1, func(c *Call) Value {
		if o := c.Arg(0).AsObject(); o != nil {
			_, ok := o.internal.(*rawJSON)
			return Bool(ok)
		}
		return False
	})
}

type rawJSON struct{ text *String }

func isJSONSpace(c uint16) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// jsonParser parses JSON text (LiteralParser), over the bytes of an ASCII
// string (the common case, read in place) or over UTF-16 code units.
type jsonParser[T byte | uint16] struct {
	vm    *VM
	realm *Realm
	s     []T
	i     int
	// text is the string s reads when T is byte.
	text string

	// With records set, parseValue leaves the JSON Parse Record of the
	// value in rec (json-parse-with-source).
	records bool
	rec     *jsonRecord

	// keys caches recent property names (LiteralParser's identifier
	// cache), so that the names repeated by every object of an array are
	// not interned again.
	keys *[64]jsonKey
}

type jsonKey struct {
	name string
	key  PropertyKey
}

// parseJSON parses text as JSON. With whole, white space around the value
// is skipped; either way nothing else may follow it.
func (vm *VM) parseJSON(r *Realm, text *String, records, whole bool) (Value, *jsonRecord) {
	if text.IsASCII() {
		s := unsafe.Slice(unsafe.StringData(text.asciiText()), text.length)
		p := &jsonParser[byte]{vm: vm, realm: r, s: s, text: text.asciiText(), records: records}
		return p.parseText(whole)
	}
	p := &jsonParser[uint16]{vm: vm, realm: r, s: text.UTF16(), records: records}
	return p.parseText(whole)
}

func (p *jsonParser[T]) parseText(whole bool) (Value, *jsonRecord) {
	if whole {
		p.skipSpace()
	}
	v := p.parseValue()
	if whole {
		p.skipSpace()
	}
	if p.i < len(p.s) {
		p.fail()
	}
	return v, p.rec
}

// jsonRecord is a JSON Parse Record: the parsed value plus its source text
// (primitives) or the records of its elements or entries.
type jsonRecord struct {
	value    Value
	source   *String
	elements []*jsonRecord
	entries  map[PropertyKey]*jsonRecord
}

func (p *jsonParser[T]) fail() {
	p.vm.throwSyntaxError("JSON Parse error: Unexpected token")
}

func (p *jsonParser[T]) skipSpace() {
	for p.i < len(p.s) {
		if c := p.s[p.i]; c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return
		}
		p.i++
	}
}

func (p *jsonParser[T]) expect(c T) {
	if p.i >= len(p.s) || p.s[p.i] != c {
		p.fail()
	}
	p.i++
}

func (p *jsonParser[T]) literal(word string) {
	for j := 0; j < len(word); j++ {
		p.expect(T(word[j]))
	}
}

// str returns the source text from start to end as a string (a copy).
func (p *jsonParser[T]) str(start, end int) *String {
	if p.text != "" {
		if end-start == 1 {
			return smallStrings[p.text[start]]
		}
		return &String{data: unsafe.Pointer(unsafe.StringData(strings.Clone(p.text[start:end]))), isASCII: true, length: end - start}
	}
	u := make([]uint16, end-start)
	for j := range u {
		u[j] = uint16(p.s[start+j])
	}
	return NewStringUTF16(u)
}

func (p *jsonParser[T]) parseValue() Value {
	start := p.i
	v, rec := p.parseValueRec()
	if p.records {
		if rec == nil {
			rec = &jsonRecord{source: p.str(start, p.i)}
		}
		rec.value = v
		p.rec = rec
	}
	return v
}

func (p *jsonParser[T]) parseValueRec() (Value, *jsonRecord) {
	var rec *jsonRecord
	if p.records {
		rec = &jsonRecord{}
	}
	if p.i >= len(p.s) {
		p.fail()
	}
	switch c := p.s[p.i]; {
	case c == '{':
		p.i++
		o := p.realm.NewObject()
		p.skipSpace()
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++
			return ObjectValue(o), rec
		}
		for {
			p.skipSpace()
			if p.i >= len(p.s) || p.s[p.i] != '"' {
				p.fail()
			}
			k := p.parseKey()
			p.skipSpace()
			p.expect(':')
			p.skipSpace()
			v := p.parseValue()
			o.CreateDataProperty(k, v)
			if rec != nil {
				if rec.entries == nil {
					rec.entries = map[PropertyKey]*jsonRecord{}
				}
				rec.entries[k] = p.rec
			}
			p.skipSpace()
			if p.i < len(p.s) && p.s[p.i] == ',' {
				p.i++
				continue
			}
			p.expect('}')
			return ObjectValue(o), rec
		}
	case c == '[':
		p.i++
		var values []Value
		p.skipSpace()
		if p.i < len(p.s) && p.s[p.i] == ']' {
			p.i++
			return ObjectValue(p.realm.NewArray()), rec
		}
		for {
			p.skipSpace()
			values = append(values, p.parseValue())
			if rec != nil {
				rec.elements = append(rec.elements, p.rec)
			}
			p.skipSpace()
			if p.i < len(p.s) && p.s[p.i] == ',' {
				p.i++
				continue
			}
			p.expect(']')
			return ObjectValue(p.realm.NewArray(values...)), rec
		}
	case c == '"':
		return StringValue(p.parseString()), nil
	case c == 't':
		p.literal("true")
		return True, nil
	case c == 'f':
		p.literal("false")
		return False, nil
	case c == 'n':
		p.literal("null")
		return Null, nil
	case c == '-' || c >= '0' && c <= '9':
		return p.parseNumber(), nil
	}
	p.fail()
	return Undefined, nil
}

// scanPlain advances over the characters of a string up to its closing
// quote; it stops early (returning false) at an escape or a control
// character.
func (p *jsonParser[T]) scanPlain() bool {
	for p.i < len(p.s) {
		switch c := p.s[p.i]; {
		case c == '"':
			return true
		case c == '\\' || c < 0x20:
			return false
		}
		p.i++
	}
	return false
}

// parseKey parses a property name.
func (p *jsonParser[T]) parseKey() PropertyKey {
	if p.text != "" {
		start := p.i + 1
		p.i = start
		if p.scanPlain() {
			name := p.text[start:p.i]
			p.i++
			if p.keys == nil {
				p.keys = new([64]jsonKey)
			}
			h := len(name)
			if h > 0 {
				h = h*31 + int(name[0])*7 + int(name[len(name)-1])
			}
			e := &p.keys[h&63]
			if e.name != name || e.key == (PropertyKey{}) {
				e.name, e.key = name, StrKey(name)
			}
			return e.key
		}
		p.i = start - 1
	}
	return p.parseString().Key()
}

func (p *jsonParser[T]) parseString() *String {
	p.i++ // opening quote
	start := p.i
	if p.scanPlain() {
		s := p.str(start, p.i)
		p.i++
		return s
	}
	var b stringBuilder
	for j := start; j < p.i; j++ {
		b.WriteUnit(uint16(p.s[j]))
	}
	for {
		if p.i >= len(p.s) {
			p.fail()
		}
		c := uint16(p.s[p.i])
		p.i++
		switch {
		case c == '"':
			return b.String()
		case c < 0x20:
			p.fail()
		case c == '\\':
			if p.i >= len(p.s) {
				p.fail()
			}
			e := uint16(p.s[p.i])
			p.i++
			switch e {
			case '"', '\\', '/':
				b.WriteUnit(e)
			case 'b':
				b.WriteUnit('\b')
			case 'f':
				b.WriteUnit('\f')
			case 'n':
				b.WriteUnit('\n')
			case 'r':
				b.WriteUnit('\r')
			case 't':
				b.WriteUnit('\t')
			case 'u':
				if p.i+4 > len(p.s) {
					p.fail()
				}
				var v uint16
				for j := 0; j < 4; j++ {
					d := hexDigit(uint16(p.s[p.i+j]))
					if d < 0 {
						p.fail()
					}
					v = v<<4 | uint16(d)
				}
				p.i += 4
				b.WriteUnit(v)
			default:
				p.fail()
			}
		default:
			b.WriteUnit(c)
		}
	}
}

func hexDigit(c uint16) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func (p *jsonParser[T]) parseNumber() Value {
	start := p.i
	neg := p.s[p.i] == '-'
	if neg {
		p.i++
	}
	digits := func() int {
		n := 0
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	intStart := p.i
	if p.i < len(p.s) && p.s[p.i] == '0' {
		p.i++
	} else if digits() == 0 {
		p.fail()
	}
	intEnd := p.i
	simple := true
	if p.i < len(p.s) && p.s[p.i] == '.' {
		simple = false
		p.i++
		if digits() == 0 {
			p.fail()
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		simple = false
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if digits() == 0 {
			p.fail()
		}
	}
	// Integers of up to 15 digits are exact in a double.
	if simple && intEnd-intStart <= 15 {
		var n int64
		for j := intStart; j < intEnd; j++ {
			n = n*10 + int64(p.s[j]-'0')
		}
		f := float64(n)
		if neg {
			f = -f // -0 for "-0"
		}
		return Number(f)
	}
	var lexeme string
	if p.text != "" {
		lexeme = p.text[start:p.i]
	} else {
		b := make([]byte, p.i-start)
		for j := range b {
			b[j] = byte(p.s[start+j])
		}
		lexeme = string(b)
	}
	f, err := strconv.ParseFloat(lexeme, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); !ok || ne.Err != strconv.ErrRange {
			p.fail()
		}
	}
	return Number(f)
}

// internalizeJSONProperty (25.5.1.1).
func (vm *VM) internalizeJSONProperty(holder *Object, name PropertyKey, reviver Value, rec *jsonRecord) Value {
	val := holder.Get(name, ObjectValue(holder))
	context := vm.realm.NewObject()
	var elements []*jsonRecord
	var entries map[PropertyKey]*jsonRecord
	if rec != nil && SameValue(rec.value, val) {
		if !val.IsObject() {
			context.CreateDataPropertyOrThrow(StrKey("source"), StringValue(rec.source))
		}
		elements, entries = rec.elements, rec.entries
	}
	if o := val.AsObject(); o != nil {
		if vm.IsArray(val) {
			n := vm.LengthOfArrayLike(o)
			for i := int64(0); i < n; i++ {
				k := IndexKey(uint32(i))
				var er *jsonRecord
				if i < int64(len(elements)) {
					er = elements[i]
				}
				newElement := vm.internalizeJSONProperty(o, k, reviver, er)
				if newElement.IsUndefined() {
					o.Delete(k)
				} else {
					o.CreateDataProperty(k, newElement)
				}
			}
		} else {
			// EnumerableOwnProperties(val, key) is taken before any reviver call.
			var keys []PropertyKey
			for _, k := range o.OwnPropertyKeys() {
				if p, ok := o.GetOwnProperty(k); ok && p.Enumerable() && !k.IsSymbol() {
					keys = append(keys, k)
				}
			}
			for _, k := range keys {
				newElement := vm.internalizeJSONProperty(o, k, reviver, entries[k])
				if newElement.IsUndefined() {
					o.Delete(k)
				} else {
					o.CreateDataProperty(k, newElement)
				}
			}
		}
	}
	return vm.Call(reviver, ObjectValue(holder), name.Value(), val, ObjectValue(context))
}

// --- JSON.stringify ----------------------------------------------------------------------

type jsonStringifier struct {
	vm           *VM
	replacerFn   Value
	propertyList []PropertyKey
	hasList      bool
	stack        []*Object
	indent       string
	gap          string
	b            stringBuilder
}

func (vm *VM) jsonStringify(value, replacer, space Value) (*String, bool) {
	st := &jsonStringifier{vm: vm}
	if ro := replacer.AsObject(); ro != nil {
		if ro.IsCallable() {
			st.replacerFn = replacer
		} else if vm.IsArray(replacer) {
			st.hasList = true
			seen := map[PropertyKey]bool{}
			n := vm.LengthOfArrayLike(ro)
			for i := int64(0); i < n; i++ {
				v := ro.getIndex(uint32(i))
				var item *String
				switch {
				case v.IsString():
					item = v.StringRef()
				case v.IsNumber():
					item = vm.ToString(v)
				case v.IsObject() && (v.ObjectRef().class == ClassString || v.ObjectRef().class == ClassNumber):
					item = vm.ToString(v)
				}
				if item != nil {
					k := item.Key()
					if !seen[k] {
						seen[k] = true
						st.propertyList = append(st.propertyList, k)
					}
				}
			}
		}
	}
	if so := space.AsObject(); so != nil {
		switch so.class {
		case ClassNumber:
			space = Number(vm.ToNumber(space))
		case ClassString:
			space = StringValue(vm.ToString(space))
		}
	}
	switch {
	case space.IsNumber():
		n := int(math.Min(10, vm.ToIntegerOrInfinity(space)))
		if n > 0 {
			st.gap = strings.Repeat(" ", n)
		}
	case space.IsString():
		s := space.StringRef()
		if s.Length() > 10 {
			s = s.Substring(0, 10)
		}
		st.gap = s.String()
	}
	wrapper := vm.realm.NewObject()
	wrapper.CreateDataPropertyOrThrow(StrKey(""), value)
	if !st.serializeProperty(StrKey(""), wrapper) {
		return nil, false
	}
	return st.b.String(), true
}

func (st *jsonStringifier) serializeProperty(k PropertyKey, holder *Object) bool {
	vm := st.vm
	value := holder.Get(k, ObjectValue(holder))
	if value.IsObject() || value.IsBigInt() {
		toJSON := vm.GetV(value, StrKey("toJSON"))
		if toJSON.IsCallable() {
			value = vm.Call(toJSON, value, k.Value())
		}
	}
	if !st.replacerFn.IsUndefined() {
		value = vm.Call(st.replacerFn, ObjectValue(holder), k.Value(), value)
	}
	if o := value.AsObject(); o != nil {
		switch o.class {
		case ClassNumber:
			value = Number(vm.ToNumber(value))
		case ClassString:
			value = StringValue(vm.ToString(value))
		case ClassBoolean, ClassBigInt:
			value = o.internal.(*primitiveWrapper).value
		}
		if raw, ok := o.internal.(*rawJSON); ok {
			st.b.WriteString(raw.text)
			return true
		}
	}
	switch value.Kind() {
	case KindNull:
		st.b.WriteGo("null")
		return true
	case KindBool:
		if value.BoolValue() {
			st.b.WriteGo("true")
		} else {
			st.b.WriteGo("false")
		}
		return true
	case KindString:
		st.quote(value.StringRef())
		return true
	case KindNumber:
		f := value.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			st.b.WriteGo("null")
		} else {
			st.b.WriteString(NumberToString(f))
		}
		return true
	case KindBigInt:
		vm.throwTypeError("JSON.stringify cannot serialize BigInt.")
	case KindObject:
		if !value.ObjectRef().IsCallable() {
			if vm.IsArray(value) {
				st.serializeArray(value.ObjectRef())
			} else {
				st.serializeObject(value.ObjectRef())
			}
			return true
		}
	}
	return false
}

func (st *jsonStringifier) quote(s *String) {
	b := &st.b
	b.WriteUnit('"')
	n := s.Length()
	for i := 0; i < n; i++ {
		c := s.At(i)
		switch c {
		case '"':
			b.WriteGo(`\"`)
		case '\\':
			b.WriteGo(`\\`)
		case '\b':
			b.WriteGo(`\b`)
		case '\f':
			b.WriteGo(`\f`)
		case '\n':
			b.WriteGo(`\n`)
		case '\r':
			b.WriteGo(`\r`)
		case '\t':
			b.WriteGo(`\t`)
		default:
			switch {
			case c < 0x20:
				b.WriteGo("\\u00" + string("0123456789abcdef"[c>>4]) + string("0123456789abcdef"[c&15]))
			case c >= 0xD800 && c <= 0xDBFF && i+1 < n && s.At(i+1) >= 0xDC00 && s.At(i+1) <= 0xDFFF:
				b.WriteUnit(c)
				b.WriteUnit(s.At(i + 1))
				i++
			case c >= 0xD800 && c <= 0xDFFF:
				b.WriteGo("\\u" + strconv.FormatInt(int64(c), 16))
			default:
				b.WriteUnit(c)
			}
		}
	}
	b.WriteUnit('"')
}

func (st *jsonStringifier) enter(o *Object) {
	for _, s := range st.stack {
		if s == o {
			st.vm.throwTypeError("JSON.stringify cannot serialize cyclic structures.")
		}
	}
	st.stack = append(st.stack, o)
}

func (st *jsonStringifier) leave() { st.stack = st.stack[:len(st.stack)-1] }

func (st *jsonStringifier) serializeObject(o *Object) {
	vm := st.vm
	st.enter(o)
	stepback := st.indent
	st.indent += st.gap
	var keys []PropertyKey
	if st.hasList {
		keys = st.propertyList
	} else {
		for _, k := range o.OwnPropertyKeys() {
			if k.IsSymbol() {
				continue
			}
			if p, ok := o.GetOwnProperty(k); ok && p.Enumerable() {
				keys = append(keys, k)
			}
		}
	}
	st.b.WriteUnit('{')
	first := true
	for _, k := range keys {
		mark := st.b.Len()
		if !first {
			st.b.WriteUnit(',')
		}
		if st.gap != "" {
			st.b.WriteUnit('\n')
			st.b.WriteGo(st.indent)
		}
		st.quote(NewString(k.String()))
		st.b.WriteUnit(':')
		if st.gap != "" {
			st.b.WriteUnit(' ')
		}
		if !st.serializeProperty(k, o) {
			st.b.truncate(mark)
			continue
		}
		first = false
	}
	if !first && st.gap != "" {
		st.b.WriteUnit('\n')
		st.b.WriteGo(stepback)
	}
	st.b.WriteUnit('}')
	st.indent = stepback
	st.leave()
	_ = vm
}

func (st *jsonStringifier) serializeArray(o *Object) {
	vm := st.vm
	st.enter(o)
	stepback := st.indent
	st.indent += st.gap
	n := vm.LengthOfArrayLike(o)
	st.b.WriteUnit('[')
	for i := int64(0); i < n; i++ {
		if i > 0 {
			st.b.WriteUnit(',')
		}
		if st.gap != "" {
			st.b.WriteUnit('\n')
			st.b.WriteGo(st.indent)
		}
		if !st.serializeProperty(IndexKey(uint32(i)), o) {
			st.b.WriteGo("null")
		}
	}
	if n > 0 && st.gap != "" {
		st.b.WriteUnit('\n')
		st.b.WriteGo(stepback)
	}
	st.b.WriteUnit(']')
	st.indent = stepback
	st.leave()
}

func (b *stringBuilder) truncate(n int) {
	if b.wide {
		b.u = b.u[:n]
	} else {
		b.ascii = b.ascii[:n]
	}
}
