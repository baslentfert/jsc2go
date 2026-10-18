package bytecode

import "fmt"

// Validate checks structural invariants of a code block: instructions decode
// to the end, jumps, switch tables and exception handlers land on
// instruction boundaries, and operands refer to existing registers,
// constants and identifiers. It returns the first problem found.
func Validate(cb *UnlinkedCodeBlock) error {
	ins := cb.Instructions
	boundaries := make(map[int]bool)
	for offset := 0; offset < len(ins); {
		op := OpcodeID(ins[offset])
		if op >= NumberOfBytecodeIDs {
			return fmt.Errorf("invalid opcode %d at %d", ins[offset], offset)
		}
		if offset+op.Length() > len(ins) {
			return fmt.Errorf("truncated %s at %d", op, offset)
		}
		boundaries[offset] = true
		offset += op.Length()
	}
	boundaries[len(ins)] = true

	if len(ins) == 0 {
		return fmt.Errorf("empty code block")
	}

	isTarget := func(offset int) bool { return offset >= 0 && offset < len(ins) && boundaries[offset] }

	var last OpcodeID
	for offset := 0; offset < len(ins); {
		op := OpcodeID(ins[offset])
		instruction := ins[offset : offset+op.Length()]
		last = op

		var bad error
		ForEachJumpTarget(cb, instruction, func(relative int32) {
			if bad == nil && !isTarget(offset+int(relative)) {
				bad = fmt.Errorf("%s at %d jumps to %d, not an instruction", op, offset, offset+int(relative))
			}
		})
		if bad != nil {
			return bad
		}

		names := op.OperandNames()
		for i, kind := range op.OperandKinds() {
			word := instruction[i+1]
			switch kind {
			case OperandVirtualRegister:
				r := VirtualRegister(int32(word))
				if !r.IsValid() {
					continue
				}
				switch {
				case r.IsConstant():
					if r.ToConstantIndex() >= len(cb.Constants) {
						return fmt.Errorf("%s at %d: %s refers to missing constant %d", op, offset, names[i], r.ToConstantIndex())
					}
				case r.IsLocal():
					if r.ToLocal() >= cb.NumCalleeLocals {
						return fmt.Errorf("%s at %d: %s is %s beyond %d callee locals", op, offset, names[i], r, cb.NumCalleeLocals)
					}
				}
			case OperandMetadataID:
				if word >= cb.Metadata.Counts[op] {
					return fmt.Errorf("%s at %d: metadata %d out of range", op, offset, word)
				}
			}
		}
		offset += op.Length()
	}

	switch {
	case last.IsTerminal(), last.IsThrow(), last.IsUnconditionalBranch(), last == OpSwitchImmID, last == OpSwitchCharID, last == OpSwitchStringID:
	default:
		return fmt.Errorf("code block ends with %s, not a terminal", last)
	}

	for i, h := range cb.ExceptionHandlers {
		if !boundaries[int(h.Start)] || !boundaries[int(h.End)] || !isTarget(int(h.Target)) || h.Start >= h.End {
			return fmt.Errorf("exception handler %d [%d, %d) -> %d is malformed", i, h.Start, h.End, h.Target)
		}
		if OpcodeID(ins[h.Target]) != OpCatchID {
			return fmt.Errorf("exception handler %d targets %s, not op_catch", i, OpcodeID(ins[h.Target]))
		}
	}

	if cb.ThisRegister.IsValid() && cb.ThisRegister.IsLocal() && cb.ThisRegister.ToLocal() >= cb.NumCalleeLocals {
		return fmt.Errorf("this register %s out of range", cb.ThisRegister)
	}
	return nil
}
