// Package dtoa converts doubles to strings the way JavaScript does.
//
// WTF uses a dtoa port for this; Go's strconv already produces the shortest
// round-tripping digit string, so only the ECMAScript layout rules
// (Number::toString, ECMA-262 #sec-numeric-types-number-tostring) are needed.
package dtoa

import (
	"math"
	"strconv"
	"strings"
)

// NumberToString implements Number::toString(x) for radix 10.
func NumberToString(x float64) string {
	switch {
	case math.IsNaN(x):
		return "NaN"
	case x == 0:
		return "0" // both +0 and -0
	case math.IsInf(x, 1):
		return "Infinity"
	case math.IsInf(x, -1):
		return "-Infinity"
	case x < 0:
		return "-" + NumberToString(-x)
	}

	// Fast path for integers that fit exactly.
	if x < 1e21 && x == math.Trunc(x) && x < 1<<53 {
		return strconv.FormatInt(int64(x), 10)
	}

	// Shortest digits s and exponent n such that x = 0.s × 10^n.
	digits, n := shortestDigits(x)
	k := len(digits)

	var b strings.Builder
	switch {
	case k <= n && n <= 21:
		// Integer: digits followed by n-k zeros.
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", n-k))
	case 0 < n && n <= 21:
		// Decimal point inside the digits.
		b.WriteString(digits[:n])
		b.WriteByte('.')
		b.WriteString(digits[n:])
	case -6 < n && n <= 0:
		// Leading zeros after the decimal point.
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -n))
		b.WriteString(digits)
	default:
		// Exponential notation.
		b.WriteByte(digits[0])
		if k > 1 {
			b.WriteByte('.')
			b.WriteString(digits[1:])
		}
		b.WriteByte('e')
		if n-1 >= 0 {
			b.WriteByte('+')
		}
		b.WriteString(strconv.Itoa(n - 1))
	}
	return b.String()
}

// shortestDigits returns the shortest decimal digit string d (no leading or
// trailing zeros) and exponent n with x = 0.d × 10^n, for finite x > 0.
func shortestDigits(x float64) (string, int) {
	// 'e' with precision -1 gives d.ddddde±XX using the minimal number of digits.
	s := strconv.FormatFloat(x, 'e', -1, 64)
	mantissa, exp, _ := strings.Cut(s, "e")
	e, _ := strconv.Atoi(exp)
	digits := strings.Replace(mantissa, ".", "", 1)
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		digits = "0"
	}
	return digits, e + 1
}
