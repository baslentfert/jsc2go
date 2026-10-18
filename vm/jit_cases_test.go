//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package vm

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/baslentfert/jsc2go/bytecode"
)

// TestJITNoDeadCases checks that no instruction objectInstruction compiles
// in its switch is also among jitGenericOps or jitColdOps, which it sends
// to the slow path before the switch: such a case is dead code (new_reg_exp
// was, from 84d189f until the opcode left jitColdOps, while its tests
// passed on the slow path).
func TestJITNoDeadCases(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "jit_slow.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The Go name of each opcode (op_new_reg_exp is OpNewRegExpID).
	byName := map[string]bytecode.OpcodeID{}
	for op := bytecode.OpcodeID(0); op < bytecode.NumberOfBytecodeIDs; op++ {
		name := "Op"
		for _, part := range strings.Split(strings.TrimPrefix(op.String(), "op_"), "_") {
			if part != "" {
				name += strings.ToUpper(part[:1]) + part[1:]
			}
		}
		byName[name+"ID"] = op
	}
	var body *ast.BlockStmt
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "objectInstruction" {
			body = f.Body
		}
	}
	if body == nil {
		t.Fatal("objectInstruction not found")
	}
	n := 0
	for _, s := range body.List {
		sw, ok := s.(*ast.SwitchStmt)
		if !ok {
			continue
		}
		if tag, ok := sw.Tag.(*ast.Ident); !ok || tag.Name != "op" {
			continue
		}
		for _, c := range sw.Body.List {
			for _, e := range c.(*ast.CaseClause).List {
				sel, ok := e.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				op, ok := byName[sel.Sel.Name]
				if !ok {
					t.Errorf("case %s: no such opcode", sel.Sel.Name)
					continue
				}
				n++
				if jitGenericOps[op] || jitColdOps[op] {
					t.Errorf("%s has a case in objectInstruction but goes to the slow path before it", op)
				}
			}
		}
	}
	if n < 10 {
		t.Fatalf("found only %d cases: did objectInstruction change shape?", n)
	}
}
