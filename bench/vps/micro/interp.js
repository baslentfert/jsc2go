// Interpreter-bound work the JIT tier does not compile (objects, strings,
// closures): times should barely change between JSC2GO_JIT=1 and =0.
function objects(n) { var a = []; for (var i = 0; i < n; i++) a.push({ x: i, y: [i] }); var s = 0; for (var o of a) s += o.x + o.y[0]; return s; }
function strings(n) { var s = ""; for (var i = 0; i < n; i++) s += String.fromCharCode(65 + i % 26); return s.length + s.match(/ABC/g).length; }
function dict(n) { var d = {}; for (var r = 0; r < 20; r++) { d = {}; for (var i = 0; i < n; i++) d["w" + i] = i; } return Object.keys(d).length; }
function closures(n) { var fs = []; for (var i = 0; i < n; i++) fs.push(function (x) { return x + i; }); var s = 0; for (var f of fs) s += f(1); return s; }
function json(n) { var o = []; for (var i = 0; i < n; i++) o.push({ id: i, name: "n" + i, tags: ["a", "b"] }); return JSON.parse(JSON.stringify(o)).length; }
for (var [name, f, n] of [["objects", objects, 300000], ["strings", strings, 200000], ["dict", dict, 20000],
  ["closures", closures, 300000], ["json", json, 100000]]) {
  var t = Date.now(), r = f(n);
  print(name, Date.now() - t, "ms", r);
}
