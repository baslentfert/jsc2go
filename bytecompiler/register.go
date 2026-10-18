// Package bytecompiler is a port of JavaScriptCore's bytecompiler: it walks
// the AST produced by package parser and emits bytecode into a
// bytecode.UnlinkedCodeBlock.
package bytecompiler

import "github.com/baslentfert/jsc2go/bytecode"

// Port of JavaScriptCore/bytecompiler/RegisterID.h.
//
// JSC reference counts registers with RefPtr<RegisterID>: a temporary whose
// count drops to zero may be reused by the next newTemporary(). Go has no
// destructors, so the generator keeps the references a code generation
// function holds in a stack (see Generator.refScope and Generator.keep) and
// drops them all when the function returns. That releases registers at the
// end of the function instead of the end of the C++ block, which is always
// safe: a register is only ever reused later than in JSC, never earlier.

// RegisterID is a virtual register the generator has handed out.
type RegisterID struct {
	refCount    int
	vr          bytecode.VirtualRegister
	isTemporary bool
}

func newRegisterID(vr bytecode.VirtualRegister) *RegisterID { return &RegisterID{vr: vr} }

func (r *RegisterID) setIndex(vr bytecode.VirtualRegister) { r.vr = vr }
func (r *RegisterID) setTemporary()                        { r.isTemporary = true }

// Index is the register's offset (VirtualRegister::offset()).
func (r *RegisterID) Index() int { return int(r.vr) }

func (r *RegisterID) VirtualRegister() bytecode.VirtualRegister { return r.vr }
func (r *RegisterID) IsTemporary() bool                         { return r.isTemporary }
func (r *RegisterID) RefCount() int                             { return r.refCount }

func (r *RegisterID) ref() { r.refCount++ }

func (r *RegisterID) deref() {
	r.refCount--
	if r.refCount < 0 {
		panic("bytecompiler: RegisterID reference count underflow")
	}
}

// vr returns r's virtual register, or InvalidVirtualRegister for nil; it is
// the conversion C++ applies implicitly when a RegisterID* is passed as an
// operand.
func vr(r *RegisterID) bytecode.VirtualRegister {
	if r == nil {
		return bytecode.InvalidVirtualRegister
	}
	return r.vr
}

// refCounted is something a refScope releases.
type refCounted interface{ deref() }

// refScope opens a scope for references taken with keep and returns the
// function that releases them. Use it as
//
//	defer g.refScope()()
//
// at the top of every function that holds RefPtr<RegisterID> in C++.
func (g *Generator) refScope() func() {
	mark := len(g.kept)
	return func() {
		for i := len(g.kept) - 1; i >= mark; i-- {
			g.kept[i].deref()
		}
		clear(g.kept[mark:])
		g.kept = g.kept[:mark]
	}
}

// keep takes a reference to r that lasts until the innermost refScope ends
// (C++: RefPtr<RegisterID> x = r). It returns r; nil is allowed.
func (g *Generator) keep(r *RegisterID) *RegisterID {
	if r != nil {
		r.ref()
		g.kept = append(g.kept, r)
	}
	return r
}

// keepLabelScope is keep for label scopes (C++: Ref<LabelScope>).
func (g *Generator) keepLabelScope(s *LabelScope) *LabelScope {
	s.ref()
	g.kept = append(g.kept, s)
	return s
}
