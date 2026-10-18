package dtoa

import (
	"math"
	"testing"
)

func TestNumberToString(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{math.Copysign(0, -1), "0"},
		{1, "1"},
		{-42, "-42"},
		{0.1, "0.1"},
		{0.000001, "0.000001"},
		{0.0000001, "1e-7"},
		{1.5e-7, "1.5e-7"},
		{123.456, "123.456"},
		{1e21, "1e+21"},
		{1e20, "100000000000000000000"},
		{123456789012345680000, "123456789012345680000"},
		{1.7976931348623157e308, "1.7976931348623157e+308"},
		{5e-324, "5e-324"},
		{math.NaN(), "NaN"},
		{math.Inf(1), "Infinity"},
		{math.Inf(-1), "-Infinity"},
		{9007199254740993, "9007199254740992"},
		{0.30000000000000004, "0.30000000000000004"},
	}
	for _, c := range cases {
		if got := NumberToString(c.in); got != c.want {
			t.Errorf("NumberToString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
