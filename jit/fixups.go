package jit

// fixList holds an assembler's fixups (branches to labels not bound yet),
// linked per label: a label holds only the index of its last one, so that
// labels are small, pointer-free objects (a slice of fixups per label was,
// with the labels themselves, a large part of what compiling allocated).
type fixList struct{ f []fixNode }

type fixNode struct {
	at   int32
	kind uint8
	next int32 // index+1 of the label's previous fixup; 0: none
}

// add adds a fixup at at of kind to l.
func (fl *fixList) add(l *Label, at int, kind uint8) {
	fl.f = append(fl.f, fixNode{at: int32(at), kind: kind, next: l.head})
	l.head = int32(len(fl.f))
}

// each calls f for each fixup of l.
func (fl *fixList) each(l *Label, f func(at int, kind uint8)) {
	for i := l.head; i != 0; i = fl.f[i-1].next {
		n := &fl.f[i-1]
		f(int(n.at), n.kind)
	}
}

// drop forgets l's fixups at n or later.
func (fl *fixList) drop(l *Label, n int) {
	p := &l.head
	for *p != 0 {
		node := &fl.f[*p-1]
		if int(node.at) >= n {
			*p = node.next
		} else {
			p = &node.next
		}
	}
}
