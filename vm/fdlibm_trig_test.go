package vm

import (
	"math"
	"strconv"
	"testing"
)

func TestFdlibmConstants(t *testing.T) {
	// The bit patterns must match the decimal values fdlibm documents.
	for _, c := range []struct {
		v   float64
		dec string
	}{
		{sinS1, "-1.66666666666666324348e-01"}, {sinS2, "8.33333333332248946124e-03"},
		{sinS3, "-1.98412698298579493134e-04"}, {sinS4, "2.75573137070700676789e-06"},
		{sinS5, "-2.50507602534068634195e-08"}, {sinS6, "1.58969099521155010221e-10"},
		{cosC1, "4.16666666666666019037e-02"}, {cosC2, "-1.38888888888741095749e-03"},
		{cosC3, "2.48015872894767294178e-05"}, {cosC4, "-2.75573143513906633035e-07"},
		{cosC5, "2.08757232129817482790e-09"}, {cosC6, "-1.13596475577881948265e-11"},
		{invPio2, "6.36619772367581382433e-01"}, {pio2_1, "1.57079632673412561417e+00"},
		{pio2_1t, "6.07710050650619224932e-11"}, {pio2_2, "6.07710050630396597660e-11"},
		{pio2_2t, "2.02226624879595063154e-21"}, {pio2_3, "2.02226624871116645580e-21"},
		{pio2_3t, "8.47842766036889956997e-32"},
	} {
		if d, _ := strconv.ParseFloat(c.dec, 64); d != c.v {
			t.Errorf("constant %s has bits %x", c.dec, math.Float64bits(c.v))
		}
	}
}

func TestFdlibmSinCos(t *testing.T) {
	// fdlibm is accurate to < 1 ulp, so it must stay within 1 ulp of Go's
	// result everywhere, including huge arguments.
	ulps := func(a, b float64) int64 {
		d := int64(math.Float64bits(a)) - int64(math.Float64bits(b))
		if d < 0 {
			d = -d
		}
		return d
	}
	for _, x := range []float64{0.1, 0.5, 1, 2, 3, math.Pi / 180, 10, 100, 1e5, 1e9, 1e22, 1e300, -7.5, 0x1p-30} {
		if s, g := fdlibmSin(x), math.Sin(x); ulps(s, g) > 1 {
			t.Errorf("sin(%g) = %v, Go %v", x, s, g)
		}
		if c, g := fdlibmCos(x), math.Cos(x); ulps(c, g) > 1 {
			t.Errorf("cos(%g) = %v, Go %v", x, c, g)
		}
	}
	// Values V8 (also fdlibm) prints; Go's math.Sin(MaxFloat64) is off.
	if s := fdlibmSin(math.MaxFloat64); s != 0.004961954789184062 {
		t.Errorf("sin(MAX_VALUE) = %v", s)
	}
	if s := fdlibmSin(1e22); s != -0.8522008497671888 {
		t.Errorf("sin(1e22) = %v", s)
	}
	if !math.IsNaN(fdlibmSin(math.Inf(1))) || fdlibmCos(0) != 1 || !math.Signbit(fdlibmSin(math.Copysign(0, -1))) {
		t.Error("special cases")
	}
}
