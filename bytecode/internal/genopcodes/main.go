// Command genopcodes generates opcodes_gen.go from JSC's BytecodeList.rb, the
// same DSL JSC's generator/main.rb consumes. Only the Bytecode section is used.
//
// Instructions are encoded as a flat []uint32: the opcode followed by one word
// per argument, plus one word for the metadata ID if the opcode has metadata.
// This replaces JSC's narrow/wide16/wide32 byte encoding, which exists to
// keep the stream small for the native LLInt.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"strings"
	"unicode"
)

type arg struct {
	name string
	typ  string
}

type opcode struct {
	name        string // e.g. "get_by_id"
	args        []arg
	metadata    []string // top-level metadata field names
	tmps        []string
	checkpoints []string
}

func main() {
	in := flag.String("in", "BytecodeList.rb", "input DSL")
	out := flag.String("out", "opcodes_gen.go", "output Go file")
	flag.Parse()

	src, err := os.ReadFile(*in)
	if err != nil {
		fail(err)
	}
	ops := parse(string(src))
	code := generate(ops)
	formatted, err := format.Source(code)
	if err != nil {
		os.WriteFile(*out, code, 0o644)
		fail(fmt.Errorf("formatting generated code: %w", err))
	}
	if err := os.WriteFile(*out, formatted, 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "genopcodes:", err)
	os.Exit(1)
}

// --- DSL parsing ------------------------------------------------------------------------

var commentRe = regexp.MustCompile(`#[^\n]*`)

func parse(src string) []opcode {
	start := strings.Index(src, "begin_section :Bytecode")
	end := strings.Index(src, "end_section :Bytecode")
	if start < 0 || end < 0 {
		fail(fmt.Errorf("Bytecode section not found"))
	}
	body := commentRe.ReplaceAllString(src[start:end], "")
	// Skip the section header's option list.
	body = body[strings.Index(body, "\nop"):]

	var ops []opcode
	p := &scanner{s: body}
	for {
		p.skipSpace()
		if p.eof() {
			break
		}
		switch kw := p.ident(); kw {
		case "op":
			p.skipSpace()
			name := p.symbol()
			ops = append(ops, p.opBody([]string{name})...)
		case "op_group":
			p.skipSpace()
			p.symbol() // group name
			p.expect(",")
			p.skipSpace()
			p.expect("[")
			var names []string
			for {
				p.skipSpace()
				if p.peek() == ']' {
					p.pos++
					break
				}
				names = append(names, p.symbol())
				p.skipSpace()
				if p.peek() == ',' {
					p.pos++
				}
			}
			ops = append(ops, p.opBody(names)...)
		default:
			fail(fmt.Errorf("unexpected %q at %d", kw, p.pos))
		}
	}
	return ops
}

type scanner struct {
	s   string
	pos int
}

func (p *scanner) eof() bool  { return p.pos >= len(p.s) }
func (p *scanner) peek() byte { return p.s[p.pos] }

func (p *scanner) skipSpace() {
	for !p.eof() && unicode.IsSpace(rune(p.s[p.pos])) {
		p.pos++
	}
}

func (p *scanner) expect(tok string) {
	p.skipSpace()
	if !strings.HasPrefix(p.s[p.pos:], tok) {
		fail(fmt.Errorf("expected %q at %q", tok, p.s[p.pos:min(len(p.s), p.pos+40)]))
	}
	p.pos += len(tok)
}

func (p *scanner) ident() string {
	start := p.pos
	for !p.eof() && (p.peek() == '_' || p.peek() == '?' || unicode.IsLetter(rune(p.peek())) || unicode.IsDigit(rune(p.peek()))) {
		p.pos++
	}
	return p.s[start:p.pos]
}

func (p *scanner) symbol() string {
	p.expect(":")
	return p.ident()
}

// value reads a type expression up to the next ',' or '}' at depth 0.
func (p *scanner) value() string {
	p.skipSpace()
	start := p.pos
	depth := 0
	for !p.eof() {
		c := p.peek()
		switch {
		case c == '[' || c == '{':
			depth++
		case (c == ']' || c == '}') && depth > 0:
			depth--
		case (c == ',' || c == '}') && depth == 0:
			return strings.TrimSpace(p.s[start:p.pos])
		}
		p.pos++
	}
	return strings.TrimSpace(p.s[start:p.pos])
}

// dict parses `{ key: value, ... }` and returns the entries in order.
func (p *scanner) dict() []arg {
	p.expect("{")
	var out []arg
	for {
		p.skipSpace()
		if p.peek() == '}' {
			p.pos++
			return out
		}
		key := p.ident()
		p.expect(":")
		p.skipSpace()
		var val string
		if p.peek() == '{' {
			p.dict() // nested metadata struct
			val = "{...}"
		} else {
			val = p.value()
		}
		out = append(out, arg{key, val})
		p.skipSpace()
		if p.peek() == ',' {
			p.pos++
		}
	}
}

// opBody parses the optional `, key: {...}, ...` clauses after an op name.
func (p *scanner) opBody(names []string) []opcode {
	var tmpl opcode
	for {
		save := p.pos
		p.skipSpace()
		if p.eof() || p.peek() != ',' {
			p.pos = save
			break
		}
		p.pos++
		p.skipSpace()
		key := p.ident()
		p.expect(":")
		entries := p.dict()
		switch key {
		case "args":
			tmpl.args = entries
		case "metadata":
			for _, e := range entries {
				tmpl.metadata = append(tmpl.metadata, e.name)
			}
		case "tmps":
			for _, e := range entries {
				tmpl.tmps = append(tmpl.tmps, e.name)
			}
		case "checkpoints":
			for _, e := range entries {
				tmpl.checkpoints = append(tmpl.checkpoints, e.name)
			}
		case "metadata_initializers":
		default:
			fail(fmt.Errorf("unknown op key %q", key))
		}
	}
	var out []opcode
	for _, n := range names {
		op := tmpl
		op.name = n
		out = append(out, op)
	}
	return out
}

// --- Code generation ----------------------------------------------------------------------

// goType maps a DSL operand type to its Go type in package bytecode.
func goType(t string) string {
	switch t {
	case "unsigned":
		return "uint32"
	case "int":
		return "int32"
	case "bool":
		return "bool"
	}
	return t
}

func camel(s string, upperFirst bool) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for i, part := range parts {
		if part == "" {
			continue
		}
		if i == 0 && !upperFirst {
			b.WriteString(part)
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

func fieldName(a arg) string {
	n := strings.TrimSuffix(a.name, "?")
	return strings.ToUpper(n[:1]) + n[1:]
}

func paramName(a arg) string {
	n := strings.TrimSuffix(a.name, "?")
	switch n {
	case "type", "var", "function", "default", "func", "range":
		return n + "_"
	}
	return n
}

func (op opcode) typeName() string  { return "Op" + camel(op.name, true) }
func (op opcode) constName() string { return "Op" + camel(op.name, true) + "ID" }
func (op opcode) length() int {
	n := len(op.args)
	if len(op.metadata) > 0 {
		n++
	}
	return n
}

func generate(ops []opcode) []byte {
	var b bytes.Buffer
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("// Code generated by genopcodes from BytecodeList.rb. DO NOT EDIT.\n\n")
	w("package bytecode\n\nimport \"unsafe\"\n\n")

	w("// Opcode IDs, in BytecodeList.rb order.\nconst (\n")
	for i, op := range ops {
		if i == 0 {
			w("\t%s OpcodeID = iota\n", op.constName())
		} else {
			w("\t%s\n", op.constName())
		}
	}
	w("\tNumberOfBytecodeIDs = %d\n)\n\n", len(ops))

	w("var opcodeNames = [NumberOfBytecodeIDs]string{\n")
	for _, op := range ops {
		w("\t%q,\n", "op_"+op.name)
	}
	w("}\n\n")

	w("// opcodeLengths is the number of operand words after the opcode.\n")
	w("var opcodeLengths = [NumberOfBytecodeIDs]uint8{\n")
	for _, op := range ops {
		w("\t%d, // %s\n", op.length(), op.name)
	}
	w("}\n\n")

	w("var opcodeHasMetadata = [NumberOfBytecodeIDs]bool{\n")
	for _, op := range ops {
		if len(op.metadata) > 0 {
			w("\t%s: true,\n", op.constName())
		}
	}
	w("}\n\n")

	w("// opcodeNumberOfCheckpoints is non-zero for ops that can exit mid-instruction.\n")
	w("var opcodeNumberOfCheckpoints = [NumberOfBytecodeIDs]uint8{\n")
	for _, op := range ops {
		if len(op.checkpoints) > 0 {
			w("\t%s: %d,\n", op.constName(), len(op.checkpoints))
		}
	}
	w("}\n\n")

	w("var opcodeOperandNames = [NumberOfBytecodeIDs][]string{\n")
	for _, op := range ops {
		var names []string
		for _, a := range op.args {
			names = append(names, fmt.Sprintf("%q", strings.TrimSuffix(a.name, "?")))
		}
		if len(op.metadata) > 0 {
			names = append(names, `"metadataID"`)
		}
		w("\t%s: {%s},\n", op.constName(), strings.Join(names, ", "))
	}
	w("}\n\n")

	w("var opcodeOperandKinds = [NumberOfBytecodeIDs][]OperandKind{\n")
	for _, op := range ops {
		var kinds []string
		for _, a := range op.args {
			kinds = append(kinds, "Operand"+goType(a.typ))
		}
		if len(op.metadata) > 0 {
			kinds = append(kinds, "OperandMetadataID")
		}
		w("\t%s: {%s},\n", op.constName(), strings.Join(kinds, ", "))
	}
	w("}\n\n")

	for _, op := range ops {
		genOp(&b, op)
	}
	return b.Bytes()
}

func encode(a arg, expr string) string {
	switch goType(a.typ) {
	case "bool":
		return "boolWord(" + expr + ")"
	}
	return "uint32(" + expr + ")"
}

// wordTypes are the operand types that are 32-bit words.
var wordTypes = map[string]bool{
	"uint32": true, "int32": true, "VirtualRegister": true, "BoundLabel": true,
	"SymbolTableOrScopeDepth": true, "ResolveType": true, "GetPutInfo": true,
	"ECMAMode": true, "OperandTypes": true, "ResultType": true, "PutByIdFlags": true,
}

// wordLayout reports whether op's struct has a 32-bit field per operand
// word, so that it can be read from the instruction directly.
func wordLayout(op opcode) bool {
	for _, a := range op.args {
		if !wordTypes[goType(a.typ)] {
			return false
		}
	}
	return true
}

func decode(a arg, word string) string {
	switch t := goType(a.typ); t {
	case "bool":
		return word + " != 0"
	case "uint32":
		return word
	default:
		return t + "(" + word + ")"
	}
}

func genOp(b *bytes.Buffer, op opcode) {
	w := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	tn := op.typeName()
	hasMetadata := len(op.metadata) > 0

	w("// %s is op_%s.\ntype %s struct {\n", tn, op.name, tn)
	for _, a := range op.args {
		w("\t%s %s\n", fieldName(a), goType(a.typ))
	}
	if hasMetadata {
		w("\tMetadataID uint32\n")
	}
	w("}\n\n")

	w("func (%s) OpcodeID() OpcodeID { return %s }\n\n", tn, op.constName())

	// Field indices, for patching operands in place (JSC's setters).
	for i, a := range op.args {
		w("const %s%sIndex = %d\n", tn, fieldName(a), i+1)
	}
	if len(op.args) > 0 {
		w("\n")
	}

	// Checkpoints, numbered in declaration order.
	for i, c := range op.checkpoints {
		w("const %s%sCheckpoint = %d\n", tn, strings.ToUpper(c[:1])+c[1:], i)
	}
	if len(op.checkpoints) > 0 {
		w("\n")
	}

	// Emit.
	var params, words []string
	for _, a := range op.args {
		params = append(params, paramName(a)+" "+goType(a.typ))
		words = append(words, encode(a, paramName(a)))
	}
	w("// Emit%s writes op_%s.\n", camel(op.name, true), op.name)
	w("func Emit%s(gen Emitter", camel(op.name, true))
	for _, p := range params {
		w(", %s", p)
	}
	w(") {\n")
	if len(op.checkpoints) > 0 {
		w("\tgen.SetUsesCheckpoints()\n")
	}
	if hasMetadata {
		w("\tmetadataID := gen.AddMetadataFor(%s)\n", op.constName())
		words = append(words, "metadataID")
	}
	w("\tgen.RecordOpcode(%s)\n", op.constName())
	w("\tgen.Write(uint32(%s)", op.constName())
	for _, word := range words {
		w(", %s", word)
	}
	w(")\n}\n\n")

	// Decode.
	w("// Decode%s decodes op_%s; ins starts at the opcode.\n", camel(op.name, true), op.name)
	w("func Decode%s(ins []uint32) %s {\n", camel(op.name, true), tn)
	n := len(op.args)
	if hasMetadata {
		n++
	}
	if n > 0 {
		// One bounds check for all operands.
		w("\t_ = ins[%d]\n", n)
	}
	if n > 0 && wordLayout(op) {
		// The struct is the operand words: decoding is a load, cheap
		// enough for Go to inline it into the interpreter loop (which,
		// being huge, only gets callees of inlining cost up to 20).
		w("\treturn *(*%s)(unsafe.Pointer(&ins[1]))\n}\n\n", tn)
		w("var _ = [1]struct{}{}[unsafe.Sizeof(%s{})-%d] // the layout is %d words\n\n", tn, 4*n, n)
		return
	}
	w("\treturn %s{", tn)
	var inits []string
	for i, a := range op.args {
		inits = append(inits, fmt.Sprintf("%s: %s", fieldName(a), decode(a, fmt.Sprintf("ins[%d]", i+1))))
	}
	if hasMetadata {
		inits = append(inits, fmt.Sprintf("MetadataID: ins[%d]", len(op.args)+1))
	}
	w("%s}\n}\n\n", strings.Join(inits, ", "))
}
