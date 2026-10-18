package vm

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/text/cases"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
)

// String objects and String.prototype (StringObject.cpp, StringConstructor.cpp,
// StringPrototype.cpp).

// stringObject is the String exotic object (10.4.3).
type stringObject struct {
	ordinaryObject
	s *String
}

func (r *Realm) newStringObject(s *String, proto *Object) *Object {
	if proto != nil && proto == r.StringPrototype && r.stringObjectShape != nil {
		// The object, its value and its length in one allocation, with the
		// shape the first one got (new String in a loop: date-format-xparb
		// made 800,000 a run).
		b := new(stringObjectBlock)
		b.w = primitiveWrapper{StringValue(s)}
		b.v[0] = Int(s.Length())
		b.o = Object{realm: r, proto: proto, extensible: true, class: ClassString,
			props: propertyMap{shape: r.stringObjectShape, vals: b.v[:1:1]}, internal: &b.w, exotic: stringObject{s: s}}
		return &b.o
	}
	o := r.newObject(proto)
	o.class = ClassString
	o.internal = &primitiveWrapper{StringValue(s)}
	o.exotic = stringObject{s: s}
	o.putDirect(lengthKey, Int(s.Length()), 0)
	if proto == r.StringPrototype && proto != nil && r.stringObjectShape == nil {
		r.stringObjectShape = o.props.shape
	}
	return o
}

// stringObjectBlock is a String object with its value and length.
type stringObjectBlock struct {
	o Object
	w primitiveWrapper
	v [1]Value
}

// plainStringObject returns the string of o if o is a String object whose
// conversion to a primitive is known to give it (ToPrimitive's fast path,
// like JSC's check of an unmodified String prototype chain): o has only
// its length as an own property, its prototype is the realm's
// String.prototype, whose valueOf and toString are the original ones, and
// neither prototype has Symbol.toPrimitive.
func (r *Realm) plainStringObject(o *Object) (*String, bool) {
	so, ok := o.exotic.(stringObject)
	if !ok || o.props.shape != r.stringObjectShape || o.proto != r.StringPrototype {
		return nil, false
	}
	sp, op := r.StringPrototype, r.ObjectPrototype
	c := &r.stringToPrimitive
	if sp.props.shape != c.sp || sp.props.shape.version != c.spVersion ||
		op.props.shape != c.op || op.props.shape.version != c.opVersion {
		if !r.checkStringToPrimitive() {
			return nil, false
		}
	}
	// A store replaces a value without changing the shape: compare the
	// methods themselves.
	if sp.proto != op || op.proto != nil ||
		sp.props.vals[c.valueOf].p != unsafe.Pointer(c.valueOfFn) ||
		sp.props.vals[c.toString].p != unsafe.Pointer(c.toStringFn) {
		return nil, false
	}
	return so.s, true
}

// stringToPrimitive caches the prototypes' shapes for which
// plainStringObject checked them, and the slots of valueOf and toString.
type stringToPrimitive struct {
	sp, op                *shape
	spVersion, opVersion  uint32
	valueOf, toString     int
	valueOfFn, toStringFn *Object
}

func (r *Realm) checkStringToPrimitive() bool {
	sp, op := r.StringPrototype, r.ObjectPrototype
	c := &r.stringToPrimitive
	toPrim := SymKey(SymbolToPrimitive)
	valueOf, toString := sp.props.find(valueOfKey), sp.props.find(toStringKey)
	if valueOf < 0 || toString < 0 || sp.props.find(toPrim) >= 0 || op.props.find(toPrim) >= 0 ||
		sp.props.shape.flags[valueOf]&accessorFlag != 0 || sp.props.shape.flags[toString]&accessorFlag != 0 {
		return false
	}
	c.sp, c.spVersion = sp.props.shape, sp.props.shape.version
	c.op, c.opVersion = op.props.shape, op.props.shape.version
	c.valueOf, c.toString = valueOf, toString
	return true
}

var (
	valueOfKey  = StrKey("valueOf")
	toStringKey = StrKey("toString")
)

func (so stringObject) stringProperty(k PropertyKey) (Property, bool) {
	if i, ok := k.Index(); ok && int64(i) < int64(so.s.Length()) {
		return Property{Value: StringValue(so.s.Substring(int(i), int(i)+1)), Flags: Enumerable}, true
	}
	return Property{}, false
}

func (so stringObject) getOwnProperty(o *Object, k PropertyKey) (Property, bool) {
	if p, ok := o.ordinaryGetOwnProperty(k); ok {
		return p, true
	}
	return so.stringProperty(k)
}

func (so stringObject) defineOwnProperty(o *Object, k PropertyKey, d PropertyDescriptor) bool {
	if p, ok := so.stringProperty(k); ok {
		return isCompatiblePropertyDescriptor(o.extensible, d, p, true)
	}
	return o.ordinaryDefineOwnProperty(k, d)
}

func (so stringObject) ownKeys(o *Object) []PropertyKey {
	n := so.s.Length()
	keys := make([]PropertyKey, 0, n+o.props.len())
	for i := 0; i < n; i++ {
		keys = append(keys, IndexKey(uint32(i)))
	}
	for _, k := range o.ordinaryOwnKeys() {
		if i, ok := k.Index(); ok && int64(i) < int64(n) {
			continue
		}
		keys = append(keys, k)
	}
	return keys
}

// thisStringValue is RequireObjectCoercible(this) + ToString.
func (vm *VM) thisString(c *Call, name string) *String {
	if c.This.IsNullish() {
		vm.throwTypeError("String.prototype.%s called on null or undefined", name)
	}
	return vm.ToString(c.This)
}

func (r *Realm) initString() {
	vm := r.vm
	proto := r.newStringObject(emptyString, r.ObjectPrototype)
	r.StringPrototype = proto
	c := r.constructor("String", 1, func(c *Call) Value {
		var s *String
		if len(c.Args) == 0 {
			s = emptyString
		} else if c.NewTarget == nil && c.Args[0].IsSymbol() {
			return Str(c.Args[0].SymbolRef().DescriptiveString())
		} else {
			s = vm.ToString(c.Args[0])
		}
		if c.NewTarget == nil {
			return StringValue(s)
		}
		p := vm.GetPrototypeFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.StringPrototype })
		return ObjectValue(c.Realm().newStringObject(s, p))
	}, proto)

	r.method(c, "fromCharCode", 1, func(c *Call) Value {
		if len(c.Args) == 1 {
			return StringValue(charString(vm.ToUint16(c.Args[0])))
		}
		u := make([]uint16, len(c.Args))
		for i, a := range c.Args {
			u[i] = vm.ToUint16(a)
		}
		return StringValue(NewStringUTF16(u))
	})
	r.method(c, "fromCodePoint", 1, func(c *Call) Value {
		var b stringBuilder
		for _, a := range c.Args {
			n := vm.ToNumber(a)
			if n != math.Trunc(n) || n < 0 || n > 0x10FFFF || math.IsNaN(n) {
				vm.throwRangeError("Arguments contain a value that is out of range of code points")
			}
			b.WriteRune(rune(n))
		}
		return StringValue(b.String())
	})
	r.method(c, "raw", 1, func(c *Call) Value {
		cooked := vm.ToObject(c.Arg(0))
		raw := vm.ToObject(cooked.GetStr("raw"))
		n := vm.LengthOfArrayLike(raw)
		var b stringBuilder
		for i := int64(0); i < n; i++ {
			b.WriteString(vm.ToString(raw.getIdx(i)))
			if i+1 < n && int(i+1) < len(c.Args) {
				b.WriteString(vm.ToString(c.Args[i+1]))
			}
		}
		return StringValue(b.String())
	})

	thisStringValue := func(c *Call, name string) Value {
		return vm.thisPrimitive(c.This, KindString, ClassString, "String.prototype."+name)
	}
	r.stringToPrimitive.toStringFn = r.method(proto, "toString", 0, func(c *Call) Value { return thisStringValue(c, "toString") })
	r.stringToPrimitive.valueOfFn = r.method(proto, "valueOf", 0, func(c *Call) Value { return thisStringValue(c, "valueOf") })

	r.method(proto, "at", 1, func(c *Call) Value {
		s := vm.thisString(c, "at")
		n := s.Length()
		rel := vm.ToIntegerOrInfinity(c.Arg(0))
		k := rel
		if rel < 0 {
			k = float64(n) + rel
		}
		if k < 0 || k >= float64(n) {
			return Undefined
		}
		return StringValue(s.Substring(int(k), int(k)+1))
	})
	r.method(proto, "charAt", 1, func(c *Call) Value {
		s := vm.thisString(c, "charAt")
		pos := vm.ToIntegerOrInfinity(c.Arg(0))
		if pos < 0 || pos >= float64(s.Length()) {
			return StringValue(emptyString)
		}
		return StringValue(s.Substring(int(pos), int(pos)+1))
	})
	r.method(proto, "charCodeAt", 1, func(c *Call) Value {
		s := vm.thisString(c, "charCodeAt")
		pos := vm.ToIntegerOrInfinity(c.Arg(0))
		if pos < 0 || pos >= float64(s.Length()) {
			return NaN
		}
		return Int(int(s.At(int(pos))))
	})
	r.method(proto, "codePointAt", 1, func(c *Call) Value {
		s := vm.thisString(c, "codePointAt")
		pos := vm.ToIntegerOrInfinity(c.Arg(0))
		if pos < 0 || pos >= float64(s.Length()) {
			return Undefined
		}
		cp, _ := s.CodePointAt(int(pos))
		return Int(int(cp))
	})
	r.method(proto, "concat", 1, func(c *Call) Value {
		s := vm.thisString(c, "concat")
		for _, a := range c.Args {
			s = Concat(s, vm.ToString(a))
		}
		return StringValue(s)
	})
	r.method(proto, "endsWith", 1, func(c *Call) Value {
		s := vm.thisString(c, "endsWith")
		if vm.isRegExp(c.Arg(0)) {
			vm.throwTypeError("Argument to String.prototype.endsWith cannot be a RegExp")
		}
		search := vm.ToString(c.Arg(0))
		n := s.Length()
		end := n
		if e := c.Arg(1); !e.IsUndefined() {
			end = int(math.Min(math.Max(vm.ToIntegerOrInfinity(e), 0), float64(n)))
		}
		start := end - search.Length()
		if start < 0 {
			return False
		}
		return Bool(s.Substring(start, end).Equals(search))
	})
	r.method(proto, "startsWith", 1, func(c *Call) Value {
		s := vm.thisString(c, "startsWith")
		if vm.isRegExp(c.Arg(0)) {
			vm.throwTypeError("Argument to String.prototype.startsWith cannot be a RegExp")
		}
		search := vm.ToString(c.Arg(0))
		n := s.Length()
		start := int(math.Min(math.Max(vm.ToIntegerOrInfinity(c.Arg(1)), 0), float64(n)))
		if start+search.Length() > n {
			return False
		}
		return Bool(s.Substring(start, start+search.Length()).Equals(search))
	})
	r.method(proto, "includes", 1, func(c *Call) Value {
		s := vm.thisString(c, "includes")
		if vm.isRegExp(c.Arg(0)) {
			vm.throwTypeError("Argument to String.prototype.includes cannot be a RegExp")
		}
		search := vm.ToString(c.Arg(0))
		start := int(math.Min(math.Max(vm.ToIntegerOrInfinity(c.Arg(1)), 0), float64(s.Length())))
		return Bool(s.IndexOf(search, start) >= 0)
	})
	setFast(r.method(proto, "indexOf", 1, func(c *Call) Value {
		s := vm.thisString(c, "indexOf")
		search := vm.ToString(c.Arg(0))
		start := int(math.Min(math.Max(vm.ToIntegerOrInfinity(c.Arg(1)), 0), float64(s.Length())))
		return Int(s.IndexOf(search, start))
	}), fastStringIndexOf)
	r.method(proto, "lastIndexOf", 1, func(c *Call) Value {
		s := vm.thisString(c, "lastIndexOf")
		search := vm.ToString(c.Arg(0))
		numPos := vm.ToNumber(c.Arg(1))
		pos := posInf
		if !math.IsNaN(numPos) {
			pos = integerOrInfinity(numPos)
		}
		n := s.Length()
		start := int(math.Min(math.Max(pos, 0), float64(n)))
		m := search.Length()
		for i := min(start, n-m); i >= 0; i-- {
			if s.Substring(i, i+m).Equals(search) {
				return Int(i)
			}
		}
		return Int(-1)
	})
	r.method(proto, "isWellFormed", 0, func(c *Call) Value {
		s := vm.thisString(c, "isWellFormed")
		return Bool(isWellFormed(s))
	})
	r.method(proto, "toWellFormed", 0, func(c *Call) Value {
		s := vm.thisString(c, "toWellFormed")
		if isWellFormed(s) {
			return StringValue(s)
		}
		u := append([]uint16(nil), s.UTF16()...)
		for i := 0; i < len(u); i++ {
			switch c := u[i]; {
			case c >= 0xD800 && c <= 0xDBFF:
				if i+1 < len(u) && u[i+1] >= 0xDC00 && u[i+1] <= 0xDFFF {
					i++
				} else {
					u[i] = 0xFFFD
				}
			case c >= 0xDC00 && c <= 0xDFFF:
				u[i] = 0xFFFD
			}
		}
		return StringValue(NewStringUTF16(u))
	})
	setFast(r.method(proto, "localeCompare", 1, func(c *Call) Value {
		return Int(vm.localeCompare(vm.thisString(c, "localeCompare"), vm.ToString(c.Arg(0))))
	}), func(vm *VM, this Value, args []Value) (Value, bool) {
		if len(args) == 0 || !this.IsString() || !args[0].IsString() {
			return Undefined, false
		}
		return Int(vm.localeCompare(this.StringRef(), args[0].StringRef())), true
	})
	r.method(proto, "normalize", 0, func(c *Call) Value {
		s := vm.thisString(c, "normalize")
		form := "NFC"
		if f := c.Arg(0); !f.IsUndefined() {
			form = vm.toGoString(f)
		}
		var nf norm.Form
		switch form {
		case "NFC":
			nf = norm.NFC
		case "NFD":
			nf = norm.NFD
		case "NFKC":
			nf = norm.NFKC
		case "NFKD":
			nf = norm.NFKD
		default:
			vm.throwRangeError("argument does not match any normalization form")
		}
		return StringValue(normalizeString(s, nf))
	})
	pad := func(name string, atStart bool) {
		r.method(proto, name, 1, func(c *Call) Value {
			s := vm.thisString(c, name)
			maxLen := vm.ToLength(c.Arg(0))
			n := int64(s.Length())
			if maxLen <= n {
				return StringValue(s)
			}
			filler := NewString(" ")
			if f := c.Arg(1); !f.IsUndefined() {
				filler = vm.ToString(f)
			}
			if filler.Length() == 0 {
				return StringValue(s)
			}
			fillLen := maxLen - n
			if maxLen > 1<<30 {
				vm.throwRangeError("Out of memory")
			}
			var b stringBuilder
			for int64(b.Len()) < fillLen {
				b.WriteString(filler)
			}
			fill := b.String().Substring(0, int(fillLen))
			if atStart {
				return StringValue(Concat(fill, s))
			}
			return StringValue(Concat(s, fill))
		})
	}
	pad("padStart", true)
	pad("padEnd", false)
	r.method(proto, "repeat", 1, func(c *Call) Value {
		s := vm.thisString(c, "repeat")
		n := vm.ToIntegerOrInfinity(c.Arg(0))
		if n < 0 || math.IsInf(n, 1) {
			vm.throwRangeError("String.prototype.repeat argument must be greater than or equal to 0 and not be Infinity")
		}
		if n == 0 || s.Length() == 0 {
			return StringValue(emptyString)
		}
		if float64(s.Length())*n > 1<<30 {
			vm.throwRangeError("Out of memory")
		}
		var b stringBuilder
		for i := 0; i < int(n); i++ {
			b.WriteString(s)
		}
		return StringValue(b.String())
	})
	setFast(r.method(proto, "slice", 2, func(c *Call) Value {
		s := vm.thisString(c, "slice")
		n := int64(s.Length())
		from := vm.relativeIndex(c.Arg(0), n, 0)
		to := vm.relativeIndex(c.Arg(1), n, n)
		if from >= to {
			return StringValue(emptyString)
		}
		return StringValue(s.Substring(int(from), int(to)))
	}), fastStringSlice)
	setFast(r.method(proto, "substring", 2, func(c *Call) Value {
		s := vm.thisString(c, "substring")
		n := float64(s.Length())
		start := math.Min(math.Max(vm.ToIntegerOrInfinity(c.Arg(0)), 0), n)
		end := n
		if e := c.Arg(1); !e.IsUndefined() {
			end = math.Min(math.Max(vm.ToIntegerOrInfinity(e), 0), n)
		}
		if start > end {
			start, end = end, start
		}
		return StringValue(s.Substring(int(start), int(end)))
	}), fastStringSubstring)
	r.method(proto, "substr", 2, func(c *Call) Value {
		s := vm.thisString(c, "substr")
		n := int64(s.Length())
		start := vm.relativeIndex(c.Arg(0), n, 0)
		length := float64(n - start)
		if l := c.Arg(1); !l.IsUndefined() {
			length = vm.ToIntegerOrInfinity(l)
		}
		end := math.Min(float64(start)+length, float64(n))
		if float64(start) >= end {
			return StringValue(emptyString)
		}
		return StringValue(s.Substring(int(start), int(end)))
	})
	caseMethod := func(name string, caser func() stringCaser) {
		r.method(proto, name, 0, func(c *Call) Value {
			s := vm.thisString(c, name)
			if s.IsASCII() {
				if strings.Contains(name, "Upper") {
					return Str(strings.ToUpper(s.asciiText()))
				}
				return Str(strings.ToLower(s.asciiText()))
			}
			return StringValue(mapPreservingLoneSurrogates(s, func(t string) string { return caser().String(t) }))
		})
	}
	caseMethod("toLowerCase", func() stringCaser { return finalSigmaCaser{} })
	caseMethod("toUpperCase", func() stringCaser { return cases.Upper(language.Und) })
	caseMethod("toLocaleLowerCase", func() stringCaser { return finalSigmaCaser{} })
	caseMethod("toLocaleUpperCase", func() stringCaser { return cases.Upper(language.Und) })
	trim := func(name string, start, end bool) *Object {
		return r.method(proto, name, 0, func(c *Call) Value {
			s := vm.thisString(c, name)
			i, j := 0, s.Length()
			if start {
				for i < j && isStrWhiteSpaceChar(s.At(i)) {
					i++
				}
			}
			if end {
				for j > i && isStrWhiteSpaceChar(s.At(j-1)) {
					j--
				}
			}
			return StringValue(s.Substring(i, j))
		})
	}
	trim("trim", true, true)
	trimStart := trim("trimStart", true, false)
	trimEnd := trim("trimEnd", false, true)
	proto.putDirect(StrKey("trimLeft"), ObjectValue(trimStart), BuiltinFlags)
	proto.putDirect(StrKey("trimRight"), ObjectValue(trimEnd), BuiltinFlags)

	r.method(proto, "split", 2, func(c *Call) Value {
		if c.This.IsNullish() {
			vm.throwTypeError("String.prototype.split called on null or undefined")
		}
		sep, limit := c.Arg(0), c.Arg(1)
		if sep.IsObject() {
			if splitter := vm.GetMethod(sep, SymKey(SymbolSplit)); !splitter.IsUndefined() {
				return vm.Call(splitter, sep, c.This, limit)
			}
		}
		s := vm.ToString(c.This)
		lim := uint32(math.MaxUint32)
		if !limit.IsUndefined() {
			lim = vm.ToUint32(limit)
		}
		r := vm.ToString(sep)
		if lim == 0 {
			return ObjectValue(c.Realm().NewArray())
		}
		if sep.IsUndefined() {
			return ObjectValue(c.Realm().NewArray(StringValue(s)))
		}
		n := s.Length()
		if n == 0 {
			if r.Length() > 0 {
				return ObjectValue(c.Realm().NewArray(StringValue(s)))
			}
			return ObjectValue(c.Realm().NewArray())
		}
		var parts []Value
		if r.Length() == 0 {
			for i := 0; i < n && uint32(len(parts)) < lim; i++ {
				parts = append(parts, StringValue(s.Substring(i, i+1)))
			}
			return ObjectValue(c.Realm().NewArray(parts...))
		}
		p := 0
		for {
			q := s.IndexOf(r, p)
			if q < 0 {
				break
			}
			parts = append(parts, StringValue(s.Substring(p, q)))
			if uint32(len(parts)) >= lim {
				return ObjectValue(c.Realm().NewArray(parts...))
			}
			p = q + r.Length()
		}
		parts = append(parts, StringValue(s.Substring(p, n)))
		return ObjectValue(c.Realm().NewArray(parts...))
	})
	r.method(proto, "replace", 2, func(c *Call) Value {
		return vm.stringReplace(c, false)
	})
	r.method(proto, "replaceAll", 2, func(c *Call) Value {
		return vm.stringReplace(c, true)
	})
	regexpDelegate := func(name string, sym *Symbol, flags string) {
		r.method(proto, name, 1, func(c *Call) Value {
			if c.This.IsNullish() {
				vm.throwTypeError("String.prototype.%s called on null or undefined", name)
			}
			re := c.Arg(0)
			if re.IsObject() {
				if name == "matchAll" && vm.isRegExp(re) {
					fl := re.ObjectRef().GetStr("flags")
					if fl.IsNullish() {
						vm.throwTypeError("RegExp flags is null or undefined")
					}
					if !strings.Contains(vm.toGoString(fl), "g") {
						vm.throwTypeError("String.prototype.matchAll requires a global RegExp")
					}
				}
				if m := vm.GetMethod(re, SymKey(sym)); !m.IsUndefined() {
					return vm.Call(m, re, c.This)
				}
			}
			s := vm.ToString(c.This)
			rx := vm.regExpCreate(re, Str(flags))
			return vm.Invoke(ObjectValue(rx), SymKey(sym), StringValue(s))
		})
	}
	regexpDelegate("match", SymbolMatch, "")
	regexpDelegate("matchAll", SymbolMatchAll, "g")
	regexpDelegate("search", SymbolSearch, "")

	// String iterator.
	r.StringIteratorPrototype = r.newObject(r.IteratorPrototype)
	r.toStringTag(r.StringIteratorPrototype, "String Iterator")
	r.method(r.StringIteratorPrototype, "next", 0, func(c *Call) Value {
		o := c.This.AsObject()
		var it *stringIterator
		if o != nil {
			it, _ = o.internal.(*stringIterator)
		}
		if it == nil {
			vm.throwTypeError("%%StringIteratorPrototype%%.next requires that |this| be a String Iterator instance")
		}
		if it.s == nil || it.pos >= it.s.Length() {
			it.s = nil
			return c.Realm().iterResult(Undefined, true)
		}
		_, n := it.s.CodePointAt(it.pos)
		v := it.s.Substring(it.pos, it.pos+n)
		it.pos += n
		return c.Realm().iterResult(StringValue(v), false)
	})
	r.symbolMethod(proto, SymbolIterator, 0, func(c *Call) Value {
		s := vm.thisString(c, "[Symbol.iterator]")
		it := c.Realm().newObject(c.Realm().StringIteratorPrototype)
		it.class = ClassIterator
		it.internal = &stringIterator{s: s}
		return ObjectValue(it)
	}, BuiltinFlags)

	// Annex B HTML methods.
	html := func(name, tag, attr string) {
		r.method(proto, name, len(attr)/max(len(attr), 1), func(c *Call) Value {
			s := vm.thisString(c, name)
			var b stringBuilder
			b.WriteGo("<" + tag)
			if attr != "" {
				v := vm.ToString(c.Arg(0))
				b.WriteGo(" " + attr + "=\"")
				for i := 0; i < v.Length(); i++ {
					if u := v.At(i); u == '"' {
						b.WriteGo("&quot;")
					} else {
						b.WriteUnit(u)
					}
				}
				b.WriteUnit('"')
			}
			b.WriteUnit('>')
			b.WriteString(s)
			b.WriteGo("</" + tag + ">")
			return StringValue(b.String())
		})
	}
	html("anchor", "a", "name")
	html("big", "big", "")
	html("blink", "blink", "")
	html("bold", "b", "")
	html("fixed", "tt", "")
	html("fontcolor", "font", "color")
	html("fontsize", "font", "size")
	html("italics", "i", "")
	html("link", "a", "href")
	html("small", "small", "")
	html("strike", "strike", "")
	html("sub", "sub", "")
	html("sup", "sup", "")
}

type stringIterator struct {
	s   *String
	pos int
}

func isWellFormed(s *String) bool {
	if s.IsASCII() {
		return true
	}
	u := s.UTF16()
	for i := 0; i < len(u); i++ {
		switch c := u[i]; {
		case c >= 0xD800 && c <= 0xDBFF:
			if i+1 < len(u) && u[i+1] >= 0xDC00 && u[i+1] <= 0xDFFF {
				i++
			} else {
				return false
			}
		case c >= 0xDC00 && c <= 0xDFFF:
			return false
		}
	}
	return true
}

// mapPreservingLoneSurrogates applies f to the well-formed runs of s and
// keeps lone surrogates as they are.
func mapPreservingLoneSurrogates(s *String, f func(string) string) *String {
	u := s.UTF16()
	var b stringBuilder
	start := 0
	flush := func(end int) {
		if end > start {
			b.WriteGo(f(string(utf16.Decode(u[start:end]))))
		}
	}
	for i := 0; i < len(u); i++ {
		c := u[i]
		if c >= 0xD800 && c <= 0xDBFF && i+1 < len(u) && u[i+1] >= 0xDC00 && u[i+1] <= 0xDFFF {
			i++
			continue
		}
		if c >= 0xD800 && c <= 0xDFFF {
			flush(i)
			b.WriteUnit(c)
			start = i + 1
		}
	}
	flush(len(u))
	return b.String()
}

func normalizeString(s *String, f norm.Form) *String {
	if s.IsASCII() {
		return s
	}
	return mapPreservingLoneSurrogates(s, f.String)
}

// stringReplace is String.prototype.replace / replaceAll.
func (vm *VM) stringReplace(c *Call, all bool) Value {
	if c.This.IsNullish() {
		vm.throwTypeError("String.prototype.replace called on null or undefined")
	}
	searchValue, replaceValue := c.Arg(0), c.Arg(1)
	if searchValue.IsObject() {
		if all && vm.isRegExp(searchValue) {
			fl := searchValue.ObjectRef().GetStr("flags")
			if fl.IsNullish() {
				vm.throwTypeError("RegExp flags is null or undefined")
			}
			if !strings.Contains(vm.toGoString(fl), "g") {
				vm.throwTypeError("String.prototype.replaceAll requires a global RegExp")
			}
		}
		if replacer := vm.GetMethod(searchValue, SymKey(SymbolReplace)); !replacer.IsUndefined() {
			return vm.Call(replacer, searchValue, c.This, replaceValue)
		}
	}
	s := vm.ToString(c.This)
	search := vm.ToString(searchValue)
	functional := replaceValue.IsCallable()
	var replaceStr *String
	if !functional {
		replaceStr = vm.ToString(replaceValue)
	}
	searchLen := search.Length()
	advance := max(1, searchLen)
	var positions []int
	pos := s.IndexOf(search, 0)
	for pos >= 0 {
		positions = append(positions, pos)
		if !all {
			break
		}
		if pos+advance > s.Length() {
			break
		}
		pos = s.IndexOf(search, pos+advance)
	}
	if len(positions) == 0 {
		return StringValue(s)
	}
	var b stringBuilder
	end := 0
	for _, p := range positions {
		b.WriteString(s.Substring(end, p))
		var replacement *String
		if functional {
			replacement = vm.ToString(vm.Call(replaceValue, Undefined, StringValue(search), Int(p), StringValue(s)))
		} else {
			replacement = vm.getSubstitution(search, s, p, nil, Undefined, replaceStr)
		}
		b.WriteString(replacement)
		end = p + searchLen
	}
	b.WriteString(s.Substring(end, s.Length()))
	return StringValue(b.String())
}

// getSubstitution (22.1.3.19.1).
func (vm *VM) getSubstitution(matched, str *String, position int, captures []Value, namedCaptures Value, replacement *String) *String {
	var b stringBuilder
	n := replacement.Length()
	m := len(captures)
	tailPos := min(position+matched.Length(), str.Length())
	for i := 0; i < n; i++ {
		ch := replacement.At(i)
		if ch != '$' || i+1 >= n {
			b.WriteUnit(ch)
			continue
		}
		next := replacement.At(i + 1)
		switch {
		case next == '$':
			b.WriteUnit('$')
			i++
		case next == '&':
			b.WriteString(matched)
			i++
		case next == '`':
			b.WriteString(str.Substring(0, position))
			i++
		case next == '\'':
			b.WriteString(str.Substring(tailPos, str.Length()))
			i++
		case next >= '0' && next <= '9':
			digits := int(next - '0')
			consumed := 1
			if i+2 < n {
				if d2 := replacement.At(i + 2); d2 >= '0' && d2 <= '9' {
					two := digits*10 + int(d2-'0')
					if two >= 1 && two <= m {
						digits = two
						consumed = 2
					}
				}
			}
			if digits >= 1 && digits <= m {
				if v := captures[digits-1]; !v.IsUndefined() {
					b.WriteString(vm.ToString(v))
				}
				i += consumed
			} else {
				b.WriteUnit('$')
			}
		case next == '<':
			if namedCaptures.IsUndefined() {
				b.WriteUnit('$')
				continue
			}
			end := -1
			for j := i + 2; j < n; j++ {
				if replacement.At(j) == '>' {
					end = j
					break
				}
			}
			if end < 0 {
				b.WriteUnit('$')
				continue
			}
			groupName := replacement.Substring(i+2, end)
			capture := vm.GetV(namedCaptures, groupName.Key())
			if !capture.IsUndefined() {
				b.WriteString(vm.ToString(capture))
			}
			i = end
		default:
			b.WriteUnit('$')
		}
	}
	return b.String()
}

// delete: the index properties of a String exotic object are not configurable.
func (so stringObject) delete(o *Object, k PropertyKey) bool {
	if _, ok := so.stringProperty(k); ok {
		return false
	}
	return o.ordinaryDelete(k)
}

// stringCaser is the part of cases.Caser that case mapping uses.
type stringCaser interface{ String(string) string }

// finalSigmaCaser lowercases like cases.Lower but decides Σ itself: the
// Final_Sigma condition of SpecialCasing is applied with case-ignorable
// characters skipped first (so U+0345, both cased and case-ignorable, does
// not make a following Σ final), as test262 and other engines expect.
type finalSigmaCaser struct{}

func (finalSigmaCaser) String(t string) string {
	lower := cases.Lower(language.Und)
	if !strings.ContainsRune(t, 'Σ') {
		return lower.String(t)
	}
	rs := []rune(t)
	var b strings.Builder
	start := 0
	for i, r := range rs {
		if r != 'Σ' {
			continue
		}
		b.WriteString(lower.String(string(rs[start:i])))
		if isFinalSigma(rs, i) {
			b.WriteRune('ς')
		} else {
			b.WriteRune('σ')
		}
		start = i + 1
	}
	b.WriteString(lower.String(string(rs[start:])))
	return b.String()
}

func isFinalSigma(rs []rune, i int) bool {
	j := i - 1
	for j >= 0 && isCaseIgnorable(rs[j]) {
		j--
	}
	if j < 0 || !isCased(rs[j]) {
		return false
	}
	k := i + 1
	for k < len(rs) && isCaseIgnorable(rs[k]) {
		k++
	}
	return k == len(rs) || !isCased(rs[k])
}

// isCased is the Unicode Cased property.
func isCased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) ||
		unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}

// isCaseIgnorable is the Unicode Case_Ignorable property.
func isCaseIgnorable(r rune) bool {
	switch r {
	case 0x27, 0x2E, 0x3A, 0x5E, 0x60, 0xA8, 0xAD, 0xAF, 0xB4, 0xB7, 0xB8, 0x387, 0x55F, 0x5F4,
		0x2018, 0x2019, 0x2024, 0x2027, 0xFE13, 0xFE52, 0xFE55, 0xFF07, 0xFF0E, 0xFF1A:
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}

// localeCompare compares s and that as String.prototype.localeCompare
// does: with the root collation (CLDR's, which JSC's ICU uses too).
func (vm *VM) localeCompare(s, that *String) int {
	if r, ok := compareAlnum(s, that); ok {
		return r
	}
	if vm.collator == nil {
		vm.collator = collate.New(language.Und) // one per VM: not safe for concurrent use
	}
	a, b := s.GoString(), that.GoString()
	// ASCII text is already in NFC.
	if !s.isASCII {
		a = norm.NFC.String(a)
	}
	if !that.isASCII {
		b = norm.NFC.String(b)
	}
	return vm.collator.CompareString(a, b)
}

// compareAlnum compares strings of ASCII letters and digits as the root
// collation does, without making Go strings and collation keys: by their
// letters and digits first (digits before letters, a letter's cases
// alike), then by case, a lowercase letter first at the first difference.
// TestCompareAlnum checks it against the collator.
func compareAlnum(s, t *String) (int, bool) {
	if !s.isASCII || !t.isASCII || s.left != nil || t.left != nil {
		return 0, false
	}
	a, b := s.asciiText(), t.asciiText()
	tertiary := 0
	for i := 0; i < len(a) || i < len(b); i++ {
		if i == len(a) {
			if !alnumOnly(b[i:]) {
				return 0, false
			}
			return -1, true
		}
		if i == len(b) {
			if !alnumOnly(a[i:]) {
				return 0, false
			}
			return 1, true
		}
		x, y := alnumWeight[a[i]], alnumWeight[b[i]]
		if x == 0 || y == 0 {
			return 0, false
		}
		if x>>1 != y>>1 {
			if !alnumOnly(a[i:]) || !alnumOnly(b[i:]) {
				return 0, false
			}
			if x>>1 < y>>1 {
				return -1, true
			}
			return 1, true
		}
		if tertiary == 0 && x != y {
			tertiary = int(x&1) - int(y&1)
		}
	}
	return tertiary, true
}

func alnumOnly(s string) bool {
	for i := range len(s) {
		if alnumWeight[s[i]] == 0 {
			return false
		}
	}
	return true
}

// alnumWeight is, for ASCII letters and digits, the order of their primary
// weights times two, plus one for uppercase letters; 0 for other bytes.
var alnumWeight = func() (w [256]uint8) {
	for c := '0'; c <= '9'; c++ {
		w[c] = uint8(1+c-'0') << 1
	}
	for c := 'a'; c <= 'z'; c++ {
		w[c] = uint8(11+c-'a') << 1
		w[c-'a'+'A'] = w[c] | 1
	}
	return
}()
