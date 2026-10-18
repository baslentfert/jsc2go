//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package vm

import (
	"slices"
	"unsafe"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/jit"
)

// Switches in machine code, as JSC's Baseline JIT has them
// (emit_op_switch_imm, _char and _string in jit/JITOpcodes.cpp): a dense
// table becomes a jump table of code addresses and an indirect jump, a
// sparse one (a list) and string switches a binary search on the keys.
// Values the tables cannot hold go to the default; strings whose key is
// not cached (or that are ropes, for switch_char) take the slow path.

// jitSwitch is a jump table to fill once the code is placed: table[i] is
// the address of labels[i].
type jitSwitch struct {
	table  []uintptr
	labels []*jit.Label
}

// switchInstruction compiles switch_imm, switch_char and switch_string;
// it reports false for other instructions.
func (c *jitCompiler) switchInstruction(pc int, op bytecode.OpcodeID, in []uint32) bool {
	m := &c.m
	u := c.cb.u
	var t *bytecode.SimpleJumpTable
	var scrutinee bytecode.VirtualRegister
	switch op {
	case bytecode.OpSwitchImmID:
		b := bytecode.DecodeSwitchImm(in)
		t, scrutinee = &u.SwitchJumpTables[b.TableIndex], b.Scrutinee
	case bytecode.OpSwitchCharID:
		b := bytecode.DecodeSwitchChar(in)
		t, scrutinee = &u.SwitchJumpTables[b.TableIndex], b.Scrutinee
	case bytecode.OpSwitchStringID:
		b := bytecode.DecodeSwitchString(in)
		return c.stringSwitch(pc, in, &u.StringSwitchJumpTables[b.TableIndex], b.Scrutinee)
	default:
		return false
	}
	targets, _ := switchTargets(c.cb, pc, in)
	slow := c.slow(pc, targets...)
	if _, ok := c.constant(scrutinee); ok {
		m.B(slow)
		return true
	}
	dflt := c.target(pc, bytecode.BoundLabel(t.DefaultOffset))
	if op == bytecode.OpSwitchImmID {
		// An int32, or a number of an int32's value (-0 is 0); others
		// match no case.
		c.exactInt32(jit.S0, scrutinee, dflt)
	} else {
		// A one-character string: its code unit.
		u16, loaded := jit.NewLabel(), jit.NewLabel()
		m.Load64(jit.S1, rRegs, c.off(scrutinee)+8)
		m.Branch64Imm(jit.NE, jit.S1, uint32(tagString), dflt)
		m.Load64(jit.S1, rRegs, c.off(scrutinee))
		m.Load64(jit.S2, jit.S1, offStrLeft)
		m.BranchNonZero(jit.S2, slow) // a rope
		m.Load64(jit.S2, jit.S1, offStrLength)
		m.Branch64Imm(jit.NE, jit.S2, 1, dflt)
		m.Load8(jit.S2, jit.S1, offStrIsASCII)
		m.BranchZero(jit.S2, u16)
		m.Load64(jit.S2, jit.S1, offStrASCII)
		m.Load8(jit.S0, jit.S2, 0)
		m.B(loaded)
		m.Bind(u16)
		m.Load64(jit.S2, jit.S1, offStrU16)
		m.Load16(jit.S0, jit.S2, 0)
		m.Bind(loaded)
	}
	c.intSwitch(pc, t, dflt)
	return true
}

// intSwitch dispatches the int32 in S0 on table t: through a jump table
// when it is dense, else by a binary search on its keys.
func (c *jitCompiler) intSwitch(pc int, t *bytecode.SimpleJumpTable, dflt *jit.Label) {
	m := &c.m
	if t.IsList {
		type kase struct {
			key    int32
			target *jit.Label
		}
		var cases []kase
		for i := 0; i+1 < len(t.BranchOffsets); i += 2 {
			key := t.BranchOffsets[i]
			if !slices.ContainsFunc(cases, func(k kase) bool { return k.key == key }) {
				cases = append(cases, kase{key, c.target(pc, bytecode.BoundLabel(t.BranchOffsets[i+1]))})
			}
		}
		slices.SortFunc(cases, func(a, b kase) int { return int(a.key) - int(b.key) })
		keys := make([]uint64, len(cases))
		labels := make([]*jit.Label, len(cases))
		for i, k := range cases {
			keys[i], labels[i] = uint64(int64(k.key)), k.target
		}
		c.binarySwitch(keys, labels, false, dflt)
		return
	}
	n := len(t.BranchOffsets)
	sw := jitSwitch{table: make([]uintptr, n), labels: make([]*jit.Label, n)}
	for i, off := range t.BranchOffsets {
		if off == 0 {
			sw.labels[i] = dflt
		} else {
			sw.labels[i] = c.target(pc, bytecode.BoundLabel(off))
		}
	}
	c.switches = append(c.switches, sw)
	m.MovImm(jit.S1, uint64(uint32(t.Min)))
	m.Alu32(jit.Sub32, jit.S0, jit.S0, jit.S1)
	m.ZeroExt32(jit.S0, jit.S0)
	m.MovImm(jit.S1, uint64(n))
	m.Branch64(jit.UGE, jit.S0, jit.S1, dflt)
	m.MovImm(jit.S1, uint64(uintptr(unsafe.Pointer(&sw.table[0]))))
	m.AddShift64(jit.S1, jit.S1, jit.S0, 3)
	m.Load64(jit.S1, jit.S1, 0)
	m.JumpReg(jit.S1)
}

// binarySwitch branches to labels[i] when S0 equals keys[i] (sorted; as
// signed 32-bit values, or 64-bit pointers when wide), else to dflt
// (JSC's BinarySwitch). Clobbers S1.
func (c *jitCompiler) binarySwitch(keys []uint64, labels []*jit.Label, wide bool, dflt *jit.Label) {
	m := &c.m
	branch := func(cond jit.Cond, l *jit.Label) {
		if wide {
			m.Branch64(cond, jit.S0, jit.S1, l)
		} else {
			m.Branch32(cond, jit.S0, jit.S1, l)
		}
	}
	if len(keys) <= 4 {
		for i, k := range keys {
			m.MovImm(jit.S1, k)
			branch(jit.EQ, labels[i])
		}
		m.B(dflt)
		return
	}
	mid := len(keys) / 2
	greater := jit.NewLabel()
	m.MovImm(jit.S1, keys[mid])
	branch(jit.EQ, labels[mid])
	if wide {
		branch(jit.UGT, greater)
	} else {
		branch(jit.SGT, greater)
	}
	c.binarySwitch(keys[:mid], labels[:mid], wide, dflt)
	m.Bind(greater)
	c.binarySwitch(keys[mid+1:], labels[mid+1:], wide, dflt)
}

// stringSwitch compiles switch_string: a string whose property key is
// cached (an atom) is looked up among the cases' atoms; other strings
// that may match take the slow path, and other values the default.
func (c *jitCompiler) stringSwitch(pc int, in []uint32, t *bytecode.StringJumpTable, scrutinee bytecode.VirtualRegister) bool {
	m := &c.m
	targets, _ := switchTargets(c.cb, pc, in)
	slow := c.slow(pc, targets...)
	if _, ok := c.constant(scrutinee); ok || len(t.OffsetTable) > 64 || unsafe.Sizeof(PropertyKey{}.name) != 8 {
		m.B(slow)
		return true
	}
	dflt := c.target(pc, bytecode.BoundLabel(t.DefaultOffset))
	type kase struct {
		atom   uint64
		target *jit.Label
	}
	var cases []kase
	for s, e := range t.OffsetTable {
		k := StrKey(s)
		if k.idx != 0 {
			continue // an index-like key: such strings take the slow path
		}
		c.keepKeys = append(c.keepKeys, k) // atoms are weak: the code keeps them
		cases = append(cases, kase{uint64(*(*uintptr)(unsafe.Pointer(&k.name))), c.target(pc, bytecode.BoundLabel(e.BranchOffset))})
	}
	slices.SortFunc(cases, func(a, b kase) int {
		switch {
		case a.atom < b.atom:
			return -1
		case a.atom > b.atom:
			return 1
		}
		return 0
	})
	m.Load64(jit.S1, rRegs, c.off(scrutinee)+8)
	m.Branch64Imm(jit.NE, jit.S1, uint32(tagString), dflt)
	m.Load64(jit.S1, rRegs, c.off(scrutinee))
	m.Load64(jit.S1, jit.S1, offStrKey) // *PropertyKey
	m.BranchZero(jit.S1, slow)
	m.Load32(jit.S2, jit.S1, offKeyIdx)
	m.BranchNonZero(jit.S2, slow)
	m.Load64(jit.S0, jit.S1, offKeyName) // the atom
	keys := make([]uint64, len(cases))
	labels := make([]*jit.Label, len(cases))
	for i, k := range cases {
		keys[i], labels[i] = k.atom, k.target
	}
	c.binarySwitch(keys, labels, true, dflt)
	return true
}
