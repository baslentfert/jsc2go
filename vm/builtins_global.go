package vm

import (
	"math"
	"math/big"
	"strings"
	"unicode/utf8"
)

// Global functions and value properties (JSGlobalObjectFunctions.cpp).

func (r *Realm) initGlobalFunctions() {
	vm := r.vm
	g := r.GlobalObject
	g.putDirect(StrKey("globalThis"), ObjectValue(r.GlobalThis), Writable|Configurable)
	g.putDirect(StrKey("NaN"), NaN, 0)
	g.putDirect(StrKey("Infinity"), Number(posInf), 0)
	g.putDirect(StrKey("undefined"), Undefined, 0)

	parseFloatFn := r.method(g, "parseFloat", 1, func(c *Call) Value {
		s := trimJSSpaceStart(vm.ToString(c.Arg(0)))
		return Number(parseFloatPrefix(s))
	})
	parseIntFn := r.method(g, "parseInt", 2, func(c *Call) Value {
		s := trimJSSpaceStart(vm.ToString(c.Arg(0)))
		radix := vm.ToInt32(c.Arg(1))
		return Number(parseIntPrefix(s, int(radix)))
	})
	setFast(parseIntFn, fastParseInt)
	r.NumberPrototype.GetStr("constructor").ObjectRef().putDirect(StrKey("parseFloat"), ObjectValue(parseFloatFn), BuiltinFlags)
	r.NumberPrototype.GetStr("constructor").ObjectRef().putDirect(StrKey("parseInt"), ObjectValue(parseIntFn), BuiltinFlags)
	r.method(g, "isNaN", 1, func(c *Call) Value { return Bool(math.IsNaN(vm.ToNumber(c.Arg(0)))) })
	r.method(g, "isFinite", 1, func(c *Call) Value {
		n := vm.ToNumber(c.Arg(0))
		return Bool(!math.IsNaN(n) && !math.IsInf(n, 0))
	})
	r.evalFunction = r.method(g, "eval", 1, func(c *Call) Value {
		return vm.indirectEval(c.Realm(), c.Arg(0))
	})

	const uriReserved = ";/?:@&=+$,"
	const uriUnescaped = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.!~*'()"
	r.method(g, "encodeURI", 1, func(c *Call) Value {
		return StringValue(vm.uriEncode(vm.ToString(c.Arg(0)), uriReserved+uriUnescaped+"#"))
	})
	r.method(g, "encodeURIComponent", 1, func(c *Call) Value {
		return StringValue(vm.uriEncode(vm.ToString(c.Arg(0)), uriUnescaped))
	})
	r.method(g, "decodeURI", 1, func(c *Call) Value {
		return StringValue(vm.uriDecode(vm.ToString(c.Arg(0)), uriReserved+"#"))
	})
	r.method(g, "decodeURIComponent", 1, func(c *Call) Value {
		return StringValue(vm.uriDecode(vm.ToString(c.Arg(0)), ""))
	})
	r.method(g, "escape", 1, func(c *Call) Value {
		s := vm.ToString(c.Arg(0))
		const unescaped = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789@*_+-./"
		const hex = "0123456789ABCDEF"
		var b stringBuilder
		for i := 0; i < s.Length(); i++ {
			u := s.At(i)
			switch {
			case u < 128 && strings.IndexByte(unescaped, byte(u)) >= 0:
				b.WriteUnit(u)
			case u < 256:
				b.WriteGo("%" + string(hex[u>>4]) + string(hex[u&15]))
			default:
				b.WriteGo("%u" + string(hex[u>>12]) + string(hex[u>>8&15]) + string(hex[u>>4&15]) + string(hex[u&15]))
			}
		}
		return StringValue(b.String())
	})
	r.method(g, "unescape", 1, func(c *Call) Value {
		s := vm.ToString(c.Arg(0))
		n := s.Length()
		var b stringBuilder
		hexAt := func(i, count int) (uint16, bool) {
			if i+count > n {
				return 0, false
			}
			var v uint16
			for j := 0; j < count; j++ {
				d := hexDigit(s.At(i + j))
				if d < 0 {
					return 0, false
				}
				v = v<<4 | uint16(d)
			}
			return v, true
		}
		for i := 0; i < n; i++ {
			u := s.At(i)
			if u == '%' {
				if i+1 < n && s.At(i+1) == 'u' {
					if v, ok := hexAt(i+2, 4); ok {
						b.WriteUnit(v)
						i += 5
						continue
					}
				}
				if v, ok := hexAt(i+1, 2); ok {
					b.WriteUnit(v)
					i += 2
					continue
				}
			}
			b.WriteUnit(u)
		}
		return StringValue(b.String())
	})

	// jsc shell compatibility.
	r.method(g, "print", 1, func(c *Call) Value {
		parts := make([]string, len(c.Args))
		for i, a := range c.Args {
			parts[i] = vm.ToString(a).GoString()
		}
		line := strings.Join(parts, " ")
		if vm.Print != nil {
			vm.Print(line)
		}
		return Undefined
	})
}

// parseFloatPrefix parses the longest StrDecimalLiteral prefix.
func parseFloatPrefix(s *String) float64 {
	if !s.IsASCII() {
		// Only the ASCII prefix can be part of a number.
		n := 0
		for n < s.Length() && s.At(n) < 128 {
			n++
		}
		s = s.Substring(0, n)
	}
	t := s.asciiText()
	sign := ""
	rest := t
	if len(rest) > 0 && (rest[0] == '+' || rest[0] == '-') {
		sign, rest = rest[:1], rest[1:]
	}
	if strings.HasPrefix(rest, "Infinity") {
		if sign == "-" {
			return negInf
		}
		return posInf
	}
	i := 0
	digits := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
		digits++
	}
	if i < len(rest) && rest[i] == '.' {
		j := i + 1
		frac := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
			frac++
		}
		if digits > 0 || frac > 0 {
			i = j
			digits += frac
		}
	}
	if digits == 0 {
		return math.NaN()
	}
	if i < len(rest) && (rest[i] == 'e' || rest[i] == 'E') {
		j := i + 1
		if j < len(rest) && (rest[j] == '+' || rest[j] == '-') {
			j++
		}
		k := j
		for k < len(rest) && rest[k] >= '0' && rest[k] <= '9' {
			k++
		}
		if k > j {
			i = k
		}
	}
	return StringToNumber(NewString(sign + rest[:i]))
}

// parseIntPrefix is parseInt after whitespace trimming (19.2.5).
func parseIntPrefix(s *String, radix int) float64 {
	i := 0
	n := s.Length()
	sign := 1.0
	if i < n && (s.At(i) == '+' || s.At(i) == '-') {
		if s.At(i) == '-' {
			sign = -1
		}
		i++
	}
	stripPrefix := true
	if radix != 0 {
		if radix < 2 || radix > 36 {
			return math.NaN()
		}
		if radix != 16 {
			stripPrefix = false
		}
	} else {
		radix = 10
	}
	if stripPrefix && i+1 < n && s.At(i) == '0' && (s.At(i+1) == 'x' || s.At(i+1) == 'X') {
		i += 2
		radix = 16
	}
	start := i
	for i < n {
		c := s.At(i)
		if c >= 128 || digitValue(byte(c)) >= radix {
			break
		}
		i++
	}
	if i == start {
		return math.NaN()
	}
	// Exact while below 2^53: no big numbers needed.
	var v uint64
	for j := start; j < i; j++ {
		v = v*uint64(radix) + uint64(digitValue(byte(s.At(j))))
		if v >= 1<<53 {
			break
		}
		if j == i-1 {
			return sign * float64(v)
		}
	}
	digits := s.Substring(start, i).String()
	if radix == 10 && len(digits) > 20 {
		// Decimal values with many digits round like a decimal literal.
		f := StringToNumber(NewString(digits))
		return sign * f
	}
	b, _ := new(big.Int).SetString(strings.ToLower(digits), radix)
	f, _ := new(big.Float).SetInt(b).Float64()
	return sign * f
}

// uriEncode is Encode (19.2.6.5).
func (vm *VM) uriEncode(s *String, unescaped string) *String {
	const hex = "0123456789ABCDEF"
	var b stringBuilder
	n := s.Length()
	for k := 0; k < n; k++ {
		c := s.At(k)
		if c < 128 && strings.IndexByte(unescaped, byte(c)) >= 0 {
			b.WriteUnit(c)
			continue
		}
		cp, size := s.CodePointAt(k)
		if cp >= 0xD800 && cp <= 0xDFFF {
			vm.throwError(ErrorKindURIError, "String contained an illegal UTF-16 sequence.")
		}
		k += size - 1
		var buf [4]byte
		m := utf8.EncodeRune(buf[:], cp)
		for _, x := range buf[:m] {
			b.WriteGo("%" + string(hex[x>>4]) + string(hex[x&15]))
		}
	}
	return b.String()
}

// uriDecode is Decode (19.2.6.6).
func (vm *VM) uriDecode(s *String, reserved string) *String {
	fail := func() { vm.throwError(ErrorKindURIError, "URI error") }
	var b stringBuilder
	n := s.Length()
	hexByte := func(k int) byte {
		if k+2 >= n+0 && k+2 > n-1 && k+2 >= n {
			if k+2 > n-1 {
				fail()
			}
		}
		h1, h2 := hexDigit(s.At(k+1)), hexDigit(s.At(k+2))
		if s.At(k) != '%' || h1 < 0 || h2 < 0 {
			fail()
		}
		return byte(h1<<4 | h2)
	}
	for k := 0; k < n; k++ {
		c := s.At(k)
		if c != '%' {
			b.WriteUnit(c)
			continue
		}
		start := k
		if k+2 >= n {
			fail()
		}
		first := hexByte(k)
		k += 2
		if first < 0x80 {
			if strings.IndexByte(reserved, first) >= 0 {
				for j := start; j <= k; j++ {
					b.WriteUnit(s.At(j))
				}
			} else {
				b.WriteUnit(uint16(first))
			}
			continue
		}
		var count int
		switch {
		case first&0xE0 == 0xC0:
			count = 2
		case first&0xF0 == 0xE0:
			count = 3
		case first&0xF8 == 0xF0:
			count = 4
		default:
			fail()
		}
		octets := []byte{first}
		if k+3*(count-1) >= n {
			fail()
		}
		for j := 1; j < count; j++ {
			k++
			o := hexByte(k)
			if o&0xC0 != 0x80 {
				fail()
			}
			octets = append(octets, o)
			k += 2
		}
		r, size := utf8.DecodeRune(octets)
		if r == utf8.RuneError && size <= 1 || size != len(octets) || r >= 0xD800 && r <= 0xDFFF {
			fail()
		}
		b.WriteRune(r)
	}
	return b.String()
}
