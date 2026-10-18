package vm

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestNativesKeepNoCall checks the rule nativeFunction.invoke relies on: a
// native function does not keep its *Call or c.Args after it returns (the
// Call and its argument buffer are reused). It looks at every function
// literal with a *Call parameter and reports closures inside it that use
// the parameter (but those that run during the call: arguments of vm.Try
// and closures called where they are written), and statements that store
// the parameter or its Args (assignments, composite literals, returns,
// channel sends).
func TestNativesKeepNoCall(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			var typ *ast.FuncType
			var body *ast.BlockStmt
			switch fn := n.(type) {
			case *ast.FuncLit:
				typ, body = fn.Type, fn.Body
			case *ast.FuncDecl:
				typ, body = fn.Type, fn.Body
			default:
				return true
			}
			if body == nil {
				return true
			}
			for _, field := range typ.Params.List {
				star, ok := field.Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				if id, ok := star.X.(*ast.Ident); !ok || id.Name != "Call" {
					continue
				}
				for _, p := range field.Names {
					checkCallParam(t, fset, body, p)
				}
			}
			return true
		})
	}
}

// checkCallParam reports the places in body that may keep param.
func checkCallParam(t *testing.T, fset *token.FileSet, body *ast.BlockStmt, param *ast.Ident) {
	t.Helper()
	isParam := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Obj != nil && id.Obj == param.Obj
	}
	// kept reports whether e is the parameter itself or its Args slice.
	kept := func(e ast.Expr) bool {
		if isParam(e) {
			return false // passing c on is fine; storing it is checked below
		}
		if sel, ok := e.(*ast.SelectorExpr); ok && sel.Sel.Name == "Args" && isParam(sel.X) {
			return true
		}
		return false
	}
	report := func(n ast.Node, what string) {
		t.Errorf("%s: native function %s %s", fset.Position(n.Pos()), what, param.Name)
	}
	// Closures that run during the call.
	sync := map[*ast.FuncLit]bool{}
	var walk func(n ast.Node, nested bool) bool
	walk = func(n ast.Node, nested bool) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if lit, ok := x.Fun.(*ast.FuncLit); ok {
				sync[lit] = true
			}
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Try" {
				for _, a := range x.Args {
					if lit, ok := a.(*ast.FuncLit); ok {
						sync[lit] = true
					}
				}
			}
		case *ast.FuncLit:
			if sync[x] {
				return true
			}
			ast.Inspect(x.Body, func(m ast.Node) bool {
				if id, ok := m.(*ast.Ident); ok && isParam(id) {
					report(id, "captures")
				}
				return true
			})
			return false
		case *ast.AssignStmt:
			for i, rhs := range x.Rhs {
				// c.Args into a local variable is fine (args := c.Args); into
				// anything else (a field, a map, an element) it is kept.
				if kept(rhs) || isParam(rhs) {
					if i < len(x.Lhs) {
						if _, local := x.Lhs[i].(*ast.Ident); local {
							continue
						}
					}
					report(x, "stores")
				}
			}
		case *ast.CompositeLit:
			for _, e := range x.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					e = kv.Value
				}
				if kept(e) || isParam(e) {
					report(x, "stores")
				}
			}
		case *ast.ReturnStmt:
			for _, e := range x.Results {
				if kept(e) || isParam(e) {
					report(x, "returns")
				}
			}
		case *ast.SendStmt:
			if kept(x.Value) || isParam(x.Value) {
				report(x, "sends")
			}
		}
		return true
	}
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		return walk(n, false)
	})
}
