package vm

import (
	"math"
	"math/big"
	"strings"
)

// Atomics (AtomicsObject.cpp) and the Uint8Array base64/hex methods
// (JSGenericTypedArrayViewConstructor's fromBase64 and friends). The VM has
// a single agent, so atomics are plain memory operations.

func (r *Realm) initAtomics() {
	vm := r.vm
	a := r.NewObject()
	r.GlobalObject.putDirect(StrKey("Atomics"), ObjectValue(a), BuiltinFlags)
	r.toStringTag(a, "Atomics")

	// validate is ValidateIntegerTypedArray + ValidateAtomicAccess.
	var validateAccess func(c *Call, waitable bool, waitName string) (*typedArray, int)
	// waitName, when set, also requires shared memory (DoWait step 3).
	validateWait := func(c *Call, waitName string) (*typedArray, int) {
		return validateAccess(c, true, waitName)
	}
	validate := func(c *Call, waitable bool) (*typedArray, int) { return validateAccess(c, waitable, "") }
	validateAccess = func(c *Call, waitable bool, waitName string) (*typedArray, int) {
		_, t, n := vm.validateTypedArray(c.Arg(0), "Atomics")
		if waitable {
			if t.kind != kindInt32 && t.kind != kindBigInt64 {
				vm.throwTypeError("Atomics.wait requires an Int32Array or BigInt64Array")
			}
			if waitName != "" && !t.buf().shared {
				vm.throwTypeError("%s requires a shared buffer", waitName)
			}
		} else {
			switch t.kind {
			case kindFloat16, kindFloat32, kindFloat64, kindUint8Clamped:
				vm.throwTypeError("Atomics operations require an integer TypedArray")
			}
		}
		index := vm.ToIndex(c.Arg(1))
		if index >= int64(n) {
			vm.throwRangeError("Atomics access index out of range")
		}
		return t, int(index)
	}
	revalidate := func(t *typedArray, i int) {
		if t.isOutOfBounds() || i >= t.length() {
			vm.throwTypeError("TypedArray is detached or out of bounds")
		}
	}
	rmw := func(name string, op func(old, v Value, t *typedArray) Value) {
		r.method(a, name, 3, func(c *Call) Value {
			t, i := validate(c, false)
			v := vm.toTypedValue(t.kind, c.Arg(2))
			revalidate(t, i)
			defer lockShared(t)()
			old := t.getElement(i)
			t.setElement(i, op(old, v, t))
			return old
		})
	}
	bitOp := func(f func(x, y *big.Int) *big.Int) func(old, v Value, t *typedArray) Value {
		return func(old, v Value, t *typedArray) Value {
			if t.kind.isBigInt() {
				return BigIntValue(f(old.BigIntRef(), v.BigIntRef()))
			}
			x := big.NewInt(int64(old.Float()))
			y := big.NewInt(int64(toInt32(v.Float())))
			if !math.IsNaN(v.Float()) && !math.IsInf(v.Float(), 0) {
				y = big.NewInt(int64(math.Trunc(math.Mod(v.Float(), 1<<53))))
			}
			res := f(x, y)
			res.And(res, big.NewInt(1<<53-1))
			return Number(float64(res.Int64()))
		}
	}
	rmw("add", bitOp(func(x, y *big.Int) *big.Int { return new(big.Int).Add(x, y) }))
	rmw("sub", bitOp(func(x, y *big.Int) *big.Int { return new(big.Int).Sub(x, y) }))
	rmw("and", bitOp(func(x, y *big.Int) *big.Int { return new(big.Int).And(x, y) }))
	rmw("or", bitOp(func(x, y *big.Int) *big.Int { return new(big.Int).Or(x, y) }))
	rmw("xor", bitOp(func(x, y *big.Int) *big.Int { return new(big.Int).Xor(x, y) }))
	rmw("exchange", func(old, v Value, t *typedArray) Value { return v })
	r.method(a, "compareExchange", 4, func(c *Call) Value {
		t, i := validate(c, false)
		expected := vm.toTypedValue(t.kind, c.Arg(2))
		replacement := vm.toTypedValue(t.kind, c.Arg(3))
		revalidate(t, i)
		defer lockShared(t)()
		old := t.getElement(i)
		// Compare the raw bytes of the converted values.
		eb := make([]byte, t.kind.size())
		encodeElement(t.kind, eb, expected)
		if StrictEquals(decodeElement(t.kind, eb), old) {
			t.setElement(i, replacement)
		}
		return old
	})
	r.method(a, "load", 2, func(c *Call) Value {
		t, i := validate(c, false)
		revalidate(t, i)
		defer lockShared(t)()
		return t.getElement(i)
	})
	r.method(a, "store", 3, func(c *Call) Value {
		t, i := validate(c, false)
		v := c.Arg(2)
		if t.kind.isBigInt() {
			v = BigIntValue(vm.ToBigInt(v))
		} else {
			v = Number(vm.ToIntegerOrInfinity(v))
		}
		revalidate(t, i)
		defer lockShared(t)()
		t.setElement(i, v)
		return v
	})
	r.method(a, "isLockFree", 1, func(c *Call) Value {
		n := vm.ToIntegerOrInfinity(c.Arg(0))
		return Bool(n == 1 || n == 2 || n == 4 || n == 8)
	})
	r.method(a, "pause", 0, func(c *Call) Value {
		if n := c.Arg(0); !n.IsUndefined() && (!n.IsNumber() || !isIntegralNumber(n)) {
			vm.throwTypeError("Atomics.pause requires an integral number")
		}
		return Undefined
	})
	r.method(a, "notify", 3, func(c *Call) Value {
		t, i := validate(c, true)
		count := posInf
		if cnt := c.Arg(2); !cnt.IsUndefined() {
			count = math.Max(vm.ToIntegerOrInfinity(cnt), 0)
		}
		if !t.buf().shared {
			return Int(0)
		}
		return Int(vm.notifyWaiters(t, i, count))
	})
	r.method(a, "wait", 4, func(c *Call) Value {
		t, i := validateWait(c, "Atomics.wait")
		if !t.buf().shared {
			vm.throwTypeError("Atomics.wait requires a shared buffer")
		}
		v := vm.toTypedValue(t.kind, c.Arg(2))
		q := vm.ToNumber(c.Arg(3))
		if math.IsNaN(q) {
			q = posInf
		}
		q = math.Max(q, 0)
		if !vm.CanBlock {
			vm.throwTypeError("Atomics.wait cannot be called in this context")
		}
		return Str(vm.waitSync(t, i, v, q))
	})
	r.method(a, "waitAsync", 4, func(c *Call) Value {
		t, i := validateWait(c, "Atomics.waitAsync")
		if !t.buf().shared {
			vm.throwTypeError("Atomics.waitAsync requires a shared buffer")
		}
		v := vm.toTypedValue(t.kind, c.Arg(2))
		q := vm.ToNumber(c.Arg(3))
		if math.IsNaN(q) {
			q = posInf
		}
		q = math.Max(q, 0)
		result := c.Realm().NewObject()
		p := c.Realm().newPromise()
		if outcome := vm.waitAsync(t, i, v, q, p); outcome != "" {
			result.CreateDataProperty(StrKey("async"), False)
			result.CreateDataProperty(StrKey("value"), Str(outcome))
			return ObjectValue(result)
		}
		result.CreateDataProperty(StrKey("async"), True)
		result.CreateDataProperty(StrKey("value"), ObjectValue(p))
		return ObjectValue(result)
	})
}

// --- Uint8Array base64 / hex ------------------------------------------------------------

const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
const base64URLAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

func (r *Realm) initUint8ArrayBase64() {
	vm := r.vm
	u8 := r.TypedArrayConstructors[kindUint8]
	proto := r.TypedArrayPrototypes[kindUint8]

	type b64opts struct {
		url       bool
		lastChunk string
	}
	readOptions := func(v Value, wantLastChunk bool) b64opts {
		opts := b64opts{lastChunk: "loose"}
		if v.IsUndefined() {
			return opts
		}
		o := v.AsObject()
		if o == nil {
			vm.throwTypeError("options must be an object")
		}
		alpha := o.GetStr("alphabet")
		if !alpha.IsUndefined() {
			if !alpha.IsString() {
				vm.throwTypeError("alphabet must be a string")
			}
			switch alpha.StringRef().String() {
			case "base64":
			case "base64url":
				opts.url = true
			default:
				vm.throwTypeError("invalid alphabet")
			}
		}
		if wantLastChunk {
			lc := o.GetStr("lastChunkHandling")
			if !lc.IsUndefined() {
				if !lc.IsString() {
					vm.throwTypeError("lastChunkHandling must be a string")
				}
				switch s := lc.StringRef().String(); s {
				case "loose", "strict", "stop-before-partial":
					opts.lastChunk = s
				default:
					vm.throwTypeError("invalid lastChunkHandling")
				}
			}
		}
		return opts
	}
	requireString := func(v Value) *String {
		if !v.IsString() {
			vm.throwTypeError("argument must be a string")
		}
		return v.StringRef()
	}
	throwSyntax := func() { vm.throwSyntaxError("Invalid base64 or hex string") }

	r.method(u8, "fromBase64", 1, func(c *Call) Value {
		s := requireString(c.Arg(0))
		opts := readOptions(c.Arg(1), true)
		bytes, _, err := fromBase64(s, opts.url, opts.lastChunk, -1)
		if err {
			throwSyntax()
		}
		o := vm.newTypedArrayOfKind(kindUint8, len(bytes))
		copy(typedArrayOf(o).buf().data, bytes)
		return ObjectValue(o)
	})
	r.method(u8, "fromHex", 1, func(c *Call) Value {
		s := requireString(c.Arg(0))
		bytes, _, err := fromHex(s, -1)
		if err {
			throwSyntax()
		}
		o := vm.newTypedArrayOfKind(kindUint8, len(bytes))
		copy(typedArrayOf(o).buf().data, bytes)
		return ObjectValue(o)
	})
	thisU8 := func(c *Call, name string) *typedArray {
		o := c.This.AsObject()
		var t *typedArray
		if o != nil {
			t = typedArrayOf(o)
		}
		if t == nil || t.kind != kindUint8 {
			vm.throwTypeError("Uint8Array.prototype.%s requires a Uint8Array", name)
		}
		return t
	}
	result := func(c *Call, read, written int) Value {
		o := c.Realm().NewObject()
		o.CreateDataProperty(StrKey("read"), Int(read))
		o.CreateDataProperty(StrKey("written"), Int(written))
		return ObjectValue(o)
	}
	r.method(proto, "setFromBase64", 1, func(c *Call) Value {
		t := thisU8(c, "setFromBase64")
		s := requireString(c.Arg(0))
		opts := readOptions(c.Arg(1), true)
		if t.isOutOfBounds() {
			vm.throwTypeError("TypedArray is detached or out of bounds")
		}
		n := t.length()
		bytes, read, err := fromBase64(s, opts.url, opts.lastChunk, n)
		copy(t.buf().data[t.byteOffset:], bytes)
		if err {
			throwSyntax()
		}
		return result(c, read, len(bytes))
	})
	r.method(proto, "setFromHex", 1, func(c *Call) Value {
		t := thisU8(c, "setFromHex")
		s := requireString(c.Arg(0))
		if t.isOutOfBounds() {
			vm.throwTypeError("TypedArray is detached or out of bounds")
		}
		bytes, read, err := fromHex(s, t.length())
		copy(t.buf().data[t.byteOffset:], bytes)
		if err {
			throwSyntax()
		}
		return result(c, read, len(bytes))
	})
	r.method(proto, "toBase64", 0, func(c *Call) Value {
		t := thisU8(c, "toBase64")
		opts := readOptions(c.Arg(0), false)
		omitPadding := false
		if o := c.Arg(0).AsObject(); o != nil {
			omitPadding = o.GetStr("omitPadding").ToBoolean()
		}
		if t.isOutOfBounds() {
			vm.throwTypeError("TypedArray is detached or out of bounds")
		}
		data := t.buf().data[t.byteOffset : t.byteOffset+t.length()]
		alphabet := base64Alphabet
		if opts.url {
			alphabet = base64URLAlphabet
		}
		var b strings.Builder
		for i := 0; i < len(data); i += 3 {
			chunk := uint32(data[i]) << 16
			n := 1
			if i+1 < len(data) {
				chunk |= uint32(data[i+1]) << 8
				n++
			}
			if i+2 < len(data) {
				chunk |= uint32(data[i+2])
				n++
			}
			for j := 0; j < 4; j++ {
				if j <= n {
					b.WriteByte(alphabet[chunk>>(18-6*j)&63])
				} else if !omitPadding {
					b.WriteByte('=')
				}
			}
		}
		return Str(b.String())
	})
	r.method(proto, "toHex", 0, func(c *Call) Value {
		t := thisU8(c, "toHex")
		if t.isOutOfBounds() {
			vm.throwTypeError("TypedArray is detached or out of bounds")
		}
		const digits = "0123456789abcdef"
		var b strings.Builder
		for _, x := range t.buf().data[t.byteOffset : t.byteOffset+t.length()] {
			b.WriteByte(digits[x>>4])
			b.WriteByte(digits[x&15])
		}
		return Str(b.String())
	})
}

// fromBase64 is FromBase64 (Uint8Array base64 proposal); maxLength < 0 means
// unlimited. It returns the bytes, the number of code units read and
// whether a syntax error occurred (bytes decoded before the error are kept).
func fromBase64(s *String, url bool, lastChunk string, maxLength int) ([]byte, int, bool) {
	if maxLength == 0 {
		return nil, 0, false
	}
	n := s.Length()
	var bytes []byte
	read := 0
	var chunk []int
	chunkStart := 0
	index := 0
	isSpace := func(c uint16) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' }
	decodeChunk := func(ch []int, throwOnExtraBits bool) ([]byte, bool) {
		if len(ch) == 2 {
			v := ch[0]<<18 | ch[1]<<12
			if throwOnExtraBits && v&0xFFFF != 0 {
				return nil, true
			}
			return []byte{byte(v >> 16)}, false
		}
		if len(ch) == 3 {
			v := ch[0]<<18 | ch[1]<<12 | ch[2]<<6
			if throwOnExtraBits && v&0xFF != 0 {
				return nil, true
			}
			return []byte{byte(v >> 16), byte(v >> 8)}, false
		}
		v := ch[0]<<18 | ch[1]<<12 | ch[2]<<6 | ch[3]
		return []byte{byte(v >> 16), byte(v >> 8), byte(v)}, false
	}
	alphabetValue := func(c uint16) int {
		switch {
		case c >= 'A' && c <= 'Z':
			return int(c - 'A')
		case c >= 'a' && c <= 'z':
			return int(c-'a') + 26
		case c >= '0' && c <= '9':
			return int(c-'0') + 52
		case !url && c == '+', url && c == '-':
			return 62
		case !url && c == '/', url && c == '_':
			return 63
		}
		return -1
	}
	for {
		for index < n && isSpace(s.At(index)) {
			index++
		}
		if index == n {
			if len(chunk) > 0 {
				if lastChunk == "stop-before-partial" {
					return bytes, read, false
				}
				if lastChunk == "strict" || len(chunk) == 1 {
					return bytes, read, true
				}
				if maxLength >= 0 && len(bytes)+len(chunk)-1 > maxLength {
					return bytes, read, false
				}
				dec, _ := decodeChunk(chunk, false)
				bytes = append(bytes, dec...)
				read = n
			}
			return bytes, n, false
		}
		c := s.At(index)
		index++
		if c == '=' {
			if len(chunk) < 2 {
				return bytes, read, true
			}
			for index < n && isSpace(s.At(index)) {
				index++
			}
			if len(chunk) == 2 {
				if index == n {
					if lastChunk == "stop-before-partial" {
						return bytes, read, false
					}
					return bytes, read, true
				}
				if s.At(index) == '=' {
					index++
					for index < n && isSpace(s.At(index)) {
						index++
					}
				}
			}
			if index < n {
				return bytes, read, true
			}
			if maxLength >= 0 && len(bytes)+len(chunk)-1 > maxLength {
				return bytes, read, false
			}
			dec, bad := decodeChunk(chunk, lastChunk == "strict")
			if bad {
				return bytes, read, true
			}
			bytes = append(bytes, dec...)
			return bytes, n, false
		}
		v := alphabetValue(c)
		if v < 0 {
			return bytes, read, true
		}
		remaining := -1
		if maxLength >= 0 {
			remaining = maxLength - len(bytes)
		}
		if remaining == 1 && len(chunk) == 2 || remaining == 2 && len(chunk) == 3 {
			return bytes, read, false
		}
		if len(chunk) == 0 {
			chunkStart = index - 1
		}
		_ = chunkStart
		chunk = append(chunk, v)
		if len(chunk) == 4 {
			dec, _ := decodeChunk(chunk, false)
			bytes = append(bytes, dec...)
			chunk = chunk[:0]
			read = index
			if maxLength >= 0 && len(bytes) == maxLength {
				return bytes, read, false
			}
		}
	}
}

// fromHex is FromHex; maxLength < 0 means unlimited.
func fromHex(s *String, maxLength int) ([]byte, int, bool) {
	n := s.Length()
	if n%2 != 0 {
		return nil, 0, true
	}
	var bytes []byte
	read := 0
	for read < n && (maxLength < 0 || len(bytes) < maxLength) {
		h1 := hexDigit(s.At(read))
		h2 := hexDigit(s.At(read + 1))
		if h1 < 0 || h2 < 0 {
			return bytes, read, true
		}
		bytes = append(bytes, byte(h1<<4|h2))
		read += 2
	}
	return bytes, read, false
}

// --- Waiter lists (Atomics.waitAsync / notify) ------------------------------------------
