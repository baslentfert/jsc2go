package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
)

// Port of JavaScriptCore/bytecompiler/{Label,LabelScope}.h.
//
// Instructions are sequences of 32-bit words, so every jump offset fits in
// its operand and JSC's out-of-line jump targets are not needed.

// Label is a jump target. A forward label collects the offsets of the jumps
// that refer to it and patches them when its location is set.
type Label struct {
	location        int // -1 while forward
	bound           bool
	unresolvedJumps []int
	// retained counts owners besides the creator that keep the label (C++
	// refCount > 1); LabelScope.breakTargetMayBeBound looks at it.
	retained int
}

func (l *Label) IsForward() bool { return l.location < 0 }
func (l *Label) IsBound() bool   { return l.bound }

// Location is the instruction offset of a non-forward label.
func (l *Label) Location() int {
	if l.IsForward() {
		panic("bytecompiler: location of forward label")
	}
	l.bound = true
	return l.location
}

// bindAt returns the target relative to the instruction at offset, or
// records offset as a jump to patch (Label::bind(unsigned)).
func (l *Label) bindAt(offset int) bytecode.BoundLabel {
	l.bound = true
	if !l.IsForward() {
		return bytecode.BoundLabel(l.location - offset)
	}
	l.unresolvedJumps = append(l.unresolvedJumps, offset)
	return 0
}

// bind returns the absolute location of a non-forward label (Label::bind()).
func (l *Label) bind() int { return int(l.bindAt(0)) }

// setLocation binds the label to location and patches the pending jumps.
func (l *Label) setLocation(g *Generator, location int) {
	l.location = location
	for _, offset := range l.unresolvedJumps {
		op := bytecode.OpcodeID(g.instructions[offset])
		i := bytecode.JumpTargetOperandIndex(op)
		if i == 0 {
			panic("bytecompiler: unresolved jump at non-jump instruction " + op.String())
		}
		g.instructions[offset+i] = uint32(int32(location - offset))
	}
}

// LabelScopeType is the kind of a break/continue target.
type LabelScopeType uint8

const (
	LabelScopeLoop LabelScopeType = iota
	LabelScopeSwitch
	LabelScopeNamedLabel
)

// LabelScope is a statement that break or continue can target.
type LabelScope struct {
	refCount       int
	typ            LabelScopeType
	name           ident.Identifier // null if unnamed
	scopeDepth     int
	breakTarget    *Label
	continueTarget *Label // loops only
}

func (s *LabelScope) Type() LabelScopeType   { return s.typ }
func (s *LabelScope) Name() ident.Identifier { return s.name }
func (s *LabelScope) ScopeDepth() int        { return s.scopeDepth }
func (s *LabelScope) BreakTarget() *Label    { return s.breakTarget }
func (s *LabelScope) ContinueTarget() *Label { return s.continueTarget }

func (s *LabelScope) ref() { s.refCount++ }

func (s *LabelScope) deref() {
	s.refCount--
	if s.refCount < 0 {
		panic("bytecompiler: LabelScope reference count underflow")
	}
}

// breakTargetMayBeBound reports whether some jump may target the break label.
func (s *LabelScope) breakTargetMayBeBound() bool {
	if s.refCount != 1 {
		return true
	}
	if s.breakTarget.retained > 0 {
		return true
	}
	return s.breakTarget.IsBound()
}
