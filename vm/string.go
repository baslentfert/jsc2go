package vm

import (
	"errors"
	"strings"
	"sync/atomic"
	"unicode/utf16"
	"unsafe"

	"github.com/baslentfert/jsc2go/ident"
)

// String is a JavaScript string: a sequence of UTF-16 code units. ASCII
// strings, by far the most common, are kept as Go strings; others as
// []uint16. Concatenations build ropes that are flattened on first use, so
// that building a string piece by piece stays linear.
type String struct {
	// data points to the text of a flat string: length bytes when isASCII,
	// else length code units. One pointer instead of a Go string and a
	// slice keeps String in the 64-byte size class (JSC's JSString is small
	// too, with the text in a separate StringImpl).
	data    unsafe.Pointer
	length  int
	isASCII bool

	// Rope: the concatenation left+right, flattened lazily.
	left, right *String

	// app is the buffer this string was appended into (see appendConcat).
	app *appendBuffer

	// key caches Key (like an atomized JSString): a string used as a
	// property name again needs no interning. Atomic because global strings
	// are shared by VMs on other goroutines.
	key atomic.Pointer[PropertyKey]
}

var emptyString = &String{isASCII: true}

// errStringIndex is At's panic for an index outside the string (as
// indexing the former fields panicked).
var errStringIndex = errors.New("jsc2go: string index out of range")

// asciiText is the text of a flat ASCII string, "" for other strings (as
// the former ascii field was).
func (s *String) asciiText() string {
	if !s.isASCII || s.data == nil {
		return ""
	}
	return unsafe.String((*byte)(s.data), s.length)
}

// units are the code units of a flat string that is not ASCII, nil for
// other strings.
func (s *String) units() []uint16 {
	if s.isASCII || s.data == nil {
		return nil
	}
	return unsafe.Slice((*uint16)(s.data), s.length)
}

// asciiString makes a flat string of ASCII text t.
func asciiString(t string) *String {
	return &String{data: unsafe.Pointer(unsafe.StringData(t)), length: len(t), isASCII: true}
}

// unitsString makes a flat string of code units u (not all ASCII).
func unitsString(u []uint16) *String {
	return &String{data: unsafe.Pointer(unsafe.SliceData(u)), length: len(u)}
}

// NewString makes a string from WTF-8 text (see package ident).
func NewString(s string) *String {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return NewStringUTF16(ident.DecodeWTF8(s))
		}
	}
	return &String{data: unsafe.Pointer(unsafe.StringData(s)), isASCII: true, length: len(s)}
}

// NewStringUTF16 makes a string from code units (taking ownership of u).
func NewStringUTF16(u []uint16) *String {
	for _, c := range u {
		if c >= 0x80 {
			return &String{data: unsafe.Pointer(unsafe.SliceData(u)), length: len(u)}
		}
	}
	b := make([]byte, len(u))
	for i, c := range u {
		b[i] = byte(c)
	}
	return &String{data: unsafe.Pointer(unsafe.StringData(string(b))), isASCII: true, length: len(u)}
}

// NewStringFromRunes makes a string from Unicode text.
func NewStringFromGo(s string) *String {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return NewStringUTF16(utf16.Encode([]rune(s)))
		}
	}
	return &String{data: unsafe.Pointer(unsafe.StringData(s)), isASCII: true, length: len(s)}
}

func (s *String) Length() int { return s.length }

func (s *String) flatten() {
	if s.left == nil {
		return
	}
	// Collect the leaves iteratively; ropes can be deep.
	var leaves []*String
	stack := []*String{s.right, s.left}
	ascii := true
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.left != nil {
			stack = append(stack, n.right, n.left)
			continue
		}
		ascii = ascii && n.isASCII
		leaves = append(leaves, n)
	}
	if ascii {
		var b strings.Builder
		b.Grow(s.length)
		for _, l := range leaves {
			b.WriteString(l.asciiText())
		}
		t := b.String()
		s.data, s.isASCII = unsafe.Pointer(unsafe.StringData(t)), true
	} else {
		u := make([]uint16, 0, s.length)
		for _, l := range leaves {
			if l.isASCII {
				t := l.asciiText()
				for i := 0; i < len(t); i++ {
					u = append(u, uint16(t[i]))
				}
			} else {
				u = append(u, l.units()...)
			}
		}
		s.data, s.isASCII = unsafe.Pointer(unsafe.SliceData(u)), false
	}
	s.left, s.right = nil, nil
}

// At returns the code unit at index i.
func (s *String) At(i int) uint16 {
	s.flatten()
	if uint(i) >= uint(s.length) {
		panic(errStringIndex)
	}
	if s.isASCII {
		return uint16(*(*byte)(unsafe.Add(s.data, i)))
	}
	return *(*uint16)(unsafe.Add(s.data, 2*i))
}

// UTF16 returns the code units (do not modify).
func (s *String) UTF16() []uint16 {
	s.flatten()
	if s.isASCII {
		t := s.asciiText()
		u := make([]uint16, len(t))
		for i := 0; i < len(t); i++ {
			u[i] = uint16(t[i])
		}
		return u
	}
	return s.units()
}

// String returns the text as WTF-8 (lone surrogates preserved).
func (s *String) String() string {
	s.flatten()
	if s.isASCII {
		return s.asciiText()
	}
	return ident.EncodeWTF8(s.units())
}

// GoString returns the text as UTF-8, replacing lone surrogates.
func (s *String) GoString() string {
	s.flatten()
	if s.isASCII {
		return s.asciiText()
	}
	return string(utf16.Decode(s.units()))
}

// IsASCII reports whether the string is plain ASCII (flattening it).
func (s *String) IsASCII() bool {
	s.flatten()
	return s.isASCII
}

// Key returns the property key naming this string.
func (s *String) Key() PropertyKey {
	if k := s.key.Load(); k != nil {
		return *k
	}
	k := StrKey(s.String())
	s.key.Store(&k)
	return k
}

func (s *String) Equals(o *String) bool {
	if s == o {
		return true
	}
	if s.length != o.length {
		return false
	}
	s.flatten()
	o.flatten()
	if s.isASCII && o.isASCII {
		return s.asciiText() == o.asciiText()
	}
	if s.isASCII != o.isASCII {
		return false // an ASCII string never equals one with non-ASCII units
	}
	ou := o.units()
	for i, c := range s.units() {
		if ou[i] != c {
			return false
		}
	}
	return true
}

// Compare orders strings by code units.
func (s *String) Compare(o *String) int {
	s.flatten()
	o.flatten()
	if s.isASCII && o.isASCII {
		return strings.Compare(s.asciiText(), o.asciiText())
	}
	n := min(s.length, o.length)
	for i := range n {
		a, b := s.At(i), o.At(i)
		if a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}
	switch {
	case s.length < o.length:
		return -1
	case s.length > o.length:
		return 1
	}
	return 0
}

// Concat returns s + o.
func Concat(s, o *String) *String {
	if s.length+o.length > maxStringLength {
		panic(errStringTooLong)
	}
	switch {
	case s.length == 0:
		return o
	case o.length == 0:
		return s
	case s.length+o.length < 64:
		s.flatten()
		o.flatten()
		if s.isASCII && o.isASCII {
			return &String{data: unsafe.Pointer(unsafe.StringData(s.asciiText() + o.asciiText())), isASCII: true, length: s.length + o.length}
		}
		u := make([]uint16, 0, s.length+o.length)
		u = append(u, s.UTF16()...)
		u = append(u, o.UTF16()...)
		return &String{data: unsafe.Pointer(unsafe.SliceData(u)), length: len(u)}
	}
	if s.left == nil && o.length <= maxAppendChunk {
		if r := appendConcat(s, o); r != nil {
			return r
		}
	}
	return &String{left: s, right: o, length: s.length + o.length}
}

// Repeatedly appending to the end of a string (s += x in a loop) writes into
// a shared, growing buffer instead of building a long rope, like QuickJS's
// in-place concatenation: a string made this way remembers its buffer, and
// when it still ends where the buffer's used part ends, the next
// concatenation appends in place. Earlier strings keep referring to their
// prefix of the buffer, which never changes.
type appendBuffer struct {
	b     []byte   // contents of an ASCII buffer
	u     []uint16 // contents otherwise
	ascii bool
}

const (
	maxAppendChunk = 1024 // longer right operands become ropes
	maxAppendStart = 4096 // longer left operands without a buffer become ropes
)

func appendConcat(s, o *String) *String {
	o.flatten()
	ab := s.app
	ascii := s.isASCII && o.isASCII
	if ab == nil || ab.used() != s.length || ab.ascii != ascii {
		if s.length > maxAppendStart {
			return nil // copying s each time could be quadratic: use a rope
		}
		ab = &appendBuffer{ascii: ascii}
		n := 2 * (s.length + o.length)
		if ascii {
			ab.b = append(make([]byte, 0, n), s.asciiText()...)
		} else {
			ab.u = append(make([]uint16, 0, n), s.UTF16()...)
		}
	}
	n := s.length + o.length
	if ascii {
		ab.b = append(ab.b, o.asciiText()...)
		return &String{data: unsafe.Pointer(unsafe.StringData(unsafe.String(&ab.b[0], n))), isASCII: true, length: n, app: ab}
	}
	if o.isASCII {
		t := o.asciiText()
		for i := 0; i < len(t); i++ {
			ab.u = append(ab.u, uint16(t[i]))
		}
	} else {
		ab.u = append(ab.u, o.units()...)
	}
	return &String{data: unsafe.Pointer(unsafe.SliceData(ab.u[:n:n])), length: n, app: ab}
}

func (ab *appendBuffer) used() int {
	if ab.ascii {
		return len(ab.b)
	}
	return len(ab.u)
}

// Substring returns the code units [start, end).
func (s *String) Substring(start, end int) *String {
	if start == 0 && end == s.length {
		return s
	}
	s.flatten()
	if end-start == 1 {
		if c := s.At(start); c < 256 {
			return smallStrings[c]
		}
	}
	if s.isASCII {
		sub := s.asciiText()[start:end]
		// Share the text only with a big part of it: a small substring
		// would keep all of s alive.
		if 4*len(sub) < len(s.asciiText()) {
			sub = strings.Clone(sub)
		}
		return &String{data: unsafe.Pointer(unsafe.StringData(sub)), isASCII: true, length: end - start}
	}
	return NewStringUTF16(append([]uint16(nil), s.units()[start:end]...))
}

// IndexOf finds sub at or after from, or returns -1.
func (s *String) IndexOf(sub *String, from int) int {
	s.flatten()
	sub.flatten()
	if s.isASCII && sub.isASCII {
		if from > len(s.asciiText()) {
			return -1
		}
		i := strings.Index(s.asciiText()[from:], sub.asciiText())
		if i < 0 {
			return -1
		}
		return i + from
	}
	n, m := s.length, sub.length
	for i := from; i+m <= n; i++ {
		match := true
		for j := 0; j < m; j++ {
			if s.At(i+j) != sub.At(j) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// CodePointAt returns the code point at i and the number of code units it
// spans.
func (s *String) CodePointAt(i int) (rune, int) {
	c := s.At(i)
	if c >= 0xD800 && c <= 0xDBFF && i+1 < s.length {
		d := s.At(i + 1)
		if d >= 0xDC00 && d <= 0xDFFF {
			return (rune(c)-0xD800)<<10 + (rune(d) - 0xDC00) + 0x10000, 2
		}
	}
	return rune(c), 1
}

// maxStringLength is the longest string (V8's limit; JSC allows 2^31-1 but
// would first allocate gigabytes). Building a
// longer one panics with errStringTooLong, which the interpreter turns into
// a RangeError.
const maxStringLength = 1<<29 - 24

type stringTooLong struct{}

var errStringTooLong = stringTooLong{}

// stringBuilder accumulates UTF-16 code units.
type stringBuilder struct {
	u     []uint16
	ascii []byte
	wide  bool
}

func (b *stringBuilder) WriteUnit(c uint16) {
	if b.Len() >= maxStringLength {
		panic(errStringTooLong)
	}
	if !b.wide && c < 0x80 {
		b.ascii = append(b.ascii, byte(c))
		return
	}
	b.widen()
	b.u = append(b.u, c)
}

func (b *stringBuilder) widen() {
	if b.wide {
		return
	}
	b.wide = true
	b.u = make([]uint16, len(b.ascii), len(b.ascii)+16)
	for i, c := range b.ascii {
		b.u[i] = uint16(c)
	}
}

func (b *stringBuilder) WriteString(s *String) {
	if b.Len()+s.Length() > maxStringLength {
		panic(errStringTooLong)
	}
	if !b.wide && s.IsASCII() {
		b.ascii = append(b.ascii, s.asciiText()...)
		return
	}
	b.widen()
	b.u = append(b.u, s.UTF16()...)
}

// WriteGo appends Go (UTF-8) text.
func (b *stringBuilder) WriteGo(s string) {
	for _, r := range s {
		b.WriteRune(r)
	}
}

func (b *stringBuilder) WriteRune(r rune) {
	if r < 0x10000 {
		b.WriteUnit(uint16(r))
		return
	}
	r -= 0x10000
	b.WriteUnit(uint16(0xD800 + (r >> 10)))
	b.WriteUnit(uint16(0xDC00 + (r & 0x3FF)))
}

func (b *stringBuilder) Len() int {
	if b.wide {
		return len(b.u)
	}
	return len(b.ascii)
}

func (b *stringBuilder) String() *String {
	if b.wide {
		return NewStringUTF16(b.u)
	}
	return &String{data: unsafe.Pointer(unsafe.StringData(string(b.ascii))), isASCII: true, length: len(b.ascii)}
}

// codeUnits exposes the flattened contents without copying: Latin-1 bytes
// for ASCII strings, otherwise UTF-16. Callers must not modify them.
func (s *String) codeUnits() ([]byte, []uint16) {
	s.flatten()
	if s.isASCII {
		return unsafe.Slice(unsafe.StringData(s.asciiText()), s.length), nil
	}
	return nil, s.units()
}

// smallStrings holds the one-character strings for the Latin-1 range, so
// that indexing and fromCharCode need not allocate (JSC's SmallStrings).
var smallStrings = func() (t [256]*String) {
	for i := range t {
		if i < 0x80 {
			t[i] = &String{data: unsafe.Pointer(unsafe.StringData(string(rune(i)))), isASCII: true, length: 1}
		} else {
			t[i] = &String{data: unsafe.Pointer(unsafe.SliceData([]uint16{uint16(i)})), length: 1}
		}
	}
	return t
}()

// charString returns the string of the single code unit c.
func charString(c uint16) *String {
	if c < 256 {
		return smallStrings[c]
	}
	return &String{data: unsafe.Pointer(unsafe.SliceData([]uint16{c})), length: 1}
}
