package vm

import (
	"encoding/binary"
	"math"
	"math/big"
	"strconv"
	"unsafe"
)

// ArrayBuffer storage and TypedArray exotic objects (JSArrayBuffer,
// ArrayBufferView, JSGenericTypedArrayView; ECMA-262 25.1, 10.4.5, 23.2).

type arrayBufferData struct {
	data      []byte
	detached  bool
	shared    bool
	resizable bool
	maxLength int
	detachKey Value
}

func (b *arrayBufferData) byteLength() int {
	if b.detached {
		return 0
	}
	return len(b.data)
}

func bufferOf(o *Object) *arrayBufferData {
	if b, ok := o.internal.(*arrayBufferData); ok {
		return b
	}
	return nil
}

type typedArrayKind uint8

const (
	kindInt8 typedArrayKind = iota
	kindUint8
	kindUint8Clamped
	kindInt16
	kindUint16
	kindInt32
	kindUint32
	kindFloat16
	kindFloat32
	kindFloat64
	kindBigInt64
	kindBigUint64
)

var typedArrayNames = [numberOfTypedArrayKinds]string{"Int8Array", "Uint8Array", "Uint8ClampedArray", "Int16Array",
	"Uint16Array", "Int32Array", "Uint32Array", "Float16Array", "Float32Array", "Float64Array", "BigInt64Array", "BigUint64Array"}

var typedArrayElementSize = [numberOfTypedArrayKinds]int{1, 1, 1, 2, 2, 4, 4, 2, 4, 8, 8, 8}

func (k typedArrayKind) size() int      { return typedArrayElementSize[k] }
func (k typedArrayKind) isBigInt() bool { return k == kindBigInt64 || k == kindBigUint64 }
func (k typedArrayKind) String() string { return typedArrayNames[k] }

// typedArray is [[ViewedArrayBuffer]], [[TypedArrayName]], [[ByteOffset]]
// and [[ArrayLength]] (arrayLength < 0: length-tracking).
type typedArray struct {
	kind        typedArrayKind
	buffer      *Object
	byteOffset  int
	arrayLength int
	// vector points at element 0 and vlen counts the elements generated
	// code may access there without looking at the buffer, while the VM's
	// detachEpoch is still vepoch (JSC's JSArrayBufferView keeps its
	// vector and length in the view too). vlen is 0 for views of
	// resizable buffers, which can shrink.
	vector unsafe.Pointer
	vlen   int
	vepoch uint64
}

// newTypedArrayData makes the typedArray of a view of buffer.
func (vm *VM) newTypedArrayData(kind typedArrayKind, buffer *Object, byteOffset, length int) *typedArray {
	t := &typedArray{kind: kind, buffer: buffer, byteOffset: byteOffset, arrayLength: length}
	b := bufferOf(buffer)
	if b != nil && !b.detached && !b.resizable && length > 0 && byteOffset+length*kind.size() <= len(b.data) {
		t.vector, t.vlen, t.vepoch = unsafe.Pointer(&b.data[byteOffset]), length, vm.detachEpoch
	}
	return t
}

func (t *typedArray) buf() *arrayBufferData { return bufferOf(t.buffer) }

// isOutOfBounds is IsTypedArrayOutOfBounds (10.4.5.12), detached included.
func (t *typedArray) isOutOfBounds() bool {
	b := t.buf()
	if b.detached {
		return true
	}
	bufLen := len(b.data)
	if t.byteOffset > bufLen {
		return true
	}
	if t.arrayLength >= 0 && t.byteOffset+t.arrayLength*t.kind.size() > bufLen {
		return true
	}
	return false
}

// length is TypedArrayLength (0 when out of bounds).
func (t *typedArray) length() int {
	if t.isOutOfBounds() {
		return 0
	}
	if t.arrayLength >= 0 {
		return t.arrayLength
	}
	return (len(t.buf().data) - t.byteOffset) / t.kind.size()
}

func (t *typedArray) byteLength() int { return t.length() * t.kind.size() }

// isValidIndex is IsValidIntegerIndex.
func (t *typedArray) isValidIndex(index float64) bool {
	if t.buf().detached {
		return false
	}
	if index != math.Trunc(index) || index == 0 && math.Signbit(index) {
		return false
	}
	return index >= 0 && index < float64(t.length())
}

// --- Element conversion --------------------------------------------------------------

func (vm *VM) toTypedValue(kind typedArrayKind, v Value) Value {
	if kind.isBigInt() {
		return BigIntValue(vm.ToBigInt(v))
	}
	return Number(vm.ToNumber(v))
}

// encodeElement writes the (already converted) numeric value to b.
func encodeElement(kind typedArrayKind, b []byte, v Value) {
	switch kind {
	case kindBigInt64, kindBigUint64:
		binary.LittleEndian.PutUint64(b, bigIntToUint64(v.BigIntRef()))
		return
	}
	f := v.Float()
	switch kind {
	case kindInt8, kindUint8:
		b[0] = byte(toInt32(f))
	case kindUint8Clamped:
		b[0] = clampUint8(f)
	case kindInt16, kindUint16:
		binary.LittleEndian.PutUint16(b, uint16(toInt32(f)))
	case kindInt32, kindUint32:
		binary.LittleEndian.PutUint32(b, uint32(toInt32(f)))
	case kindFloat16:
		binary.LittleEndian.PutUint16(b, float64ToFloat16(f))
	case kindFloat32:
		binary.LittleEndian.PutUint32(b, math.Float32bits(float32(f)))
	case kindFloat64:
		binary.LittleEndian.PutUint64(b, math.Float64bits(f))
	}
}

func decodeElement(kind typedArrayKind, b []byte) Value {
	switch kind {
	case kindInt8:
		return Int(int(int8(b[0])))
	case kindUint8, kindUint8Clamped:
		return Int(int(b[0]))
	case kindInt16:
		return Int(int(int16(binary.LittleEndian.Uint16(b))))
	case kindUint16:
		return Int(int(binary.LittleEndian.Uint16(b)))
	case kindInt32:
		return Int(int(int32(binary.LittleEndian.Uint32(b))))
	case kindUint32:
		return Number(float64(binary.LittleEndian.Uint32(b)))
	case kindFloat16:
		return Number(float16ToFloat64(binary.LittleEndian.Uint16(b)))
	case kindFloat32:
		return Number(float64(math.Float32frombits(binary.LittleEndian.Uint32(b))))
	case kindFloat64:
		return Number(math.Float64frombits(binary.LittleEndian.Uint64(b)))
	case kindBigInt64:
		return BigIntValue(big.NewInt(int64(binary.LittleEndian.Uint64(b))))
	case kindBigUint64:
		return BigIntValue(new(big.Int).SetUint64(binary.LittleEndian.Uint64(b)))
	}
	return Undefined
}

func clampUint8(f float64) byte {
	switch {
	case math.IsNaN(f) || f <= 0:
		return 0
	case f >= 255:
		return 255
	}
	return byte(math.RoundToEven(f))
}

var two64 = new(big.Int).Lsh(big.NewInt(1), 64)

func bigIntToUint64(b *big.Int) uint64 {
	m := new(big.Int).Mod(b, two64)
	return m.Uint64()
}

// getElement reads element i (valid index) of t.
func (t *typedArray) getElement(i int) Value {
	size := t.kind.size()
	off := t.byteOffset + i*size
	return decodeElement(t.kind, t.buf().data[off:off+size])
}

func (t *typedArray) setElement(i int, v Value) {
	size := t.kind.size()
	off := t.byteOffset + i*size
	encodeElement(t.kind, t.buf().data[off:off+size], v)
}

// element returns the bytes of element i when i is a valid index of a
// typed array that is entirely in bounds (get_by_val's and put_by_val's
// fast path; everything else takes the spec path).
func (t *typedArray) element(i uint32) ([]byte, bool) {
	b := t.buf()
	size := t.kind.size()
	n := t.arrayLength
	if n < 0 {
		n = (len(b.data) - t.byteOffset) / size
	}
	if int64(i) >= int64(n) || t.byteOffset+n*size > len(b.data) || b.detached {
		return nil, false
	}
	off := t.byteOffset + int(i)*size
	return b.data[off : off+size], true
}

// fastGet reads element i, or reports false to take the spec path.
func (t *typedArray) fastGet(i uint32) (Value, bool) {
	if e, ok := t.element(i); ok {
		return decodeElement(t.kind, e), true
	}
	return Undefined, false
}

// fastSet writes number v to element i, or reports false to take the spec
// path (which also converts non-numbers, with their side effects).
func (t *typedArray) fastSet(i uint32, v Value) bool {
	if !v.IsNumber() || t.kind.isBigInt() {
		return false
	}
	e, ok := t.element(i)
	if ok {
		encodeElement(t.kind, e, v)
	}
	return ok
}

// typedArraySetElement is TypedArraySetElement (10.4.5.16).
func (vm *VM) typedArraySetElement(t *typedArray, index float64, v Value) {
	nv := vm.toTypedValue(t.kind, v)
	if t.isValidIndex(index) {
		t.setElement(int(index), nv)
	}
}

// canonicalNumericIndex is CanonicalNumericIndexString for a property key.
func canonicalNumericIndex(k PropertyKey) (float64, bool) {
	if k.IsSymbol() {
		return 0, false
	}
	if i, ok := k.Index(); ok {
		return float64(i), true
	}
	s := k.name.String()
	if s == "-0" {
		return math.Copysign(0, -1), true
	}
	if len(s) == 0 {
		return 0, false
	}
	switch c := s[0]; {
	case c == '-' || c == 'I' || c == 'N' || c >= '0' && c <= '9':
	default:
		return 0, false
	}
	n := StringToNumber(NewString(s))
	if NumberToString(n).String() != s {
		return 0, false
	}
	return n, true
}

// --- TypedArray exotic object -----------------------------------------------------------

type typedArrayObject struct{ ordinaryObject }

func typedArrayOf(o *Object) *typedArray {
	if t, ok := o.internal.(*typedArray); ok && o.class == ClassTypedArray {
		return t
	}
	return nil
}

func (typedArrayObject) getOwnProperty(o *Object, k PropertyKey) (Property, bool) {
	if n, ok := canonicalNumericIndex(k); ok {
		t := typedArrayOf(o)
		if !t.isValidIndex(n) {
			return Property{}, false
		}
		return Property{Value: t.getElement(int(n)), Flags: DefaultFlags}, true
	}
	return o.ordinaryGetOwnProperty(k)
}

func (typedArrayObject) hasProperty(o *Object, k PropertyKey) bool {
	if n, ok := canonicalNumericIndex(k); ok {
		return typedArrayOf(o).isValidIndex(n)
	}
	return o.ordinaryHasProperty(k)
}

func (typedArrayObject) defineOwnProperty(o *Object, k PropertyKey, d PropertyDescriptor) bool {
	if n, ok := canonicalNumericIndex(k); ok {
		t := typedArrayOf(o)
		if !t.isValidIndex(n) {
			return false
		}
		if d.Has&HasConfigurable != 0 && !d.Configurable || d.Has&HasEnumerable != 0 && !d.Enumerable ||
			d.IsAccessorDescriptor() || d.Has&HasWritable != 0 && !d.Writable {
			return false
		}
		if d.Has&HasValue != 0 {
			o.vm().typedArraySetElement(t, n, d.Value)
		}
		return true
	}
	return o.ordinaryDefineOwnProperty(k, d)
}

func (typedArrayObject) get(o *Object, k PropertyKey, receiver Value) Value {
	if n, ok := canonicalNumericIndex(k); ok {
		t := typedArrayOf(o)
		if !t.isValidIndex(n) {
			return Undefined
		}
		return t.getElement(int(n))
	}
	return o.ordinaryGet(k, receiver)
}

func (typedArrayObject) set(o *Object, k PropertyKey, v Value, receiver Value) bool {
	if n, ok := canonicalNumericIndex(k); ok {
		t := typedArrayOf(o)
		if receiver.AsObject() == o {
			o.vm().typedArraySetElement(t, n, v)
			return true
		}
		if !t.isValidIndex(n) {
			return true
		}
	}
	return o.ordinarySet(k, v, receiver)
}

func (typedArrayObject) delete(o *Object, k PropertyKey) bool {
	if n, ok := canonicalNumericIndex(k); ok {
		return !typedArrayOf(o).isValidIndex(n)
	}
	return o.ordinaryDelete(k)
}

func (typedArrayObject) ownKeys(o *Object) []PropertyKey {
	t := typedArrayOf(o)
	n := t.length()
	keys := make([]PropertyKey, 0, n+o.props.len())
	for i := 0; i < n; i++ {
		keys = append(keys, IndexKey(uint32(i)))
	}
	return append(keys, o.ordinaryOwnKeys()...)
}

// --- Helpers used by the builtins --------------------------------------------------------

// validateTypedArray is ValidateTypedArray (23.2.4.4); it returns the
// array and its length.
func (vm *VM) validateTypedArray(v Value, name string) (*Object, *typedArray, int) {
	o := v.AsObject()
	var t *typedArray
	if o != nil {
		t = typedArrayOf(o)
	}
	if t == nil {
		vm.throwTypeError("%s requires that |this| be a TypedArray", name)
	}
	if t.isOutOfBounds() {
		vm.throwTypeError("Underlying ArrayBuffer has been detached from the view or out-of-bounds")
	}
	return o, t, t.length()
}

func indexString(i int) string { return strconv.Itoa(i) }

const numberOfTypedArrayKinds = 12

// preventExtensions is the TypedArray [[PreventExtensions]]: a TypedArray
// whose length may still change (IsTypedArrayFixedLength false) stays
// extensible.
func (typedArrayObject) preventExtensions(o *Object) bool {
	t := typedArrayOf(o)
	if b := t.buf(); t.arrayLength < 0 || b.resizable && !b.shared {
		return false
	}
	o.ordinaryPreventExtensions()
	return true
}
