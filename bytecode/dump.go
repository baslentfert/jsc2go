package bytecode

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Port of JavaScriptCore/bytecode/BytecodeDumper.cpp: a human-readable
// listing of a code block, in JSC's format.

// Dump writes a listing of cb to w.
func Dump(w io.Writer, cb *UnlinkedCodeBlock) {
	instructionCount, withMetadata := 0, 0
	for offset := 0; offset < len(cb.Instructions); {
		op := OpcodeID(cb.Instructions[offset])
		instructionCount++
		if op.HasMetadata() {
			withMetadata++
		}
		offset += op.Length()
	}
	fmt.Fprintf(w, "<%s>: %d instructions (%d instructions with metadata); %d words; %d parameter(s); %d callee register(s); %d variable(s); scope at %s\n",
		cb.CodeType, instructionCount, withMetadata, len(cb.Instructions), cb.NumParameters, cb.NumCalleeLocals, cb.NumVars, cb.ScopeRegister)

	for offset := 0; offset < len(cb.Instructions); {
		op := OpcodeID(cb.Instructions[offset])
		fmt.Fprintln(w, DumpInstruction(cb, offset))
		offset += op.Length()
	}

	if len(cb.Identifiers) > 0 {
		fmt.Fprintf(w, "\nIdentifiers:\n")
		for i, id := range cb.Identifiers {
			fmt.Fprintf(w, "  id%d = %s\n", i, identifierString(id.String()))
		}
	}

	if len(cb.Constants) > 0 {
		fmt.Fprintf(w, "\nConstants:\n")
		for i, c := range cb.Constants {
			description := ""
			switch cb.ConstantsRepresentation[i] {
			case SourceCodeRepresentationDouble:
				description = ": in source as double"
			case SourceCodeRepresentationInteger:
				description = ": in source as integer"
			case SourceCodeRepresentationLinkTimeConstant:
				description = ": in source as link-time-constant"
			}
			fmt.Fprintf(w, "   k%d = %s%s\n", i, ConstantString(c), description)
		}
	}

	if len(cb.ExceptionHandlers) > 0 {
		fmt.Fprintf(w, "\nException Handlers:\n")
		for i, h := range cb.ExceptionHandlers {
			fmt.Fprintf(w, "\t %d: { start: [%4d] end: [%4d] target: [%4d] } %s\n", i+1, h.Start, h.End, h.Target, h.Type)
		}
	}

	if len(cb.SwitchJumpTables) > 0 {
		fmt.Fprintf(w, "Switch Jump Tables:\n")
		for i, t := range cb.SwitchJumpTables {
			fmt.Fprintf(w, "  %1d = {\n", i)
			if t.IsList {
				for j := 0; j < len(t.BranchOffsets); j += 2 {
					fmt.Fprintf(w, "\t\t%4d => %04d\n", t.BranchOffsets[j], t.BranchOffsets[j+1])
				}
			} else {
				for j, target := range t.BranchOffsets {
					if target != 0 {
						fmt.Fprintf(w, "\t\t%4d => %04d\n", int32(j)+t.Min, target)
					}
				}
			}
			fmt.Fprintf(w, "\t\tdefault => %04d\n      }\n", t.DefaultOffset)
		}
	}

	if len(cb.StringSwitchJumpTables) > 0 {
		fmt.Fprintf(w, "\nString Switch Jump Tables:\n")
		for i, t := range cb.StringSwitchJumpTables {
			fmt.Fprintf(w, "  %1d = {\n", i)
			keys := make([]string, 0, len(t.OffsetTable))
			for k := range t.OffsetTable {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(a, b int) bool { return t.OffsetTable[keys[a]].IndexInTable < t.OffsetTable[keys[b]].IndexInTable })
			for _, k := range keys {
				fmt.Fprintf(w, "\t\t%q => %04d\n", k, t.OffsetTable[k].BranchOffset)
			}
			fmt.Fprintf(w, "\t\tdefault => %04d\n      }\n", t.DefaultOffset)
		}
	}
}

// identifierString shows symbols as Symbol(description).
func identifierString(s string) string {
	if len(s) > 0 && (s[0] == 0xff || s[0] == 0xfe) {
		return "Symbol(" + s[1:] + ")"
	}
	return s
}

// registerName names a register, showing the value of constants.
func registerName(cb *UnlinkedCodeBlock, r VirtualRegister) string {
	if r.IsConstant() {
		if r.ToConstantIndex() >= len(cb.Constants) {
			return fmt.Sprintf("INVALID_CONSTANT(%s)", r)
		}
		return fmt.Sprintf("%s(%s)", ConstantString(cb.Constants[r.ToConstantIndex()]), r)
	}
	return r.String()
}

// DumpInstruction formats the instruction at offset.
func DumpInstruction(cb *UnlinkedCodeBlock, offset int) string {
	op := OpcodeID(cb.Instructions[offset])
	ins := cb.Instructions[offset : offset+op.Length()]
	var b strings.Builder
	fmt.Fprintf(&b, "[%4d] %-18s ", offset, strings.TrimPrefix(op.String(), "op_"))
	names := op.OperandNames()
	first := true
	for i, kind := range op.OperandKinds() {
		if kind == OperandMetadataID {
			continue
		}
		if !first {
			b.WriteString(", ")
		}
		first = false
		word := ins[i+1]
		b.WriteString(names[i])
		b.WriteByte(':')
		switch kind {
		case OperandVirtualRegister:
			b.WriteString(registerName(cb, VirtualRegister(int32(word))))
		case OperandBoundLabel:
			target := int32(word)
			fmt.Fprintf(&b, "%d(->%d)", target, offset+int(target))
		case Operandint32:
			fmt.Fprint(&b, int32(word))
		case Operandbool:
			fmt.Fprint(&b, word != 0)
		case OperandECMAMode:
			b.WriteString(ECMAMode(word).String())
		case OperandResolveType:
			b.WriteString(ResolveType(word).String())
		case OperandGetPutInfo:
			b.WriteString(GetPutInfo(word).String())
		case OperandSymbolTableOrScopeDepth:
			fmt.Fprint(&b, word)
		default:
			fmt.Fprint(&b, word)
		}
	}
	return strings.TrimRight(b.String(), " ")
}
