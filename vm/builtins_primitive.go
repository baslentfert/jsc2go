package vm

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"weak"

	"github.com/baslentfert/jsc2go/wtf/dtoa"
)

var (
	posInf = math.Inf(1)
	negInf = math.Inf(-1)
)

// --- Symbol ------------------------------------------------------------------------------

func (r *Realm) initSymbol() {
	vm := r.vm
	proto := r.NewObject()
	r.SymbolPrototype = proto
	c := r.constructor("Symbol", 0, func(c *Call) Value {
		if c.NewTarget != nil {
			vm.throwTypeError("Symbol is not a constructor")
		}
		var desc *String
		if d := c.Arg(0); !d.IsUndefined() {
			desc = vm.ToString(d)
		}
		return SymbolValue(NewSymbol(desc))
	}, proto)
	for _, s := range []struct {
		name string
		sym  *Symbol
	}{
		{"asyncDispose", SymbolAsyncDispose}, {"asyncIterator", SymbolAsyncIterator}, {"dispose", SymbolDispose},
		{"hasInstance", SymbolHasInstance}, {"isConcatSpreadable", SymbolIsConcatSpreadable}, {"iterator", SymbolIterator},
		{"match", SymbolMatch}, {"matchAll", SymbolMatchAll}, {"replace", SymbolReplace}, {"search", SymbolSearch},
		{"species", SymbolSpecies}, {"split", SymbolSplit}, {"toPrimitive", SymbolToPrimitive},
		{"toStringTag", SymbolToStringTag}, {"unscopables", SymbolUnscopables},
	} {
		c.putDirect(StrKey(s.name), SymbolValue(s.sym), 0)
	}
	r.method(c, "for", 1, func(c *Call) Value {
		key := vm.ToString(c.Arg(0))
		k := key.String()
		if w, ok := vm.symbolRegistry[k]; ok {
			if s := w.Value(); s != nil {
				return SymbolValue(s)
			}
		}
		if len(vm.symbolRegistry) >= 2*max(vm.symbolRegistryPurged, 64) {
			vm.symbolRegistry = purgeWeak(vm.symbolRegistry, func(w weak.Pointer[Symbol]) bool { return w.Value() != nil })
			vm.symbolRegistryPurged = len(vm.symbolRegistry)
		}
		s := &Symbol{description: key, registered: key}
		vm.symbolRegistry[k] = weak.Make(s)
		return SymbolValue(s)
	})
	r.method(c, "keyFor", 1, func(c *Call) Value {
		s := c.Arg(0)
		if !s.IsSymbol() {
			vm.throwTypeError("Symbol.keyFor requires a symbol")
		}
		if k := s.SymbolRef().registered; k != nil {
			return StringValue(k)
		}
		return Undefined
	})
	thisSymbol := func(c *Call, name string) *Symbol {
		return vm.thisPrimitive(c.This, KindSymbol, ClassSymbol, "Symbol.prototype."+name).SymbolRef()
	}
	r.method(proto, "toString", 0, func(c *Call) Value {
		return Str(thisSymbol(c, "toString").DescriptiveString())
	})
	r.method(proto, "valueOf", 0, func(c *Call) Value { return SymbolValue(thisSymbol(c, "valueOf")) })
	r.getter(proto, StrKey("description"), func(c *Call) Value {
		if d := thisSymbol(c, "description").description; d != nil {
			return StringValue(d)
		}
		return Undefined
	})
	r.symbolMethod(proto, SymbolToPrimitive, 1, func(c *Call) Value {
		return SymbolValue(thisSymbol(c, "[Symbol.toPrimitive]"))
	}, Configurable)
	r.toStringTag(proto, "Symbol")
}

// --- Boolean -----------------------------------------------------------------------------

func (r *Realm) initBoolean() {
	vm := r.vm
	proto := r.newObject(r.ObjectPrototype)
	proto.class = ClassBoolean
	proto.internal = &primitiveWrapper{False}
	r.BooleanPrototype = proto
	r.constructor("Boolean", 1, func(c *Call) Value {
		b := Bool(c.Arg(0).ToBoolean())
		if c.NewTarget == nil {
			return b
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.BooleanPrototype })
		o.class = ClassBoolean
		o.internal = &primitiveWrapper{b}
		return ObjectValue(o)
	}, proto)
	r.method(proto, "toString", 0, func(c *Call) Value {
		b := vm.thisPrimitive(c.This, KindBool, ClassBoolean, "Boolean.prototype.toString")
		return StringValue(vm.ToString(b))
	})
	r.method(proto, "valueOf", 0, func(c *Call) Value {
		return vm.thisPrimitive(c.This, KindBool, ClassBoolean, "Boolean.prototype.valueOf")
	})
}

// --- Number ------------------------------------------------------------------------------

func (r *Realm) initNumber() {
	vm := r.vm
	proto := r.newObject(r.ObjectPrototype)
	proto.class = ClassNumber
	proto.internal = &primitiveWrapper{Int(0)}
	r.NumberPrototype = proto
	c := r.constructor("Number", 1, func(c *Call) Value {
		n := Int(0)
		if len(c.Args) > 0 {
			prim := vm.ToNumeric(c.Args[0])
			if prim.IsBigInt() {
				f, _ := new(big.Float).SetInt(prim.BigIntRef()).Float64()
				n = Number(f)
			} else {
				n = prim
			}
		}
		if c.NewTarget == nil {
			return n
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.NumberPrototype })
		o.class = ClassNumber
		o.internal = &primitiveWrapper{n}
		return ObjectValue(o)
	}, proto)
	for _, k := range []struct {
		name string
		v    float64
	}{
		{"EPSILON", math.Nextafter(1, 2) - 1}, {"MAX_SAFE_INTEGER", 1<<53 - 1}, {"MAX_VALUE", math.MaxFloat64},
		{"MIN_SAFE_INTEGER", -(1<<53 - 1)}, {"MIN_VALUE", 5e-324}, {"NaN", math.NaN()},
		{"NEGATIVE_INFINITY", negInf}, {"POSITIVE_INFINITY", posInf},
	} {
		c.putDirect(StrKey(k.name), Number(k.v), 0)
	}
	r.method(c, "isFinite", 1, func(c *Call) Value {
		v := c.Arg(0)
		return Bool(v.IsNumber() && !math.IsNaN(v.Float()) && !math.IsInf(v.Float(), 0))
	})
	setFast(r.method(c, "isInteger", 1, func(c *Call) Value { return Bool(isIntegralNumber(c.Arg(0))) }),
		func(vm *VM, this Value, args []Value) (Value, bool) {
			return Bool(isIntegralNumber(argAt(args, 0))), true
		})
	r.method(c, "isNaN", 1, func(c *Call) Value {
		v := c.Arg(0)
		return Bool(v.IsNumber() && math.IsNaN(v.Float()))
	})
	r.method(c, "isSafeInteger", 1, func(c *Call) Value {
		v := c.Arg(0)
		return Bool(isIntegralNumber(v) && math.Abs(v.Float()) <= 1<<53-1)
	})

	thisNumber := func(c *Call, name string) float64 {
		return vm.thisPrimitive(c.This, KindNumber, ClassNumber, "Number.prototype."+name).Float()
	}
	r.method(proto, "toString", 1, func(c *Call) Value {
		x := thisNumber(c, "toString")
		radix := 10.0
		if rv := c.Arg(0); !rv.IsUndefined() {
			radix = vm.ToIntegerOrInfinity(rv)
		}
		if radix < 2 || radix > 36 {
			vm.throwRangeError("toString() radix argument must be between 2 and 36")
		}
		if radix == 10 {
			return StringValue(NumberToString(x))
		}
		return Str(numberToRadixString(x, int(radix)))
	})
	r.method(proto, "toLocaleString", 0, func(c *Call) Value {
		return StringValue(NumberToString(thisNumber(c, "toLocaleString")))
	})
	r.method(proto, "valueOf", 0, func(c *Call) Value { return Number(thisNumber(c, "valueOf")) })
	r.method(proto, "toFixed", 1, func(c *Call) Value {
		x := thisNumber(c, "toFixed")
		f := vm.ToIntegerOrInfinity(c.Arg(0))
		if f < 0 || f > 100 || math.IsInf(f, 0) {
			vm.throwRangeError("toFixed() argument must be between 0 and 100")
		}
		if math.IsNaN(x) {
			return Str("NaN")
		}
		if math.Abs(x) >= 1e21 || math.IsInf(x, 0) {
			return StringValue(NumberToString(x))
		}
		return Str(toFixed(x, int(f)))
	})
	r.method(proto, "toExponential", 1, func(c *Call) Value {
		x := thisNumber(c, "toExponential")
		fv := c.Arg(0)
		f := vm.ToIntegerOrInfinity(fv)
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return StringValue(NumberToString(x))
		}
		if f < 0 || f > 100 || math.IsInf(f, 0) {
			vm.throwRangeError("toExponential() argument must be between 0 and 100")
		}
		return Str(toExponential(x, int(f), fv.IsUndefined()))
	})
	r.method(proto, "toPrecision", 1, func(c *Call) Value {
		x := thisNumber(c, "toPrecision")
		pv := c.Arg(0)
		if pv.IsUndefined() {
			return StringValue(NumberToString(x))
		}
		p := vm.ToIntegerOrInfinity(pv)
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return StringValue(NumberToString(x))
		}
		if p < 1 || p > 100 {
			vm.throwRangeError("toPrecision() argument must be between 1 and 100")
		}
		return Str(toPrecision(x, int(p)))
	})
}

func isIntegralNumber(v Value) bool {
	if !v.IsNumber() {
		return false
	}
	f := v.Float()
	return !math.IsNaN(f) && !math.IsInf(f, 0) && math.Trunc(f) == f
}

func pow10Int(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// roundHalfUp returns floor(r + 1/2) for r >= 0.
func roundHalfUp(r *big.Rat) *big.Int {
	t := new(big.Rat).Add(r, big.NewRat(1, 2))
	return new(big.Int).Quo(t.Num(), t.Denom())
}

// scaledDigits returns n with n × 10^(e-p+1) closest to |x| (ties to the
// larger n) and exactly p digits, plus the exponent e (x finite, nonzero).
func scaledDigits(x float64, p int) (string, int) {
	r := new(big.Rat).SetFloat64(math.Abs(x))
	// e = floor(log10(|x|)) exactly: the float estimate can be off by one.
	e := int(math.Floor(math.Log10(math.Abs(x))))
	pow := func(e int) *big.Rat {
		if e >= 0 {
			return new(big.Rat).SetInt(pow10Int(e))
		}
		return new(big.Rat).SetFrac(big.NewInt(1), pow10Int(-e))
	}
	for pow(e).Cmp(r) > 0 {
		e--
	}
	for pow(e+1).Cmp(r) <= 0 {
		e++
	}
	for attempt := 0; attempt < 3; attempt++ {
		scale := e - p + 1
		q := new(big.Rat).Set(r)
		if scale >= 0 {
			q.Quo(q, new(big.Rat).SetInt(pow10Int(scale)))
		} else {
			q.Mul(q, new(big.Rat).SetInt(pow10Int(-scale)))
		}
		n := roundHalfUp(q)
		s := n.String()
		switch {
		case len(s) > p:
			e++
		case len(s) < p:
			e--
		default:
			return s, e
		}
	}
	panic("scaledDigits did not converge")
}

func toFixed(x float64, f int) string {
	sign := ""
	if x < 0 {
		sign = "-"
	}
	r := new(big.Rat).SetFloat64(math.Abs(x))
	r.Mul(r, new(big.Rat).SetInt(pow10Int(f)))
	s := roundHalfUp(r).String()
	if f == 0 {
		return sign + s
	}
	if len(s) <= f {
		s = strings.Repeat("0", f-len(s)+1) + s
	}
	return sign + s[:len(s)-f] + "." + s[len(s)-f:]
}

func exponentString(e int) string {
	if e < 0 {
		return "e-" + strconv.Itoa(-e)
	}
	return "e+" + strconv.Itoa(e)
}

func toExponential(x float64, f int, shortest bool) string {
	sign := ""
	if x < 0 {
		sign = "-"
	}
	var digits string
	var e int
	switch {
	case x == 0:
		digits = strings.Repeat("0", f+1)
	case shortest:
		digits, e = shortestDigitsOf(dtoa.NumberToString(math.Abs(x)))
		e--
	default:
		digits, e = scaledDigits(x, f+1)
	}
	m := digits[:1]
	if len(digits) > 1 {
		m += "." + digits[1:]
	}
	return sign + m + exponentString(e)
}

// shortestDigitsOf extracts the significant digits and the point position
// from a Number::toString result of a positive finite number.
func shortestDigitsOf(s string) (string, int) {
	mant, exp, hasExp := strings.Cut(s, "e")
	e := 0
	if hasExp {
		e, _ = strconv.Atoi(exp)
	}
	intPart, frac, _ := strings.Cut(mant, ".")
	digits := intPart + frac
	point := len(intPart) + e
	trimmed := strings.TrimLeft(digits, "0")
	point -= len(digits) - len(trimmed)
	trimmed = strings.TrimRight(trimmed, "0")
	if trimmed == "" {
		return "0", 1
	}
	return trimmed, point
}

func toPrecision(x float64, p int) string {
	sign := ""
	if x < 0 {
		sign = "-"
	}
	var digits string
	var e int
	if x == 0 {
		digits = strings.Repeat("0", p)
	} else {
		digits, e = scaledDigits(x, p)
	}
	if e < -6 || e >= p {
		m := digits[:1]
		if p > 1 {
			m += "." + digits[1:]
		}
		return sign + m + exponentString(e)
	}
	if e == p-1 {
		return sign + digits
	}
	if e >= 0 {
		return sign + digits[:e+1] + "." + digits[e+1:]
	}
	return sign + "0." + strings.Repeat("0", -(e+1)) + digits
}

// numberToRadixString is Number.prototype.toString for radix ≠ 10 (a port
// of the DoubleToRadixCString algorithm JSC uses).
func numberToRadixString(value float64, radix int) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	case value == 0:
		return "0"
	}
	const chars = "0123456789abcdefghijklmnopqrstuvwxyz"
	negative := value < 0
	if negative {
		value = -value
	}
	integer := math.Floor(value)
	fraction := value - integer
	delta := 0.5 * (math.Nextafter(value, posInf) - value)
	delta = math.Max(math.Nextafter(0, 1), delta)
	var frac []byte
	if fraction >= delta {
		for {
			// Rounded products: a fused multiply-subtract would change the digits.
			fraction = float64(fraction * float64(radix))
			delta = float64(delta * float64(radix))
			digit := int(fraction)
			frac = append(frac, chars[digit])
			fraction -= float64(digit)
			if fraction > 0.5 || (fraction == 0.5 && digit&1 == 1) {
				if fraction+delta > 1 {
					// Round up and propagate the carry.
					for {
						i := len(frac) - 1
						if i < 0 {
							integer++
							break
						}
						c := frac[i]
						d := strings.IndexByte(chars, c) + 1
						if d < radix {
							frac[i] = chars[d]
							break
						}
						frac = frac[:i]
					}
					break
				}
			}
			if fraction < delta {
				break
			}
		}
	}
	// Integer part: exact via big.Int.
	bi, _ := new(big.Float).SetFloat64(integer).Int(nil)
	s := bi.Text(radix)
	if len(frac) > 0 {
		s += "." + string(frac)
	}
	if negative {
		s = "-" + s
	}
	return s
}

// --- BigInt ------------------------------------------------------------------------------

func (r *Realm) initBigInt() {
	vm := r.vm
	proto := r.NewObject()
	r.BigIntPrototype = proto
	c := r.constructor("BigInt", 1, func(c *Call) Value {
		if c.NewTarget != nil {
			vm.throwTypeError("BigInt is not a constructor")
		}
		prim := vm.ToPrimitive(c.Arg(0), HintNumber)
		if prim.IsNumber() {
			f := prim.Float()
			if !isIntegralNumber(prim) {
				vm.throwRangeError("Not an integer")
			}
			b, _ := new(big.Float).SetFloat64(f).Int(nil)
			return BigIntValue(b)
		}
		return BigIntValue(vm.ToBigInt(prim))
	}, proto)
	asN := func(signed bool) NativeFunc {
		return func(c *Call) Value {
			bits := vm.ToIndex(c.Arg(0))
			n := vm.ToBigInt(c.Arg(1))
			if bits == 0 {
				return BigIntValue(new(big.Int))
			}
			// Values that already fit are returned as is, so huge bit counts
			// need not materialize 2^bits.
			if signed && int64(n.BitLen()) < bits || !signed && n.Sign() >= 0 && int64(n.BitLen()) <= bits {
				return BigIntValue(n)
			}
			if bits > maxBigIntBits {
				vm.throwRangeError("Maximum BigInt size exceeded")
			}
			mod := new(big.Int).Lsh(big.NewInt(1), uint(bits))
			m := new(big.Int).Mod(n, mod) // Euclidean: non-negative
			if signed && m.Cmp(new(big.Int).Rsh(mod, 1)) >= 0 {
				m.Sub(m, mod)
			}
			return BigIntValue(m)
		}
	}
	r.method(c, "asIntN", 2, asN(true))
	r.method(c, "asUintN", 2, asN(false))
	thisBigInt := func(c *Call, name string) *big.Int {
		return vm.thisPrimitive(c.This, KindBigInt, ClassBigInt, "BigInt.prototype."+name).BigIntRef()
	}
	r.method(proto, "toString", 0, func(c *Call) Value {
		x := thisBigInt(c, "toString")
		radix := 10.0
		if rv := c.Arg(0); !rv.IsUndefined() {
			radix = vm.ToIntegerOrInfinity(rv)
		}
		if radix < 2 || radix > 36 {
			vm.throwRangeError("toString() radix argument must be between 2 and 36")
		}
		return Str(x.Text(int(radix)))
	})
	r.method(proto, "toLocaleString", 0, func(c *Call) Value {
		return Str(thisBigInt(c, "toLocaleString").String())
	})
	r.method(proto, "valueOf", 0, func(c *Call) Value { return BigIntValue(thisBigInt(c, "valueOf")) })
	r.toStringTag(proto, "BigInt")
}
