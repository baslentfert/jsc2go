package vm

import (
	"math"
	"math/big"
	"math/rand/v2"
)

// The Math object (MathObject.cpp).

func (r *Realm) initMath() {
	vm := r.vm
	m := r.NewObject()
	r.GlobalObject.putDirect(StrKey("Math"), ObjectValue(m), BuiltinFlags)
	r.toStringTag(m, "Math")
	for _, c := range []struct {
		name string
		v    float64
	}{
		{"E", math.E}, {"LN10", math.Ln10}, {"LN2", math.Ln2}, {"LOG10E", math.Log10E}, {"LOG2E", math.Log2E},
		{"PI", math.Pi}, {"SQRT1_2", math.Sqrt2 / 2}, {"SQRT2", math.Sqrt2},
	} {
		m.putDirect(StrKey(c.name), Number(c.v), 0)
	}
	unary := func(name string, f func(float64) float64) {
		setFast(r.method(m, name, 1, func(c *Call) Value { return Number(f(vm.ToNumber(c.Arg(0)))) }), fastMathUnary(f))
	}
	unary("abs", math.Abs)
	unary("acos", math.Acos)
	unary("acosh", math.Acosh)
	unary("asin", math.Asin)
	unary("asinh", math.Asinh)
	unary("atan", math.Atan)
	unary("atanh", math.Atanh)
	unary("cbrt", math.Cbrt)
	unary("ceil", math.Ceil)
	unary("cos", fdlibmCos)
	unary("cosh", math.Cosh)
	unary("exp", math.Exp)
	unary("expm1", math.Expm1)
	unary("floor", math.Floor)
	unary("fround", func(x float64) float64 { return float64(float32(x)) })
	unary("log", math.Log)
	unary("log1p", math.Log1p)
	unary("log10", math.Log10)
	unary("log2", math.Log2)
	unary("round", func(x float64) float64 {
		if math.IsNaN(x) || math.IsInf(x, 0) || x == math.Trunc(x) {
			return x
		}
		if x < 0 && x >= -0.5 {
			return math.Copysign(0, -1)
		}
		f := math.Floor(x)
		if x-f >= 0.5 {
			return f + 1
		}
		return f
	})
	unary("sign", func(x float64) float64 {
		switch {
		case x > 0:
			return 1
		case x < 0:
			return -1
		}
		return x
	})
	unary("sin", fdlibmSin)
	unary("sinh", fdlibmSinh)
	unary("sqrt", math.Sqrt)
	unary("tan", math.Tan)
	unary("tanh", math.Tanh)
	unary("trunc", math.Trunc)
	unary("f16round", func(x float64) float64 { return float16ToFloat64(float64ToFloat16(x)) })
	r.method(m, "clz32", 1, func(c *Call) Value {
		n := vm.ToUint32(c.Arg(0))
		count := 0
		for i := 31; i >= 0 && n&(1<<uint(i)) == 0; i-- {
			count++
		}
		return Int(count)
	})
	r.method(m, "imul", 2, func(c *Call) Value {
		a := vm.ToInt32(c.Arg(0))
		b := vm.ToInt32(c.Arg(1))
		return Int(int(a * b))
	})
	r.method(m, "atan2", 2, func(c *Call) Value {
		y := vm.ToNumber(c.Arg(0))
		x := vm.ToNumber(c.Arg(1))
		return Number(math.Atan2(y, x))
	})
	r.method(m, "pow", 2, func(c *Call) Value {
		x := vm.ToNumber(c.Arg(0))
		y := vm.ToNumber(c.Arg(1))
		return Number(numberPow(x, y))
	})
	r.method(m, "hypot", 2, func(c *Call) Value {
		nums := make([]float64, len(c.Args))
		for i, a := range c.Args {
			nums[i] = vm.ToNumber(a)
		}
		inf, nan := false, false
		for _, n := range nums {
			if math.IsInf(n, 0) {
				inf = true
			}
			if math.IsNaN(n) {
				nan = true
			}
		}
		if inf {
			return Number(posInf)
		}
		if nan {
			return Number(math.NaN())
		}
		maxAbs := 0.0
		for _, n := range nums {
			maxAbs = math.Max(maxAbs, math.Abs(n))
		}
		if maxAbs == 0 {
			return Int(0)
		}
		sum, comp := 0.0, 0.0
		for _, n := range nums {
			x := n / maxAbs
			y := float64(x*x) - comp // no FMA
			t := sum + y
			comp = (t - sum) - y
			sum = t
		}
		return Number(math.Sqrt(sum) * maxAbs)
	})
	minmax := func(name string, isMax bool) {
		setFast(r.method(m, name, 2, func(c *Call) Value {
			result := posInf
			if isMax {
				result = negInf
			}
			nums := make([]float64, len(c.Args))
			for i, a := range c.Args {
				nums[i] = vm.ToNumber(a)
			}
			for _, n := range nums {
				switch {
				case math.IsNaN(n) || math.IsNaN(result):
					result = math.NaN()
				case isMax && (n > result || n == 0 && result == 0 && !math.Signbit(n)):
					result = n
				case !isMax && (n < result || n == 0 && result == 0 && math.Signbit(n)):
					result = n
				}
			}
			return Number(result)
		}), fastMinMax(isMax))
	}
	minmax("max", true)
	minmax("min", false)
	r.method(m, "random", 0, func(c *Call) Value { return Number(rand.Float64()) })
	r.method(m, "sumPrecise", 1, func(c *Call) Value {
		items := c.Arg(0)
		if items.IsNullish() {
			vm.throwTypeError("Math.sumPrecise requires an iterable")
		}
		sum := new(big.Rat)
		state := "minus-zero"
		var count int64
		vm.iterate(items, func(v Value) bool {
			count++
			if count >= 1<<53 {
				vm.throwRangeError("too many values")
			}
			if !v.IsNumber() {
				vm.throwTypeError("Math.sumPrecise requires numbers")
			}
			n := v.Float()
			if state == "not-a-number" {
				return true
			}
			switch {
			case math.IsNaN(n):
				state = "not-a-number"
			case math.IsInf(n, 1):
				if state == "minus-infinity" {
					state = "not-a-number"
				} else {
					state = "plus-infinity"
				}
			case math.IsInf(n, -1):
				if state == "plus-infinity" {
					state = "not-a-number"
				} else {
					state = "minus-infinity"
				}
			case n == 0 && math.Signbit(n):
			default:
				if state == "minus-zero" || state == "finite" {
					state = "finite"
					sum.Add(sum, new(big.Rat).SetFloat64(n))
				}
			}
			return true
		})
		switch state {
		case "not-a-number":
			return Number(math.NaN())
		case "plus-infinity":
			return Number(posInf)
		case "minus-infinity":
			return Number(negInf)
		case "minus-zero":
			return Number(math.Copysign(0, -1))
		}
		f, _ := sum.Float64()
		return Number(f)
	})
}

// float64ToFloat16 rounds x to IEEE binary16 (round half to even).
func float64ToFloat16(x float64) uint16 {
	bits := math.Float64bits(x)
	sign := uint16(bits>>48) & 0x8000
	if math.IsNaN(x) {
		return sign | 0x7e00
	}
	a := math.Abs(x)
	if math.IsInf(x, 0) || a >= 65520 {
		return sign | 0x7c00
	}
	if a < math.Ldexp(1, -25) {
		if a > math.Ldexp(1, -25) {
			return sign | 1
		}
		return sign
	}
	frac, exp := math.Frexp(a) // a = frac × 2^exp, frac in [0.5, 1)
	e := exp - 1               // a = (2*frac) × 2^e
	if e < -14 {
		// Subnormal: units of 2^-24.
		m := math.RoundToEven(a * math.Ldexp(1, 24))
		return sign | uint16(m)
	}
	m := math.RoundToEven((2*frac - 1) * 1024)
	if m == 1024 {
		m = 0
		e++
		if e > 15 {
			return sign | 0x7c00
		}
	}
	return sign | uint16(e+15)<<10 | uint16(m)
}

func float16ToFloat64(h uint16) float64 {
	sign := 1.0
	if h&0x8000 != 0 {
		sign = -1
	}
	e := int(h>>10) & 0x1f
	m := float64(h & 0x3ff)
	switch e {
	case 0:
		return sign * m * math.Ldexp(1, -24)
	case 0x1f:
		if m != 0 {
			return math.NaN()
		}
		return sign * posInf
	}
	return sign * (1 + m/1024) * math.Ldexp(1, e-15)
}

// fdlibmSinh is fdlibm's e_sinh.c (the libm JSC uses); Go's math.Sinh is
// less accurate for small arguments.
func fdlibmSinh(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) || x == 0 {
		return x
	}
	h := 0.5
	if x < 0 {
		h = -0.5
	}
	ax := math.Abs(x)
	switch {
	case ax < 22:
		if ax < 0x1p-28 {
			return x // sinh(tiny) = tiny with inexact
		}
		t := math.Expm1(ax)
		if ax < 1 {
			return h * (2*t - t*t/(t+1))
		}
		return h * (t + t/(t+1))
	case ax < math.Log(math.MaxFloat64):
		return h * math.Exp(ax)
	case ax <= 710.4758600739439:
		w := math.Exp(0.5 * ax)
		return (h * w) * w
	}
	return x * math.MaxFloat64 // overflow
}
