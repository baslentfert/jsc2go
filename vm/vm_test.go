package vm

import (
	"testing"
	"unsafe"
)

// Regression tests for behavior test262 does not pin down.
func TestScripts(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"sparse switch_imm (list jump table)", `
			function a(ch) { switch (ch) { case 32: case 160: return "space"; case 13: case 10: return "nl"; default: return "other"; } }
			[a(32), a(160), a(10), a(13), a(5)].join()`, "space,space,nl,nl,other"},
		{"labeled loop with switch break (acorn's skipSpace)", `
			function skip(input) {
				var pos = 0;
				loop: while (pos < input.length) {
					var ch = input.charCodeAt(pos);
					switch (ch) {
					case 32: case 160: ++pos; break
					case 13: if (input.charCodeAt(pos + 1) === 10) { ++pos }
					case 10: case 8232: case 8233: ++pos; break
					default: if (ch > 8 && ch < 14) { ++pos } else { break loop }
					}
				}
				return pos;
			}
			[skip("  \n x"), skip("\n  x"), skip("\r\n\t y")].join()`, "4,3,4"},
		{"sparse switch_char", `
			function c(s) { switch (s) { case "a": return 1; case "z": return 2; case "é": return 3; default: return 0 } }
			[c("a"), c("z"), c("é"), c("q")].join()`, "1,2,3,0"},
		{"closure variable cache with eval injection", `
			function outer() {
				var x = "outer";
				function mid(code) { eval(code); return function() { return x; }; }
				var f1 = mid(""), r1 = f1();
				var f2 = mid("var x = 'injected'");
				return [r1, f1(), f2(), f1()].join();
			}
			outer()`, "outer,outer,injected,outer"},
		{"closure variable cache with with scope", `
			function o() {
				var x = 1;
				function m(obj) { with (obj) { return (function() { return x; })(); } }
				return [m({}), m({x: 2}), m({})].join();
			}
			o()`, "1,2,1"},
		{"put_by_id cache shadowing a prototype's field", `
			function V(x) { this.x = x; }
			V.prototype = { x: 0, y: 1 };
			var out = [];
			for (var i = 0; i < 4; i++) out.push(new V(i).x);
			out.push(V.prototype.x, Object.keys(new V(9)).join("|"));
			// The prototype's property becomes read-only: the cached store must
			// not add an own property any more (sloppy: silently ignored).
			Object.defineProperty(V.prototype, "x", { writable: false });
			out.push(new V(5).x, new V(6).hasOwnProperty("x"));
			// A setter on the prototype is called instead of adding.
			Object.defineProperty(V.prototype, "x", { set(v) { out.push("set" + v); }, configurable: true });
			new V(7);
			out.join()`, "0,1,2,3,0,x,0,false,set7"},
		{"closure variable cache writes", `
			function counter() { var n = 0; return function() { n = n + 1; return n; }; }
			var c1 = counter(), c2 = counter();
			c1(); c1(); c2();
			[c1(), c2()].join()`, "3,2"},
		{"global lexical slot cache keeps TDZ and const checks", `
			function readL() { try { return L; } catch (e) { return e.name; } }
			function writeL(v) { try { L = v; return L; } catch (e) { return e.name; } }
			function writeC() { try { C = 2; return C; } catch (e) { return e.name; } }
			var out = [readL(), writeL(1)];
			let L = 5;
			const C = 1;
			for (var i = 0; i < 3; i++) out.push(readL(), writeL(i), writeC());
			out.join()`, "ReferenceError,ReferenceError,5,0,TypeError,0,1,TypeError,1,2,TypeError"},
		{"fused global read", `
			globalThis.g = 1;
			function readG() { return g; }
			var out = [];
			for (var i = 0; i < 3; i++) { out.push(readG()); g = i + 10; }
			delete globalThis.g;
			try { readG(); } catch (e) { out.push(e.name); }
			out.join()`, "1,10,11,ReferenceError"},
		{"NaN payloads that collide with value tags", `
			var out = [];
			var b = new Uint8Array(8), f = new Float64Array(b.buffer), f32 = new Float32Array(b.buffer);
			for (var tag = 0; tag < 16; tag++) {
				b.fill(0); b[0] = tag; b[6] = 0xfc; b[7] = 0xff;
				var x = f[0];
				out.push(typeof x === "number" && x !== x);
			}
			b.fill(0); b[2] = 0xe0; b[3] = 0xff; // float32 NaN widening to 0xfffc000000000000
			out.push(Number.isNaN(f32[0]));
			new DataView(b.buffer).setUint32(0, 0xfffc0000); out.push(Number.isNaN(new DataView(b.buffer).getFloat64(0)));
			out.every(Boolean) + "," + out.length`, "true,18"},
		{"int32 arithmetic fast paths", `
			[1 << 31, -1 >>> 0, 0x80000000 | 0, 4294967296.5 | 0, -2147483649 >> 0, 2 ** 53 + 2 | 0,
			 1e300 | 0, NaN | 0, 5 & -3, 7 ^ 2, -7 >> 1, -7 >>> 28, 1 << 33, 0.1 + 0.2, 7 % -3, -0 * 1].join()`,
			"-2147483648,4294967295,-2147483648,0,2147483647,2,0,0,5,5,-4,15,2,0.30000000000000004,1,0"},
	}
	for _, tt := range tests {
		v := New()
		got, err := v.Eval(tt.src, tt.name)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if s := v.ToString(got).String(); s != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, s, tt.want)
		}
	}
}

func TestValueSize(t *testing.T) {
	if n := unsafe.Sizeof(Value{}); n != 16 {
		t.Errorf("Value is %d bytes, want 16", n)
	}
}

func TestFusedInstructions(t *testing.T) {
	// Patterns the interpreter runs as one dispatch must keep the semantics
	// of the separate instructions, including the intermediate registers.
	v := New()
	got, err := v.Eval(`
		var H = new Int32Array(8), F = new Float64Array(4), out = [];
		H[1] = -5; H[2] = 7; F[1] = 2.5;
		function f(p, a, b) {
			var x = (a + b) | 0, y = (a - b) | 0, z = H[p >> 2] | 0, w = F[p >> 2] | 0, u = H[p >> 2];
			var big = (2147483647 + b) | 0, o = ({valueOf() { return 3 }}) | 0, s = ("4" + a) | 0;
			return [x, y, z, w, u, big, o, s, H[100 >> 2] | 0, H[-4 >> 2]].join();
		}
		for (var i = 0; i < 3; i++) out.push(f(4, 1.5, 2));
		var c = function() { var k = 3; return function() { return k + 1; }; }();
		out.push(c(), c());
		out.join(";")`, "fused")
	if err != nil {
		t.Fatal(err)
	}
	want := "3,0,-5,2,-5,-2147483647,3,41,0,;3,0,-5,2,-5,-2147483647,3,41,0,;3,0,-5,2,-5,-2147483647,3,41,0,;4;4"
	if s := v.ToString(got).String(); s != want {
		t.Errorf("got  %s\nwant %s", s, want)
	}
}

func TestStringObjectToPrimitive(t *testing.T) {
	// ToPrimitive's fast path for String objects (plainStringObject) must
	// notice every change that makes the spec path give something else.
	v := New()
	got, err := v.Eval(`
		var out = [];
		function t(label) { var s = new String("ab"); out.push(label + ":" + ("x" + s) + "|" + (s + 1) + "|" + String(s) + "|" + (s == "ab") + "|" + (s < "b")); }
		for (var i = 0; i < 3; i++) t("plain");
		var s2 = new String("cd"); s2.valueOf = function() { return "own"; }; out.push("own:" + ("x" + s2));
		class Sub extends String {}; out.push("sub:" + ("x" + new Sub("ef")));
		var vo = String.prototype.valueOf;
		String.prototype.valueOf = function() { return "patched"; }; t("valueOf replaced");
		String.prototype.valueOf = vo; t("restored");
		delete String.prototype.valueOf; t("valueOf deleted");
		String.prototype.valueOf = vo; t("readded");
		var ts = String.prototype.toString; String.prototype.toString = function() { return "ts"; }; t("toString replaced"); String.prototype.toString = ts;
		Object.prototype[Symbol.toPrimitive] = function(h) { return "OP:" + h; }; t("Object.prototype toPrimitive"); delete Object.prototype[Symbol.toPrimitive]; t("removed");
		String.prototype[Symbol.toPrimitive] = function(h) { return "SP:" + h; }; t("String.prototype toPrimitive"); delete String.prototype[Symbol.toPrimitive]; t("removed2");
		Object.defineProperty(String.prototype, "valueOf", { get() { return function() { return "getter"; }; }, configurable: true }); t("accessor");
		Object.defineProperty(String.prototype, "valueOf", { value: vo, writable: true, configurable: true }); t("back");
		var p = Object.getPrototypeOf(String.prototype); Object.setPrototypeOf(String.prototype, { valueOf: function() { return 1; } }); delete String.prototype.valueOf; t("proto changed"); String.prototype.valueOf = vo; Object.setPrototypeOf(String.prototype, p); t("final");
		out.join("\n")`, "strprim")
	if err != nil {
		t.Fatal(err)
	}
	want := `plain:xab|ab1|ab|true|true
plain:xab|ab1|ab|true|true
plain:xab|ab1|ab|true|true
own:xown
sub:xef
valueOf replaced:xpatched|patched1|ab|false|false
restored:xab|ab1|ab|true|true
valueOf deleted:xab|ab1|ab|true|true
readded:xab|ab1|ab|true|true
toString replaced:xab|ab1|ts|true|true
Object.prototype toPrimitive:xOP:default|OP:default1|OP:string|false|true
removed:xab|ab1|ab|true|true
String.prototype toPrimitive:xSP:default|SP:default1|SP:string|false|true
removed2:xab|ab1|ab|true|true
accessor:xgetter|getter1|ab|false|false
back:xab|ab1|ab|true|true
proto changed:x1|2|ab|false|false
final:xab|ab1|ab|true|true`
	if s := v.ToString(got).String(); s != want {
		t.Errorf("got\n%s\nwant\n%s", s, want)
	}
}
