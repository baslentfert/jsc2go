package bytecompiler

import (
	"strings"
	"testing"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/parser"
)

func compileProgram(t *testing.T, src string, mode bytecode.CodeGenerationMode) *bytecode.UnlinkedCodeBlock {
	t.Helper()
	source := parser.NewSourceCode(parser.NewSourceProvider(src, "test.js", parser.SourceTypeProgram))
	cb, err := CompileProgram(source, parser.NoLexicallyScopedFeatures, mode)
	if err != nil {
		t.Fatalf("compile %q: %v", src, err)
	}
	return cb
}

// compileTree compiles all nested functions and validates every code block.
func compileTree(t *testing.T, cb *bytecode.UnlinkedCodeBlock, mode bytecode.CodeGenerationMode) []*bytecode.UnlinkedCodeBlock {
	t.Helper()
	if err := bytecode.Validate(cb); err != nil {
		var b strings.Builder
		bytecode.Dump(&b, cb)
		t.Fatalf("invalid code block: %v\n%s", err, b.String())
	}
	blocks := []*bytecode.UnlinkedCodeBlock{cb}
	for _, list := range [][]*bytecode.UnlinkedFunctionExecutable{cb.FunctionDecls, cb.FunctionExprs} {
		for _, fn := range list {
			kind := bytecode.CodeForCall
			if fn.IsClassConstructorFunction() {
				kind = bytecode.CodeForConstruct
			}
			fcb, err := CodeBlockForFunction(fn, kind, mode)
			if err != nil {
				t.Fatalf("compile %s: %v", fn.EcmaName(), err)
			}
			blocks = append(blocks, compileTree(t, fcb, mode)...)
		}
	}
	return blocks
}

func opcodes(cb *bytecode.UnlinkedCodeBlock) []string {
	var ops []string
	for offset := 0; offset < len(cb.Instructions); {
		op := bytecode.OpcodeID(cb.Instructions[offset])
		ops = append(ops, strings.TrimPrefix(op.String(), "op_"))
		offset += op.Length()
	}
	return ops
}

var snippets = []string{
	`var x = 1; x + 2;`,
	`let a = [1, 2, 3], b = {a, ...a, [a]: 1, get g() { return 1 }, set g(v) {}};`,
	`function f(a, b = a, ...rest) { return arguments.length + rest.length; }`,
	`function* g() { const x = yield 1; try { yield* [x]; } finally { return 2; } }`,
	`async function h(p) { for await (const v of p) { if (v) break; } return await p; }`,
	`async function* ag() { yield await 1; yield* ag(); }`,
	`class A { #x = 1; static #y; static { this.z = 1; } constructor() { this.#x++; } get x() { return this.#x; } static m() { return #y in A; } }`,
	`class B extends A { constructor() { super(); super.m(); } f = () => super.x; }`,
	`switch (x) { case 1: case 2: break; case 3: default: x++; } switch (s) { case "a": case "b": case "c": }`,
	`outer: for (let i in o) { for (const j of a) { if (j) continue outer; else break outer; } }`,
	`try { throw 1 } catch ({e}) { e } finally { }`,
	`with (o) { x = y; } eval("x"); delete o.p; delete o[p]; typeof q; void 0;`,
	`a?.b?.[c]?.(d); a ?? b; a ||= b; a &&= b; a ??= b; a.b **= 2; a[b] >>>= 1;`,
	"tag`a${1}b${2}`; `x${y}z`;",
	`({a, b: [c, ...d], ...e} = f); [a, , b = 1, ...c] = d;`,
	`{ using x = f(); } async function u() { await using y = g(); }`,
	`label: { break label; } do { } while (false); while (x) { x--; }`,
	`new Foo(...args); f.call(o, 1); f.apply(o, [1, 2]); Reflect.construct(F, [1]); o.hasOwnProperty(k);`,
	`import("m"); new.target; x => this;`,
}

func TestCompileSnippets(t *testing.T) {
	for _, mode := range []bytecode.CodeGenerationMode{0, bytecode.CodeGenerationModeDebugger | bytecode.CodeGenerationModeTypeProfiler | bytecode.CodeGenerationModeControlFlowProfiler} {
		for _, src := range snippets {
			// Wrap snippets that use new.target or await at the top level.
			if strings.Contains(src, "new.target") {
				src = "function wrap() { " + src + " }"
			}
			cb := compileProgram(t, src, mode)
			compileTree(t, cb, mode)
		}
	}
}

func TestGeneratorification(t *testing.T) {
	cb := compileProgram(t, `function* g() { let a = 1; yield a; return a; }`, 0)
	blocks := compileTree(t, cb, 0)
	// The program, the wrapper function and the generator body.
	if len(blocks) != 3 {
		t.Fatalf("got %d code blocks, want 3", len(blocks))
	}
	body := blocks[2]
	ops := opcodes(body)
	if ops[0] != "enter" || ops[1] != "switch_imm" {
		t.Fatalf("generator body should start with enter, switch_imm; got %v", ops[:2])
	}
	for _, op := range ops {
		if op == "yield" {
			t.Fatalf("op_yield left after generatorification: %v", ops)
		}
	}
	if len(body.SwitchJumpTables) != 1 || len(body.SwitchJumpTables[0].BranchOffsets) != 2 {
		t.Fatalf("expected a 2-entry state switch, got %+v", body.SwitchJumpTables)
	}
}

func TestCompareAndJumpFusion(t *testing.T) {
	cb := compileProgram(t, `function f(a, b) { if (a < b) return 1; return 2; }`, 0)
	blocks := compileTree(t, cb, 0)
	ops := strings.Join(opcodes(blocks[1]), " ")
	if !strings.Contains(ops, "jnless") || strings.Contains(ops, " less ") {
		t.Fatalf("expected a fused jnless, got %s", ops)
	}
}

func TestTypeofComparison(t *testing.T) {
	cb := compileProgram(t, `function f(a) { return typeof a === "undefined"; }`, 0)
	blocks := compileTree(t, cb, 0)
	ops := strings.Join(opcodes(blocks[1]), " ")
	if !strings.Contains(ops, "typeof_is_undefined") || strings.Contains(ops, "stricteq") {
		t.Fatalf("expected typeof_is_undefined, got %s", ops)
	}
}

func TestConstantArray(t *testing.T) {
	cb := compileProgram(t, `var a = [1, 2.5, 3];`, 0)
	found := false
	for _, c := range cb.Constants {
		if buf, ok := c.(*bytecode.ArrayBuffer); ok {
			found = true
			if len(buf.Values) != 3 || buf.IndexingType&bytecode.CopyOnWrite == 0 {
				t.Fatalf("bad array buffer %+v", buf)
			}
		}
	}
	if !found {
		t.Fatal("expected a constant array buffer")
	}
}
