package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/baslentfert/jsc2go/ident"
)

func parseProgram(t *testing.T, src string) *ProgramNode {
	t.Helper()
	program, err := ParseProgram(NewSourceCode(NewSourceProvider(src, "test.js", SourceTypeProgram)), NoLexicallyScopedFeatures)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return program
}

func parseError(t *testing.T, src string) *ParserError {
	t.Helper()
	_, err := ParseProgram(NewSourceCode(NewSourceProvider(src, "test.js", SourceTypeProgram)), NoLexicallyScopedFeatures)
	if err == nil {
		t.Fatalf("parse %q: expected an error", src)
	}
	return err
}

func onlyExpr(t *testing.T, program *ProgramNode) ExpressionNode {
	t.Helper()
	s, ok := program.SingleStatement().(*ExprStatementNode)
	if !ok {
		t.Fatalf("expected a single expression statement, got %T", program.SingleStatement())
	}
	return s.Expr
}

func TestConstantFolding(t *testing.T) {
	cases := []struct {
		src       string
		want      float64
		isInteger bool
	}{
		{"1 + 2", 3, true},
		{"2 * 3.5", 7, false},
		{"7 / 2", 3.5, false},
		{"8 / 2", 4, true},
		{"-5", -5, true},
		{"~0", -1, true},
		{"1 << 4", 16, true},
		{"-1 >>> 28", 15, true},
		{"2 ** 10", 1024, true},
		{"7 % 4", 3, true},
	}
	for _, c := range cases {
		n, ok := onlyExpr(t, parseProgram(t, c.src)).(*NumberNode)
		if !ok {
			t.Errorf("%s: not folded", c.src)
			continue
		}
		if n.Value != c.want || n.IsInteger != c.isInteger {
			t.Errorf("%s: got %v (integer %v), want %v (integer %v)", c.src, n.Value, n.IsInteger, c.want, c.isInteger)
		}
	}
	if _, ok := onlyExpr(t, parseProgram(t, "!0")).(*BooleanNode); !ok {
		t.Error("!0 should fold to a boolean")
	}
	if n, ok := onlyExpr(t, parseProgram(t, "-1n")).(*BigIntNode); !ok || !n.Sign {
		t.Error("-1n should fold to a negative BigInt literal")
	}
}

func TestBinaryPrecedence(t *testing.T) {
	e := onlyExpr(t, parseProgram(t, "a + b * c ** d ** e"))
	add, ok := e.(*BinaryOpNode)
	if !ok || add.Op != BinAdd {
		t.Fatalf("top: %T", e)
	}
	mul, ok := add.Expr2.(*BinaryOpNode)
	if !ok || mul.Op != BinMult {
		t.Fatalf("rhs of +: %T", add.Expr2)
	}
	pow, ok := mul.Expr2.(*BinaryOpNode)
	if !ok || pow.Op != BinPow {
		t.Fatalf("rhs of *: %T", mul.Expr2)
	}
	// ** is right-associative: c ** (d ** e).
	if inner, ok := pow.Expr2.(*BinaryOpNode); !ok || inner.Op != BinPow {
		t.Errorf("** should be right associative, rhs is %T", pow.Expr2)
	}

	if l, ok := onlyExpr(t, parseProgram(t, "a || b && c")).(*LogicalOpNode); !ok || l.Operator != LogicalOr {
		t.Errorf("|| should bind looser than &&")
	}
	parseError(t, "a ?? b || c")
	parseError(t, "-a ** b")
}

func TestCallKinds(t *testing.T) {
	cases := map[string]string{
		"f()":              "FunctionCallResolveNode",
		"eval(x)":          "EvalFunctionCallNode",
		"a[b]()":           "FunctionCallBracketNode",
		"(function(){})()": "FunctionCallValueNode",
		"o.f()":            "FunctionCallDotNode",
	}
	for src, want := range cases {
		if got := typeName(onlyExpr(t, parseProgram(t, src))); got != want {
			t.Errorf("%s: got %s, want %s", src, got, want)
		}
	}
	kinds := map[string]CallDotKind{
		"f.call(t, 1)":               CallDotCall,
		"f.apply(t, args)":           CallDotApply,
		"Reflect.apply(f, t, args)":  CallDotNormal,
		"o.hasOwnProperty(k)":        CallDotHasOwnProperty,
		"Reflect.construct(F, args)": CallDotReflectConstruct,
	}
	for src, want := range kinds {
		n, ok := onlyExpr(t, parseProgram(t, src)).(*FunctionCallDotNode)
		if !ok || n.Kind != want {
			t.Errorf("%s: got kind %v, want %v", src, n.Kind, want)
		}
	}
	if program := parseProgram(t, "eval('1')"); !program.UsesEval() {
		t.Error("eval call should set EvalFeature")
	}
}

func typeName(v any) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", v), "*parser.")
}

func TestAssignmentKinds(t *testing.T) {
	cases := map[string]string{
		"x = 1":      "AssignResolveNode",
		"x += 1":     "ReadModifyResolveNode",
		"x ??= 1":    "ShortCircuitReadModifyResolveNode",
		"o.p = 1":    "AssignDotNode",
		"o.p -= 1":   "ReadModifyDotNode",
		"o[k] = 1":   "AssignBracketNode",
		"o[k] *= 2":  "ReadModifyBracketNode",
		"[a, b] = c": "DestructuringAssignmentNode",
		"({a} = c)":  "DestructuringAssignmentNode",
		"f() = 1":    "AssignErrorNode",
	}
	for src, want := range cases {
		if got := typeName(onlyExpr(t, parseProgram(t, src))); got != want {
			t.Errorf("%s: got %s, want %s", src, got, want)
		}
	}
	parseError(t, "'use strict'; f() = 1")
	parseError(t, "f() &&= 1")
}

func TestFunctionNameInference(t *testing.T) {
	program := parseProgram(t, "var f = function() {}; let g = () => 1; var C = class {}; var o = { m: function() {} };")
	names := []string{}
	for _, s := range program.Statements.Statements {
		decl := s.(*DeclarationStatement)
		assign := decl.Expr.(*AssignResolveNode)
		switch v := assign.Right.(type) {
		case BaseFuncExprNode:
			names = append(names, v.Metadata().EcmaName().String())
		case *ClassExprNode:
			names = append(names, v.EcmaName().String())
		case *ObjectLiteralNode:
			m := v.List.Properties[0].Assign.(BaseFuncExprNode)
			names = append(names, m.Metadata().EcmaName().String())
		}
	}
	if got := strings.Join(names, ","); got != "f,g,C,m" {
		t.Errorf("inferred names: %s", got)
	}
}

func TestScopesAndCaptures(t *testing.T) {
	program := parseProgram(t, `
		var a = 1, b = 2;
		let c = 3;
		function f() { return a + c; }
	`)
	if !program.VarDeclarations.Contains(ident.FromString("a")) || !program.VarDeclarations.Contains(ident.FromString("f")) {
		t.Error("a and f should be var declarations")
	}
	if !program.LexicalVariables.Contains(ident.FromString("c")) {
		t.Error("c should be a lexical declaration")
	}
	if !program.Captures(ident.FromString("a")) || program.Captures(ident.FromString("b")) {
		t.Error("a is captured by f, b is not")
	}
	if !program.LexicalVariables.Captures(ident.FromString("c")) {
		t.Error("c is captured by f")
	}
	if len(program.FunctionStack) != 1 || program.FunctionStack[0].Ident.String() != "f" {
		t.Errorf("function stack: %v", program.FunctionStack)
	}
}

func TestEarlyErrors(t *testing.T) {
	cases := []string{
		"let a; let a;",
		"let a; var a;",
		"'use strict'; with (o) {}",
		"'use strict'; var eval;",
		"function f(a, a) { 'use strict'; }",
		"(a, a) => 1",
		"break;",
		"continue;",
		"x: x: ;",
		"return 1",
		"class A { constructor() {} constructor() {} }",
		"class A { #x; #x; }",
		"this.#x",
		"new.target",
		"super()",
		"for (let x of y, z) {}",
		"a => { 'use strict'; with (b) {} }",
		"yield => 1; function* g() { yield => 1 }",
		"async function f() { await => 1 }",
		"({ __proto__: 1, __proto__: 2 })",
		"0789n",
		"'use strict'; 010",
		"`${}`",
		"import x from 'y'",
		"export var x",
	}
	for _, src := range cases {
		parseError(t, src)
	}
}

func TestValidPrograms(t *testing.T) {
	cases := []string{
		"var yield, await, let, async, of, get, set, static;",
		"async\nfunction f() {}",
		"a\n++b",
		"if (a) function f() {}",
		"label: function f() {}",
		"({ get a() {}, set a(v) {}, async *b() {}, [c]: 1, ...d, e, 1: 2, 'f': 3 })",
		"class A extends B { static { this.x = 1 } #p() {} get #q() {} set #q(v) {} static async *m() {} 'constructor'() {} }",
		"for (const [k, v] of Object.entries(o)) {}",
		"for (var i = 0, j = 10; i < j; i++, j--) {}",
		"for (x in o) {} for (x.y of z) {} for ([a, b] of c) {}",
		"switch (a) { case 1: let x = 1; break; default: }",
		"try {} catch ({ message }) {} finally {}",
		"x = a ? b : c ? d : e",
		"tag`hello ${world} \\unicode`",
		"a?.[b]?.(c)",
		"function f() { new.target; () => new.target }",
		"(async () => { await x; for await (const y of z) {} })()",
		"function* g() { const x = yield; yield* g(); }",
		"/[/]/.test(x) / y / z",
		"x = {}\n/foo/g.exec(s)",
	}
	for _, src := range cases {
		parseProgram(t, src)
	}
}

func TestModules(t *testing.T) {
	src := `
		import def, { a, b as c, "str" as d } from "mod";
		import * as ns from "ns" with { type: "json" };
		export const x = 1;
		export default function () {}
		export { a as aa, c };
		export * from "other";
		export * as nsx from "other2";
		await x;
	`
	module, err := ParseModule(NewSourceCode(NewSourceProvider(src, "m.js", SourceTypeModule)), ModuleEvaluateMode)
	if err != nil {
		t.Fatal(err)
	}
	if !module.UsesAwait {
		t.Error("top-level await should be recorded")
	}
	var exported []string
	for name, as := range module.ModuleScopeData.ExportedBindings {
		for _, a := range as {
			exported = append(exported, name.Description()+"->"+a.Description())
		}
	}
	if got := strings.Join(exported, " "); got != "x->x *default*->default a->aa c->c" {
		t.Errorf("exported bindings: %s", got)
	}
}

// TestLazyReparse checks the two-phase design: nested function bodies are
// only syntax checked, then reparsed into a FunctionNode on demand.
func TestLazyReparse(t *testing.T) {
	program := parseProgram(t, "function add(a, b = 2, ...rest) { let s = a + b; for (const r of rest) s += r; return s; }")
	md := program.FunctionStack[0]
	if md.ParseMode != NormalFunctionMode || md.ParameterCount != 1 {
		t.Fatalf("metadata: mode %v, parameter count %d", md.ParseMode, md.ParameterCount)
	}

	fn, err := ParseFunction(md.Source, ParseConfig{
		Name:                    md.Ident,
		ParseMode:               md.ParseMode,
		FunctionMode:            md.FunctionMode,
		SuperBinding:            md.SuperBinding,
		LexicallyScopedFeatures: md.LexicallyScopedFeatures,
		ConstructorKind:         md.ConstructorKind,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fn.Parameters.Size() != 3 || fn.Parameters.IsSimpleParameterList {
		t.Errorf("parameters: %d, simple %v", fn.Parameters.Size(), fn.Parameters.IsSimpleParameterList)
	}
	if !fn.UsesNonSimpleParameterList() {
		t.Error("NonSimpleParameterListFeature should be set")
	}
	// As in JSC, the body is a single block that does not get its own scope:
	// its declarations belong to the function itself.
	body, ok := fn.SingleStatement().(*BlockNode)
	if !ok {
		t.Fatalf("body: %T", fn.SingleStatement())
	}
	if !body.LexicalVariables.IsEmpty() || !fn.LexicalVariables.Contains(ident.FromString("s")) {
		t.Error("`let s` should be declared in the function scope")
	}
	if n := len(body.Statements.Statements); n != 3 {
		t.Fatalf("body has %d statements, want 3", n)
	}
	if _, ok := body.Statements.Statements[2].(*ReturnNode); !ok {
		t.Errorf("last statement: %T", body.Statements.Statements[2])
	}
}

func TestErrorMessages(t *testing.T) {
	cases := map[string]string{
		"var 1;":        "Unexpected number '1'. Expected a parameter pattern or a ')' in parameter list.",
		"if (":          "Unexpected end of script",
		"let let = 1":   "Unexpected keyword 'let'. Cannot use 'let' as an identifier name for a LexicalDeclaration.",
		"a = 'unclosed": "Unexpected EOF",
		"x = 1 +* 2":    "Unexpected token '*'",
	}
	for src, want := range cases {
		if got := parseError(t, src).Message; !strings.HasPrefix(got, want) {
			t.Errorf("%q: got %q, want prefix %q", src, got, want)
		}
	}
}

// TestTest262Sample runs a handful of test262 directories if the suite is
// checked out next to the module (see cmd/test262parse for the full run).
func TestTest262Sample(t *testing.T) {
	root := filepath.Join("..", "test262", "test", "language", "statements", "class")
	if _, err := os.Stat(root); err != nil {
		t.Skip("test262 not checked out")
	}
	failures := 0
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".js") || failures > 10 {
			return nil
		}
		data, _ := os.ReadFile(path)
		src := string(data)
		if flags := regexp.MustCompile(`flags: \[(.*)\]`).FindStringSubmatch(src); flags != nil && (strings.Contains(flags[1], "module") || strings.Contains(flags[1], "onlyStrict")) {
			return nil
		}
		if strings.Contains(src, "decorators") {
			return nil
		}
		expectError := strings.Contains(src, "phase: parse")
		_, perr := ParseProgram(NewSourceCode(NewSourceProvider(src, path, SourceTypeProgram)), NoLexicallyScopedFeatures)
		if (perr != nil) != expectError {
			failures++
			t.Errorf("%s: expectError=%v, got %v", path, expectError, perr)
		}
		return nil
	})
}
