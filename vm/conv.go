package vm

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"

	"github.com/baslentfert/jsc2go/wtf/dtoa"
)

// Type conversions (ECMA-262 7.1) and the abstract comparison operations
// (7.2). JSC spreads these over JSCJSValue.cpp, JSString, NumberPrototype
// and Operations.h.

// Hint is the preferred type of ToPrimitive.
type Hint uint8

const (
	HintDefault Hint = iota
	HintNumber
	HintString
)

func (h Hint) String() string {
	switch h {
	case HintNumber:
		return "number"
	case HintString:
		return "string"
	}
	return "default"
}

// ToPrimitive (7.1.1).
func (vm *VM) ToPrimitive(v Value, hint Hint) Value {
	o := v.AsObject()
	if o == nil {
		return v
	}
	if o.class == ClassString {
		if s, ok := vm.realm.plainStringObject(o); ok {
			return StringValue(s)
		}
	}
	exoticToPrim := vm.GetMethod(v, SymKey(SymbolToPrimitive))
	if !exoticToPrim.IsUndefined() {
		result := vm.Call(exoticToPrim, v, Str(hint.String()))
		if result.IsObject() {
			vm.throwTypeError("Symbol.toPrimitive returned an object")
		}
		return result
	}
	if hint == HintDefault {
		hint = HintNumber
	}
	return vm.OrdinaryToPrimitive(o, hint)
}

// OrdinaryToPrimitive (7.1.1.1).
func (vm *VM) OrdinaryToPrimitive(o *Object, hint Hint) Value {
	methods := [2]string{"valueOf", "toString"}
	if hint == HintString {
		methods = [2]string{"toString", "valueOf"}
	}
	for _, name := range methods {
		method := o.GetStr(name)
		if method.IsCallable() {
			result := vm.Call(method, ObjectValue(o))
			if !result.IsObject() {
				return result
			}
		}
	}
	vm.throwTypeError("No default value")
	return Undefined
}

// ToNumber (7.1.4).
func (vm *VM) ToNumber(v Value) float64 {
	switch v.Kind() {
	case KindNumber:
		return v.num()
	case KindUndefined:
		return math.NaN()
	case KindNull:
		return 0
	case KindBool:
		return v.boolNumber()
	case KindString:
		return StringToNumber(v.StringRef())
	case KindSymbol:
		vm.throwTypeError("Cannot convert a symbol to a number")
	case KindBigInt:
		vm.throwTypeError("Conversion from 'BigInt' to 'number' is not allowed.")
	case KindObject:
		return vm.ToNumber(vm.ToPrimitive(v, HintNumber))
	}
	return math.NaN()
}

// ToNumeric (7.1.3) returns a number or a BigInt value.
func (vm *VM) ToNumeric(v Value) Value {
	if v.IsNumber() || v.IsBigInt() {
		return v
	}
	prim := vm.ToPrimitive(v, HintNumber)
	if prim.IsBigInt() {
		return prim
	}
	return Number(vm.ToNumber(prim))
}

// ToIntegerOrInfinity (7.1.5).
func (vm *VM) ToIntegerOrInfinity(v Value) float64 {
	return integerOrInfinity(vm.ToNumber(v))
}

func integerOrInfinity(f float64) float64 {
	if math.IsNaN(f) {
		return 0
	}
	f = math.Trunc(f)
	if f == 0 {
		return 0 // no -0
	}
	return f
}

func toInt32(f float64) int32 {
	// For |f| < 2^63 the low 32 bits of the truncated value are ToInt32's
	// modulo-2^32 result (NaN fails the comparison).
	if f > -9223372036854775808 && f < 9223372036854775808 {
		return int32(int64(f))
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	f = math.Mod(math.Trunc(f), 4294967296)
	if f < 0 {
		f += 4294967296
	}
	return int32(uint32(f))
}

func toUint32(f float64) uint32 { return uint32(toInt32(f)) }

func (vm *VM) ToInt32(v Value) int32 {
	if v.IsNumber() {
		return toInt32(v.num())
	}
	return toInt32(vm.ToNumber(v))
}

func (vm *VM) ToUint32(v Value) uint32 { return uint32(vm.ToInt32(v)) }

func (vm *VM) ToUint16(v Value) uint16 { return uint16(vm.ToInt32(v)) }

// ToLength (7.1.20).
func (vm *VM) ToLength(v Value) int64 {
	f := vm.ToIntegerOrInfinity(v)
	if f <= 0 {
		return 0
	}
	return int64(math.Min(f, 1<<53-1))
}

// ToIndex (7.1.22).
func (vm *VM) ToIndex(v Value) int64 {
	if v.IsUndefined() {
		return 0
	}
	f := vm.ToIntegerOrInfinity(v)
	if f < 0 || f > 1<<53-1 {
		vm.throwRangeError("Invalid index")
	}
	return int64(f)
}

// ToString (7.1.17).
func (vm *VM) ToString(v Value) *String {
	switch v.Kind() {
	case KindString:
		return v.StringRef()
	case KindNumber:
		return NumberToString(v.num())
	case KindUndefined:
		return strUndefined
	case KindNull:
		return strNull
	case KindBool:
		if v.BoolValue() {
			return strTrue
		}
		return strFalse
	case KindBigInt:
		return NewString(v.BigIntRef().String())
	case KindSymbol:
		vm.throwTypeError("Cannot convert a symbol to a string")
	case KindObject:
		return vm.ToString(vm.ToPrimitive(v, HintString))
	}
	return emptyString
}

// toGoString converts v with ToString and returns WTF-8 text.
func (vm *VM) toGoString(v Value) string { return vm.ToString(v).String() }

var (
	strUndefined = NewString("undefined")
	strNull      = NewString("null")
	strTrue      = NewString("true")
	strFalse     = NewString("false")
)

var smallIntStrings = func() []*String {
	s := make([]*String, 1024)
	for i := range s {
		s[i] = NewString(strconv.Itoa(i))
	}
	return s
}()

// NumberToString is Number::toString(x) for radix 10.
func NumberToString(f float64) *String {
	if i := int(f); float64(i) == f && i >= 0 && i < len(smallIntStrings) && !(f == 0 && math.Signbit(f)) {
		return smallIntStrings[i]
	}
	return NewString(dtoa.NumberToString(f))
}

// ToPropertyKey (7.1.19).
func (vm *VM) ToPropertyKey(v Value) PropertyKey {
	switch v.Kind() {
	case KindString:
		return v.StringRef().Key()
	case KindSymbol:
		return SymKey(v.SymbolRef())
	case KindNumber:
		if i := uint32(v.num()); float64(i) == v.num() && i != math.MaxUint32 && !(v.num() == 0 && math.Signbit(v.num())) {
			return IndexKey(i)
		}
	case KindObject:
		return vm.ToPropertyKey(vm.ToPrimitive(v, HintString))
	}
	return vm.ToString(v).Key()
}

// ToObject (7.1.18).
func (vm *VM) ToObject(v Value) *Object {
	switch v.Kind() {
	case KindObject:
		return v.ObjectRef()
	case KindUndefined, KindNull:
		vm.throwTypeError("Cannot convert %s to object", v.TypeOfForMessage())
	}
	return vm.realm.wrapPrimitive(v)
}

// TypeOfForMessage names a value's type in error messages.
func (v Value) TypeOfForMessage() string {
	switch v.Kind() {
	case KindUndefined:
		return "undefined"
	case KindNull:
		return "null"
	}
	return v.TypeOf()
}

// ToBoolean is Value.ToBoolean; ToBigInt (7.1.13) follows.
func (vm *VM) ToBigInt(v Value) *big.Int {
	prim := vm.ToPrimitive(v, HintNumber)
	switch prim.Kind() {
	case KindBigInt:
		return prim.BigIntRef()
	case KindBool:
		return big.NewInt(int64(prim.boolNumber()))
	case KindString:
		n, ok := StringToBigInt(prim.StringRef())
		if !ok {
			vm.throwSyntaxError("Failed to parse String to BigInt")
		}
		return n
	case KindUndefined, KindNull:
		vm.throwTypeError("Invalid argument type in ToBigInt operation")
	case KindNumber:
		vm.throwTypeError("Invalid argument type in ToBigInt operation")
	case KindSymbol:
		vm.throwTypeError("Invalid argument type in ToBigInt operation")
	}
	return nil
}

// --- String to number ------------------------------------------------------------------

func isStrWhiteSpaceChar(c uint16) bool {
	switch c {
	case '\t', '\n', 0x0B, 0x0C, '\r', ' ', 0xA0, 0x2028, 0x2029, 0xFEFF:
		return true
	}
	return c > 0xFF && unicode.Is(unicode.Zs, rune(c))
}

// trimJSSpace removes StrWhiteSpaceChars from both ends.
func trimJSSpace(s *String) *String {
	start, end := 0, s.Length()
	for start < end && isStrWhiteSpaceChar(s.At(start)) {
		start++
	}
	for end > start && isStrWhiteSpaceChar(s.At(end-1)) {
		end--
	}
	return s.Substring(start, end)
}

func trimJSSpaceStart(s *String) *String {
	start := 0
	for start < s.Length() && isStrWhiteSpaceChar(s.At(start)) {
		start++
	}
	return s.Substring(start, s.Length())
}

// StringToNumber (7.1.4.1.1).
func StringToNumber(str *String) float64 {
	s := trimJSSpace(str)
	if s.Length() == 0 {
		return 0
	}
	if !s.IsASCII() {
		return math.NaN()
	}
	t := s.asciiText()
	if len(t) > 2 && t[0] == '0' {
		radix := 0
		switch t[1] {
		case 'x', 'X':
			radix = 16
		case 'o', 'O':
			radix = 8
		case 'b', 'B':
			radix = 2
		}
		if radix != 0 {
			n, ok := new(big.Int).SetString(t[2:], radix)
			if !ok || strings.ContainsAny(t[2:], "_+-") {
				return math.NaN()
			}
			f, _ := new(big.Float).SetInt(n).Float64()
			return f
		}
	}
	switch t {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if !isStrDecimalLiteral(t) {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f
		}
		return math.NaN()
	}
	return f
}

// isStrDecimalLiteral checks StrDecimalLiteral without Infinity: an optional
// sign, digits with an optional fraction, and an optional exponent.
func isStrDecimalLiteral(t string) bool {
	i := 0
	if i < len(t) && (t[i] == '+' || t[i] == '-') {
		i++
	}
	digits := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
		digits++
	}
	if i < len(t) && t[i] == '.' {
		i++
		for i < len(t) && t[i] >= '0' && t[i] <= '9' {
			i++
			digits++
		}
	}
	if digits == 0 {
		return false
	}
	if i < len(t) && (t[i] == 'e' || t[i] == 'E') {
		i++
		if i < len(t) && (t[i] == '+' || t[i] == '-') {
			i++
		}
		expDigits := 0
		for i < len(t) && t[i] >= '0' && t[i] <= '9' {
			i++
			expDigits++
		}
		if expDigits == 0 {
			return false
		}
	}
	return i == len(t)
}

// StringToBigInt (7.1.14).
func StringToBigInt(str *String) (*big.Int, bool) {
	s := trimJSSpace(str)
	if s.Length() == 0 {
		return new(big.Int), true
	}
	if !s.IsASCII() {
		return nil, false
	}
	t := s.asciiText()
	radix := 10
	if len(t) > 2 && t[0] == '0' {
		switch t[1] {
		case 'x', 'X':
			radix = 16
		case 'o', 'O':
			radix = 8
		case 'b', 'B':
			radix = 2
		}
		if radix != 10 {
			t = t[2:]
		}
	}
	neg := false
	if radix == 10 && len(t) > 0 && (t[0] == '+' || t[0] == '-') {
		neg = t[0] == '-'
		t = t[1:]
	}
	if len(t) == 0 {
		return nil, false
	}
	for i := 0; i < len(t); i++ {
		if digitValue(t[i]) >= radix {
			return nil, false
		}
	}
	n, ok := new(big.Int).SetString(t, radix)
	if !ok {
		return nil, false
	}
	if neg {
		n.Neg(n)
	}
	return n, true
}

func digitValue(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	}
	return 99
}

// --- Equality and comparison ---------------------------------------------------------

// IsLooselyEqual (7.2.14).
func (vm *VM) IsLooselyEqual(x, y Value) bool {
	for {
		if x.Kind() == y.Kind() {
			return StrictEquals(x, y)
		}
		switch {
		case x.isNullishOrHTMLDDA() && y.isNullishOrHTMLDDA():
			return true
		case x.IsNumber() && y.IsString():
			return x.num() == StringToNumber(y.StringRef())
		case x.IsString() && y.IsNumber():
			return StringToNumber(x.StringRef()) == y.num()
		case x.IsBigInt() && y.IsString():
			n, ok := StringToBigInt(y.StringRef())
			return ok && x.BigIntRef().Cmp(n) == 0
		case x.IsString() && y.IsBigInt():
			x, y = y, x
			continue
		case x.IsBool():
			x = Number(x.boolNumber())
			continue
		case y.IsBool():
			y = Number(y.boolNumber())
			continue
		case x.IsObject() && (y.IsString() || y.IsNumber() || y.IsBigInt() || y.IsSymbol()):
			x = vm.ToPrimitive(x, HintDefault)
			continue
		case y.IsObject() && (x.IsString() || x.IsNumber() || x.IsBigInt() || x.IsSymbol()):
			y = vm.ToPrimitive(y, HintDefault)
			continue
		case x.IsBigInt() && y.IsNumber():
			return compareBigIntNumber(x.BigIntRef(), y.num()) == cmpEqual
		case x.IsNumber() && y.IsBigInt():
			return compareBigIntNumber(y.BigIntRef(), x.num()) == cmpEqual
		}
		return false
	}
}

type cmpResult int8

const (
	cmpLess cmpResult = iota - 1
	cmpEqual
	cmpGreater
	cmpUndefined
)

// compareBigIntNumber compares b with f exactly.
func compareBigIntNumber(b *big.Int, f float64) cmpResult {
	switch {
	case math.IsNaN(f):
		return cmpUndefined
	case math.IsInf(f, 1):
		return cmpLess
	case math.IsInf(f, -1):
		return cmpGreater
	}
	bf := new(big.Float).SetInt(b)
	return cmpResult(bf.Cmp(big.NewFloat(f)))
}

// lessThan is IsLessThan (7.2.13); it returns cmpUndefined for NaN
// operands, otherwise whether x < y as cmpLess (true) or cmpEqual (false).
func (vm *VM) lessThan(x, y Value, leftFirst bool) (less bool, undefined bool) {
	if x.IsNumber() && y.IsNumber() {
		if math.IsNaN(x.num()) || math.IsNaN(y.num()) {
			return false, true
		}
		return x.num() < y.num(), false
	}
	var px, py Value
	if leftFirst {
		px = vm.ToPrimitive(x, HintNumber)
		py = vm.ToPrimitive(y, HintNumber)
	} else {
		py = vm.ToPrimitive(y, HintNumber)
		px = vm.ToPrimitive(x, HintNumber)
	}
	if px.IsString() && py.IsString() {
		return px.StringRef().Compare(py.StringRef()) < 0, false
	}
	if px.IsBigInt() && py.IsString() {
		ny, ok := StringToBigInt(py.StringRef())
		if !ok {
			return false, true
		}
		return px.BigIntRef().Cmp(ny) < 0, false
	}
	if px.IsString() && py.IsBigInt() {
		nx, ok := StringToBigInt(px.StringRef())
		if !ok {
			return false, true
		}
		return nx.Cmp(py.BigIntRef()) < 0, false
	}
	nx := vm.ToNumeric(px)
	ny := vm.ToNumeric(py)
	switch {
	case nx.IsNumber() && ny.IsNumber():
		if math.IsNaN(nx.num()) || math.IsNaN(ny.num()) {
			return false, true
		}
		return nx.num() < ny.num(), false
	case nx.IsBigInt() && ny.IsBigInt():
		return nx.BigIntRef().Cmp(ny.BigIntRef()) < 0, false
	case nx.IsBigInt():
		r := compareBigIntNumber(nx.BigIntRef(), ny.num())
		return r == cmpLess, r == cmpUndefined
	default:
		r := compareBigIntNumber(ny.BigIntRef(), nx.num())
		return r == cmpGreater, r == cmpUndefined
	}
}

// Less etc. are the relational operators.
func (vm *VM) Less(x, y Value) bool {
	r, u := vm.lessThan(x, y, true)
	return r && !u
}

func (vm *VM) Greater(x, y Value) bool {
	r, u := vm.lessThan(y, x, false)
	return r && !u
}

func (vm *VM) LessEq(x, y Value) bool {
	r, u := vm.lessThan(y, x, false)
	return !r && !u
}

func (vm *VM) GreaterEq(x, y Value) bool {
	r, u := vm.lessThan(x, y, true)
	return !r && !u
}

// safeToString formats a primitive without running user code.
func safeToString(v Value) string {
	switch v.Kind() {
	case KindUndefined:
		return "undefined"
	case KindNull:
		return "null"
	case KindBool:
		if v.BoolValue() {
			return "true"
		}
		return "false"
	case KindNumber:
		return dtoa.NumberToString(v.num())
	case KindString:
		return v.StringRef().GoString()
	case KindBigInt:
		return v.BigIntRef().String() + "n"
	case KindSymbol:
		return v.SymbolRef().DescriptiveString()
	case KindObject:
		if v.ObjectRef().IsCallable() {
			return "function"
		}
		return "[object Object]"
	}
	return "<empty>"
}
