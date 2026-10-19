//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package vm

import (
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/baslentfert/jsc2go/bytecode"
)

// TestJITDifferential runs random numeric loops with and without the JIT
// tier and compares what they compute.
func TestJITDifferential(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	n := 300
	if testing.Verbose() && os.Getenv("JIT_FUZZ_N") != "" {
		n, _ = strconv.Atoi(os.Getenv("JIT_FUZZ_N"))
	}
	if testing.Short() {
		n = 40
	}
	ran := 0
	for seed := int64(1); seed <= int64(n); seed++ {
		src := randomLoopProgram(rand.New(rand.NewSource(seed)))
		before := jitCalls.Load()
		jitForceFar = seed%2 == 0
		with := runForTest(t, src, true)
		jitForceFar = false
		if jitCalls.Load() > before {
			ran++
		}
		without := runForTest(t, src, false)
		if with != without {
			t.Fatalf("seed %d: JIT %q, interpreter %q\n%s", seed, with, without, src)
		}
	}
	t.Logf("generated code ran in %d of %d programs", ran, n)
	if ran < n/2 {
		t.Errorf("generated code ran in only %d of %d programs", ran, n)
	}
}

func runForTest(t *testing.T, src string, useJIT bool) string {
	saved := jitEnabled
	jitEnabled = useJIT
	defer func() { jitEnabled = saved }()
	v := New()
	var out strings.Builder
	v.Print = func(s string) { out.WriteString(s); out.WriteByte('\n') }
	if _, err := v.Eval(src, "fuzz.js"); err != nil {
		out.WriteString("error: " + err.Error())
	}
	return out.String()
}

// compareJIT runs src with and without the JIT and fails if what it prints
// differs, is empty or is an error.
func compareJIT(t *testing.T, src string) {
	t.Helper()
	with := runForTest(t, src, true)
	without := runForTest(t, src, false)
	if with != without {
		t.Fatalf("JIT %q, interpreter %q", with, without)
	}
	if with == "" || strings.HasPrefix(with, "error:") {
		t.Fatalf("no result: %q", with)
	}
}

// randomLoopProgram makes a function with a hot loop over locals, an array
// and typed arrays; values change type now and then, so that guards fail.
func randomLoopProgram(r *rand.Rand) string {
	vars := []string{"a", "b", "c", "d"}
	pick := func(s []string) string { return s[r.Intn(len(s))] }
	consts := []string{"0", "1", "-1", "3", "7", "31", "0.5", "-0", "1e9", "2147483647", "-2147483648", "4294967295", "NaN", "Infinity", "1e300"}
	odd := []string{`"s"`, "undefined", "null", "true", "{}", "[]", `"12"`}
	operand := func() string {
		switch r.Intn(6) {
		case 0:
			return pick(consts)
		case 1:
			return "i"
		case 2:
			return "arr[i % arr.length]"
		case 3:
			return pick([]string{"f64", "i32", "u8", "i16", "u32", "f32", "c8"}) + "[i % 16]"
		case 4:
			return pick([]string{"o.x", "o.y", "o.p", "o.g", "q.x", "outer", "G", "L", "objs[i % 5].x"})
		}
		return pick(vars)
	}
	binops := []string{"+", "-", "*", "/", "%", "|", "&", "^", "<<", ">>", ">>>"}
	cmps := []string{"<", "<=", ">", ">=", "==", "!=", "===", "!=="}
	var body strings.Builder
	for k := 0; k < 3+r.Intn(6); k++ {
		switch r.Intn(18) {
		case 9:
			fmt.Fprintf(&body, "%s = %s;\n", pick([]string{"o.x", "o.y", "q.x", "outer", "G", "L", "objs[i % 5].x"}), operand())
		case 10:
			fmt.Fprintf(&body, "if (i == %d) %s;\n", 300+r.Intn(4000), pick([]string{
				"o.z = 1", "delete o.y", "o = {y: 2, x: 1}", "q = Object.create(o)", "outer = 'str'",
				"Object.defineProperty(o, 'x', {get() { return 9 }})", "G = undefined", "L = {}"}))
		case 11:
			fmt.Fprintf(&body, "%s += %s;\n", pick([]string{"o.x", "outer", "G", "L"}), operand())
		case 12:
			fmt.Fprintf(&body, "if (%s %s %s) break;\n", operand(), pick(cmps), "1e15")
		case 14:
			fmt.Fprintf(&body, "%s = (%s %s %s) %s (%s %s %s);\n", pick(vars), operand(), pick(cmps), operand(),
				pick([]string{"&", "|", "^", "+", "<<"}), operand(), pick(cmps), operand())
		case 15:
			fmt.Fprintf(&body, "%s = h(%s, i);\n", pick(vars), operand())
		case 16:
			fmt.Fprintf(&body, "bump(o); %s = %s;\n", pick(vars), pick([]string{"outer", "o.x", "G"}))
		case 17:
			fmt.Fprintf(&body, "%s = rec(i %% 5) + %s;\n", pick(vars), operand())
		case 13:
			fmt.Fprintf(&body, "if ((%s >>> 0) %s (%s >>> 0)) %s = %s;\n", operand(), pick([]string{"<", "<=", ">", ">="}), operand(), pick(vars), operand())
		case 0, 1, 2:
			fmt.Fprintf(&body, "%s = %s %s %s;\n", pick(vars), operand(), pick(binops), operand())
		case 3:
			fmt.Fprintf(&body, "if (%s %s %s) %s++; else %s--;\n", operand(), pick(cmps), operand(), pick(vars), pick(vars))
		case 4:
			fmt.Fprintf(&body, "arr[i %% arr.length] = %s;\n", operand())
		case 5:
			fmt.Fprintf(&body, "%s[i %% 16] = %s;\n", pick([]string{"f64", "i32", "u8", "i16", "u32", "f32", "c8"}), operand())
		case 6:
			fmt.Fprintf(&body, "if (i == %d) %s = %s;\n", 500+r.Intn(3000), pick(vars), pick(odd))
		case 7:
			fmt.Fprintf(&body, "%s = ~%s + -%s;\n", pick(vars), operand(), operand())
		case 8:
			fmt.Fprintf(&body, "if (!%s) %s = (%s >>> 0) %% 1000;\n", operand(), pick(vars), operand())
		}
	}
	return fmt.Sprintf(`var G = 5; let L = 6;
var proto = {p: 3, get g() { return 4 }};
function f() {
  var outer = 1;
  return (function () {
  var o = Object.create(proto); o.x = 1; o.y = 2;
  var q = {x: 7};
  var objs = [{x: 1}, {y: 2, x: 3}, {z: 1, y: 1, x: 5}, Object.create({x: 9}), {w: 0, x: 2}];
  var a = 1, b = 2, c = 3, d = 4;
  function h(v, i) { if (i === %d) throw new Error("boom"); outer = (outer | 0) + 1; return typeof v == "number" ? v * 2 : v; }
  function bump(obj) { obj.x = (obj.x | 0) + 3; G = (G | 0) + 1; }
  function rec(n) { return n <= 0 ? 0 : 1 + rec(n - 1); }
  var arr = [1, 2.5, 3, , 5, -0, 7, 8];
  var f64 = new Float64Array(16), i32 = new Int32Array(16), u8 = new Uint8Array(16), i16 = new Int16Array(16);
  var u32 = new Uint32Array(16), f32 = new Float32Array(16), c8 = new Uint8ClampedArray(16);
  try {
  for (var i = 0; i < 5000; i++) {
%s  }
  } catch (e) { a = "caught " + e.message + " at " + i; }
  return [a, b, c, d, arr.join(), Array.from(f64).join(), Array.from(i32).join(), Array.from(u8).join(),
    Array.from(i16).join(), Array.from(u32).join(), Array.from(f32).join(), Array.from(c8).join(),
    o.x, o.y, o.z, q.x, objs.map(v => v.x).join(), outer, G, typeof L == "object" ? "obj" : L].map(x => Object.is(x, -0) ? "-0" : String(x)).join(";");
  })();
}
print(f()); print(f());
`, []any{[]int{-1, 1200, 4321}[r.Intn(3)], body.String()}...)
}

// TestJITNestedCalls runs calls from generated code to generated code
// whose callees stop in every way (exceptions deep in a chain, guards,
// loops, recursion deeper than the chain, the call depth limit, with
// statements, getters) with and without the JIT, the JIT compiling
// everything at once.
func TestJITNestedCalls(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		function leaf(x, i) { if (i === 7777) throw new Error("boom at " + i); return typeof x === "number" ? x + 1 : String(x).length; }
		function mid(x, i) { var s = 0; for (var k = 0; k < (i % 3); k++) s += leaf(x, i); return s + leaf(x, i); }
		function top(x, i) { return mid(x, i) * 2; }
		function run(vals) {
		  var acc = 0;
		  for (var i = 0; i < 20000; i++) {
		    try { acc = (acc + top(vals[i % vals.length], i)) | 0; } catch (e) { out.push(e.message); }
		  }
		  return acc;
		}
		out.push(run([1, 2.5, 3]), run([1, "abc", null, {}, 4]));
		function rec(n) { return n === 0 ? 0 : 1 + rec(n - 1); }
		var r = 0; for (var i = 0; i < 300; i++) r += rec(150); out.push(r);
		function inf(n) { return inf(n + 1) + 1; }
		try { inf(0); } catch (e) { out.push(e instanceof RangeError, e.message); }
		function slowLoop(n) { var s = 0; for (var j = 0; j < n; j++) s += j; return s; }
		function callsLoop() { var t = 0; for (var i = 0; i < 3000; i++) t = (t + slowLoop(20000)) | 0; return t; }
		out.push(callsLoop());
		var obj = { get v() { return 5; } };
		function getter(o) { return o.v; }
		var g = 0; for (var i = 0; i < 5000; i++) g += getter(i % 2 ? obj : { v: 1 });
		out.push(g);
		var w = { q: 2 };
		function useThis() { return this && this.q; }
		var wr = 0; for (var i = 0; i < 3000; i++) { with (w) { wr += (function () { return useThis(); })() || 0; } }
		out.push(wr);
		function strict() { "use strict"; return this; }
		var st = 0; for (var i = 0; i < 3000; i++) if (strict() === undefined) st++;
		out.push(st);
		print(out.join("|"));`
	compareJIT(t, src)
}

// TestJITPropertyAdds runs put_by_id stores that add properties, which
// generated code makes itself, in every way the cache must refuse or
// miss: non-extensible objects, setters and read-only properties appearing
// on the prototype, storage without room, other prototypes with the same
// shape, and polymorphic sites; with and without the JIT, the JIT
// compiling everything at once.
func TestJITPropertyAdds(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		function P(a, b, c, d, e) { this.a = a; this.b = b; this.c = c; this.d = d; this.e = e; }
		var keep = [];
		for (var i = 0; i < 3000; i++) {
		  var o = new P(i, i + 1, "s" + i, null, {});
		  if (i === 1000) Object.defineProperty(P.prototype, "c", { set(v) { out.push("setter " + v); }, configurable: true });
		  if (i === 1500) delete P.prototype.c;
		  if (i === 2000) Object.defineProperty(P.prototype, "d", { value: 9, writable: false, configurable: true });
		  if (i === 2500) delete P.prototype.d;
		  if (i % 997 === 0) keep.push(JSON.stringify(o) + "|" + Object.keys(o).join());
		}
		function addX(o, v) { o.x = v; o.y = v + 1; return o; }
		var plain = 0, frozen = 0, sealed = 0, nonext = 0;
		for (var i = 0; i < 3000; i++) {
		  var t = {};
		  if (i % 4 === 1) Object.preventExtensions(t);
		  if (i % 4 === 2) Object.freeze(t);
		  if (i % 4 === 3) Object.seal(t);
		  addX(t, i);
		  if (t.x === i) plain++; else if (Object.isFrozen(t)) frozen++; else if (Object.isSealed(t)) sealed++; else nonext++;
		}
		out.push(plain, frozen, sealed, nonext);
		function Big() { for (var k = 0; k < 20; k++) this["p" + k] = k; this.q = 1; this.r = 2; this.s = 3; }
		var bs = 0; for (var i = 0; i < 2000; i++) { var b = new Big(); bs += b.s + b.p19; }
		out.push(bs);
		var proto = { z: 1 }; function Z() { this.w = 1; }
		for (var i = 0; i < 3000; i++) { if (i === 1500) Z.prototype = proto; var z = new Z(); z.v = 2; if (i % 1000 === 0) out.push(Object.keys(z).join() + z.z); }
		// One site adding to objects of many shapes: the polymorphic cache fills up and misses.
		function addZ(o, v) { o.zz = v; return o; }
		var polySum = 0;
		for (var i = 0; i < 6000; i++) {
		  var t;
		  switch (i % 7) {
		  case 0: t = {}; break;
		  case 1: t = {a: 1}; break;
		  case 2: t = {b: 1, c: 2}; break;
		  case 3: t = {d: 1, e: 2, f: 3}; break;
		  case 4: t = Object.create({q: 1}); break;
		  case 5: t = []; break;
		  default: t = {g: 1}; Object.preventExtensions(t);
		  }
		  addZ(t, i);
		  polySum += (t.zz | 0) + Object.keys(t).length;
		}
		out.push(polySum);
		// Same (empty) shape, different prototypes: a case whose shape matches but
		// whose prototype chain does not, with more cases behind it.
		var protos = [{p: 0}, {p: 1}, {p: 2}, {p: 3}, {p: 4}, {p: 5}];
		function setV(o, v) { o.vv = v; return o; }
		var protoSum = 0;
		for (var i = 0; i < 6000; i++) {
		  var o = (i % 3 === 2) ? {} : Object.create(protos[i % protos.length]);
		  setV(o, i);
		  protoSum += o.vv + (o.p | 0);
		}
		out.push(protoSum);
		// Polymorphic: other shapes first, then the same empty shape with
		// different prototypes, so that a later case of the list matches the shape
		// but not the prototype chain.
		var pA = {pa: 1}, pB = {pb: 2};
		function setW(o, v) { o.ww = v; return o; }
		var wSum = 0;
		for (var i = 0; i < 6000; i++) {
		  var o;
		  switch (i % 5) {
		  case 0: o = {a: 1}; break;
		  case 1: o = {b: 1}; break;
		  case 2: o = Object.create(pA); break;
		  case 3: o = Object.create(pB); break;
		  default: o = {c: 1};
		  }
		  setW(o, i);
		  wSum += o.ww + (o.pa | 0) + (o.pb | 0) + Object.keys(o).length;
		}
		out.push(wSum);
		out.push(keep.join("/"));
		print(out.join("|"));`
	compareJIT(t, src)
}

// TestJITConstruct runs new through generated code (create_this and
// new_object from the object pools, construct as a direct call): classes,
// derived classes, constructors returning objects or primitives,
// new.target, throwing constructors, non-constructors, a replaced
// prototype and polymorphic sites; with and without the JIT, the JIT
// compiling everything at once.
func TestJITConstruct(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		function V(x, y) { this.x = x; this.y = y; }
		V.prototype.len = function () { return this.x + this.y; };
		function Ret(a) { this.a = a; if (a % 3 === 0) return { other: a }; if (a % 3 === 1) return 5; }
		function NT() { this.t = new.target === NT; }
		function Thrower(i) { if (i === 777) throw new Error("ctor " + i); this.i = i; }
		class Base { constructor(v) { this.v = v; } get dbl() { return this.v * 2; } }
		class Derived extends Base { constructor(v) { super(v + 1); this.w = v; } }
		class Empty {}
		var arrow = () => 1;
		var sum = 0, rets = [], errs = [];
		for (var i = 0; i < 3000; i++) {
		  var v = new V(i, 1);
		  sum += v.len();
		  var r = new Ret(i);
		  if (i % 500 === 0) rets.push(JSON.stringify(r));
		  if (new NT().t) sum++;
		  try { sum += new Thrower(i).i; } catch (e) { errs.push(e.message); }
		  var d = new Derived(i);
		  sum += d.dbl + d.w + (d instanceof Base ? 1 : 0);
		  sum += new Empty() instanceof Empty ? 1 : 0;
		  if (i === 1500) V.prototype = { len() { return -1; } };
		  if (i % 1000 === 999) { try { new arrow(); } catch (e) { errs.push(e.constructor.name); } }
		  var C = [V, Base, Derived, Empty, Ret][i % 5];
		  var p = new C(i, 2);
		  sum += (p.x | 0) + (p.v | 0) + (p.a | 0);
		}
		function deep(n) { return n === 0 ? new V(1, 2) : deep(n - 1); }
		for (var i = 0; i < 300; i++) sum += deep(100).len();
		out.push(sum, rets.join(";"), errs.join(";"));
		print(out.join("|"));`
	compareJIT(t, src)
}

// TestJITSlowPaths runs instructions whose machine code handles some cases
// and leaves the others to their slow paths (loose equality, add, instanceof,
// arguments objects and the like) in loops, comparing the JIT with the
// interpreter.
func TestJITSlowPaths(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		var dda = typeof $262 !== "undefined" && $262.IsHTMLDDA ? $262.IsHTMLDDA : undefined;
		var o1 = {}, o2 = {}, s1 = "ab", big = 10n;
		var vals = [0, -0, 1, 1.5, NaN, "1", "1.5", "", "ab", s1 + "", "a" + "b", true, false,
			null, undefined, o1, o2, [1], { valueOf() { return 1; } }, Symbol.iterator, big, 10, "10", dda];
		var eq = [], neq = 0, jeq = 0, adds = [];
		for (var i = 0; i < vals.length; i++) {
		  for (var j = 0; j < vals.length; j++) {
		    var a = vals[i], b = vals[j];
		    eq.push(a == b ? 1 : 0);
		    if (a != b) neq++;
		    if (a == b) jeq += i * j; else jeq -= 1;
		    if (!(a != b)) jeq += 2;
		    if (typeof a !== "symbol" && typeof b !== "symbol" && typeof a !== "bigint" && typeof b !== "bigint")
		      adds.push(String(a + b));
		  }
		}
		out.push(eq.join(""), neq, jeq, adds.join(","));
		function Base() {} function Other() {}
		var inst = 0, isObj = 0, undef = 0, prim = [];
		for (var i = 0; i < 2000; i++) {
		  var x = i % 3 === 0 ? new Base() : i % 3 === 1 ? new Other() : i;
		  if (x instanceof Base) inst++;
		  if (typeof x === "undefined" || typeof vals[i % vals.length] === "undefined") undef++;
		  if (i % 7 === 0) prim.push(` + "`${x}${i}`" + `);
		}
		out.push(inst, undef, prim.length, prim.slice(0, 5).join());
		function args() { var s = 0; for (var k = 0; k < arguments.length; k++) s += arguments[k]; arguments[0] = 9; return s; }
		function apply() { return args.apply(this, arguments); }
		function lex(n) { var fs = []; for (let k = 0; k < n; k++) fs.push(() => k); return fs[n - 1](); }
		var acc = 0;
		for (var i = 0; i < 2000; i++) { acc += args(i, 1, 2) + apply(i, 3) + lex(3); }
		out.push(acc);
		try { for (var i = 0; i < 100; i++) if (i === 50) i instanceof 5; } catch (e) { out.push(e.constructor.name, i); }
		print(out.join("|"));`
	compareJIT(t, src)
}

// TestJITInstanceof runs instanceof in loops with the cases generated code
// handles and those it leaves to Go (bound functions, @@hasInstance,
// proxies, changed prototypes, classes), comparing the JIT with the
// interpreter.
func TestJITInstanceof(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		function A() {} function B() {} B.prototype = Object.create(A.prototype);
		class C {} class D extends C {}
		var Bound = A.bind(null);
		function Custom() {} Object.defineProperty(Custom, Symbol.hasInstance, { value: function (v) { return v === 7; } });
		class Static { static [Symbol.hasInstance](v) { return typeof v === "string"; } }
		var proxied = new Proxy(new B(), {});
		var trap = new Proxy({}, { getPrototypeOf() { return A.prototype; } });
		var bare = Object.create(null);
		var notCallable = Object.create(Function.prototype); notCallable.prototype = A.prototype;
		var vals = [new A(), new B(), new C(), new D(), 7, "s", null, undefined, proxied, trap, bare, {}, [], A, Object.create(new B())];
		var ctors = [A, B, C, D, Bound, Custom, Static, Object, Array, Function, Proxy];
		var out = [];
		for (var round = 0; round < 3; round++) {
		  for (var i = 0; i < vals.length; i++) {
		    for (var j = 0; j < ctors.length; j++) {
		      try { out.push(vals[i] instanceof ctors[j] ? 1 : 0); } catch (e) { out.push("E"); }
		    }
		  }
		  try { out.push(new A() instanceof notCallable); } catch (e) { out.push("E1"); }
		  if (round === 0) A.prototype = {};
		  if (round === 1) { Object.defineProperty(A, Symbol.hasInstance, { value: function () { return true; } }); }
		}
		function F() {} F.prototype = 5;
		for (var k = 0; k < 50; k++) { try { out.push({} instanceof F); } catch (e) { out.push(e.constructor.name); } }
		for (var k = 0; k < 50; k++) { try { out.push({} instanceof 5); } catch (e) { out.push(e.constructor.name); } }
		print(out.join(""));`
	compareJIT(t, src)
}

// TestJITStringEquals compares strings of many lengths and kinds (ropes,
// non-ASCII, append buffers) with ==, ===, != and !== in loops, comparing
// the JIT with the interpreter.
func TestJITStringEquals(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		function rep(c, n) { var s = ""; for (var i = 0; i < n; i++) s += c; return s; }
		var strs = [""];
		[1, 7, 8, 9, 15, 16, 17, 63, 64, 65, 255, 256, 257, 300].forEach(function (n) {
		  var a = rep("a", n);
		  strs.push(a, a.slice(0), (a + "!").slice(0, n), rep("a", n - 1) + "b", "b" + rep("a", n - 1), rep("é", n));
		});
		var big = rep("xy", 40); strs.push(big + big, (big + big).slice(0), rep("x", 70) + rep("y", 90));
		var app = "q"; for (var i = 0; i < 20; i++) { app += "q"; strs.push(app); strs.push(rep("q", i + 2)); }
		var out = [];
		for (var i = 0; i < strs.length; i++) {
		  var n = 0;
		  for (var j = 0; j < strs.length; j++) {
		    var a = strs[i], b = strs[j];
		    if (a == b) n += 1;
		    if (a === b) n += 2;
		    if (a != b) n += 4;
		    if (a !== b) n += 8;
		    n += (a == b ? 16 : 0) + (a !== b ? 32 : 0);
		  }
		  out.push(n);
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITTailCalls runs strict mode tail calls, deep enough that they must
// not grow the stack, and to callees of every kind, comparing the JIT with
// the interpreter.
func TestJITTailCalls(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `(function () { "use strict";
		function count(n, acc) { if (n === 0) return acc; return count(n - 1, acc + 1); }
		function even(n) { return n === 0 ? true : odd(n - 1); }
		function odd(n) { return n === 0 ? false : even(n - 1); }
		function viaNative(x) { return Math.max(x, 3); }
		class K {}
		function viaClass() { return K(); }
		function viaThrow(n) { if (n === 0) throw new Error("bottom"); return viaThrow(n - 1); }
		function sum(a, b, c) { return a + b + (c === undefined ? 100 : c); }
		function fewer(a) { return sum(a, 1); }
		function more(a) { return sum(a, 1, 2, 3); }
		var out = [];
		for (var i = 0; i < 5; i++) out.push(count(1000000, 0));
		out.push(even(300001), odd(300001));
		var s = 0;
		for (var i = 0; i < 2000; i++) s += viaNative(i) + fewer(i) + more(i);
		out.push(s);
		try { viaClass(); } catch (e) { out.push(e.constructor.name); }
		try { viaThrow(500000); } catch (e) { out.push(e.message); }
		print(out.join(","));
		})()`
	compareJIT(t, src)
}

// TestJITTypeof runs typeof and the typeof and is_number checks on values
// of every kind in a loop, comparing the JIT with the interpreter.
func TestJITTypeof(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		class K {}
		var vals = [undefined, null, true, false, 0, -0, NaN, 1.5, "", "s", Symbol("x"), 10n, {}, [], function () {},
			() => 1, K, new K(), Math.max, Math.max.bind(null), new Proxy({}, {}), new Proxy(function () {}, {}),
			Object.create(null), /re/, new Date(0), new String("w")];
		var out = [];
		for (var r = 0; r < 3; r++) {
		  for (var i = 0; i < vals.length; i++) {
		    var v = vals[i], t = typeof v;
		    out.push(t, typeof v === "object" ? 1 : 0, typeof v === "function" ? 1 : 0, typeof v === "number" ? 1 : 0,
		      (typeof v).length, t === typeof vals[(i + 1) % vals.length] ? 1 : 0);
		  }
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITCharCodeAt runs s.charCodeAt(i), which generated code does itself
// for flat strings, with every kind of string, index and receiver,
// comparing the JIT with the interpreter.
func TestJITCharCodeAt(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var big = ""; for (var i = 0; i < 100; i++) big += "ab";
		var strs = ["", "a", "hello", "héllo", "中文", "x".repeat(70) + "y".repeat(70), big, "q" + big + "😀"];
		var idx = [0, 1, 2, 4, 5, 139, 140, -1, -0, 0.5, NaN, Infinity, 1e10, "1", undefined, null, true];
		var out = [];
		for (var r = 0; r < 3; r++) {
		  for (var i = 0; i < strs.length; i++) {
		    var s = strs[i], sum = 0;
		    for (var j = 0; j < idx.length; j++) {
		      var c = s.charCodeAt(idx[j]);
		      sum += c !== c ? 1000003 : c;
		      s.charCodeAt(idx[j]);
		    }
		    for (var k = 0; k < s.length; k++) sum = (sum * 31 + s.charCodeAt(k)) | 0;
		    out.push(sum, s.charCodeAt(), s.charCodeAt(1, 2));
		  }
		  var cc = String.prototype.charCodeAt;
		  out.push(cc.call(12345, 2), cc.call(new String("xyz"), 1), cc.call({ toString() { return "obj"; } }, 0));
		  try { cc.call(null, 0); } catch (e) { out.push(e.constructor.name); }
		  try { new ("abc".charCodeAt)(0); } catch (e) { out.push(e.constructor.name); }
		  if (r === 1) String.prototype.charCodeAt = function (i) { return -i; };
		  if (r === 2) String.prototype.charCodeAt = cc;
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITArrayPush runs a.push(x), which generated code does itself for
// plain arrays, on arrays of every kind and with indexed properties on the
// prototypes, comparing the JIT with the interpreter.
func TestJITArrayPush(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		class Sub extends Array {}
		var wm = new WeakMap();
		function make(kind) {
		  var a = [1, 2, 3];
		  switch (kind) {
		  case 1: Object.freeze(a); break;
		  case 2: Object.seal(a); break;
		  case 3: Object.defineProperty(a, "length", { writable: false }); break;
		  case 4: a = new Sub(); a.push(7); break;
		  case 5: a = new Proxy([1], {}); break;
		  case 6: a[100] = 5; break;
		  case 7: wm.set(a, 1); break;
		  case 8: a = []; break;
		  case 9: a = { length: 2, 0: "x", 1: "y" }; break;
		  }
		  return a;
		}
		var out = [];
		for (var r = 0; r < 4; r++) {
		  for (var kind = 0; kind < 10; kind++) {
		    var a = make(kind), n = 0, err = "";
		    try {
		      for (var i = 0; i < 300; i++) {
		        if (kind === 9) n += Array.prototype.push.call(a, i);
		        else { n += a.push(i); a.push(i * 2); }
		      }
		      n += a.push() + a.push(1, 2);
		    } catch (e) { err = e.constructor.name; }
		    out.push(kind, n, a.length, String(a[3]), String(a[299]), err);
		  }
		  if (r === 1) {
		    var log = [];
		    Object.defineProperty(Array.prototype, 5, { set(v) { log.push("set" + v); }, configurable: true });
		    var b = [0, 1, 2, 3, 4];
		    for (var i = 0; i < 3; i++) b.push(i);
		    out.push(b.length, String(b[5]), log.join("/"));
		    delete Array.prototype[5];
		  }
		  if (r === 2) { Object.prototype[2] = "proto"; var c = [0, 1]; c.push(9); out.push(c.length, c[2], c.hasOwnProperty(2)); delete Object.prototype[2]; }
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITThunks calls every builtin with a thunk (jit_thunks.go) with
// values of every kind and with other numbers of arguments, also after
// replacing some, comparing the JIT with the interpreter.
func TestJITThunks(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var nums = [0, -0, 0.5, -0.5, 0.49999999999999994, -0.49999999999999994, 1.5, -1.5, 2.5, -2.5, 3.7, -3.7,
			1e15 + 0.5, -(1e15 + 0.5), 4503599627370495.5, 4503599627370496, 9007199254740991, 9007199254740992,
			-9007199254740991, 2147483647, -2147483648, 2147483648, 4294967295, 4294967296, 1e19, -1e19, 1e300,
			NaN, Infinity, -Infinity, 65, 255, 256, 65535, 65536, 65601, 0x10FFFF, 0x110000, -1];
		var others = ["3", "", "abc", true, null, undefined, {}, [], 10n, Symbol.iterator, { valueOf() { return 7; } }];
		var all = nums.concat(others);
		function show(v) { return typeof v === "number" ? (Object.is(v, -0) ? "-0" : String(v)) : typeof v === "symbol" ? "sym" : typeof v === "bigint" ? v + "n" : String(v); }
		var out = [];
		function tryit(f) { try { out.push(show(f())); } catch (e) { out.push(e.constructor.name); } }
		var M = Math;
		for (var r = 0; r < 3; r++) {
		  for (var i = 0; i < all.length; i++) {
		    var x = all[i], y = all[(i * 7 + 3) % all.length];
		    tryit(() => M.sqrt(x)); tryit(() => M.abs(x)); tryit(() => M.floor(x)); tryit(() => M.ceil(x));
		    tryit(() => M.trunc(x)); tryit(() => M.round(x)); tryit(() => M.max(x, y)); tryit(() => M.min(x, y));
		    tryit(() => M.max(x)); tryit(() => M.min(x, y, 1)); tryit(() => M.imul(x, y)); tryit(() => M.imul(x));
		    tryit(() => isNaN(x)); tryit(() => isFinite(x)); tryit(() => Number.isNaN(x)); tryit(() => Number.isFinite(x));
		    tryit(() => Number.isSafeInteger(x)); tryit(() => Object.is(x, y)); tryit(() => Object.is(x, x));
		    tryit(() => Number(x)); tryit(() => String(x)); tryit(() => Number());
		    tryit(() => String.fromCharCode(x)); tryit(() => String.fromCodePoint(x)); tryit(() => String.fromCharCode(x, 66));
		  }
		  var strs = ["", "hello", "héllo wörld", "中文", "a😀b", "\ud83d", "\ude00x", "x".repeat(40) + "y".repeat(40)];
		  var idx = [0, 1, 2, 3, -1, -2, -9, 9, 100, 0.5, NaN, "1", undefined];
		  for (var i = 0; i < strs.length; i++) {
		    for (var j = 0; j < idx.length; j++) {
		      var s = strs[i], k = idx[j];
		      tryit(() => s.charCodeAt(k)); tryit(() => s.charAt(k)); tryit(() => s.at(k)); tryit(() => s.codePointAt(k));
		    }
		  }
		  var a = [1, 2, 3], b = [], c = Object.freeze([1, 2]), d = [1, , 3];
		  for (var i = 0; i < 40; i++) { a.push(i); b.push(String(i)); if (i % 3 === 0) out.push(show(a.pop())); }
		  tryit(() => c.push(1)); tryit(() => c.pop()); tryit(() => d.pop()); tryit(() => d.pop()); tryit(() => [].pop());
		  tryit(() => Array.prototype.pop.call({ length: 2, 1: "z" }));
		  out.push(a.join("/"), b.length);
		  Object.prototype[1] = "proto";
		  var h = [0, , 2]; tryit(() => h.pop()); tryit(() => h.pop()); tryit(() => h.pop());
		  delete Object.prototype[1];
		  if (r === 1) { M = { sqrt: x => -1, abs: Math.abs, floor: Math.floor, ceil: Math.ceil, trunc: Math.trunc, round: Math.round, max: Math.max, min: Math.min, imul: (a, b) => 42 }; }
		  if (r === 2) M = Math;
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITPrimitiveGet reads properties of strings and numbers through
// get_by_id (their prototypes' methods and getters, Object.prototype's,
// replaced and deleted ones), comparing the JIT with the interpreter.
func TestJITPrimitiveGet(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		Object.defineProperty(String.prototype, "self", { get() { return typeof this; }, configurable: true });
		Object.defineProperty(String.prototype, "strictSelf", { get: function () { "use strict"; return typeof this; }, configurable: true });
		Object.defineProperty(Number.prototype, "twice", { get() { return this * 2; }, configurable: true });
		var vals = ["abc", "", "été", 42, 1.5, -0, NaN, true, new String("obj"), new Number(7), { charAt: () => "own" }];
		for (var r = 0; r < 4; r++) {
		  for (var i = 0; i < vals.length; i++) {
		    var v = vals[i];
		    try {
		      out.push(typeof v.charAt === "function" ? v.charAt(0) : "-", typeof v.toFixed, v.self, v.strictSelf, v.twice,
		        typeof v.hasOwnProperty, v.constructor && v.constructor.name, v.missing);
		      var { length } = typeof v === "string" ? v : "xy";
		      out.push(length);
		    } catch (e) { out.push(e.constructor.name); }
		  }
		  if (r === 1) String.prototype.charAt = function () { return "replaced"; };
		  if (r === 2) { delete String.prototype.charAt; String.prototype.extra = 1; }
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITSwitch runs switch statements the bytecompiler makes switch_imm,
// switch_char and switch_string of (dense and sparse cases, odd values,
// repeated cases), comparing the JIT with the interpreter.
func TestJITSwitch(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		function dense(x) { switch (x) { case 0: return "a"; case 1: return "b"; case 2: return "c"; case 3: return "d";
		  case 4: return "e"; case 5: return "f"; case 6: return "g"; case 7: return "h"; case 8: return "i"; default: return "z"; } }
		function sparse(x) { switch (x) { case -1000: return 1; case -1: return 2; case 7: return 3; case 100: return 4;
		  case 1000: return 5; case 100000: return 6; case 2147483647: return 7; case -2147483648: return 8; case 7: return 9; } return 0; }
		function fall(x) { var s = ""; switch (x) { case 1: s += "1"; case 2: s += "2"; break; case 3: s += "3"; default: s += "d"; case 4: s += "4"; } return s; }
		function chr(x) { switch (x) { case "a": return 1; case "b": return 2; case "é": return 3; case "中": return 4; case "z": return 5; default: return 0; } }
		function str(x) { switch (x) { case "alpha": return 1; case "beta": return 2; case "": return 3; default: return 0; } }
		var vals = [0, 1, 2, 3, 4, 5, 8, 9, -1, -0, 7, 100, 1000, 100000, -1000, 2147483647, -2147483648, 2147483648, 1.5, NaN, Infinity,
			"a", "b", "z", "é", "中", "ab", "", "alpha", "beta", "1", true, null, undefined, {}, [1]];
		var out = [];
		for (var r = 0; r < 3; r++)
		  for (var i = 0; i < vals.length; i++) {
		    var v = vals[i];
		    out.push(dense(v), sparse(v), fall(v), chr(v), str(v));
		  }
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITMegamorphic reads properties at sites that see more shapes than a
// cache has cases (the megamorphic cache), with properties deleted,
// replaced by getters, on prototypes only and on objects in dictionary
// mode, comparing the JIT with the interpreter.
func TestJITMegamorphic(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var objs = [];
		for (var i = 0; i < 12; i++) {
		  var o = {};
		  for (var j = 0; j < i; j++) o["p" + j] = j;
		  o.x = i * 10; o.y = "y" + i;
		  objs.push(o);
		}
		var proto = { x: "from proto", y: "proto y" };
		objs.push(Object.create(proto), Object.create(proto, { x: { value: "own", enumerable: true } }));
		var dict = { x: 1, y: 2 };
		for (var j = 0; j < 100; j++) dict["d" + j] = j;
		for (var j = 0; j < 100; j += 2) delete dict["d" + j];
		objs.push(dict);
		function rd(o) { return o.x + "/" + o.y; }
		var out = [];
		for (var r = 0; r < 6; r++) {
		  var s = [];
		  for (var i = 0; i < objs.length; i++) s.push(rd(objs[i]));
		  out.push(s.join(","));
		  if (r === 1) { delete objs[3].x; objs[4].x = "changed"; }
		  if (r === 2) { Object.defineProperty(objs[5], "x", { get() { return "getter"; }, configurable: true }); }
		  if (r === 3) { dict.x = "dict changed"; delete dict.y; proto.x = "proto changed"; }
		  if (r === 4) { objs[6].x = undefined; Object.freeze(objs[7]); delete objs[8].y; objs[8].y = "re-added"; }
		}
		print(out.join("|"));`
	compareJIT(t, src)
}

// TestJITGetLength reads .length of typed arrays (JSC's intrinsic getter
// case) and of other objects through get_length's cache, also after the
// getter is replaced or shadowed and when buffers are detached or resized.
func TestJITGetLength(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		class Sub extends Uint16Array {}
		var rab = new ArrayBuffer(16, { maxByteLength: 64 });
		var vals = [new Uint8Array(5), new Int8Array(3), new Uint8ClampedArray(2), new Int16Array(4), new Uint16Array(1),
		  new Int32Array(6), new Uint32Array(7), new Float32Array(8), new Float64Array(9), new BigInt64Array(2),
		  new BigUint64Array(3), new Sub(4), new Uint8Array(new ArrayBuffer(16), 4, 8), new Float64Array(new ArrayBuffer(64), 8),
		  new Int32Array(rab), new Int32Array(rab, 4, 2), { length: 11 }, { length: "x" }, {}, [1, 2], "abc",
		  (function () { return arguments; })(1, 2, 3), function (a, b) {}];
		if (typeof Float16Array === "function") vals.push(new Float16Array(5));
		function len(v) { return v.length; }
		var own = new Uint8Array(4);
		Object.defineProperty(own, "length", { value: 99 });
		vals.push(own);
		for (var r = 0; r < 6; r++) {
		  for (var i = 0; i < vals.length; i++) {
		    try { out.push(len(vals[i])); } catch (e) { out.push(e.constructor.name); }
		  }
		  if (r === 1) rab.resize(8);
		  if (r === 2) vals[0].buffer.transfer();
		  if (r === 3) Object.defineProperty(Sub.prototype, "length", { get() { return -1; } });
		  if (r === 4) Object.defineProperty(Object.getPrototypeOf(Int8Array.prototype), "length", { get() { return -2; } });
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITAbsent reads absent properties through get_by_id's miss cases
// (undefined while the prototype chain stays as it was), also after the
// property appears on the object, a prototype or Object.prototype, and
// after prototypes change.
func TestJITAbsent(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		class A {} class B extends A {} class C extends B {}
		var proto = { p: 1 };
		var objs = [{}, { a: 1 }, new C(), Object.create(null), Object.create(proto), [], function () {}, "str", 5, new Uint8Array(2)];
		function get(o) { return o.missing; }
		function len(o) { return o.length; }
		for (var r = 0; r < 9; r++) {
		  for (var i = 0; i < objs.length; i++) {
		    out.push(String(get(objs[i])), String(len(objs[i])));
		  }
		  if (r === 1) objs[1].missing = "own";
		  if (r === 2) A.prototype.missing = "A";
		  if (r === 3) proto.missing = "proto";
		  if (r === 4) Object.prototype.missing = "Object";
		  if (r === 5) { delete Object.prototype.missing; delete A.prototype.missing; delete proto.missing; }
		  if (r === 6) Object.setPrototypeOf(objs[3], { missing: "new proto", length: 3 });
		  if (r === 7) Object.setPrototypeOf(proto, { missing: "deeper" });
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITAppend appends with a[a.length] = x (put_by_val past the end) to
// plain arrays and to arrays where it must not be done in place: not
// extensible, length read-only, sparse, a subclass, or with an indexed
// setter on Array.prototype.
func TestJITAppend(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		class Sub extends Array {}
		function fill(a, n, k) {
		  for (var i = 0; i < n; i++) {
		    try { a[a.length + k] = i * 2; } catch (e) { out.push(e.constructor.name); }
		  }
		  return a;
		}
		function show(a) { out.push(a.length + ":" + Array.prototype.join.call(a, "|")); }
		for (var r = 0; r < 3; r++) {
		  show(fill([], 20, 0));
		  show(fill([1, 2, 3], 5, 0));
		  show(fill(new Array(10), 3, 0));
		  show(fill([1, , 3], 3, 0));
		  show(fill([], 3, 1));
		  show(fill(Object.preventExtensions([1, 2]), 2, 0));
		  show(fill(Object.freeze([1, 2]), 2, 0));
		  show(fill(Object.defineProperty([1, 2], "length", { writable: false }), 2, 0));
		  var sp = [1, 2]; sp[100] = 5; sp.length = 2; show(fill(sp, 2, 0));
		  show(fill(new Sub(), 3, 0));
		  show(fill({ length: 0 }, 3, 0));
		  (function () { "use strict"; try { var f = Object.freeze([]); f[0] = 1; } catch (e) { out.push(e.constructor.name); } })();
		  if (r === 1) Object.defineProperty(Array.prototype, 3, { set(v) { out.push("setter " + v); }, configurable: true });
		}
		delete Array.prototype[3];
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITHoles fills arrays made by new Array(n), whose elements start as
// holes, and reads holes, indexes past the end and characters of strings
// through get_by_val, also when Array.prototype or Object.prototype have
// indexed properties and for keys that are not array indexes.
func TestJITHoles(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		class Mat extends Array {}
		function make(n) { var a = new Array(n); for (var i = 0; i < n; i += 2) a[i] = i; return a; }
		function read(a, i) { return a[i]; }
		function show(a) { out.push(a.length + ":" + Array.prototype.join.call(a, "|") + ":" + Object.keys(a).length); }
		var neg = [1, 2]; neg[-1] = "minus"; neg[4294967295] = "max"; neg[2147483648] = "big";
		var rope = "ab"; for (var k = 0; k < 6; k++) rope += rope;
		var strs = ["hello", "été", "€éx", rope, ""];
		for (var r = 0; r < 4; r++) {
		  show(make(7));
		  show(Array(5));
		  var f = Object.freeze(new Array(3)); try { (function () { "use strict"; f[1] = 1; })(); } catch (e) { out.push(e.constructor.name); }
		  var p = Object.preventExtensions(new Array(3)); p[0] = 1; show(p);
		  var a = [1, , 3];
		  for (var i = -2; i < 6; i++) out.push(String(read(a, i)));
		  out.push(read(neg, -1), read(neg, 4294967295), read(neg, 2147483648), String(read(neg, 3)));
		  for (var s = 0; s < strs.length; s++)
		    for (var i = -1; i <= strs[s].length; i++) out.push(String(read(strs[s], i)));
		  out.push(String(read({ 0: "x", 2: "y" }, 1)), String(read(new Uint8Array(2), 5)));
		  var m = new Mat(4); m[1] = "m"; out.push(String(read(m, 0)), String(read(m, 2)), String(read(m, 9)));
		  var t = [, , 3]; Object.setPrototypeOf(t, new Uint8Array(4)); out.push(String(read(t, 0)), String(read(t, 7)));
		  t[1] = "t"; out.push(String(read(t, 1)));
		  var px = [, 2]; Object.setPrototypeOf(px, new Proxy([], { get(o, k) { return "proxy " + String(k); } }));
		  out.push(String(read(px, 0)), String(read(px, 5)));
		  if (r === 0) Mat.prototype[2] = "fromMat";
		  if (r === 1) { delete Mat.prototype[2]; Array.prototype[1] = "fromArrayProto"; }
		  if (r === 2) { delete Array.prototype[1]; Object.prototype[3] = "fromObjectProto"; }
		}
		delete Object.prototype[3];
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITGetters reads accessor properties whose getters get_by_id calls
// in generated code: class and prototype getters, getters of primitives,
// getters that throw, recurse, change the object or run code that is not
// compiled, accessors without a getter, native getters, and getters that
// are replaced.
func TestJITGetters(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		class P { constructor(x) { this.x = x; } get double() { return this.x * 2; } get boom() { throw new Error("boom " + this.x); } }
		class Q extends P { get deep() { return this.x > 0 ? new Q(this.x - 1).deep + 1 : 0; } get change() { this.y = 1; return this.x; } }
		Object.defineProperty(String.prototype, "first", { get() { return this[0]; }, configurable: true });
		Object.defineProperty(Number.prototype, "neg", { get() { "use strict"; return -this; }, configurable: true });
		var setterOnly = Object.defineProperty({}, "w", { set(v) {}, configurable: true });
		var slowGetter = { get v() { try { return eval("1 + 1"); } catch (e) { return e; } } };
		var argsGetter = { get a() { return arguments.length + ":" + typeof this; } };
		function dbl(o) { return o.double; }
		function boom(o) { try { return o.boom; } catch (e) { return e.message; } }
		function deep(o) { return o.deep; }
		function first(s) { return s.first; }
		function neg(n) { return n.neg; }
		function w(o) { return o.w; }
		function v(o) { return o.v; }
		function a(o) { return o.a; }
		function size(m) { return m.size; }
		function change(o) { return o.change; }
		var m = new Map([[1, 2]]);
		for (var r = 0; r < 5; r++) {
		  for (var i = 0; i < 4; i++) {
		    var q = new Q(i);
		    out.push(dbl(q), dbl(new P(i)), dbl({ double: "data" }), boom(q), deep(q), change(q), change(new Q(i + 10)));
		    out.push(first("abc"), first("x" + i), neg(i), String(w(setterOnly)), v(slowGetter), a(argsGetter), size(m));
		  }
		  if (r === 2) Object.defineProperty(P.prototype, "double", { get() { return "replaced " + this.x; }, configurable: true });
		  if (r === 3) delete String.prototype.first;
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITDeepPut adds properties to instances of deep class hierarchies
// (put_by_id caches walking more than three prototypes), also after a
// setter or a read-only property appears somewhere up the chain and after
// prototypes change.
func TestJITDeepPut(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		class A {} class B extends A {} class C extends B {} class D extends C {} class E extends D {}
		var Deep = E;
		for (var d = 0; d < 14; d++) Deep = class extends Deep {};
		function add(o, i) { o.v = i; o.w = i * 2; return o; }
		function show(o) { out.push(Object.keys(o).join("") + ":" + o.v + ":" + o.w); }
		for (var r = 0; r < 7; r++) {
		  for (var i = 0; i < 3; i++) {
		    show(add(new E(), i));
		    show(add(new Deep(), i));
		    show(add(Object.create(Object.create(Object.create(Object.create({})))), i));
		  }
		  if (r === 1) Object.defineProperty(C.prototype, "v", { set(x) { out.push("setter " + x); }, configurable: true });
		  if (r === 2) { delete C.prototype.v; Object.defineProperty(A.prototype, "w", { value: "ro", writable: false, configurable: true }); }
		  if (r === 3) { delete A.prototype.w; Object.prototype.v = "shadowed"; }
		  if (r === 4) { delete Object.prototype.v; Object.setPrototypeOf(D.prototype, { set w(x) { out.push("new proto " + x); } }); }
		  if (r === 5) Object.setPrototypeOf(D.prototype, C.prototype);
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITGrowStorage adds more properties to objects than their storage
// holds, so that generated code grows it from the pools (capacities 2 to
// 32 and past them), while garbage makes the collector run.
func TestJITGrowStorage(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		function P2() { this.a = 1; this.b = 2; }
		function P4() { this.a = 1; this.b = 2; this.c = 3; this.d = 4; }
		function fill(o, n) {
		  o.p0 = 0; o.p1 = 1; o.p2 = 2; o.p3 = 3; o.p4 = 4; o.p5 = 5; o.p6 = 6; o.p7 = 7;
		  o.p8 = 8; o.p9 = 9; o.p10 = 10; o.p11 = 11; o.p12 = 12; o.p13 = 13; o.p14 = 14; o.p15 = 15;
		  o.p16 = 16; o.p17 = 17; o.p18 = 18; o.p19 = 19; o.p20 = 20; o.p21 = 21; o.p22 = 22; o.p23 = 23;
		  o.p24 = 24; o.p25 = 25; o.p26 = 26; o.p27 = 27; o.p28 = 28; o.p29 = 29; o.p30 = 30; o.p31 = 31;
		  o.p32 = { n: n }; o.p33 = "s" + n; o.p34 = [n]; o.p35 = 35;
		  return o;
		}
		var keep = [];
		for (var r = 0; r < 60; r++) {
		  var objs = [fill({}, r), fill(new P2(), r), fill(new P4(), r), fill(Object.create(null), r)];
		  for (var g = 0; g < 200; g++) keep[g % 50] = { junk: [g, "x" + g] };
		  for (var i = 0; i < objs.length; i++) {
		    var o = objs[i], s = 0;
		    for (var k in o) s += typeof o[k] === "number" ? o[k] : 0;
		    out.push(Object.keys(o).length + ":" + s + ":" + o.p32.n + o.p33 + o.p34[0]);
		  }
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITMegamorphicPut stores to an own property of objects of many
// shapes at one site (put_by_id's megamorphic cache), also after the
// property becomes read-only or an accessor, or its object is frozen.
func TestJITMegamorphicPut(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		var objs = [];
		for (var i = 0; i < 12; i++) {
		  var o = {};
		  o["k" + i] = i;
		  o.v = 0;
		  objs.push(o);
		}
		function set(o, x) { o.v = x; }
		function setStrict(o, x) { "use strict"; try { o.v = x; } catch (e) { out.push(e.constructor.name); } }
		for (var r = 0; r < 6; r++) {
		  for (var i = 0; i < objs.length; i++) { set(objs[i], r * 100 + i); setStrict(objs[i], r * 100 + i + 1); }
		  out.push(objs.map(function (o) { return o.v; }).join("|"));
		  if (r === 1) Object.defineProperty(objs[2], "v", { writable: false });
		  if (r === 2) Object.freeze(objs[5]);
		  if (r === 3) Object.defineProperty(objs[7], "v", { get() { return "acc"; }, set(x) { out.push("set " + x); } });
		  if (r === 4) { delete objs[9].v; Object.defineProperty(Object.prototype, "v", { set(x) { out.push("proto " + x); }, configurable: true }); }
		}
		delete Object.prototype.v;
		print(out.join(","));`
	compareJIT(t, src)
}

// TestLazyPrototype checks functions whose prototype object is made when
// first looked at: the order of keys, descriptors, defining, freezing and
// assigning before it is made, construction and instanceof, also with
// nearly everything compiled.
func TestLazyPrototype(t *testing.T) {
	src := `
		var out = [];
		function make() { return function F(a, b) { this.a = a; }; }
		var f1 = make(); f1.x = 1;
		out.push(Object.getOwnPropertyNames(f1).join("|"));
		var d = Object.getOwnPropertyDescriptor(make(), "prototype");
		out.push(typeof d.value, d.writable, d.enumerable, d.configurable);
		var f2 = make(); out.push(f2.prototype.constructor === f2, Object.getPrototypeOf(f2.prototype) === Object.prototype);
		var f3 = make(); var o3 = new f3(1); out.push(o3 instanceof f3, Object.getPrototypeOf(o3) === f3.prototype, o3.a);
		var f4 = make(); out.push(({}) instanceof f4);
		var f5 = make(); f5.prototype = { tag: "mine" }; out.push(new f5().tag, f5.prototype.tag);
		var f6 = make(); Object.freeze(f6); var p6 = Object.getOwnPropertyDescriptor(f6, "prototype");
		out.push(Object.isFrozen(f6), p6.writable, typeof p6.value, p6.value.constructor === f6);
		var f7 = make(); Object.defineProperty(f7, "prototype", { value: 7 }); out.push(f7.prototype);
		var f8 = make(); out.push(delete f8.prototype, typeof f8.prototype, f8.hasOwnProperty("prototype"), "prototype" in f8);
		var f9 = make(); out.push(Reflect.ownKeys(f9).join("|"), JSON.stringify(Object.keys(f9)));
		var f10 = make(); f10.prototype.m = function () { return "m"; }; out.push(new f10().m());
		function G() {} var gs = [];
		for (var i = 0; i < 50; i++) { var g = (function () { return function () {}; })(); gs.push(g); if (i % 7 === 0) new g(); }
		out.push(gs.filter(function (g) { return g.prototype.constructor === g; }).length);
		out.push(typeof (() => 1).prototype, typeof (function* () {}).prototype, typeof (async function () {}).prototype, typeof (class {}).prototype);
		print(out.join(","));`
	want := "length|name|prototype|x,object,true,false,false,true,true,true,true,1,false,mine,mine,true,false,object,true,7,false,object,true,true,length|name|prototype,[],m,50,undefined,object,undefined,object"
	if got := strings.TrimSpace(runForTest(t, src, false)); got != want {
		t.Errorf("interpreter: got %s\nwant %s", got, want)
	}
	if !jitEnabled {
		return
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	if got := strings.TrimSpace(runForTest(t, src, true)); got != want {
		t.Errorf("JIT: got %s\nwant %s", got, want)
	}
}

// TestJITByValStringKeys reads and writes o[k] with string keys through
// the megamorphic tables (and for-in's o[k]): many shapes and dictionary
// objects, index-like strings, symbols, array lengths, getters, inherited
// and absent properties, frozen objects and keys made by concatenation.
func TestJITByValStringKeys(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		var objs = [];
		for (var i = 0; i < 10; i++) { var o = { a: i, b: "b" + i }; o["k" + i] = i; objs.push(o); }
		var dict = { a: 1, b: 2, c: 3 }; delete dict.c; objs.push(dict);
		var big = {}; for (var i = 0; i < 80; i++) big["p" + i] = i; big.a = "big"; objs.push(big);
		var withGetter = { get a() { return "getter"; }, b: 1 }; objs.push(withGetter);
		var inherits = Object.create({ a: "inherited" }); objs.push(inherits);
		var frozen = Object.freeze({ a: "frozen", b: 0 }); objs.push(frozen);
		var arr = [1, 2, 3]; arr.a = "arr"; objs.push(arr);
		var sym = Symbol("s");
		function get(o, k) { return o[k]; }
		function set(o, k, v) { o[k] = v; }
		function setStrict(o, k, v) { "use strict"; try { o[k] = v; } catch (e) { out.push(e.constructor.name); } }
		for (var r = 0; r < 4; r++) {
		  for (var i = 0; i < objs.length; i++) {
		    var o = objs[i];
		    out.push(String(get(o, "a")), String(get(o, "b")), String(get(o, "missing")), String(get(o, "a" + "")), String(get(o, "0")), String(get(o, "length")));
		    set(o, "a", r); setStrict(o, "b", r); set(o, "n" + r, r);
		    out.push(String(o.a), String(o.b));
		  }
		  var so = {}; so[sym] = 1; out.push(get(so, sym), get(["x", "y"], "1"));
		  var keys = [];
		  for (var k in objs[3]) { keys.push(k + "=" + objs[3][k]); if (k === "a") delete objs[3].b; }
		  out.push(keys.join(";"));
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestForOfFastArray iterates arrays with for-of, destructuring and yield*
// in fast mode (no iterator objects) and the cases that leave it: holes
// read through the prototype, arrays changed during the loop, break and
// throw, replaced Symbol.iterator or %ArrayIteratorPrototype%.next.
func TestForOfFastArray(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		class Sub extends Array {}
		function sum(a) { var s = []; for (var x of a) s.push(String(x)); return s.join("|"); }
		function grow(a) { var n = 0; for (var x of a) { if (n++ < 3) a.push(x * 10); } return n; }
		function shrink(a) { var s = []; for (var x of a) { s.push(x); a.length = 2; } return s.join("|"); }
		function brk(a) { for (var x of a) { if (x === 2) break; } return x; }
		function thr(a) { try { for (var x of a) { if (x === 2) throw new Error("e" + x); } } catch (e) { return e.message; } }
		function destr(a) { var [p, q, ...r] = a; return p + "/" + q + "/" + r.join(","); }
		function* gen(a) { yield* a; }
		for (var r = 0; r < 4; r++) {
		  out.push(sum([1, 2, 3]), sum([1, , 3]), sum([]), sum(Sub.of(4, 5)), sum("ab"), sum(new Uint8Array([7, 8])));
		  out.push(grow([1, 2]), shrink([1, 2, 3, 4]), brk([1, 2, 3]), thr([1, 2, 3]), destr([1, 2, 3, 4]), [...gen([5, 6])].join(","));
		  out.push((function () { var s = 0; for (var x of arguments) s += x; return s; })(1, 2, 3));
		  if (r === 1) Object.defineProperty(Array.prototype, 1, { get() { return "proto1"; }, set(v) {}, configurable: true });
		  if (r === 2) {
		    delete Array.prototype[1];
		    var oldNext = Object.getPrototypeOf([][Symbol.iterator]()).next;
		    Object.getPrototypeOf([][Symbol.iterator]()).next = function () { var res = oldNext.call(this); if (!res.done) res.value = "n" + res.value; return res; };
		  }
		  if (r === 3) Array.prototype[Symbol.iterator] = function* () { yield "own"; };
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestForInCache enumerates objects whose keys come from their shape's
// cache: the same shape many times, keys deleted or added during the loop,
// prototypes that gain enumerable or indexed properties, other prototypes
// with the same shape, and objects that do not qualify.
func TestForInCache(t *testing.T) {
	src := `
		var out = [];
		function keys(o) { var s = []; for (var k in o) s.push(k + "=" + o[k]); return s.join(";"); }
		function del(o) { var s = []; for (var k in o) { s.push(k); delete o.b; o.z = 1; } return s.join(";"); }
		var P = { }; Object.defineProperty(P, "hidden", { value: 1, enumerable: false });
		for (var r = 0; r < 5; r++) {
		  for (var i = 0; i < 3; i++) out.push(keys({ a: i, b: "x", c: true }), keys(Object.create(P, { own: { value: i, enumerable: true } })));
		  out.push(del({ a: 1, b: 2, c: 3 }), keys([1, 2]), keys("str"), keys({ 1: "one", a: "a" }));
		  var o1 = { a: 1 }, o2 = Object.create(P); o2.a = 2; out.push(keys(o1), keys(o2));
		  if (r === 1) P.added = "fromP";
		  if (r === 2) { delete P.added; Object.prototype[0] = "idx"; }
		  if (r === 3) { delete Object.prototype[0]; Object.prototype.inherited = "obj"; }
		}
		delete Object.prototype.inherited;
		print(out.join(","));`
	if got, want := runForTest(t, src, false), runForTest(t, src, true); got != want || got == "" {
		t.Fatalf("interpreter %q, JIT %q", got, want)
	}
	got := strings.TrimSpace(runForTest(t, src, false))
	if !strings.Contains(got, "a=0;b=x;c=true") || !strings.Contains(got, "added=fromP") || !strings.Contains(got, "0=idx") || !strings.Contains(got, "inherited=obj") || strings.Contains(got, "hidden") {
		t.Errorf("for-in keys: %s", got)
	}
}

// TestJITIsArray calls Array.isArray through its thunk on arrays,
// subclasses, proxies of arrays and of objects, objects and primitives,
// and with no argument.
func TestJITIsArray(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		class Sub extends Array {}
		var vals = [[], [1], new Sub(), new Proxy([], {}), new Proxy({}, {}), {}, "a", 1, null, undefined, new Uint8Array(1), (function () { return arguments; })()];
		function isA(v) { return Array.isArray(v); }
		for (var r = 0; r < 3; r++) {
		  for (var i = 0; i < vals.length; i++) out.push(isA(vals[i]));
		  out.push(Array.isArray(), Array.isArray([], 1));
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITClosureCalls calls closures made anew in loops and calls (call
// cases keyed on the code block, JSC's closure calls): each captures its
// own values, so a wrong scope shows; also constructors, more closures than
// cases, and functions sharing a site with them.
func TestJITClosureCalls(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		function adder(k) { return function (v) { return v + k; }; }
		function maker(k) { return function C(v) { this.v = v * k; }; }
		function apply(f, x) { return f(x); }
		function build(C, x) { return new C(x).v; }
		function other(v) { return "other" + v; }
		function getter(k) { return { get g() { return k * 2; } }; }
		function maker2(k) { return function (v) { return v * 10 + k; }; }
		function sub(k) { return function (v) { return v - k; }; }
		function mul(k) { return function (v) { return v * k; }; }
		function neg(k) { return function (v) { return -v - k; }; }
		for (var r = 0; r < 3; r++) {
		  var s = 0;
		  for (var i = 0; i < 40; i++) s += apply(adder(i % 9), i);
		  out.push(s);
		  var t = 0;
		  for (var i = 0; i < 30; i++) t += build(maker(i % 5), i);
		  out.push(t);
		  out.push([1, 2, 3].map(function (x) { return x * r; }).join("|"));
		  var fs = [adder(1), other, adder(2), x => x * 3, adder(3), other];
		  for (var i = 0; i < 12; i++) out.push(String(apply(fs[i % fs.length], i)));
		  // Getters: a new closure per object.
		  var g = 0;
		  for (var i = 0; i < 30; i++) g += getter(i).g;
		  out.push(g);
		  // More functions than cases, each made anew.
		  var h = 0;
		  for (var i = 0; i < 60; i++) h += apply([adder(i), maker2(i), sub(i), mul(i), neg(i)][i % 5], i);
		  out.push(h);
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITVirtualCalls calls many different functions from one site (more
// than its cases: JSC's virtual calls through the callee's own code):
// more arguments than parameters, class constructors without new,
// functions with handlers, and recursion through the site.
func TestJITVirtualCalls(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		var fs = [];
		for (var i = 0; i < 20; i++) fs.push(new Function("a", "b", "return a * " + i + " + (b === undefined ? 0 : b);"));
		fs.push(function (a) { return arguments.length; });
		fs.push(function (a, b, c, d) { return [a, b, c, d].join("/"); });
		fs.push(function (a) { try { return a.x.y; } catch (e) { return "caught"; } });
		class K { constructor(a) { this.a = a; } }
		fs.push(K);
		function call(f, x, y) { try { return f(x, y); } catch (e) { return e.constructor.name; } }
		function call3(f) { try { return f(1, 2, 3); } catch (e) { return e.constructor.name; } }
		function rec(n) { return n === 0 ? 0 : 1 + call(fs[n % 3] === undefined ? rec : rec, n - 1); }
		for (var r = 0; r < 4; r++) {
		  for (var i = 0; i < fs.length; i++) out.push(String(call(fs[i], i, r)), String(call3(fs[i])));
		  out.push(rec(200));
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITSwitchTables runs switch_imm (dense tables through a jump table,
// sparse ones by binary search), switch_char and switch_string in machine
// code, with values the tables cannot hold and strings that need the slow
// path (ropes, strings without a cached key).
func TestJITSwitchTables(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	var cases strings.Builder
	for i := 0; i < 70; i++ {
		fmt.Fprintf(&cases, "case \"k%d\": return %d; ", i, i)
	}
	src := `
		var out = [];
		function dense(x) { switch (x) { case 0: return "a"; case 1: return "b"; case 2: return "c"; case 3: return "d"; case 5: return "f"; default: return "z"; } }
		function sparse(x) { switch (x) { case -2147483648: return "min"; case -7: return "m7"; case 0: return "zero"; case 10: return "ten"; case 1000: return "k"; case 99999: return "big"; case 2147483647: return "max"; default: return "dflt"; } }
		function chr(s) { switch (s) { case "a": return 1; case "b": return 2; case "€": return 3; case "z": return 4; default: return 0; } }
		function str(s) { switch (s) { case "alpha": return 1; case "beta": return 2; case "": return 3; case "0": return 4; case "été": return 5; case "gamma": return 6; default: return 0; } }
		function many(s) { switch (s) { ` + cases.String() + ` default: return -1; } }
		var ints = [0, 1, 2, 3, 4, 5, 6, -1, 1.0, 1.5, -0, NaN, "1", true, null, undefined, -2147483648, -7, 10, 1000, 99999, 2147483647, 2147483648, 4294967296];
		var chars = ["a", "b", "€", "z", "", "ab", "y", 97, null];
		var strs = ["alpha", "beta", "", "0", "été", "gamma", "delta", "alphabet".substring(0, 5), "be" + "ta", 0, "k5", "k69", "k70"];
		for (var r = 0; r < 3; r++) {
		  for (var i = 0; i < ints.length; i++) out.push(dense(ints[i]), sparse(ints[i]));
		  for (var i = 0; i < chars.length; i++) out.push(chr(chars[i]));
		  var rope = "x"; rope = rope.substring(0, 0) + "a"; out.push(chr(rope));
		  for (var i = 0; i < strs.length; i++) out.push(str(strs[i]), many(strs[i]));
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITProtoEpoch changes prototypes that cache cases walked through
// (adding, deleting, redefining, reifying, setPrototypeOf, adding to a
// watched object through a put cache) while the cases are hot: the epoch
// must make them check the chain again.
func TestJITProtoEpoch(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	for _, src := range []string{
		// A method found on a grandparent, shadowed, deleted, redefined.
		`class A { f() { return 1 } } class B extends A {} class C extends B {}
		 var c = new C(), s = 0
		 for (var i = 0; i < 3000; i++) {
		   s += c.f()
		   if (i == 500) B.prototype.f = function () { return 10 }
		   if (i == 1000) delete B.prototype.f
		   if (i == 1500) A.prototype.f = function () { return 100 }
		   if (i == 2000) Object.defineProperty(A.prototype, 'f', { get() { return () => 1000 } })
		   if (i == 2500) Object.setPrototypeOf(B.prototype, { f() { return 7 } })
		 }
		 print(s)`,
		// Misses: a property absent from the chain, then added at its end.
		`var base = {}, mid = Object.create(base), o = Object.create(mid), n = 0
		 for (var i = 0; i < 2000; i++) {
		   if (o.x === undefined) n++; else n += o.x
		   if (i == 700) base.x = 5
		   if (i == 1400) Object.setPrototypeOf(base, { y: 1 }), delete base.x
		 }
		 print(n)`,
		// Put caches adding a property: a setter or read-only property
		// appears on the chain, and adding to a watched object.
		`function P() {} var proto = P.prototype, log = 0
		 for (var i = 0; i < 2000; i++) {
		   var o = new P(); o.v = i
		   if (i == 600) Object.defineProperty(proto, 'v', { set(x) { log += 1 }, configurable: true })
		   if (i == 1200) Object.defineProperty(proto, 'v', { value: 3, writable: false, configurable: true })
		   if (i == 1600) delete proto.v
		   log += o.hasOwnProperty('v') ? 1000 : 0
		   proto['k' + (i % 50)] = i // adds to a watched object
		 }
		 print(log, Object.keys(proto).length)`,
		// A lazy prototype reified on a function in the chain.
		`function F() {} var o = Object.create(F), s = ''
		 for (var i = 0; i < 1500; i++) {
		   s = typeof o.name + typeof o.prototype
		   if (i == 700) F.prototype.q = 1
		 }
		 print(s, o.prototype.q)`,
	} {
		compareJIT(t, src)
	}
}

// TestJITSharedTransitions runs cached gets and puts on objects whose
// shapes come from deleting properties, changing attributes, sealing and
// freezing (shared transitions, not dictionaries).
func TestJITSharedTransitions(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	compareJIT(t, `
		function P(i) { this.a = i; this.tmp = 1; this.b = i * 2; delete this.tmp
		  if (i % 3 == 0) Object.defineProperty(this, 'a', { writable: false })
		  if (i % 5 == 0) Object.freeze(this)
		  if (i % 7 == 0) Object.seal(this) }
		var s = 0, fails = 0
		for (var i = 0; i < 3000; i++) {
		  var p = new P(i)
		  p.b = p.b + 1; p.a = 0; p.c = 1
		  s += p.a + p.b + (p.c || 0) + ('tmp' in p ? 1000 : 0)
		  try { (function () { 'use strict'; p.b = 5 })() } catch (e) { fails++ }
		}
		print(s, fails)`)
}

// TestJITVarInjection reads a global through a hot resolve_scope while a
// sloppy eval in an enclosing function adds a var of the same name: the
// site must then find the function's binding (VM.varInjection).
func TestJITVarInjection(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	compareJIT(t, `
		var g = 1, out = []
		function outer() {
		  function read() { return g }
		  var s = 0
		  for (var i = 0; i < 3000; i++) {
		    s += read()
		    if (i == 1500) eval("var g = 100")
		  }
		  return s
		}
		out.push(outer(), outer())
		var o = { g: 7 }
		function viaWith() { with (o) { return (function () { var s = 0; for (var i = 0; i < 2000; i++) s += g; return s })() } }
		out.push(viaWith())
		print(out.join(","))`)
}

// TestJITTypedArrayViews reads and writes typed arrays through their own
// vector while hot, then detaches the buffer (transfer) or shrinks a
// resizable one: the code must see the detached or shorter view.
func TestJITTypedArrayViews(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	compareJIT(t, `
		var out = []
		function run(a, n) { var s = 0; for (var i = 0; i < n; i++) { a[i & 7] = i; s += a[(i + 3) & 7] === undefined ? 1000 : a[(i + 3) & 7] } return s }
		var kinds = [Int32Array, Float64Array, Uint8Array, Float32Array, Int8Array, Int16Array, Uint16Array, Uint32Array, Uint8ClampedArray]
		for (var k = 0; k < kinds.length; k++) {
		  var buf = new ArrayBuffer(64), a = new kinds[k](buf, 0, 8)
		  out.push(run(a, 2000))
		  var moved = buf.transfer()
		  out.push(run(a, 50), a.length, new kinds[k](moved, 0, 8)[1])
		}
		var rb = new ArrayBuffer(64, { maxByteLength: 128 }), r = new Int32Array(rb)
		out.push(run(r, 2000)); rb.resize(8); out.push(run(r, 50), r.length)
		var sub = new Int32Array(new ArrayBuffer(64), 16, 4)
		out.push(run(sub, 2000))
		print(out.join(","))`)
}

// TestJITMegamorphicProtos runs a megamorphic site over eight classes
// that share a shape but not a prototype, whose methods live on the
// prototypes (megaGet's prototype entries), and changes the prototypes
// while it is hot.
func TestJITMegamorphicProtos(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	compareJIT(t, `
		var classes = []
		for (var c = 0; c < 8; c++) {
		  var F = function (v) { this.v = v }
		  F.prototype.kind = function () { return 1 }
		  F.prototype.tag = c
		  classes.push(F)
		}
		var objs = classes.map(function (F, i) { return new F(i) })
		var s = 0
		for (var i = 0; i < 4000; i++) {
		  var o = objs[i & 7]
		  s += o.kind() + o.tag + (o.missing === undefined ? 0 : 1000)
		  if (i == 1000) classes[3].prototype.kind = function () { return 100 }
		  if (i == 2000) delete classes[5].prototype.tag
		  if (i == 2500) Object.setPrototypeOf(objs[6], { kind: function () { return 7 }, tag: 70 })
		  if (i == 3000) Object.defineProperty(classes[1].prototype, 'tag', { get: function () { return 9 } })
		  if (i == 3500) objs[2].tag = 50
		}
		print(s)`)
}

// TestJITStringObjectLength reads length of String objects (new String,
// a subclass, a rope) in generated code.
func TestJITStringObjectLength(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	compareJIT(t, `
		class S extends String {}
		var vals = [new String("abc"), new S("hello"), new String("x".repeat(70) + "y".repeat(5)), "plain", [1, 2], new String("")]
		var s = 0
		for (var i = 0; i < 3000; i++) {
		  var r = new String(i)
		  while (r.length < 6) r = "0" + r
		  s += r.length + vals[i % vals.length].length
		}
		print(s)`)
}

// TestJITMegamorphicAdds adds a property on a megamorphic site (megaPut's
// add entries) while a setter, then a read-only property appears on the
// prototype and objects are frozen. Besides generated code against the
// interpreter, the output is checked: both run megaPut.put in Go.
func TestJITMegamorphicAdds(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var P = {}, log = 0
		var shapes = []
		for (var s = 0; s < 8; s++) { var o = Object.create(P); for (var j = 0; j <= s; j++) o["k" + s + "_" + j] = 1; shapes.push(o) }
		function mk(i) { var o = Object.create(P); var src = shapes[i & 7]; for (var k in src) o[k] = 1; if (i % 11 == 0) Object.freeze(o); return o }
		function set(o, i) { o.added = i }
		var n = 0
		for (var i = 0; i < 4000; i++) {
		  var o = mk(i); set(o, i); if (o.hasOwnProperty("added")) n++
		  if (i == 1500) Object.defineProperty(P, "added", { set: function (v) { log++ }, configurable: true })
		  if (i == 2500) Object.defineProperty(P, "added", { value: 5, writable: false, configurable: true })
		  if (i == 3000) delete P.added
		}
		print(n, log)`
	// A setter appearing after the site went megamorphic (the epoch must
	// end the add entries; without it the result was 1504 1496).
	src2 := `
		var P = {}, log = 0
		var shapes = []
		for (var s = 0; s < 8; s++) { var o = Object.create(P); for (var j = 0; j <= s; j++) o["k" + s + "_" + j] = 1; shapes.push(o) }
		function mk(i) { var o = Object.create(P); var src = shapes[i & 7]; for (var k in src) o[k] = 1; return o }
		function set(o, i) { o.added = i }
		var n = 0
		for (var i = 0; i < 3000; i++) {
		  var o = mk(i); set(o, i); if (o.hasOwnProperty("added")) n++
		  if (i == 1500) Object.defineProperty(P, "added", { set: function (v) { log++ }, configurable: true })
		}
		print(n, log)`
	// The expected outputs are those of the code before the add entries.
	for _, c := range []struct{ src, want string }{{src, "2272 1000"}, {src2, "1501 1499"}} {
		compareJIT(t, c.src)
		if got := strings.TrimSpace(runForTest(t, c.src, false)); got != c.want {
			t.Fatalf("got %q, want %q", got, c.want)
		}
	}
}

// TestJITMegamorphicAbsent reads absent properties on megamorphic sites
// (megaGet's absent entries) while the key appears on a prototype, on
// Object.prototype, as an own property and as a getter; the output is the
// code's before the absent entries (without the epoch check it was 4554).
func TestJITMegamorphicAbsent(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
var P = {}, objs = []
for (var s = 0; s < 8; s++) { var o = Object.create(P); for (var j = 0; j <= s; j++) o["k" + s + "_" + j] = 1; objs.push(o) }
var d = {}; for (var j = 0; j < 80; j++) d["d" + j] = j; objs.push(d)
var keys = ["absent", "k0_0", "other"], n = 0
function look(o, k) { return o[k] }
function lookId(o) { return o.absent }
for (var i = 0; i < 4000; i++) {
  var o = objs[i % objs.length]
  var v = look(o, keys[i % 3]); if (v !== undefined) n += typeof v === "number" ? v : 1000
  var w = lookId(o); if (w !== undefined) n += w
  if (i == 1000) P.absent = 5
  if (i == 1500) delete P.absent
  if (i == 2000) Object.prototype.other = 7
  if (i == 2500) delete Object.prototype.other
  if (i == 3000) objs[2].absent = 11
  if (i == 3500) Object.defineProperty(P, "absent", { get: function () { return 3 }, configurable: true })
}
print(n)`
	compareJIT(t, src)
	if got := strings.TrimSpace(runForTest(t, src, false)); got != "7108" {
		t.Fatalf("got %q, want 7108", got)
	}
}

// TestJITStringAppend appends to strings in generated code (appendString):
// one character and longer pieces, two strings continuing the same one
// (the second must not write into the first's buffer), non-ASCII pieces,
// numbers and empty strings, past the pool's 32 strings.
func TestJITStringAppend(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	compareJIT(t, `
		var table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
		function enc(n) { var r = ""; for (var i = 0; i < n; i++) r += table[i % 64]; return r }
		var s = enc(100), forks = []
		for (var k = 0; k < 50; k++) { var a = s + "x" + k, b = s + "yy"; forks.push(a.length + b.length, a.slice(-3), b.slice(-3)) }
		var m = enc(70)
		for (var i = 0; i < 300; i++) { m += (i % 7 == 0 ? "é" : i % 5 == 0 ? "" : i % 3 == 0 ? i : "q") }
		var h = 0; for (var i = 0; i < m.length; i++) h = (h * 31 + m.charCodeAt(i)) | 0
		var c = ""; for (var i = 0; i < 300; i++) c = c + "x"
		var d = ""; for (var i = 0; i < 300; i++) d = "y" + d
		var e = ""; for (var i = 0; i < 300; i++) e = e + i
		print(enc(5000).length, enc(1000).slice(500, 520), forks.join(","), m.length, h, c.length, d.length, e.length)`)
}

// TestJITNewArray makes array literals in generated code (newSmallArray):
// zero to five elements, holes, nesting, pushes past the inline room,
// arrays kept while the pool is refilled, and Array.prototype's methods.
func TestJITNewArray(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	compareJIT(t, `
		Array.prototype.sum = function () { var s = 0; for (var i = 0; i < this.length; i++) s += (this[i] | 0); return s }
		var kept = [], h = 0
		for (var i = 0; i < 2000; i++) {
		  var a = [], b = [i], c = [i, "x", null], d = [1, , 3, i], e = [[i], [i + 1, [i + 2]]], f = [1, 2, 3, 4, 5]
		  a.push(i); a.push(i, i, i, i, i)
		  b.length = 3
		  if (i % 100 == 0) kept.push(c, d)
		  h += a.sum() + b.length + c.length + d.sum() + (1 in d ? 1000 : 0) + e[1][1][0] + f.sum() + Array.isArray(e[0])
		}
		print(h, kept.length, kept[4].join("|"), kept[5].join("|"), JSON.stringify(kept.slice(0, 2)))`)
}

// TestJITNewClosure makes closures in generated code (newClosure): function
// expressions and declarations in a loop, object literal methods,
// captured variables, name and length, the lazy prototype, new and
// instanceof, arrows and generators (slow path), past the pool's size.
func TestJITNewClosure(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	compareJIT(t, `
		var out = 0, kept = []
		for (var i = 0; i < 2000; i++) {
		  var f = function named(a, b) { return a + b + i }
		  function decl(x) { return x * i }
		  var o = { m(q) { return q + 1 }, get g() { return 2 } }
		  var arrow = (z) => z + i
		  var gen = function* () { yield i }
		  out += f(1, 2) + decl(2) + o.m(3) + o.g + arrow(1) + gen().next().value
		  out += f.length + decl.length + (f.name == "named" ? 1 : 0) + (decl.name == "decl" ? 1 : 0)
		  if (i % 50 == 0) {
		    var C = function (v) { this.v = v }
		    C.prototype.get = function () { return this.v }
		    var c = new C(i)
		    out += c.get() + (c instanceof C ? 1 : 0) + Object.keys(C.prototype).length
		    kept.push(f, decl, C)
		  }
		}
		print(out, kept.length, kept[3](4, 5), kept[4](3), typeof kept[5].prototype, Object.getOwnPropertyNames(kept[0]).sort().join(","))`)
}

// TestJITNewScope makes scopes for captured variables in generated code
// (newScope): one to five slots, let in its TDZ, closures kept after the
// pool is refilled, and catch and block scopes.
func TestJITNewScope(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	compareJIT(t, `
		var kept = [], out = 0
		function one(a) { return function () { return a } }
		function four(a, b, c, d) { return function () { return a + b + c + d } }
		function five(a, b, c, d, e) { return function () { return a + b + c + d + e } }
		function tdz() { var r; try { r = (function () { return x })(); } catch (e) { r = -1 } let x = 2; return r + (function () { return x })() }
		for (var i = 0; i < 2000; i++) {
		  var f1 = one(i), f4 = four(i, 1, 2, 3), f5 = five(i, 1, 2, 3, 4)
		  out += f1() + f4() + f5() + tdz()
		  for (let j = 0; j < 2; j++) kept.length < 50 && kept.push(function () { return j + i })
		  try { throw i } catch (e) { out += (function () { return e })() }
		}
		print(out, kept.length, kept[7](), kept[49]())`)
}

// TestJITNewRegExp evaluates regexp literals in generated code
// (newRegExp): a new object each time with lastIndex 0 and its own global
// state, source and flags, compile on one of them, past the pool's size.
func TestJITNewRegExp(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	compareJIT(t, `
		var out = [], kept = [], n = 0
		for (var i = 0; i < 2000; i++) {
		  var g = /a(b)?/g, s = "ab a ab"
		  g.exec(s); g.exec(s)
		  n += g.lastIndex
		  var r = /x+/i
		  n += r.test("aXXa") ? 1 : 0
		  n += ("a1b22c333".replace(/\d+/g, "#")).length
		  if (i % 100 == 0) { kept.push(g, r); out.push(g.source + "/" + g.flags + ":" + g.lastIndex) }
		  if (i == 500) r.compile("q", "g")
		}
		print(n, out.slice(0, 3).join(","), kept.length, kept[0] !== kept[2], kept[1].source, kept[3].lastIndex)`)
}

// TestJITArgumentsByVal reads arguments[i] in machine code: mapped
// parameters after assignments either way, more arguments than
// parameters, deleted and redefined indexes, strict functions, captured
// parameters (a scope), and indexes past the end with Object.prototype
// elements.
func TestJITArgumentsByVal(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var out = [];
		function sum() { var s = 0; for (var i = 0; i < arguments.length; i++) s += arguments[i]; return s; }
		function mapped(a, b) { a = 10; arguments[1] = 20; return [arguments[0], b, arguments[2], arguments[3]].join("/"); }
		function strict(a, b) { "use strict"; a = 10; return [arguments[0], arguments[1], arguments[5]].join("/"); }
		function del(a, b) { delete arguments[0]; return [arguments[0], arguments[1], a].join("/"); }
		function getter(a, b) {
		  var n = 0;
		  Object.defineProperty(arguments, 1, { get: function () { return "g" + (++n); } });
		  return [arguments[0], arguments[1], arguments[1], b].join("/");
		}
		function captured(a, b) { var f = function () { return a; }; a = 7; return [arguments[0], f(), arguments[1]].join("/"); }
		function past(a) { return [arguments[1], arguments[2], arguments[-1], arguments[1.5]].join("/"); }
		function anyIndex(i) { return arguments[i]; }
		for (var r = 0; r < 30; r++) {
		  out.push(sum(1, 2, 3, r), sum(), sum("a", "b"));
		  out.push(mapped(1, 2, 3), mapped(1), strict(1, 2), del(1, 2), getter(1, 2), captured(1, 2));
		  if (r == 20) Object.prototype[2] = "proto2";
		  out.push(past(1), past(1, 2, 3));
		  out.push(anyIndex(0), anyIndex(1), anyIndex(4294967295));
		}
		delete Object.prototype[2];
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITTailCallDepth makes tail calls deeper than the stack limit: past
// a quarter of it, generated code must leave them to the interpreter's
// proper tail calls too, or the call at the bottom overflows the stack
// (it did once a cache made the recursive call direct).
func TestJITTailCallDepth(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		"use strict";
		var s = "abcdefghij";
		function tail(n) { return n == 0 ? s.substr(0, 2) : tail(n - 1); }
		function plain(n) { return n == 0 ? 0 : 1 + plain(n - 1); }
		function probe(n) { try { return probe(n + 1); } catch (e) { return n; } }
		function tryTail(n) { try { return tail(n); } catch (e) { return "overflow at " + n; } }
		var out = [tryTail(100), tryTail(3000), plain(2000), probe(0)];
		for (var r = 0; r < 5; r++) out.push(tryTail(3000 + r), plain(2000));
		print(out.join(","));`
	compareJIT(t, src)
	// Code jettisoned while the frames of a chain inside it are finished
	// (finishNative) leaves those frames to the interpreter.
	compareJIT(t, `
		"use strict";
		var s = "abcdef";
		function tail(n) { return n == 0 ? s.substr(1, 2) + String(n) : tail(n - 1); }
		function mid(i) { return tail(5000 + i); }
		var out = [];
		for (var r = 0; r < 20; r++) out.push(mid(r));
		print(out.join(","));`)
}

// TestJITSuspend runs slow paths and builtins of code called directly by
// other generated code, which suspends for them (jit_suspend.go): throws
// caught by the outermost function, builtins that call generated code that
// suspends again, deep recursion and garbage collections while code is
// suspended (the stack moves, the collector starts marking), proper tail
// calls, and a callee whose code is jettisoned while it is suspended.
func TestJITSuspend(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		"use strict";
		var out = [];
		var s = "abcdefghij";
		function leaf(i) { return s.substr(i & 7, 2) + JSON.stringify({ i: i }); }
		function mid(i) { return leaf(i) + leaf(i + 1); }
		function thrower(i) { if (i % 7 == 3) throw new Error("e" + i); return s.substr(i & 3, 1); }
		function midThrow(i) { return thrower(i) + "!"; }
		function outer(n) { var r = []; for (var i = 0; i < n; i++) { try { r.push(midThrow(i)); } catch (e) { r.push(e.message); } } return r.join(","); }
		function mapper(a) { return a.map(function (x) { return mid(x).length + x; }); }
		function viaMap(i) { return mapper([i, i + 1, i + 2]).join("/"); }
		function deep(n) { return n == 0 ? s.substr(1, 3) : Object.keys({ a: deep(n - 1) }).length + deep(n - 1).length * 0 + ""; }
		function rec(n) { return n == 0 ? deep(12) : rec(n - 1); }
		function garbage(i) { var a = []; for (var k = 0; k < 2000; k++) a.push({ k: k, s: "x" + k }); return JSON.stringify(a).length + i; }
		function midGarbage(i) { return garbage(i) % 1000; }
		function tail(n) { return n == 0 ? s.substr(0, 2) : tail(n - 1); }
		function midTail(i) { return tail(i % 5); }
		function poly(x) { return x + 1; }
		function midPoly(x) { return poly(x) + String(x).length; }
		function parse(i) { return JSON.parse(i % 5 == 2 ? "{bad" : "[" + i + "]")[0]; }
		function midParse(i) { return parse(i) * 2; }
		function outerParse(n) { var r = 0; for (var i = 0; i < n; i++) { try { r += midParse(i); } catch (e) { r += 1000; } } return r; }
		function midDeepTail(i) { return tail(3000 + i); }
		function big(i) { var a = []; for (var k = 0; k < 100000; k++) a.push({ k: k }); return a.map(function (o) { return o.k; }).length + i; }
		function midBig(i) { return big(i) % 7; }
		function jf(x, d) { var y = x * 2 + 1; if (d > 0) return y + jh(d); return y; }
		function jh(d) { return [1].map(function () { var t = 0; for (var k = 0; k < 40; k++) t += String(jf("s" + k, 0)).length; return t; })[0]; }
		function probe(n) { try { return probe(n + 1); } catch (e) { return n; } }
		function deepThrow(i) { return [i].map(function (x) { return JSON.parse("{bad" + x); })[0]; }
		function midDeepThrow(i) { return deepThrow(i) + 1; }
		var before = probe(0), caught = 0;
		for (var k = 0; k < 2000; k++) { try { midDeepThrow(k); } catch (e) { caught++; } try { midParse(2); } catch (e) { caught++; } }
		out.push("depth " + (probe(0) - before) + " caught " + caught);
		for (var r = 0; r < 40; r++) {
		  out.push(mid(r), outer(10), viaMap(r), rec(3), midTail(r));
		  if (r % 10 == 0) out.push(midGarbage(r));
		  out.push(midPoly(r < 30 ? r : "s" + r));
		  out.push(outerParse(12), midDeepTail(r));
		  if (r % 8 == 0) out.push(midBig(r));
		  out.push(jf(r, 1));
		}
		print(out.join(";").length, out.slice(-12).join(";"));`
	compareJIT(t, src)
}

// TestJITStringSlice runs String.prototype.slice through its thunk and
// its fallbacks: ASCII, UTF-16 and rope strings, negative and too large
// indexes, non-integer and non-number arguments, zero to three arguments,
// and results used afterwards (length, comparisons, keys, appends).
func TestJITStringSlice(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var strs = ["abcdefghijklmnop", "x", "", "héllo wörld ünïcode", "0123456789".repeat(20)];
		var rope = "abc"; for (var k = 0; k < 6; k++) rope = rope + "-" + k;
		strs.push(rope);
		var args = [0, 1, 3, -1, -4, 100, -100, 7, 2.5, NaN, undefined, "2", null, -0];
		function run(s, out, keys) {
		  for (var a = 0; a < args.length; a++) {
		    var x = s.slice(args[a]);
		    out.push(x.length + ":" + x);
		    for (var b = 0; b < args.length; b++) {
		      var y = s.slice(args[a], args[b]);
		      out.push(y);
		      keys[y] = (keys[y] | 0) + 1;
		      if (y.length > 0) out.push(y.charCodeAt(y.length - 1), y + "!", y == s, y === s.substring(0));
		    }
		  }
		  out.push(s.slice(), s.slice(1, 2, 3));
		}
		var out = [], keys = {};
		for (var r = 0; r < 3; r++) for (var i = 0; i < strs.length; i++) run(strs[i], out, keys);
		var n = 0; for (var k in keys) n += keys[k];
		var all = out.join("|"), h = 0;
		for (var k = 0; k < all.length; k++) h = (h * 31 + all.charCodeAt(k)) | 0;
		print(out.length, all.length, n, h, out.slice(0, 60).join(","));`
	compareJIT(t, src)
}

// TestJITSuspendDeep suspends at the bottom of chains of direct calls
// around jitMaxDepth deep: on arm64, 64 calls took all of CallS's room
// for generated code's pushes, and suspending at the bottom overwrote
// CallS's own frame (found on arm64 in acorn, babylon and espree).
func TestJITSuspendDeep(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	src := `
		var o = {};
		function r(n) {
		  if (n === 0) return JSON.stringify([n]).length + String(o).length;
		  return r(n - 1) + 1;
		}
		var out = [];
		for (var d = 56; d <= 70; d++) { var t = 0; for (var k = 0; k < 60; k++) t += r(d); out.push(t); }
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITTypedProfile runs get_by_val and put_by_val sites that saw only
// one kind of typed array in the interpreter (the JIT puts that kind's
// path first), then other kinds, plain arrays, objects and strings,
// indexes out of range, a detached buffer, NaN, -0, large and non-number
// values to store.
func TestJITTypedProfile(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function sum(a, n) { var s = 0; for (var i = 0; i < n; i++) s += a[i]; return s; }
		function fill(a, n, v) { for (var i = 0; i < n; i++) a[i] = v + i; return a; }
		var kinds = [Int32Array, Float64Array, Uint8Array, Float32Array, Int8Array, Int16Array, Uint16Array, Uint32Array, Uint8ClampedArray];
		var out = [];
		for (var k = 0; k < kinds.length; k++) {
		  var a = new kinds[k](64);
		  for (var r = 0; r < 300; r++) { fill(a, 64, r); out.push(sum(a, 64)); }
		}
		var i32 = new Int32Array(16), f64 = new Float64Array(16);
		for (var r = 0; r < 600; r++) { fill(i32, 16, r); out.push(sum(i32, 16)); }
		out.push(sum(f64, 16), sum([1, 2, 3], 3), sum({0: 5, 1: 6}, 2), sum("abc", 3), sum(i32, 40));
		fill(i32, 16, 2.5); out.push(sum(i32, 16));
		fill(i32, 16, 1e12); out.push(sum(i32, 16));
		fill(i32, 16, NaN); out.push(sum(i32, 16));
		fill(i32, 16, -0); out.push(sum(i32, 16));
		fill(i32, 16, "7"); out.push(sum(i32, 16));
		fill(f64, 16, NaN); out.push(sum(f64, 16));
		var plain = fill([], 16, 3); out.push(sum(plain, 16), plain.length);
		var buf = new ArrayBuffer(64), v = new Int32Array(buf);
		fill(v, 16, 9); out.push(sum(v, 16));
		var copy = buf.transfer ? buf.transfer() : null;
		out.push(sum(v, 16), v.length);
		print(out.join(","));`
	saved := jitThreshold
	jitThreshold = 100 // the interpreter profiles the sites first
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}

// TestForwardedArguments checks x.apply(y, arguments) in functions that
// pass their arguments on without an arguments object
// (parser.ArgumentsOnlyForwardedFeature), against what the object would
// give: any number of arguments and new, an apply that is not the
// original (it gets a real arguments object), Function.prototype.apply
// replaced, nested call chains, tail calls, and functions that do not
// qualify (parameters, other uses, arrows).
func TestForwardedArguments(t *testing.T) {
	src := `
		var fails = [];
		function check(what, got, want) { if (got !== want) fails.push(what + ": " + got + " != " + want); }
		function target() { return Array.prototype.join.call(arguments, ","); }
		function P() {} P.prototype.init = function () { this.s = Array.prototype.join.call(arguments, "|"); return this; };
		var K = function () { return this.init.apply(this, arguments); }; K.prototype = P.prototype;
		var obj = { m: { apply: function (t, a) { return Object.prototype.toString.call(a) + a.length + a[1]; } } };
		function viaObj() { return obj.m.apply(this, arguments); }
		function strictFwd() { "use strict"; return target.apply(null, arguments); }
		function strictParams(a, b) { "use strict"; a = 99; return target.apply(null, arguments); }
		function sloppyParams(a) { a = 42; return target.apply(null, arguments); }
		function deep() { return target.call(null, target.call(null, target.call(null, target.call(null, target.apply(null, arguments))))); }
		function viaFn() { return target.apply(null, arguments); }
		for (var r = 0; r < 300; r++) {
		  check("new", new K(r, 2, 3).s, r + "|2|3");
		  check("none", new K().s, "");
		  check("obj", viaObj(r, "b"), "[object Arguments]2b");
		  check("strict", strictFwd(r, 2), r + ",2");
		  check("strictParams", strictParams(r, 2), r + ",2");
		  check("sloppyParams", sloppyParams(r, 5), "42,5");
		  check("deep", deep(r, 1), r + ",1");
		}
		var orig = Function.prototype.apply;
		Function.prototype.apply = function (t, a) { return "patched" + Object.prototype.toString.call(a) + a.length; };
		check("patched", viaFn(1, 2, 3), "patched[object Arguments]3");
		Function.prototype.apply = orig;
		check("restored", viaFn(1, 2, 3), "1,2,3");
		print(fails.length ? fails.slice(0, 5).join("\n") : "ok");`
	saved := jitThreshold
	jitThreshold = 100
	defer func() { jitThreshold = saved }()
	if got := runForTest(t, src, false); got != "ok\n" {
		t.Fatalf("interpreter: %s", got)
	}
	compareJIT(t, src)
}

// TestJITGlobalGet reads global variables through get_from_scope's fast
// path (the global object's fields at constant addresses) while globals
// are added, deleted, made accessors and shadowed by let, with eval and
// with in between.
func TestJITGlobalGet(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	saved := jitThreshold
	jitThreshold = 50 // compiled once the caches hold the globals
	defer func() { jitThreshold = saved }()
	src := `
		var g1 = 1, out = []; globalThis.g2 = 2;
		function read() { return g1 + g2; }
		function readMaybe() { return typeof g9 === "undefined" ? -1 : g9; }
		for (var r = 0; r < 300; r++) {
		  out.push(read(), readMaybe());
		  if (r === 120) globalThis.g9 = 9;
		  if (r === 140) { for (var k = 0; k < 300; k++) globalThis["extra" + k] = k; }
		  if (r === 160) delete globalThis.g9;
		  if (r === 180) Object.defineProperty(globalThis, "g2", { get: function () { return 100; }, configurable: true });
		  if (r === 200) Object.defineProperty(globalThis, "g2", { value: 5, writable: true, configurable: true });
		  if (r === 220) g1 = "s";
		  if (r === 240) (0, eval)("var g1 = 7");
		}
		print(out.join(","));`
	compareJIT(t, src)
}

// TestJITGlobalLexical reads and writes global let, const and class
// bindings through the fast path (constant slot addresses under
// globalLexicalGen), in its TDZ, and after another script added global
// lexical bindings (which may move the slots); assigning a const from
// another script must still throw.
func TestJITGlobalLexical(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	scripts := []string{`
		let seed = 1; const K = 3; class C { v() { return 2; } }
		function rnd() { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % 100; }
		function useAll() { return K + new C().v() + (typeof later === "undefined" ? 0 : 1); }
		var out = [];
		function tdz() { try { return lateLet; } catch (e) { return e.constructor.name; } }
		for (var r = 0; r < 300; r++) out.push(rnd(), useAll(), tdz());
		let lateLet = "set";
		for (var r = 0; r < 50; r++) out.push(rnd(), tdz());
		print(out.join(","));`, `
		let more1 = 1, more2 = 2, more3 = 3, more4 = 4, more5 = 5, more6 = 6, more7 = 7, more8 = 8, more9 = 9;
		var out2 = [];
		function setK() { try { K = 9; } catch (e) { return e.constructor.name; } return K; }
		for (var r = 0; r < 300; r++) out2.push(rnd(), useAll(), tdz(), seed, setK());
		seed = 5;
		for (var r = 0; r < 50; r++) out2.push(rnd());
		print(out2.join(","));`}
	saved := jitThreshold
	jitThreshold = 50
	defer func() { jitThreshold = saved }()
	run := func(useJIT bool) string {
		savedJIT := jitEnabled
		jitEnabled = useJIT
		defer func() { jitEnabled = savedJIT }()
		v := New()
		var out strings.Builder
		v.Print = func(s string) { out.WriteString(s); out.WriteByte('\n') }
		for _, src := range scripts {
			if _, err := v.Eval(src, "lexical.js"); err != nil {
				out.WriteString("error: " + err.Error())
			}
		}
		return out.String()
	}
	with, without := run(true), run(false)
	if with != without || strings.Contains(with, "error:") {
		t.Fatalf("JIT %q\ninterpreter %q", with, without)
	}
}

// TestJITClosureVars reads and writes variables of enclosing functions
// from inner functions (global resolve type with a slot cache: the
// constant table and depth paths), from several instances of the outer
// function, in a let's TDZ, and after a sloppy eval injected a var that
// shadows one of them.
func TestJITClosureVars(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	scripts := []string{`
		var out = [];
		function outer(k) {
			var a = k, b = [k, k + 1];
			function mid() {
				var m = 2;
				function inner(i) { a = a + i; b[i & 1] += m; return a + b[0] - b[1] + (typeof x === "undefined" ? 0 : x); }
				return inner;
			}
			function late() { try { return z; } catch (e) { return e.constructor.name; } }
			var r = [mid(), late];
			let z = k * 3;
			return r;
		}
		var fs = [outer(1), outer(10), outer(100)];
		for (var r = 0; r < 300; r++) for (var j = 0; j < 3; j++) out.push(fs[j][0](r), fs[j][1]());
		print(out.join(","));`, `
		function top() {
			var q = 1;
			return function injector(code) {
				var a = 5;
				eval(code);
				return function () { var s = 0; for (var i = 0; i < 200; i++) s += a + q + (typeof b === "undefined" ? 0 : b.length); return s; };
			};
		}
		var injector = top();
		var g1 = injector("");
		var out2 = [];
		for (var r = 0; r < 100; r++) out2.push(g1(), fs[r % 3][0](r));
		var g2 = injector("var b = 'shadow'; var q = 7;");
		for (var r = 0; r < 100; r++) out2.push(g1(), g2(), fs[r % 3][0](r));
		print(out2.join(","));`}
	saved := jitThreshold
	jitThreshold = 50
	defer func() { jitThreshold = saved }()
	run := func(useJIT bool) string {
		savedJIT := jitEnabled
		jitEnabled = useJIT
		defer func() { jitEnabled = savedJIT }()
		v := New()
		var out strings.Builder
		v.Print = func(s string) { out.WriteString(s); out.WriteByte('\n') }
		for _, src := range scripts {
			if _, err := v.Eval(src, "closure.js"); err != nil {
				out.WriteString("error: " + err.Error())
			}
		}
		return out.String()
	}
	with, without := run(true), run(false)
	if with != without || strings.Contains(with, "error:") {
		t.Fatalf("JIT %q\ninterpreter %q", with, without)
	}
}

// noGC turns the GC off until the function it returns runs: generated
// code does not run while the GC's write barrier is on, so that a test
// that counts what its code did fails at random otherwise. (Then one
// collection: SetGCPercent does not stop a cycle already marking.)
func noGC() func() {
	old := debug.SetGCPercent(-1)
	runtime.GC()
	return func() { debug.SetGCPercent(old) }
}

// TestJITReprofile reads and writes typed arrays at sites that had not run
// when their function was compiled (reprofileExit): the code is thrown
// away once and compiled again with the path of the kind they saw, and
// later other kinds and plain arrays still work there.
func TestJITReprofile(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		var f32 = new Float32Array(64), i32 = new Int32Array(64), plain = [];
		for (var i = 0; i < 64; i++) { f32[i] = i / 4; i32[i] = i * 3; plain[i] = i; }
		function step(a, b, n, useTyped) {
			var s = 0;
			for (var i = 0; i < n; i++) {
				if (useTyped) { a[i & 63] = a[(i + 1) & 63] + 0.5; s += a[i & 63]; }
				else s += b[i & 63];
			}
			return s;
		}
		var out = [];
		out.push(step(f32, plain, 2000, false));
		out.push(step(f32, plain, 2000, true));
		out.push(step(f32, plain, 2000, true));
		out.push(step(i32, plain, 2000, true));
		out.push(step(plain, plain, 2000, true));
		out.push(step(f32, plain, 2000, true));
		print(out.join(","));`
	saved := jitThreshold
	jitThreshold = 50
	defer func() { jitThreshold = saved }()
	defer noGC()()
	before := jitReprofiles.Load()
	compareJIT(t, src)
	if jitReprofiles.Load() == before {
		t.Fatal("no site was reprofiled")
	}
}

// TestJITDynamicScope runs loops whose variables resolve dynamically (with
// and sloppy eval): their scope instructions take the slow path instead of
// leaving the loop to the interpreter.
func TestJITDynamicScope(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	compareJIT(t, `
		var out = [];
		function withLoop(o) {
			var s = 0, x = 100;
			with (o) { for (var i = 0; i < 300; i++) { s += x; if (i == 150) o.x = 7; if (i == 200) delete o.x; x = x + 1; } }
			return s + "/" + x + "/" + o.x;
		}
		function evalLoop(code) {
			var a = 1;
			eval(code);
			var s = 0;
			for (var i = 0; i < 300; i++) { s += a + (typeof b === "undefined" ? 0 : b); a = a + 1; }
			return s;
		}
		for (var r = 0; r < 3; r++) {
			out.push(withLoop({ y: 1 }), withLoop({ x: 3 }));
			out.push(evalLoop(""), evalLoop("var b = 2"), evalLoop("var a = 10"));
		}
		print(out.join(","));`)
}

// TestJITCatchInLoop runs a loop containing for-of loops, whose catch (it
// closes the iterator) lies inside the outer loop: the catch no longer
// keeps that loop from being compiled. Exceptions out of a for-of body go
// to the interpreter, which runs the handlers and enters the code again
// at a loop_hint.
func TestJITCatchInLoop(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	compareJIT(t, `
		class Player {
			constructor() { this.cards = []; this.rank = 0; this.wins = 0; }
			clear() { this.cards = []; }
			takeCard(c) { this.cards.push(c); }
			scoreHand() { let r = 0; for (let c of this.cards) r = (r << 4 | c) & 0xffffff; this.rank = r; }
			wonHand() { this.wins++; }
		}
		var seed = 7;
		function deal() { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % 13; }
		var errs = 0;
		function playHands(players) {
			let handsPlayed = 0, highestRank = 0;
			do {
				for (let player of players) player.clear();
				for (let i = 0; i < 5; i++) for (let player of players) player.takeCard(deal());
				for (let player of players) player.scoreHand();
				handsPlayed++;
				highestRank = 0;
				for (let player of players) if (player.rank > highestRank) highestRank = player.rank;
				try {
					for (let player of players) {
						if (handsPlayed % 37 == 0 && player === players[1]) throw handsPlayed;
						if (player.rank == highestRank) player.wonHand();
					}
				} catch (e) { errs += e; }
			} while (handsPlayed < 300);
		}
		var ps = [new Player(), new Player(), new Player()];
		for (var r = 0; r < 3; r++) playHands(ps);
		print(ps.map(p => p.wins).join(",") + "/" + errs);`)
}

// TestJITNullishEquals compares objects with undefined and null by == and
// != (eq, neq, jeq, jneq), on both sides, including an [[IsHTMLDDA]]
// object, which equals them: machine code reads its flag.
func TestJITNullishEquals(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		var vals = [{}, [], function () {}, dda, undefined, null, 0, "", false];
		var out = [];
		function cmp(a, b) {
			var r = (a == b ? 1 : 0) + (a != b ? 2 : 0) + (b == a ? 4 : 0);
			if (a == undefined) r += 8;
			if (null != b) r += 16;
			return r;
		}
		for (var k = 0; k < 200; k++)
			for (var i = 0; i < vals.length; i++)
				for (var j = 0; j < vals.length; j++)
					if (k == 199) out.push(cmp(vals[i], vals[j])); else cmp(vals[i], vals[j]);
		print(out.join(""));`
	saved := jitThreshold
	jitThreshold = 50
	defer func() { jitThreshold = saved }()
	run := func(useJIT bool) string {
		savedJIT := jitEnabled
		jitEnabled = useJIT
		defer func() { jitEnabled = savedJIT }()
		v := New()
		r := v.Realm()
		r.GlobalObject.CreateDataProperty(StrKey("dda"), ObjectValue(r.NewHTMLDDA()))
		var out strings.Builder
		v.Print = func(s string) { out.WriteString(s); out.WriteByte('\n') }
		if _, err := v.Eval(src, "nullish.js"); err != nil {
			out.WriteString("error: " + err.Error())
		}
		return out.String()
	}
	with, without := run(true), run(false)
	if with != without || strings.Contains(with, "error:") {
		t.Fatalf("JIT %q\ninterpreter %q", with, without)
	}
}

// TestJITUndefinedCompare compares numbers with a missing argument
// (undefined) in a hot function, as jumps and as values: the slow path
// notes the site, the code is compiled again and loads undefined as NaN
// there; numbers, null, strings and objects still compare right after.
func TestJITUndefinedCompare(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function hit(t, near, far) {
			var r = 0;
			if (t < near || t > far) r += 1;
			if (!(t <= far)) r += 2;
			r += (far >= t ? 4 : 0) + (t > far ? 8 : 0);
			return r + (near < far ? 16 : 0);
		}
		var out = [], s = 0;
		for (var i = 0; i < 3000; i++) s += hit(i % 7, 2);
		out.push(s);
		var others = [5, null, "4", "x", {}, [3], true, undefined, NaN, -1];
		for (var k = 0; k < others.length; k++) {
			s = 0;
			for (var i = 0; i < 300; i++) s += hit(i % 7, 2, others[k]);
			out.push(s);
		}
		print(out.join(","));`
	saved := jitThreshold
	jitThreshold = 50
	defer func() { jitThreshold = saved }()
	defer noGC()()
	before := jitReprofiles.Load()
	compareJIT(t, src)
	if jitReprofiles.Load() == before {
		t.Fatal("no comparison was recompiled for undefined")
	}
}

// TestJITNativeTailCall makes tail calls (strict mode) to builtins without
// a thunk, through the native exit, including one that throws.
func TestJITNativeTailCall(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	compareJIT(t, `
		"use strict";
		var o = { a: 1, b: 2 };
		function keys(x) { return Object.keys(x); }
		function join(a) { return a.join("-"); }
		function num(s) { return parseInt(s, 16); }
		function bad(x) { return Object.defineProperty(x, "a", { value: 1 }); }
		var out = [], n = 0;
		for (var i = 0; i < 500; i++) {
			n += keys(o).length + join([i, 2]).length + num("ff");
			if (i % 100 == 0) {
				try { bad(i); } catch (e) { out.push(e.constructor.name); }
			}
		}
		out.push(n);
		print(out.join(","));`)
}

// TestJITNativeConstruct makes new of builtins through the native exit,
// including one that is not a constructor (TypeError).
func TestJITNativeConstruct(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	compareJIT(t, `
		var out = [], n = 0;
		function make(i) {
			var d = new Date(2020, i % 12, 1), s = new String("ab" + i), a = new Array(i % 5);
			var m = new Map([[i, 1]]), r = new RegExp("a" + (i % 3)), e = new Error("e" + i);
			return d.getMonth() + s.length + a.length + m.size + (r.test("a1") ? 1 : 0) + e.message.length;
		}
		for (var i = 0; i < 500; i++) n += make(i);
		for (var i = 0; i < 3; i++) { try { new Math.max(1); } catch (e) { out.push(e.constructor.name); } }
		out.push(n);
		print(out.join(","));`)
}

// TestJITEnumeratorNext runs for-in over objects of one shape (the cached
// names, in machine code), objects that change during the loop (a key
// deleted or added: the slow path checks), prototypes with enumerable
// keys, arrays and dictionaries.
func TestJITEnumeratorNext(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	compareJIT(t, `
		function keys(o, mutate) {
			var s = "";
			for (var k in o) {
				s += k + ":" + o[k] + ";";
				if (mutate == 1 && k == "b") delete o.c;
				if (mutate == 2 && k == "a") o.z = 9;
				if (mutate == 3 && k == "b") break;
			}
			return s;
		}
		function P() { this.own = 1; }
		P.prototype.inherited = 2;
		var out = [];
		for (var i = 0; i < 400; i++) {
			var o = { a: i, b: 2, c: 3, d: 4 };
			out.push(keys(o, i % 4));
			if (i % 50 == 0) {
				out.push(keys(new P(), 0), keys([i, 2, 3], 0));
				var d = {}; for (var j = 0; j < 40; j++) d["k" + j] = j; delete d.k3;
				out.push(keys(d, 0));
			}
		}
		print(out.join("|").length + " " + out.slice(0, 12).join("|"));`)
}

// TestJITAppendWithinLength appends to arrays whose length is past their
// elements (new Array(n)): within the length the length stays, past it it
// grows; non-extensible arrays, a length that is not writable (sloppy and
// strict) and an index on Array.prototype keep the slow path's results.
func TestJITAppendWithinLength(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	compareJIT(t, `
		function fill(a, n) { for (var i = 0; i < n; i++) a[i] = i * 2; return a.length + ":" + a.join(","); }
		function fillStrict(a, n) { "use strict"; try { for (var i = 0; i < n; i++) a[i] = i; } catch (e) { return e.constructor.name + a.length; } return a.length; }
		var out = [];
		for (var r = 0; r < 60; r++) {
			out.push(fill(new Array(8), 5), fill(new Array(4), 7), fill([], 3));
			var ne = new Array(6); Object.preventExtensions(ne); out.push(fill(ne, 3));
			var fl = new Array(2); Object.defineProperty(fl, "length", { writable: false }); out.push(fill(fl, 4), fillStrict(fl, 4));
			var fl2 = new Array(3); Object.defineProperty(fl2, "length", { writable: false }); out.push(fillStrict(fl2, 2));
		}
		Array.prototype[1] = "p";
		out.push(fill(new Array(5), 3));
		Object.defineProperty(Array.prototype, 2, { set: function (v) { out.push("setter" + v); }, configurable: true });
		out.push(fill(new Array(5), 4));
		delete Array.prototype[1]; delete Array.prototype[2];
		print(out.join("|"));`)
}

// TestJITDirtyRegisters calls, from generated code, functions whose frames
// take the registers an earlier callee left with its values (VM.spDirty):
// a var read before it is assigned and parameters without an argument
// must still be undefined, and a this that is a scope.
func TestJITDirtyRegisters(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	compareJIT(t, `
		function dirty(a, b, c, d) { var o = { a: a }, p = [b, c], q = "s" + d, r = o.a * 2 + p.length; var t1 = r + 1, t2 = t1 * 3; return t2; }
		function reads(a, b, c, d) {
			var x, y, z;
			var r = (x === undefined) + 2 * (y === undefined) + 4 * (z === undefined);
			r += 8 * (b === undefined) + 16 * (c === undefined) + 32 * (d === undefined);
			if (a % 2) x = a;
			return r + 64 * arguments.length + (x === undefined ? 0 : 1000);
		}
		function lets() { let u; const k = 1; return (u === undefined ? 1 : 0) + k; }
		function miss(a, b, c, d, e) { return "" + b + c + d + e; }
		function wide(a, b, c, d, e) { var t = a * 2, u = b + c; return a + b + c + d + e + t + u; }
		var seen = {};
		var s = 0;
		for (var i = 0; i < 2000; i++) {
			s += dirty(i, i + 1, i + 2, i + 3);
			s += reads(i) + reads(i, 1) + reads(i, 1, 2, 3) + lets();
			s += wide(i, 1, 2, 3, 4); seen[miss(i)] = 1; s += wide(1, 2, 3, 4, i); seen[miss(i, 1, 2)] = 1;
			s += String(dirty({}, [], "x", i)).length;
		}
		print(s + " " + Object.keys(seen).join("|"));`)
}

// TestJITInline calls small functions from sites that saw only them, so
// that their code is inlined (jit_inline.go): arithmetic, properties,
// globals, this, missing and extra arguments, vars, several returns; a
// site whose callee changes later (the check fails: an ordinary call);
// slow paths and exceptions in the inlined code (it makes the callee's
// frame and stops), with side effects before them done once; and inlined
// code in a function that generated code calls itself.
func TestJITInline(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		var G = 3, log = [];
		function add(a, b) { return a + b; }
		function rol(n, c) { return (n << c) | (n >>> (32 - c)); }
		function pick(a, b, c) { var x; if (a > b) return a; x = c === undefined ? -1 : c; return b + x; }
		function glob(a) { G = G + 1; return a * G; }
		function getx(o) { return o.x + o.y; }
		function setx(o, v) { o.n = (o.n | 0) + 1; o.x = v; return o.x; }
		function self() { return this === undefined ? 0 : this.v; }
		function thrower(o) { return o.x.y; }
		function uv(a) { var u; if (a > 5) u = a; return u === undefined ? 1 : u; }
		function dirt(a, b, c, d) { var p = a * 3, q = b + c + d; return p + q; }
		var obj = { v: 7, m: self };
		function work(i) {
			var s = 0, o = { x: i, y: 2 };
			s += add(i, 1) + rol(i, 3) + pick(i, 5) + pick(1, i, 2) + glob(1) + getx(o);
			s += setx(o, i & 7) + obj.m() + add(1, 2, 3);
			s += dirt(i, 1, 2, 3) + uv(i & 7) + dirt(1, 2, 3, i) + uv(1) + pick(i, i + 1);
			return s;
		}
		var total = 0;
		for (var i = 0; i < 3000; i++) total = (total + work(i)) % 1000000007;
		log.push(total, G);
		// Slow paths in the inlined code: strings, objects, a getter.
		var o2 = { x: 1, y: 2 };
		for (var i = 0; i < 300; i++) {
			var a = i % 50 == 0 ? "s" : i, b = i % 70 == 0 ? {} : 1;
			total = String(work(i)).length + String(add(a, b)).length + total % 1000;
			if (i == 150) Object.defineProperty(o2, "x", { get: function () { log.push("get"); return 5; } });
			total += getx(o2) + setx(o2, i).toString().length;
		}
		log.push(total, o2.n);
		// Exceptions out of inlined code.
		var caught = 0;
		for (var i = 0; i < 500; i++) {
			try { thrower(i % 100 == 0 ? {} : { x: { y: i } }); } catch (e) { caught++; }
		}
		log.push(caught);
		// The callee changes: the check fails.
		for (var i = 0; i < 500; i++) { if (i == 250) add = function (a, b) { return a - b; }; total += add(i, 1); }
		log.push(total % 1000003);
		// Inlined code in a function that generated code calls.
		function outer(i) { return add(i, 2) + rol(i, 1); }
		var t2 = 0;
		for (var i = 0; i < 3000; i++) t2 += outer(i);
		log.push(t2);
		print(log.join(","));`
	saved := jitThreshold
	jitThreshold = 50
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}

// TestJITInlineNested inlines functions that call small functions, which
// are inlined into them (md5's md5_cmn calling safe_add and bit_rol): the
// inner callee's stops make both frames; a check of the inner callee that
// fails takes the slow path, where Go makes the call; exceptions from the
// inner callee.
func TestJITInlineNested(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function safe_add(x, y) { var lsw = (x & 0xFFFF) + (y & 0xFFFF); var msw = (x >> 16) + (y >> 16) + (lsw >> 16); return (msw << 16) | (lsw & 0xFFFF); }
		function bit_rol(num, cnt) { return (num << cnt) | (num >>> (32 - cnt)); }
		function cmn(q, a, b, x, s, t) { return safe_add(bit_rol(safe_add(safe_add(a, q), safe_add(x, t)), s), b); }
		function ff(a, b, c, d, x, s, t) { return cmn((b & c) | ((~b) & d), a, b, x, s, t); }
		var o = { n: 0, v: 3 };
		function inner(p, w) { p.n = p.n + 1; return p.v + w; }
		function outer(p, w) { var r = inner(p, w); return r + inner(p, 1); }
		function deep(p) { return p.a.b; }
		function mid(p) { return deep(p) + 1; }
		var log = [], h = 0x67452301;
		for (var i = 0; i < 4000; i++) h = ff(h, i, i * 7, i ^ 0x55, i * 3, 7, -680876936);
		log.push(h);
		// Stops in the inner callee: strings, after a side effect.
		function stops() { var s = 0; for (var i = 0; i < 2000; i++) s += String(outer(o, i % 97 == 0 ? "x" : i)).length; return s; }
		var s = stops();
		log.push(s, o.n);
		// Exceptions from the inner callee.
		var caught = 0;
		for (var i = 0; i < 2000; i++) { try { s += mid(i % 50 == 0 ? {} : { a: { b: i } }); } catch (e) { caught++; } }
		log.push(caught, s);
		// The inner callee changes: its check fails.
		for (var i = 0; i < 2000; i++) { if (i == 1000) bit_rol = function (n, c) { return n + c; }; h = ff(h, i, i, i, i, 5, 1); }
		log.push(h);
		print(log.join(","));`
	saved := jitThreshold
	jitThreshold = 50
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}

// TestJITInlineThunkCall inlines callees that call a builtin with a thunk
// (charCodeAt, push), at both levels: cases the thunk leaves to the
// builtin (out of range, several values), another function in the
// builtin's place, and an exception from a getter-less callee stop the
// inlined code.
func TestJITInlineThunkCall(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		var src = "let x_1 = y + 42; // done";
		function code(s, i) { return s.charCodeAt(i); }
		function isIdent(s, i) { var c = code(s, i); return c >= 97 && c <= 122 || c == 95; }
		function add(a, v) { a.push(v); return a.length; }
		function count(s, extra) {
			var n = 0;
			for (var i = 0; i < s.length + extra; i++) { if (isIdent(s, i)) n++; var c = code(s, i); n += c !== c ? 1000 : c & 3; }
			return n;
		}
		var log = [], n = 0;
		for (var k = 0; k < 400; k++) n += count(src, 0);
		log.push(n);
		// Out of range now and then: the builtin returns NaN.
		for (var k = 0; k < 400; k++) n += count(src, k % 100 == 0 ? 2 : 0);
		log.push(n);
		// push: one value in the thunk; several in the builtin.
		function fill(m) { var arr = [], len = 0; for (var k = 0; k < m; k++) len += add(arr, k); return len + arr[m - 1]; }
		for (var k = 0; k < 30; k++) n += fill(200);
		log.push(n);
		// Another function in the builtin's place, then a throwing one.
		var orig = String.prototype.charCodeAt;
		String.prototype.charCodeAt = function (i) { return i * 2; };
		for (var k = 0; k < 200; k++) n += count(src, 0);
		log.push(n);
		String.prototype.charCodeAt = function (i) { if (i == 7) throw new Error("t" + i); return orig.call(this, i); };
		var caught = 0;
		for (var k = 0; k < 200; k++) { try { n += count(src, 0); } catch (e) { caught++; } }
		String.prototype.charCodeAt = orig;
		log.push(caught, n);
		print(log.join(","));`
	saved := jitThreshold
	jitThreshold = 50
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}

// TestJITInlineFacts runs inlined arithmetic callees with type facts from
// the caller's arguments (ints, then doubles, strings and objects in their
// place) and branches in the callee (facts per block), and uses the
// caller's facts after the inlined call.
func TestJITInlineFacts(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function safe_add(x, y) { var lsw = (x & 0xFFFF) + (y & 0xFFFF); var msw = (x >> 16) + (y >> 16) + (lsw >> 16); return (msw << 16) | (lsw & 0xFFFF); }
		function mix(a, b) { var r = a * 2; if (b > 3) r = r + b; else r = r - b; return r + a; }
		function run(n, f) {
			var h = 0x67452301, s = 0;
			for (var i = 0; i < n; i++) {
				var x = i | 0, y = f(i);
				h = safe_add(h, x);
				s = mix(x, y) + x * 3;
				h = h ^ s;
			}
			return h + "/" + s;
		}
		var log = [];
		log.push(run(3000, function (i) { return i & 7; }));
		log.push(run(500, function (i) { return i % 50 == 0 ? 2.5 : i & 7; }));
		log.push(run(500, function (i) { return i % 70 == 0 ? "7" : i & 7; }));
		log.push(run(500, function (i) { return i % 90 == 0 ? { valueOf: function () { return 4; } } : i & 7; }));
		var big = 0;
		for (var i = 0; i < 3000; i++) big = safe_add(big, i % 100 == 0 ? 2147483647.5 : i);
		log.push(big);
		// A fact from one branch of the callee does not hold after it.
		function br(c, v) { var r = 0; if (c) r = v - 1; return v + r; }
		function brs(n, str) { var out = ""; for (var i = 0; i < n; i++) { var c = i & 1, v = str && i % 30 == 0 ? "5" : i; out += br(c, v) + ";"; } return out.length + out.slice(-40); }
		log.push(brs(3000, false), brs(300, true));
		print(log.join(","));`
	saved := jitThreshold
	jitThreshold = 50
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}

// TestJITInlineTailCall inlines callees whose result is a call (strict
// code makes it a tail call): of another small function, inlined in turn,
// or of a builtin with a thunk; the tail-called function changes, throws,
// or leaves a case to the builtin, which stops the inlined code and has
// the interpreter make the tail call.
func TestJITInlineTailCall(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `"use strict";
		function M(n) { this.m = n; }
		M.prototype.get = function (i, j) { return this.m * i + j; };
		function T(mm) { this.matrix = mm; }
		T.prototype.get = function (r, c) { return this.matrix.get(c, r); };
		function code(s, i) { return s.charCodeAt(i); }
		function sl(s, i) { return s.slice(i, i + 2); }
		function run(t, n, str) {
			var s = 0;
			for (var i = 0; i < n; i++) s += t.get(i & 7, i & 3) * 3 + (code(str, i % 500 == 499 ? str.length : i % str.length) | 0) % 7 + sl(str, i % 4).charCodeAt(0);
			return s;
		}
		var a = new M(3), t = new T(a), log = [];
		log.push(run(t, 3000, "hello"));
		// Another get behind the tail call, then one that throws.
		var b = new M(5);
		b.get = function (i, j) { return i - j; };
		t.matrix = b;
		log.push(run(t, 300, "hi"));
		b.get = function (i, j) { if (j == 5) throw new Error("x" + i); return i; };
		var caught = 0;
		for (var k = 0; k < 30; k++) { try { log.push(run(t, 20, "abc")); } catch (e) { caught++; } }
		log.push(caught);
		print(log.join(","));`
	saved := jitThreshold
	jitThreshold = 50
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}

// TestJITInlinePoly inlines both callees of sites that saw two (a matrix
// and its transpose, as in ML), at both levels; then a third callee, and
// one of the two replaced, take the call.
func TestJITInlinePoly(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `"use strict";
		function M(n) { this.m = n; }
		M.prototype.get = function (i, j) { return this.m * i + j; };
		function T(mm) { this.matrix = mm; }
		T.prototype.get = function (r, c) { return this.matrix.get(c, r); };
		function sum(x, n) { var s = 0; for (var i = 0; i < n; i++) s += x.get(i & 7, i & 3); return s; }
		function alt(a, b, n) { var s = 0; for (var i = 0; i < n; i++) s = s * 3 + (i & 1 ? a : b).get(i & 7, i & 3) & 0xfffff; return s; }
		function both(a, b, n) { return alt(a, b, n) * 1000 + sum(b, n); }
		function at(x, i) { return x.get(i, 1); }
		function outer(a, b, n) { var s = 0; for (var i = 0; i < n; i++) s += at(i & 1 ? a : b, i & 7); return s; }
		var a = new M(3), t = new T(new M(5)), log = [];
		for (var k = 0; k < 20; k++) log.push(both(a, t, 200), outer(a, t, 200));
		// A third kind of receiver.
		var u = { get: function (i, j) { return i * j - 1; } };
		log.push(sum(u, 300), outer(u, t, 300), outer(a, u, 300));
		// One of the two replaced.
		M.prototype.get = function (i, j) { return i - j; };
		log.push(both(a, t, 300), outer(a, t, 300));
		print(log.join(","));`
	saved := jitThreshold
	jitThreshold = 50
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}

// TestJITOptLoops runs loops compiled a second time with numbers in
// registers (jit_opt.go): a check failing in the middle of an iteration
// (the registers stored, the baseline code going on), exits from the
// loop, generic instructions (array reads, calls) between kept numbers,
// an entry whose registers are not numbers, and a loop that keeps giving
// up until it is not entered again.
func TestJITOptLoops(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function sumsub(a, n) {
			var s = 0, p = 1;
			for (var i = 0; i < n; i++) { p = p * 1.0001; s = s - a[i % a.length] * p; }
			return s + "/" + p;
		}
		function mods(a, n) {
			var s = 0, q = 3;
			for (var i = 0; i < n; i++) { q = q + 1; s = s + (a[i & 7] % q); }
			return s + "/" + q;
		}
		function brk(n, lim) {
			var s = 0, j = 0;
			for (var i = 0; i < n; i++) { s = s + i * 2; j = j + 1; if (s > lim) break; }
			return s + "/" + j + "/" + i;
		}
		function sq(x) { return x * x; }
		function calls(n) {
			var s = 0, t = 0.5;
			for (var i = 0; i < n; i++) { t = t + 0.25; s = s + sq(t) - Math.sqrt(t); }
			return s;
		}
		function mov(n) {
			var a = 1, b = 2, c;
			for (var i = 0; i < n; i++) { c = a; a = b; b = c + b * 0.5; }
			return a + b;
		}
		var nums = [1, 2, 3, 4, 5, 6, 7, 8], mixed = [1, 2, "3", 4, 5, 6, 7, 8], odd = [1, 2.5, 3, 4, 5, 6, 7, 8];
		var log = [];
		for (var k = 0; k < 3; k++) {
			log.push(sumsub(nums, 3000), sumsub(mixed, 3000), mods(nums, 3000), mods(odd, 3000));
			log.push(brk(5000, 1e5), brk(5000, 1e9), calls(3000), mov(2000));
		}
		// Elements taken for numbers that are not: the baseline code goes on
		// in the middle of its block, with its number cache filled again.
		function copy(dst, src, off, n) { for (var i = 0; i < n; i++) dst[off + i] = src[i]; return dst; }
		var fs = [function () { return 1; }, function () { return 2; }, 3, "x", {}], nums = [1, 2, 3, 4, 5, 6, 7, 8], r = 0;
		for (var k = 0; k < 300; k++) { var d = copy([], fs, 0, fs.length); r += d[0]() + d[1]() + d[2]; }
		for (var k = 0; k < 300; k++) var d2 = copy(new Array(10), nums, 2, nums.length);
		log.push(r, typeof d[0], typeof d[3], d.length, d2.join(":"));
		// A loop the optimized version cannot compile (a throw).
		function thr(n) { var s = 0; for (var i = 0; i < n; i++) { s = s + i; if (s < 0) throw s; } return s; }
		log.push(thr(2000), thr(3000));
		// Not numbers at the entry: the baseline loop.
		function ent(s, n) { for (var i = 0; i < n; i++) s = s + 1; return s; }
		log.push(ent(0, 1000), ent("x", 1000), ent(0.5, 1000), ent(undefined, 1000));
		print(log.join(","));`
	saved, savedOpt, savedHot := jitThreshold, jitOpt, optHotIterations
	jitThreshold, jitOpt, optHotIterations = 50, true, 0
	defer func() { jitThreshold, jitOpt, optHotIterations = saved, savedOpt, savedHot }()
	compareJIT(t, src)
}

// TestJITOptTierUp runs loops that become hot (optHotIterations) while
// their code runs: the code is compiled again with their optimized
// versions, whose results must not change.
func TestJITOptTierUp(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function f(n, x) { var s = 0; for (var i = 0; i < n; i++) s = s + i * x - (s > 1e9 ? 1e9 : 0); return s; }
		function g(a) { var s = 0; for (var i = 0; i < a.length; i++) s = s + a[i] * 0.5; return s; }
		var a = [], log = [];
		for (var i = 0; i < 50; i++) a.push(i * 1.5);
		for (var k = 0; k < 40; k++) log.push(f(30, k), g(a));
		log.push(f(100000, 3));
		print(log.join(","));`
	saved, savedOpt, savedHot := jitThreshold, jitOpt, optHotIterations
	jitThreshold, jitOpt, optHotIterations = 20, true, 100
	defer func() { jitThreshold, jitOpt, optHotIterations = saved, savedOpt, savedHot }()
	defer noGC()()
	before := optCompiled.Load()
	compareJIT(t, src)
	if optCompiled.Load() == before {
		t.Fatal("no optimized loop was compiled")
	}
}

// TestJITOptEntryFails runs a hot loop whose kept accumulator is not a
// number when the loop is entered (undefined, then a string): its entry
// fails until the optimized version is disabled, and the code is compiled
// again without keeping that register.
func TestJITOptEntryFails(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function f(n) { var acc, s = 0; for (var i = 0; i < n; i++) { acc = (acc | 0) + i; s = s + acc * 0.5; } return s; }
		function g(n, start) { var acc = start; for (var i = 0; i < n; i++) acc = acc + i; return acc; }
		var log = [];
		for (var k = 0; k < 40; k++) log.push(f(200 + k));
		for (var k = 0; k < 40; k++) log.push(g(100, k % 3 ? "s" : 0));
		print(log.join(","));`
	saved, savedOpt, savedHot := jitThreshold, jitOpt, optHotIterations
	jitThreshold, jitOpt, optHotIterations = 20, true, 100
	defer func() { jitThreshold, jitOpt, optHotIterations = saved, savedOpt, savedHot }()
	defer noGC()()
	before := optDisabled.Load()
	compareJIT(t, src)
	if optDisabled.Load() == before {
		t.Fatal("no optimized loop was disabled and compiled again")
	}
}

// TestJITOptLoopCalls runs optimized loops with calls: of an inlined
// callee that stops now and then (another kind of argument), of a builtin
// whose thunk leaves a case to Go, and of generated code that has an
// optimized loop of its own (which uses the same registers).
func TestJITOptLoopCalls(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	defer func(saved [bytecode.NumberOfBytecodeIDs]bool) { optCallOps = saved }(optCallOps)
	optCallOps[bytecode.OpCallID], optCallOps[bytecode.OpCallIgnoreResultID] = true, true
	src := `
		function add(x, y) { return x + y; }
		function inner(n) { var s = 0.5; for (var j = 0; j < n; j++) s = s * 1.01 + j; return s; }
		function f(n, k) {
			var s = 0, t = 1.5, u = 0;
			for (var i = 0; i < n; i++) {
				t = t * 1.0001;
				s = add(s, i % 97 == k ? "1" : t) * 1;
				u = u + Math.sqrt(i) + "abc".charCodeAt(i % 4) | 0;
				if (i % 50 == 0) u = u + inner(30);
			}
			return s + "/" + t + "/" + u;
		}
		var log = [];
		for (var k = 0; k < 8; k++) log.push(f(3000, k));
		print(log.join(","));`
	saved, savedOpt, savedHot := jitThreshold, jitOpt, optHotIterations
	jitThreshold, jitOpt, optHotIterations = 50, true, 0
	defer func() { jitThreshold, jitOpt, optHotIterations = saved, savedOpt, savedHot }()
	compareJIT(t, src)
}

// TestJITFusedGlobalGet reads global functions and variables through
// resolve_scope and get_from_scope compiled as one (fusedGlobalGet), then
// changes what the constants it checks stand for: the variable is
// assigned, the global object gets more properties, a global let comes to
// shadow a name, and eval injects a var.
func TestJITFusedGlobalGet(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		globalThis.early1 = 1; globalThis.early2 = 2;
		var k = 3;
		function sq(x) { return x * x + k; }
		function run(n) { var s = 0; for (var i = 0; i < n; i++) s = s + sq(i % 10) + k; return s; }
		var log = [];
		for (var j = 0; j < 30; j++) log.push(run(100));
		k = 4; log.push(run(100));
		sq = function (x) { return x + 1; }; log.push(run(100));
		for (var j = 0; j < 20; j++) globalThis["extra" + j] = j;
		log.push(run(100));
		// Properties before k and sq go: their slots move.
		delete globalThis.early1; delete globalThis.early2; log.push(run(100));
		(0, eval)("var injected = 1; var k = 7;"); log.push(run(100));
		// A var injected into an enclosing function's scope by eval.
		function outer(inject) {
			function inner(n) { var s = 0; for (var i = 0; i < n; i++) s = s + sq(i) + k; return s; }
			var r = [];
			for (var j = 0; j < 30; j++) r.push(inner(50));
			if (inject) eval("var k = 1000; var sq = function (x) { return -x; }");
			r.push(inner(50));
			return r.join(":");
		}
		log.push(outer(true), run(100));
		// A global data property that becomes an accessor (same slot).
		globalThis.kk = 5;
		function rk(n) { var s = 0; for (var i = 0; i < n; i++) s = s + kk; return s; }
		for (var j = 0; j < 30; j++) log.push(rk(40));
		Object.defineProperty(globalThis, "kk", { get: function () { return 11; }, configurable: true });
		log.push(rk(40));
		print(log.join(","));`
	saved := jitThreshold
	jitThreshold = 20
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}

// TestJITOptTierUpCalled tiers up a loop in a function that only
// generated code calls (directly), so that it never returns to Go by
// itself: the loop's counter makes it take a fuel exit, where its code is
// compiled again with the optimized version.
func TestJITOptTierUpCalled(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function inner(a, x) { var s = 0; for (var i = 0; i < a.length; i++) s = s + a[i] * x; return s; }
		function outer(a, n) { var t = 0; for (var k = 0; k < n; k++) t = t + inner(a, k); return t; }
		var a = [];
		for (var i = 0; i < 8; i++) a.push(i + 0.5);
		print(outer(a, 3000));`
	saved, savedOpt, savedHot := jitThreshold, jitOpt, optHotIterations
	jitThreshold, jitOpt, optHotIterations = 20, true, 500
	defer func() { jitThreshold, jitOpt, optHotIterations = saved, savedOpt, savedHot }()
	defer noGC()()
	before := optCompiled.Load()
	compareJIT(t, src)
	if optCompiled.Load() == before {
		t.Fatal("no optimized loop was compiled")
	}
}

// TestJITOptLoopProps runs optimized loops that read and write numeric
// properties (kept in registers): a value that becomes a string, an
// object of another shape, a setter, and a property read through a
// getter on the prototype.
func TestJITOptLoopProps(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function P(x, y) { this.x = x; this.y = y; this.vx = 0; }
		function step(ps, n, dt) {
			var e = 0;
			for (var k = 0; k < n; k++) {
				var p = ps[k % ps.length];
				var d = p.x - p.y;
				p.vx = p.vx + d * dt;
				p.x = p.x + p.vx * dt;
				e = e + d * d;
			}
			return e;
		}
		var ps = [new P(1, 2), new P(3, 1), new P(0.5, 4)], log = [];
		for (var j = 0; j < 20; j++) log.push(step(ps, 300, 0.01));
		ps[1].y = "2"; log.push(step(ps, 300, 0.01));
		ps.push({ y: 1, x: 2, vx: 0 }); log.push(step(ps, 300, 0.01));
		var q = { y: 0, vx: 0, _x: 1 };
		Object.defineProperty(q, "x", { get: function () { return this._x; }, set: function (v) { this._x = v / 2; } });
		ps.push(q); log.push(step(ps, 300, 0.01), q._x);
		log.push(ps.map(function (p) { return p.x + "/" + p.vx; }).join(";"));
		print(log.join(","));`
	saved, savedOpt, savedHot := jitThreshold, jitOpt, optHotIterations
	jitThreshold, jitOpt, optHotIterations = 50, true, 0
	defer func() { jitThreshold, jitOpt, optHotIterations = saved, savedOpt, savedHot }()
	compareJIT(t, src)
}

// TestJITOptManyRegs runs an optimized loop that keeps many numbers (on
// arm64 in F8-F31) among typed array stores, whose code uses the
// assembler's scratch register: a kept number must never be in it (the
// arm64 fuzzing found one in F15).
func TestJITOptManyRegs(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function f() {
			var a = 1, b = 2, c = 3, d = 4, e = 5, g = 6, h = 7, k = 8, l = 9, L = 0;
			var u32 = new Uint32Array(16), f32 = new Float32Array(16), arr = [1, 2.5, 3, , 5, -0, 7, 8];
			for (var i = 0; i < 5000; i++) {
				u32[i % 16] = arr[i % arr.length]; f32[i % 16] = arr[i % arr.length];
				b = ~d + -a; L += 4294967295; c = b >>> NaN;
				e = e * 1.0001 + g; g = g - h * 0.5; h = h + k; k = k * 0.999 + l; l = l + 1;
			}
			return [a, b, c, d, e, g, h, k, l, L, u32[3], f32[5]].join(",");
		}
		print(f(), f(), f());`
	saved, savedOpt, savedHot := jitThreshold, jitOpt, optHotIterations
	jitThreshold, jitOpt, optHotIterations = 2, true, 0
	defer func() { jitThreshold, jitOpt, optHotIterations = saved, savedOpt, savedHot }()
	compareJIT(t, src)
}

// TestJITBitopsUndefined runs bit operations on undefined, null and
// booleans (md5 reads past the end of its array), in compiled and inlined
// code: they are 0 (true 1) without leaving the machine code.
func TestJITBitopsUndefined(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function lo(x) { return x & 0xffff; }
		function mix(a, b) { return (a | b) ^ (a >>> 3) ^ ~b ^ (b << 2) ^ (a >> 1); }
		function run(arr, n) {
			var s = 0;
			for (var i = 0; i < n; i++) {
				var v = arr[i];
				s = (s + lo(v) + mix(v, arr[i + 1])) | 0;
				s ^= (v & 7) | (null | 0) | (true & v) | (false ^ 3);
			}
			return s;
		}
		var a = [1, 2.5, -3, true, false, null, undefined, 70000, -0];
		var out = [];
		for (var k = 0; k < 300; k++) out.push(run(a, a.length + 3));
		out.push(run(["7", {}, 1], 4));
		print(out.join(","));`
	saved := jitThreshold
	jitThreshold = 20
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}

// TestJITCallVarargsForwarded runs f.apply(this, arguments) as a direct
// call with the caller's own arguments: varying argument counts (fewer
// and more than the callee's parameters), several callees and closures at
// one site, builtins, callees that read arguments or throw, recursion,
// and this values that are not objects.
func TestJITCallVarargsForwarded(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		"use strict";
		function Class() { return function() { return this.initialize.apply(this, arguments); }; }
		var P = Class(); P.prototype.initialize = function(x, y) { this.x = x; this.y = y; return x + (y === undefined ? 0 : y); };
		var Q = Class(); Q.prototype.initialize = function() { this.n = arguments.length; var s = 0; for (var i = 0; i < arguments.length; i++) s += arguments[i]; return s; };
		function fwd(f) { return function() { return f.apply(this, arguments); }; }
		var add3 = fwd(function(a, b, c) { return [this === undefined, a, b, c].join("/"); });
		var maxf = fwd(Math.max);
		var thrower = fwd(function(a) { if (a > 5) throw new Error("t" + a); return a; });
		function rec(n) { return n <= 0 ? 0 : 1 + recf.apply(this, [n - 1]); }
		var recf = fwd(function(n) { return n <= 0 ? 0 : 1 + recf(n - 1); });
		var out = [], s = 0;
		for (var i = 0; i < 2000; i++) {
			var p = new P(i, i & 3), q = new Q(i, 1, 2, 3);
			s += p.x + p.y + q.n + (i & 1 ? new P(i).x : 0);
			if (i % 100 == 0) out.push(add3(i), add3(i, 2), add3(i, 2, 3, 4), add3(), maxf(i, 7, 3));
			try { s += thrower(i % 8); } catch (e) { s += e.message.length; }
			if (i % 500 == 0) out.push(recf(30), fwd(function() { return this; }).call(5, 1));
		}
		var few = (function(f) { return function() { return f.apply(this, arguments); }; })(function(a, b, c) { return "" + a + b + c; });
		var many = (function(f) { return function() { return f.apply(this, arguments); }; })(function(a) { return a + arguments[3]; });
		var t1 = "";
		for (var i = 0; i < 3000; i++) { t1 = few(i) + few(i, i) + few(i, 1, 2) + many(i, 1, 2, i) + t1.length; }
		out.push(s, t1);
		print(out.join(","));`
	saved := jitThreshold
	jitThreshold = 20
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}

// TestJITNewArrayWithSize runs new Array(n) in generated code: lengths of
// at most four from the array pool (holes, which reads, in, for-in, join
// and push see), larger, negative, fractional and non-number lengths
// through the slow path.
func TestJITNewArrayWithSize(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function mk(n) { return new Array(n); }
		var out = [], s = 0;
		var lens = [0, 1, 2, 3, 4, 5, 17, -0, "3", 2.5, -1, true];
		for (var i = 0; i < 3000; i++) {
			var n = lens[i % lens.length], a;
			try { a = mk(n); } catch (e) { s += 1000; continue; }
			a[1] = i;
			s += a.length + (0 in a ? 1 : 0) + (1 in a ? 2 : 0) + Object.keys(a).length + (a[0] === undefined ? 4 : 0);
			a.push(i);
			if (i % 997 == 0) out.push(a.length, a.join("-"));
		}
		out.push(s);
		print(out.join(","));`
	saved := jitThreshold
	jitThreshold = 20
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}

// TestJITCallVarargsConstantThis forwards arguments with a constant this
// (f.apply(null, arguments)): the direct call must copy the constant, not
// read a register at its index (arm64 faulted; x86 read another
// register). The callees print this, strict and sloppy.
func TestJITCallVarargsConstantThis(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function g() { "use strict"; return String(this) + ":" + Array.prototype.join.call(arguments, ","); }
		function h() { return typeof this + ":" + arguments.length; }
		function viaNull() { return g.apply(null, arguments); }
		function viaNum() { return g.apply(5, arguments); }
		function viaStr() { return g.apply("s", arguments); }
		function viaUndef() { return g.apply(undefined, arguments); }
		function sloppyNull() { return h.apply(null, arguments); }
		function sloppyNum() { return h.apply(7, arguments); }
		var out = [];
		for (var i = 0; i < 3000; i++) {
			var a = viaNull(i, 1), b = viaNum(i), c = viaStr(), d = viaUndef(1, 2, 3), e = sloppyNull(i), f = sloppyNum();
			if (i % 1000 == 999) out.push(a, b, c, d, e, f);
		}
		print(out.join(" "));`
	saved := jitThreshold
	jitThreshold = 1
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}

// TestJITDataViewByteLength reads DataView's byteLength in generated code:
// fixed views, length-tracking ones on resizable buffers, views out of
// bounds after a shrink, detached buffers (TypeError), subclasses, other
// receivers of the getter and a replaced getter.
func TestJITDataViewByteLength(t *testing.T) {
	if !jitEnabled {
		t.Skip("JIT disabled")
	}
	src := `
		function get(v) { return v.byteLength; }
		function len(v) { try { return get(v); } catch (e) { return e.constructor.name; } }
		class Sub extends DataView {}
		var rab = new ArrayBuffer(16, { maxByteLength: 64 });
		var empty = new ArrayBuffer(8);
		var views = [new DataView(new ArrayBuffer(24)), new DataView(new ArrayBuffer(32), 8, 12), new Sub(new ArrayBuffer(8)),
			new DataView(rab), new DataView(rab, 4, 8), new DataView(empty, 0, 0)];
		var out = [], s = 0;
		for (var i = 0; i < 3000; i++) {
			var v = views[i % views.length];
			var r = len(v);
			s += typeof r == "number" ? r : 1000;
			if (i == 1000) rab.resize(48);
			if (i == 1500) rab.resize(8);
			if (i == 2000) { var b = views[0].buffer; views[0] = new DataView(b); b.transfer(); empty.transfer(); }
			if (i % 500 == 499) out.push(r);
		}
		out.push(s, len(Object.create(DataView.prototype)));
		Object.defineProperty(DataView.prototype, "byteLength", { get() { return -7; } });
		for (var i = 0; i < 100; i++) s += len(views[1]);
		out.push(s);
		print(out.join(","));`
	saved := jitThreshold
	jitThreshold = 20
	defer func() { jitThreshold = saved }()
	compareJIT(t, src)
}
