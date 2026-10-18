// Numeric loops for the JIT tier: run with JSC2GO_JIT=1 and =0 and compare.
// Prints "<name> <ms> ms <result>"; the results must not depend on the JIT.
function sum(n) { var s = 0; for (var i = 0; i < n; i++) s += i * 0.5; return s; }
function bits(n) { var h = 0; for (var i = 0; i < n; i++) { h = (h * 31 + i) | 0; h ^= h >>> 7; h = h << 3 | h >>> 29; } return h; }
function mod(n) { var c = 0; for (var i = 1; i < n; i++) if (i % 7 == 3) c++; return c; }
function arr(n) { var a = []; for (var i = 0; i < 1000; i++) a.push(i); var s = 0; for (var r = 0; r < n; r++) for (var i = 0; i < a.length; i++) { s += a[i]; a[i] = a[i] + 1; } return s; }
function ta(n) {
  var f = new Float64Array(1000), u = new Uint8Array(1000), k = new Int32Array(1000), s = 0;
  for (var r = 0; r < n; r++) for (var i = 0; i < 1000; i++) { f[i] += i / 3; u[i] = i + r; k[i] = (k[i] + i * r) | 0; s += f[i] + u[i] + k[i]; }
  return s;
}
function props(n) { var s = 0, o = { x: 1, y: 2 }; for (var i = 0; i < n; i++) { s += o.x + o.y; o.x = i & 7; } return s; }
function add(a, b) { var l = (a & 0xFFFF) + (b & 0xFFFF); var m = (a >> 16) + (b >> 16) + (l >> 16); return (m << 16) | (l & 0xFFFF); }
function calls(n) { var s = 0; for (var i = 0; i < n; i++) s = add(s, i); return s; }
for (var [name, f, n] of [["sum", sum, 3e6], ["bits", bits, 3e6], ["mod", mod, 3e6], ["arr", arr, 1000],
  ["typed-arrays", ta, 1000], ["props", props, 3e6], ["calls", calls, 3e6]]) {
  var t = Date.now(), r = f(n);
  print(name, Date.now() - t, "ms", r);
}
