package vm

import (
	"math"
	"math/big"
	"sync"
)

// Math.sin and Math.cos as fdlibm computes them (k_sin.c, k_cos.c and
// e_rem_pio2.c), the libm family JSC and V8 use. Go's math.Sin and math.Cos
// use other polynomials and differ in the last bit for common arguments,
// which programs that validate their results notice (JetStream's 3d-cube).

var (
	sinS1 = math.Float64frombits(0xBFC5555555555549) // -1.66666666666666324348e-01
	sinS2 = math.Float64frombits(0x3F8111111110F8A6) //  8.33333333332248946124e-03
	sinS3 = math.Float64frombits(0xBF2A01A019C161D5) // -1.98412698298579493134e-04
	sinS4 = math.Float64frombits(0x3EC71DE357B1FE7D) //  2.75573137070700676789e-06
	sinS5 = math.Float64frombits(0xBE5AE5E68A2B9CEB) // -2.50507602534068634195e-08
	sinS6 = math.Float64frombits(0x3DE5D93A5ACFD57C) //  1.58969099521155010221e-10

	cosC1 = math.Float64frombits(0x3FA555555555554C) //  4.16666666666666019037e-02
	cosC2 = math.Float64frombits(0xBF56C16C16C15177) // -1.38888888888741095749e-03
	cosC3 = math.Float64frombits(0x3EFA01A019CB1590) //  2.48015872894767294178e-05
	cosC4 = math.Float64frombits(0xBE927E4F809C52AD) // -2.75573143513906633035e-07
	cosC5 = math.Float64frombits(0x3E21EE9EBDB4B1C4) //  2.08757232129817482790e-09
	cosC6 = math.Float64frombits(0xBDA8FAE9BE8838D4) // -1.13596475577881948265e-11

	invPio2 = math.Float64frombits(0x3FE45F306DC9C883) // 6.36619772367581382433e-01
	pio2_1  = math.Float64frombits(0x3FF921FB54400000) // first 33 bits of pi/2
	pio2_1t = math.Float64frombits(0x3DD0B4611A626331) // pi/2 - pio2_1
	pio2_2  = math.Float64frombits(0x3DD0B4611A600000) // second 33 bits of pi/2
	pio2_2t = math.Float64frombits(0x3BA3198A2E037073) // pi/2 - (pio2_1+pio2_2)
	pio2_3  = math.Float64frombits(0x3BA3198A2E000000) // third 33 bits of pi/2
	pio2_3t = math.Float64frombits(0x397B839A252049C1) // pi/2 - (pio2_1+pio2_2+pio2_3)
)

// Every product that is added or subtracted is rounded with an explicit
// float64 conversion, also when it is first stored in a variable: Go may
// otherwise fuse a*b+c into one FMA, even across statements (it does on
// arm64), which rounds once and loses fdlibm's results.

// kernelSin is __kernel_sin: sin(x+y) for |x| <= pi/4, y the tail of x;
// iy == 0 means y is zero.
func kernelSin(x, y float64, iy int) float64 {
	z := x * x
	w := z * z
	r := sinS2 + float64(z*(sinS3+float64(z*sinS4))) + float64(z*w*(sinS5+float64(z*sinS6)))
	v := z * x
	if iy == 0 {
		return x + float64(v*(sinS1+float64(z*r)))
	}
	return x - ((float64(z*(float64(0.5*y)-float64(v*r))) - y) - float64(v*sinS1))
}

// kernelCos is __kernel_cos: cos(x+y) for |x| <= pi/4.
func kernelCos(x, y float64) float64 {
	z := x * x
	w := z * z
	r := float64(z*(cosC1+float64(z*(cosC2+float64(z*cosC3))))) + float64(w*w*(cosC4+float64(z*(cosC5+float64(z*cosC6)))))
	hz := float64(0.5 * z)
	w = 1 - hz
	return w + (((1 - w) - hz) + (float64(z*r) - float64(x*y)))
}

// remPio2 is __ieee754_rem_pio2: x - n*pi/2 as y0+y1 with n.
func remPio2(x float64) (n int, y0, y1 float64) {
	hx := int32(math.Float64bits(x) >> 32)
	ix := hx & 0x7fffffff
	if ix < 0x4002d97c { // |x| < 3pi/4: n = +-1
		if hx > 0 {
			z := x - pio2_1
			if ix != 0x3ff921fb { // 33+53 bits of pi are enough
				y0 = z - pio2_1t
				y1 = (z - y0) - pio2_1t
			} else { // near pi/2: use 33+33+53 bits
				z -= pio2_2
				y0 = z - pio2_2t
				y1 = (z - y0) - pio2_2t
			}
			return 1, y0, y1
		}
		z := x + pio2_1
		if ix != 0x3ff921fb {
			y0 = z + pio2_1t
			y1 = (z - y0) + pio2_1t
		} else {
			z += pio2_2
			y0 = z + pio2_2t
			y1 = (z - y0) + pio2_2t
		}
		return -1, y0, y1
	}
	if ix <= 0x413921fb { // |x| ~<= 2^19*(pi/2): medium size
		t := math.Abs(x)
		n = int(float64(t*invPio2) + 0.5)
		fn := float64(n)
		r := t - float64(fn*pio2_1)
		w := float64(fn * pio2_1t) // first round, good to 85 bits
		// fdlibm skips the cancellation check below for the n whose results
		// are known not to need it (npio2_hw); checking always is equivalent.
		j := ix >> 20
		y0 = r - w
		if i := j - int32(math.Float64bits(y0)>>52)&0x7ff; i > 16 { // second round, good to 118 bits
			t := r
			w = float64(fn * pio2_2)
			r = t - w
			w = float64(fn*pio2_2t) - ((t - r) - w)
			y0 = r - w
			if i := j - int32(math.Float64bits(y0)>>52)&0x7ff; i > 49 { // third round, 151 bits
				t := r
				w = float64(fn * pio2_3)
				r = t - w
				w = float64(fn*pio2_3t) - ((t - r) - w)
				y0 = r - w
			}
		}
		y1 = (r - y0) - w
		if hx < 0 {
			return -n, -y0, -y1
		}
		return n, y0, y1
	}
	return remPio2Large(x)
}

const piPrec = 1400 // bits: enough to reduce any finite double exactly

var piOnce = sync.OnceValue(func() *big.Float {
	// Machin: pi = 16 atan(1/5) - 4 atan(1/239).
	atanInv := func(k int64) *big.Float {
		x := new(big.Float).SetPrec(piPrec).Quo(big.NewFloat(1).SetPrec(piPrec), big.NewFloat(float64(k)))
		x2 := new(big.Float).SetPrec(piPrec).Mul(x, x)
		sum := new(big.Float).SetPrec(piPrec).Set(x)
		term := new(big.Float).SetPrec(piPrec).Set(x)
		eps := new(big.Float).SetMantExp(big.NewFloat(1), -piPrec-8)
		for i := int64(3); ; i += 2 {
			term.Mul(term, x2)
			t := new(big.Float).SetPrec(piPrec).Quo(term, big.NewFloat(float64(i)))
			if (i/2)%2 == 1 {
				sum.Sub(sum, t)
			} else {
				sum.Add(sum, t)
			}
			if t.Cmp(eps) < 0 {
				return sum
			}
		}
	}
	a := atanInv(5)
	a.Mul(a, big.NewFloat(16))
	b := atanInv(239)
	b.Mul(b, big.NewFloat(4))
	return a.Sub(a, b)
})

// remPio2Large reduces huge arguments exactly with arbitrary precision
// (fdlibm's __kernel_rem_pio2 computes the same y0+y1).
func remPio2Large(x float64) (n int, y0, y1 float64) {
	halfPi := new(big.Float).SetPrec(piPrec).Quo(piOnce(), big.NewFloat(2))
	bx := new(big.Float).SetPrec(piPrec).SetFloat64(x)
	q := new(big.Float).SetPrec(piPrec).Quo(bx, halfPi)
	q.Add(q, big.NewFloat(0.5))
	qi, _ := q.Int(nil)
	if q.Sign() < 0 && !q.IsInt() {
		qi.Sub(qi, big.NewInt(1)) // floor
	}
	r := new(big.Float).SetPrec(piPrec).SetInt(qi)
	r.Mul(r, halfPi)
	r.Sub(bx, r)
	y0, _ = r.Float64()
	r.Sub(r, big.NewFloat(y0))
	y1, _ = r.Float64()
	return int(new(big.Int).And(qi, big.NewInt(3)).Int64()), y0, y1
}

// fdlibmSin is s_sin.c.
func fdlibmSin(x float64) float64 {
	ix := int32(math.Float64bits(x)>>32) & 0x7fffffff
	switch {
	case ix <= 0x3fe921fb: // |x| ~<= pi/4
		if ix < 0x3e400000 { // |x| < 2^-27
			return x
		}
		return kernelSin(x, 0, 0)
	case ix >= 0x7ff00000:
		return math.NaN()
	}
	n, y0, y1 := remPio2(x)
	switch n & 3 {
	case 0:
		return kernelSin(y0, y1, 1)
	case 1:
		return kernelCos(y0, y1)
	case 2:
		return -kernelSin(y0, y1, 1)
	}
	return -kernelCos(y0, y1)
}

// fdlibmCos is s_cos.c.
func fdlibmCos(x float64) float64 {
	ix := int32(math.Float64bits(x)>>32) & 0x7fffffff
	switch {
	case ix <= 0x3fe921fb:
		if ix < 0x3e46a09e { // |x| < 2^-27 * sqrt(2)
			return 1
		}
		return kernelCos(x, 0)
	case ix >= 0x7ff00000:
		return math.NaN()
	}
	n, y0, y1 := remPio2(x)
	switch n & 3 {
	case 0:
		return kernelCos(y0, y1)
	case 1:
		return -kernelSin(y0, y1, 1)
	case 2:
		return -kernelCos(y0, y1)
	}
	return kernelSin(y0, y1, 1)
}
