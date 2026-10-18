package jit

import "encoding/binary"

// An encoder for the x86-64 instructions the amd64 macro assembler uses
// (any platform can build code; only amd64 can run it).

// Register numbers: RAX 0, RCX 1, RDX 2, RBX 3, RSP 4, RBP 5, RSI 6, RDI 7,
// R8-R15 8-15; XMM0-XMM15 0-15.
const (
	xRAX = 0
	xRCX = 1
	xRDX = 2
	xRBX = 3
	xRSP = 4
	xRBP = 5
	xRSI = 6
	xRDI = 7
)

// x86 condition codes (the low nibble of Jcc and SETcc).
const (
	xccB  = 0x2 // below (CF)
	xccAE = 0x3
	xccE  = 0x4
	xccNE = 0x5
	xccBE = 0x6
	xccA  = 0x7
	xccP  = 0xA
	xccNP = 0xB
	xccL  = 0xC
	xccGE = 0xD
	xccLE = 0xE
	xccG  = 0xF
)

// X86 collects x86-64 machine code.
type X86 struct {
	Code []byte
	// waiting are the labels that got a fixup while unbound (see
	// Masm.Unbound).
	waiting []*Label
	fixes   fixList
}

// xmem is an operand: a register (direct) or memory at base + index*scale +
// disp (index -1 for none).
type xmem struct {
	direct bool
	r      byte
	base   byte
	index  int8
	scale  byte // 0-3 (1, 2, 4, 8)
	disp   int32
}

func xreg(r byte) xmem                  { return xmem{direct: true, r: r} }
func xbase(b byte, disp int32) xmem     { return xmem{base: b, index: -1, disp: disp} }
func xindex(b, i byte, scale byte) xmem { return xmem{base: b, index: int8(i), scale: scale} }

// op emits [prefix] [REX] opcode ModRM [SIB] [disp] with reg in ModRM.reg.
// prefix 0 means none; rex forces a REX prefix (byte registers SPL-DIL).
func (x *X86) op(prefix byte, w bool, opcode []byte, reg byte, m xmem, rex bool) {
	if prefix != 0 {
		x.Code = append(x.Code, prefix)
	}
	r := byte(0x40)
	if w {
		r |= 8
	}
	if reg&8 != 0 {
		r |= 4
	}
	if m.direct {
		if m.r&8 != 0 {
			r |= 1
		}
	} else {
		if m.index >= 0 && m.index&8 != 0 {
			r |= 2
		}
		if m.base&8 != 0 {
			r |= 1
		}
	}
	if r != 0x40 || rex {
		x.Code = append(x.Code, r)
	}
	x.Code = append(x.Code, opcode...)
	if m.direct {
		x.Code = append(x.Code, 0xC0|(reg&7)<<3|m.r&7)
		return
	}
	var mod byte
	switch {
	case m.disp == 0 && m.base&7 != xRBP:
		mod = 0
	case m.disp >= -128 && m.disp < 128:
		mod = 1
	default:
		mod = 2
	}
	if m.index < 0 && m.base&7 != xRSP {
		x.Code = append(x.Code, mod<<6|(reg&7)<<3|m.base&7)
	} else {
		idx := byte(4) // none
		if m.index >= 0 {
			idx = byte(m.index) & 7
		}
		x.Code = append(x.Code, mod<<6|(reg&7)<<3|4, m.scale<<6|idx<<3|m.base&7)
	}
	switch mod {
	case 1:
		x.Code = append(x.Code, byte(m.disp))
	case 2:
		x.Code = binary.LittleEndian.AppendUint32(x.Code, uint32(m.disp))
	}
}

func (x *X86) imm32(v uint32) { x.Code = binary.LittleEndian.AppendUint32(x.Code, v) }

// Moves.

// MovImm loads v: MOV r32, imm32 (zero-extending) when it fits, else
// MOV r64, imm64.
func (x *X86) MovImm(d byte, v uint64) {
	if v <= 0xFFFFFFFF {
		if d&8 != 0 {
			x.Code = append(x.Code, 0x41)
		}
		x.Code = append(x.Code, 0xB8+d&7)
		x.imm32(uint32(v))
		return
	}
	x.Code = append(x.Code, 0x48|d>>3, 0xB8+d&7)
	x.Code = binary.LittleEndian.AppendUint64(x.Code, v)
}

func (x *X86) Mov64(d, s byte)        { x.op(0, true, []byte{0x89}, s, xreg(d), false) }
func (x *X86) Mov32(d, s byte)        { x.op(0, false, []byte{0x89}, s, xreg(d), false) }
func (x *X86) Load64(d byte, m xmem)  { x.op(0, true, []byte{0x8B}, d, m, false) }
func (x *X86) Store64(s byte, m xmem) { x.op(0, true, []byte{0x89}, s, m, false) }
func (x *X86) Load32(d byte, m xmem)  { x.op(0, false, []byte{0x8B}, d, m, false) }
func (x *X86) Store32(s byte, m xmem) { x.op(0, false, []byte{0x89}, s, m, false) }
func (x *X86) Store16(s byte, m xmem) { x.op(0x66, false, []byte{0x89}, s, m, false) }
func (x *X86) Store8(s byte, m xmem)  { x.op(0, false, []byte{0x88}, s, m, true) }
func (x *X86) LoadU8(d byte, m xmem)  { x.op(0, false, []byte{0x0F, 0xB6}, d, m, false) }
func (x *X86) LoadS8(d byte, m xmem)  { x.op(0, false, []byte{0x0F, 0xBE}, d, m, false) }
func (x *X86) LoadU16(d byte, m xmem) { x.op(0, false, []byte{0x0F, 0xB7}, d, m, false) }
func (x *X86) LoadS16(d byte, m xmem) { x.op(0, false, []byte{0x0F, 0xBF}, d, m, false) }
func (x *X86) Lea(d byte, m xmem)     { x.op(0, true, []byte{0x8D}, d, m, false) }
func (x *X86) MovZXB(d, s byte)       { x.op(0, false, []byte{0x0F, 0xB6}, d, xreg(s), true) }

// StoreImm64 stores the sign-extended imm32 v as 64 bits.
func (x *X86) StoreImm64(m xmem, v uint32) {
	x.op(0, true, []byte{0xC7}, 0, m, false)
	x.imm32(v)
}

// Arithmetic. alu is ADD 0x01, OR 0x09, AND 0x21, SUB 0x29, XOR 0x31, CMP
// 0x39 (rm op= reg).
func (x *X86) Alu(opc byte, w bool, d, s byte) { x.op(0, w, []byte{opc}, s, xreg(d), false) }

// AluImm is the 0x81 group (/0 ADD, /5 SUB, /7 CMP) with an imm32.
func (x *X86) AluImm(ext byte, w bool, d byte, v uint32) {
	x.op(0, w, []byte{0x81}, ext, xreg(d), false)
	x.imm32(v)
}

func (x *X86) Imul32(d, s byte)      { x.op(0, false, []byte{0x0F, 0xAF}, d, xreg(s), false) }
func (x *X86) Test64(a, b byte)      { x.op(0, true, []byte{0x85}, b, xreg(a), false) }
func (x *X86) Not32(d byte)          { x.op(0, false, []byte{0xF7}, 2, xreg(d), false) }
func (x *X86) Idiv32(s byte)         { x.op(0, false, []byte{0xF7}, 7, xreg(s), false) }
func (x *X86) Cdq()                  { x.Code = append(x.Code, 0x99) }
func (x *X86) ShiftCL32(ext, d byte) { x.op(0, false, []byte{0xD3}, ext, xreg(d), false) } // /4 SHL, /5 SHR, /7 SAR
func (x *X86) Setcc(cc, d byte)      { x.op(0, false, []byte{0x0F, 0x90 | cc}, 0, xreg(d), true) }
func (x *X86) Ret()                  { x.Code = append(x.Code, 0xC3) }

// ShlImm64 shifts d left by n.
func (x *X86) ShlImm64(d byte, n byte) {
	x.op(0, true, []byte{0xC1}, 4, xreg(d), false)
	x.Code = append(x.Code, n)
}

// CallReg calls the address in r.
func (x *X86) CallReg(r byte) {
	if r&8 != 0 {
		x.Code = append(x.Code, 0x41)
	}
	x.Code = append(x.Code, 0xFF, 0xD0|r&7)
}

func (x *X86) Push(r byte) {
	if r&8 != 0 {
		x.Code = append(x.Code, 0x41)
	}
	x.Code = append(x.Code, 0x50+r&7)
}

func (x *X86) Pop(r byte) {
	if r&8 != 0 {
		x.Code = append(x.Code, 0x41)
	}
	x.Code = append(x.Code, 0x58+r&7)
}

// SSE2.

func (x *X86) MovqToX(d, s byte)   { x.op(0x66, true, []byte{0x0F, 0x6E}, d, xreg(s), false) }
func (x *X86) MovqFromX(d, s byte) { x.op(0x66, true, []byte{0x0F, 0x7E}, s, xreg(d), false) }
func (x *X86) Movapd(d, s byte)    { x.op(0x66, false, []byte{0x0F, 0x28}, d, xreg(s), false) }
func (x *X86) Xorps(d, s byte)     { x.op(0, false, []byte{0x0F, 0x57}, d, xreg(s), false) }
func (x *X86) Xorpd(d, s byte)     { x.op(0x66, false, []byte{0x0F, 0x57}, d, xreg(s), false) }
func (x *X86) Andpd(d, s byte)     { x.op(0x66, false, []byte{0x0F, 0x54}, d, xreg(s), false) }
func (x *X86) Ucomisd(a, b byte)   { x.op(0x66, false, []byte{0x0F, 0x2E}, a, xreg(b), false) }

// Sd is addsd 0x58, mulsd 0x59, subsd 0x5C, divsd 0x5E (d op= s).
func (x *X86) Sd(opc, d, s byte) { x.op(0xF2, false, []byte{0x0F, opc}, d, xreg(s), false) }

func (x *X86) Cvtsi2sd32(d, s byte)       { x.op(0xF2, false, []byte{0x0F, 0x2A}, d, xreg(s), false) }
func (x *X86) Cvtsi2sd64(d, s byte)       { x.op(0xF2, true, []byte{0x0F, 0x2A}, d, xreg(s), false) }
func (x *X86) Cvttsd2si32(d, s byte)      { x.op(0xF2, false, []byte{0x0F, 0x2C}, d, xreg(s), false) }
func (x *X86) Cvttsd2si64(d, s byte)      { x.op(0xF2, true, []byte{0x0F, 0x2C}, d, xreg(s), false) }
func (x *X86) Cvtss2sd(d, s byte)         { x.op(0xF3, false, []byte{0x0F, 0x5A}, d, xreg(s), false) }
func (x *X86) Cvtsd2ss(d, s byte)         { x.op(0xF2, false, []byte{0x0F, 0x5A}, d, xreg(s), false) }
func (x *X86) MovsdLoad(d byte, m xmem)   { x.op(0xF2, false, []byte{0x0F, 0x10}, d, m, false) }
func (x *X86) MovsdStore(s byte, m xmem)  { x.op(0xF2, false, []byte{0x0F, 0x11}, s, m, false) }
func (x *X86) MovssLoad(d byte, m xmem)   { x.op(0xF3, false, []byte{0x0F, 0x10}, d, m, false) }
func (x *X86) MovssStore(s byte, m xmem)  { x.op(0xF3, false, []byte{0x0F, 0x11}, s, m, false) }
func (x *X86) MovupsStore(s byte, m xmem) { x.op(0, false, []byte{0x0F, 0x11}, s, m, false) }

// Branches: rel32 fixups, patched when the label is bound.

const fixRel32 = 3

func (x *X86) rel32(l *Label) {
	at := len(x.Code)
	x.imm32(0)
	if l.pos >= 0 {
		x.patch(at, l.pos)
	} else {
		if l.head == 0 {
			x.waiting = append(x.waiting, l)
		}
		x.fixes.add(l, at, fixRel32)
	}
}

func (x *X86) patch(at, target int) {
	binary.LittleEndian.PutUint32(x.Code[at:], uint32(int32(target-(at+4))))
}

// Bind binds l here.
func (x *X86) Bind(l *Label) {
	l.pos = len(x.Code)
	x.fixes.each(l, func(at int, _ uint8) { x.patch(at, l.pos) })
	l.head = 0
}

func (x *X86) Jmp(l *Label) {
	x.Code = append(x.Code, 0xE9)
	x.rel32(l)
}

func (x *X86) Jcc(cc byte, l *Label) {
	x.Code = append(x.Code, 0x0F, 0x80|cc)
	x.rel32(l)
}
