package bytecompiler

import "github.com/baslentfert/jsc2go/bytecode"

// Port of JavaScriptCore/bytecompiler/StaticPropertyAnalyzer.h and
// StaticPropertyAnalysis.h: counts the distinct properties put on a freshly
// created object so that op_new_object / op_create_this can preallocate
// inline storage for them.

type staticPropertyAnalysis struct {
	instOffset int // the op_new_object or op_create_this
	props      map[uint32]struct{}
	refs       int // number of registers that alias the object
}

func (a *staticPropertyAnalysis) record(g *Generator) {
	size := uint32(len(a.props))
	switch op := bytecode.OpcodeID(g.instructions[a.instOffset]); op {
	case bytecode.OpNewObjectID:
		g.instructions[a.instOffset+bytecode.OpNewObjectInlineCapacityIndex] = size
	case bytecode.OpCreateThisID:
		g.instructions[a.instOffset+bytecode.OpCreateThisInlineCapacityIndex] = size
	default:
		panic("bytecompiler: static property analysis on " + op.String())
	}
}

type staticPropertyAnalyzer struct {
	analyses map[int]*staticPropertyAnalysis
}

func (s *staticPropertyAnalyzer) set(reg int, a *staticPropertyAnalysis) {
	if s.analyses == nil {
		s.analyses = make(map[int]*staticPropertyAnalysis)
	}
	s.analyses[reg] = a
	a.refs++
}

func (s *staticPropertyAnalyzer) createThis(g *Generator, dst *RegisterID, instOffset int) {
	s.set(dst.Index(), &staticPropertyAnalysis{instOffset: instOffset, props: map[uint32]struct{}{}})
}

func (s *staticPropertyAnalyzer) newObject(g *Generator, dst *RegisterID, instOffset int) {
	a := &staticPropertyAnalysis{instOffset: instOffset, props: map[uint32]struct{}{}}
	if old, ok := s.analyses[dst.Index()]; ok {
		s.killAnalysis(g, old)
		old.refs--
	}
	s.set(dst.Index(), a)
}

func (s *staticPropertyAnalyzer) putByID(dst *RegisterID, propertyIndex uint32) {
	if a, ok := s.analyses[dst.Index()]; ok {
		a.props[propertyIndex] = struct{}{}
	}
}

func (s *staticPropertyAnalyzer) mov(g *Generator, dst, src *RegisterID) {
	a, ok := s.analyses[src.Index()]
	if !ok {
		s.killRegister(g, dst)
		return
	}
	if old, ok := s.analyses[dst.Index()]; ok {
		if old == a {
			return
		}
		s.killAnalysis(g, old)
		old.refs--
	}
	s.set(dst.Index(), a)
}

// killAnalysis records the analysis unless other registers still alias the
// object (it might acquire more properties through them).
func (s *staticPropertyAnalyzer) killAnalysis(g *Generator, a *staticPropertyAnalysis) {
	if a.refs != 1 {
		return
	}
	a.record(g)
}

func (s *staticPropertyAnalyzer) killRegister(g *Generator, dst *RegisterID) {
	a, ok := s.analyses[dst.Index()]
	if !ok || len(a.props) == 0 {
		return
	}
	s.killAnalysis(g, a)
	a.refs--
	delete(s.analyses, dst.Index())
}

func (s *staticPropertyAnalyzer) killAll(g *Generator) {
	for reg, a := range s.analyses {
		delete(s.analyses, reg)
		// The removed entry is now held only by this loop, so the alias
		// count is unchanged until after the check.
		s.killAnalysis(g, a)
		a.refs--
	}
}
