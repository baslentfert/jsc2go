package parser

// ResultType is a static approximation of the type an expression produces,
// used by the bytecode generator to pick specialized opcodes. Port of
// JavaScriptCore/parser/ResultType.h.
type ResultType uint8

const (
	resultTypeInt32       ResultType = 1 << 0
	resultTypeMaybeNumber ResultType = 1 << 1
	resultTypeMaybeString ResultType = 1 << 2
	resultTypeMaybeBigInt ResultType = 1 << 3
	resultTypeMaybeNull   ResultType = 1 << 4
	resultTypeMaybeBool   ResultType = 1 << 5
	resultTypeMaybeOther  ResultType = 1 << 6
	resultTypeBits                   = resultTypeMaybeNumber | resultTypeMaybeString | resultTypeMaybeBigInt | resultTypeMaybeNull | resultTypeMaybeBool | resultTypeMaybeOther
)

func NullType() ResultType           { return resultTypeMaybeNull }
func BooleanType() ResultType        { return resultTypeMaybeBool }
func NumberType() ResultType         { return resultTypeMaybeNumber }
func NumberTypeIsInt32() ResultType  { return resultTypeInt32 | resultTypeMaybeNumber }
func StringOrNumberType() ResultType { return resultTypeMaybeNumber | resultTypeMaybeString }
func AddResultType() ResultType {
	return resultTypeMaybeNumber | resultTypeMaybeString | resultTypeMaybeBigInt
}
func StringType() ResultType { return resultTypeMaybeString }
func BigIntType() ResultType { return resultTypeMaybeBigInt }
func BigIntOrInt32Type() ResultType {
	return resultTypeMaybeBigInt | resultTypeInt32 | resultTypeMaybeNumber
}
func BigIntOrNumberType() ResultType { return resultTypeMaybeBigInt | resultTypeMaybeNumber }
func UnknownType() ResultType        { return resultTypeBits }

func (t ResultType) IsInt32() bool             { return t&resultTypeInt32 != 0 }
func (t ResultType) DefinitelyIsNumber() bool  { return t&resultTypeBits == resultTypeMaybeNumber }
func (t ResultType) DefinitelyIsString() bool  { return t&resultTypeBits == resultTypeMaybeString }
func (t ResultType) DefinitelyIsBoolean() bool { return t&resultTypeBits == resultTypeMaybeBool }
func (t ResultType) DefinitelyIsBigInt() bool  { return t&resultTypeBits == resultTypeMaybeBigInt }
func (t ResultType) DefinitelyIsNull() bool    { return t&resultTypeBits == resultTypeMaybeNull }
func (t ResultType) MightBeUndefinedOrNull() bool {
	return t&(resultTypeMaybeNull|resultTypeMaybeOther) != 0
}
func (t ResultType) MightBeNumber() bool { return t&resultTypeMaybeNumber != 0 }
func (t ResultType) IsNotNumber() bool   { return !t.MightBeNumber() }
func (t ResultType) MightBeBigInt() bool { return t&resultTypeMaybeBigInt != 0 }
func (t ResultType) IsNotBigInt() bool   { return !t.MightBeBigInt() }

func ResultTypeForAdd(op1, op2 ResultType) ResultType {
	switch {
	case op1.DefinitelyIsNumber() && op2.DefinitelyIsNumber():
		return NumberType()
	case op1.DefinitelyIsString() || op2.DefinitelyIsString():
		return StringType()
	case op1.DefinitelyIsBigInt() && op2.DefinitelyIsBigInt():
		return BigIntType()
	}
	return AddResultType()
}

func ResultTypeForNonAddArith(op1, op2 ResultType) ResultType {
	switch {
	case op1.DefinitelyIsNumber() && op2.DefinitelyIsNumber():
		return NumberType()
	case op1.DefinitelyIsBigInt() && op2.DefinitelyIsBigInt():
		return BigIntType()
	}
	return BigIntOrNumberType()
}

func ResultTypeForUnaryArith(op ResultType) ResultType {
	switch {
	case op.DefinitelyIsNumber():
		return NumberType()
	case op.DefinitelyIsBigInt():
		return BigIntType()
	}
	return BigIntOrNumberType()
}

// ResultTypeForLogicalOp: a logical op yields one of its operands, not a boolean.
func ResultTypeForLogicalOp(op1, op2 ResultType) ResultType {
	switch {
	case op1.DefinitelyIsBoolean() && op2.DefinitelyIsBoolean():
		return BooleanType()
	case op1.DefinitelyIsNumber() && op2.DefinitelyIsNumber():
		return NumberType()
	case op1.DefinitelyIsString() && op2.DefinitelyIsString():
		return StringType()
	case op1.DefinitelyIsBigInt() && op2.DefinitelyIsBigInt():
		return BigIntType()
	}
	return UnknownType()
}

func ResultTypeForCoalesce(op1, op2 ResultType) ResultType {
	switch {
	case op1.DefinitelyIsNull():
		return op2
	case !op1.MightBeUndefinedOrNull():
		return op1
	}
	return UnknownType()
}

func ResultTypeForBitOp() ResultType { return BigIntOrInt32Type() }

// OperandTypes packs the result types of a binary operation's two operands.
type OperandTypes struct {
	First, Second ResultType
}

func (o OperandTypes) Bits() uint16 { return uint16(o.First) | uint16(o.Second)<<8 }

func OperandTypesFromBits(bits uint16) OperandTypes {
	return OperandTypes{First: ResultType(bits), Second: ResultType(bits >> 8)}
}
