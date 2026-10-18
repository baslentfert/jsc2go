package vm

import (
	"math"
	"math/big"
)

// The arithmetic, bitwise and string operators (ECMA-262 13.15.3
// ApplyStringOrNumericBinaryOperator and the Number/BigInt type methods).

// Add is the + operator.
func (vm *VM) Add(a, b Value) Value {
	if a.IsNumber() && b.IsNumber() {
		return Number(a.num() + b.num())
	}
	if a.IsString() && b.IsString() {
		return StringValue(Concat(a.StringRef(), b.StringRef()))
	}
	pa := vm.ToPrimitive(a, HintDefault)
	pb := vm.ToPrimitive(b, HintDefault)
	if pa.IsString() || pb.IsString() {
		return StringValue(Concat(vm.ToString(pa), vm.ToString(pb)))
	}
	return vm.numericOp(opAdd, pa, pb)
}

type arithOp uint8

const (
	opAdd arithOp = iota
	opSub
	opMul
	opDiv
	opMod
	opPow
	opLshift
	opRshift
	opUrshift
	opBitand
	opBitor
	opBitxor
)

// Arith applies a binary numeric operator other than +.
func (vm *VM) Arith(op arithOp, a, b Value) Value {
	if a.IsNumber() && b.IsNumber() {
		return Number(numberOp(op, a.num(), b.num()))
	}
	return vm.numericOp(op, a, b)
}

func (vm *VM) numericOp(op arithOp, a, b Value) Value {
	na := vm.ToNumeric(a)
	nb := vm.ToNumeric(b)
	if na.Kind() != nb.Kind() {
		vm.throwTypeError("Invalid mix of BigInt and other type in %s.", opName(op))
	}
	if na.IsNumber() {
		return Number(numberOp(op, na.num(), nb.num()))
	}
	return BigIntValue(vm.bigIntOp(op, na.BigIntRef(), nb.BigIntRef()))
}

func opName(op arithOp) string {
	return [...]string{"addition", "subtraction", "multiplication", "division", "remainder", "exponentiation",
		"left shift", "right shift", "unsigned right shift", "bitwise and", "bitwise or", "bitwise xor"}[op]
}

func numberOp(op arithOp, x, y float64) float64 {
	switch op {
	case opAdd:
		return x + y
	case opSub:
		return x - y
	case opMul:
		return x * y
	case opDiv:
		return x / y
	case opMod:
		return numberMod(x, y)
	case opPow:
		return numberPow(x, y)
	case opLshift:
		return float64(toInt32(x) << (toUint32(y) & 31))
	case opRshift:
		return float64(toInt32(x) >> (toUint32(y) & 31))
	case opUrshift:
		return float64(toUint32(x) >> (toUint32(y) & 31))
	case opBitand:
		return float64(toInt32(x) & toInt32(y))
	case opBitor:
		return float64(toInt32(x) | toInt32(y))
	case opBitxor:
		return float64(toInt32(x) ^ toInt32(y))
	}
	panic("unreachable")
}

func numberMod(x, y float64) float64 {
	if ix, iy := int32(x), int32(y); float64(ix) == x && float64(iy) == y && iy > 0 && ix > 0 {
		return float64(ix % iy)
	}
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || y == 0 {
		return math.NaN()
	}
	if math.IsInf(y, 0) || x == 0 {
		return x
	}
	return math.Mod(x, y)
}

// numberPow is Number::exponentiate.
func numberPow(x, y float64) float64 {
	if math.IsNaN(y) {
		return math.NaN()
	}
	if y == 0 {
		return 1
	}
	if (x == 1 || x == -1) && math.IsInf(y, 0) {
		return math.NaN()
	}
	return math.Pow(x, y)
}

func (vm *VM) bigIntOp(op arithOp, x, y *big.Int) *big.Int {
	z := new(big.Int)
	switch op {
	case opAdd:
		return z.Add(x, y)
	case opSub:
		return z.Sub(x, y)
	case opMul:
		return z.Mul(x, y)
	case opDiv:
		if y.Sign() == 0 {
			vm.throwRangeError("0 is an invalid divisor value.")
		}
		return z.Quo(x, y)
	case opMod:
		if y.Sign() == 0 {
			vm.throwRangeError("0 is an invalid divisor value.")
		}
		return z.Rem(x, y)
	case opPow:
		if y.Sign() < 0 {
			vm.throwRangeError("Negative exponent is not allowed")
		}
		if y.BitLen() > 32 && x.CmpAbs(big.NewInt(1)) > 0 {
			vm.throwRangeError("Maximum BigInt size exceeded")
		}
		return z.Exp(x, y, nil)
	case opLshift, opRshift:
		shift := y
		if op == opRshift {
			shift = new(big.Int).Neg(y)
		}
		if shift.Sign() >= 0 {
			if shift.BitLen() > 31 {
				if x.Sign() == 0 {
					return z
				}
				vm.throwRangeError("Maximum BigInt size exceeded")
			}
			return z.Lsh(x, uint(shift.Int64()))
		}
		n := new(big.Int).Neg(shift)
		if n.BitLen() > 31 {
			if x.Sign() < 0 {
				return z.SetInt64(-1)
			}
			return z
		}
		return z.Rsh(x, uint(n.Int64())) // floor division, like BigInt::signedRightShift
	case opUrshift:
		vm.throwTypeError("BigInts have no unsigned right shift, use >> instead")
	case opBitand:
		return z.And(x, y)
	case opBitor:
		return z.Or(x, y)
	case opBitxor:
		return z.Xor(x, y)
	}
	panic("unreachable")
}

// Negate is unary minus.
func (vm *VM) Negate(v Value) Value {
	if v.IsNumber() {
		return Number(-v.num())
	}
	n := vm.ToNumeric(v)
	if n.IsBigInt() {
		return BigIntValue(new(big.Int).Neg(n.BigIntRef()))
	}
	return Number(-n.num())
}

// BitNot is ~.
func (vm *VM) BitNot(v Value) Value {
	n := vm.ToNumeric(v)
	if n.IsBigInt() {
		return BigIntValue(new(big.Int).Not(n.BigIntRef()))
	}
	return Number(float64(^toInt32(n.num())))
}

// Increment is ++ on a numeric value (after ToNumeric).
func (vm *VM) Increment(v Value, delta int64) Value {
	if v.IsNumber() {
		return Number(v.num() + float64(delta))
	}
	n := vm.ToNumeric(v)
	if n.IsBigInt() {
		return BigIntValue(new(big.Int).Add(n.BigIntRef(), big.NewInt(delta)))
	}
	return Number(n.num() + float64(delta))
}

// maxBigIntBits bounds the size of BigInt results (like V8's kMaxLengthBits).
const maxBigIntBits = 1 << 30
