function fib(n) { return n < 2 ? n : fib(n - 1) + fib(n - 2); }
var t = Date.now();
fib(27);
print("fib(27)", Date.now() - t, "ms");

t = Date.now();
var a = [];
for (var i = 0; i < 300000; i++) a.push({ x: i, y: i * 2 });
var s = 0;
for (var i = 0; i < a.length; i++) s += a[i].x + a[i].y;
print("objects", Date.now() - t, "ms");

t = Date.now();
var str = "";
for (var i = 0; i < 100000; i++) str += String.fromCharCode(65 + i % 26);
var m = str.match(/ABC/g).length;
print("strings", Date.now() - t, "ms", m);

t = Date.now();
var o = {};
for (var i = 0; i < 100000; i++) o["k" + (i % 1000)] = i;
print("props", Date.now() - t, "ms");

t = Date.now();
var arr = [];
for (var i = 0; i < 200000; i++) arr.push(i % 1000);
arr.sort((x, y) => x - y);
var total = arr.map(x => x * 2).filter(x => x % 3 == 0).reduce((p, c) => p + c, 0);
print("array methods", Date.now() - t, "ms", total);
