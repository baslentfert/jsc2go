#!/bin/sh
# leaks.sh JSRUN: memory leak probes. Each runs a loop that creates and drops
# things which caches or tables could keep; the live heap after the last
# garbage collection (GODEBUG=gctrace=1) must stay small. Prints one line per
# probe, "ok" or "LEAK", and exits non-zero if any leaked.
#
# On the Mac (arm64) every probe ends below 15 MB; the limit allows for other
# machines' garbage collection timing.
JSRUN=${1:-bench/bin/jsrun}
LIMIT_MB=${LIMIT_MB:-64}
failed=0

probe() {
	name=$1
	code=$2
	# Only complete trace lines: one can be cut off where the runtime's
	# output interleaves with other output (seen on Windows).
	live=$(GODEBUG=gctrace=1 "$JSRUN" -e "$code" 2>&1 | grep -E '^gc .*->[0-9]+ MB' | tail -1 |
		sed -E 's/.*->([0-9]+) MB.*/\1/')
	case $live in
	'' | *[!0-9]*)
		echo "?    $name: no complete gc trace"
		failed=1
		return
		;;
	esac
	if [ "$live" -gt "$LIMIT_MB" ]; then
		echo "LEAK $name: ${live} MB live (limit $LIMIT_MB)"
		failed=1
	else
		echo "ok   $name: ${live} MB live"
	fi
}

probe "WeakMap with temporary keys" 'var wm=new WeakMap(); for (var i=0;i<2e6;i++) wm.set({a:i, b:[1,2,3]}, {big:new Array(10)});'
probe "WeakSet" 'var ws=new WeakSet(); for (var i=0;i<2e6;i++) ws.add({a:i});'
probe "temporary WeakMaps on one key" 'var keep={}; for (var i=0;i<2e5;i++){ var m=new WeakMap(); m.set(keep, new Array(20)); }'
probe "eval and new Function" 'function f(i){ return eval("(function(){ return " + i + " })")() } for (var i=0;i<3e5;i++) f(i) + new Function("return " + i)()'
probe "eval with template literals" 'function tag(s){return s} for (var i=0;i<2e5;i++) eval("tag`x" + i + "`");'
probe "Symbol.for" 'for (var i=0;i<1e6;i++) Symbol.for("s"+i);'
probe "objects with unique keys (shapes)" 'for (var i=0;i<3e6;i++){ var o={}; o["k"+i]=1; }'
probe "class private names" 'for (var i=0;i<3e5;i++){ class C { #x=i; static s=i; } new C(); }'
probe "JSON.parse with unique keys" 'for (var i=0;i<3e5;i++) JSON.parse("{\"k" + i + "\":[1,2,{\"x\":\"" + i + "\"}]}")'
probe "symbol keys" 'for (var i=0;i<1e6;i++){ var s=Symbol("d"+i); var o={[s]:1}; }'
probe "small substrings of big strings" 'var keep=[]; for (var i=0;i<2e4;i++){ var s="x".repeat(10000)+i; keep.push(s.slice(0,5)) }'
probe "FinalizationRegistry across jobs" 'var fr=new FinalizationRegistry(function(){}); var n=0; function step(){ for (var i=0;i<1e4;i++) fr.register({x:i, b:new Array(8)}, i); if (++n<100) return Promise.resolve().then(step); } step();'
probe "WeakRef across jobs" 'var n=0; function step(){ for (var i=0;i<1e4;i++){ var r=new WeakRef({a:i, b:new Array(8)}); r.deref(); } if (++n<100) return Promise.resolve().then(step); } step();'
probe "JIT-compiled functions (new Function)" 'for (var k=0;k<3000;k++){ var f = new Function("n", "var s=" + k + "; for (var i=0;i<n;i++) s += i*2; return s"); f(3000); }'
probe "registers left by returned calls of generated code" 'function mk(i){ var a=new Array(5e6); for (var j=0;j<a.length;j++) a[j]=j+i; return a } function deep(n,b){ return n==0 ? b.length : deep(n-1,b)+0 } function churn(n){ var s=0; for (var i=0;i<n;i++){ var o={x:i}; s+=o.x } return s } for (var r=0;r<4;r++) deep(300, mk(r)); for (var r=0;r<30;r++) churn(2e5);'

exit $failed
