#!/bin/sh
# Checks jsc2go on another machine (a Linux VPS, real x86, Windows with Git
# Bash): what the Mac, where the JIT tier was built, could not test. See
# README.md.
#
#   sh bench/vps/run.sh          correctness and JetStream2 (about 45 minutes)
#   sh bench/vps/run.sh quick    correctness only (about 15 minutes)
#   sh bench/vps/run.sh full     also the other engines, octane-zlib and
#                                float-mm.c (a few hours)
#
# Everything goes to bench/vps/out/<host>-<date>/; summary.txt lists every
# step as PASS or FAIL.
set -u
mode=${1:-standard}
cd "$(dirname "$0")/../.."
root=$(pwd)
out="bench/vps/out/$(hostname | cut -d. -f1)-$(date +%Y%m%d-%H%M)"
mkdir -p "$out"
summary="$out/summary.txt"
: >"$summary"
failures=0

# step NAME COMMAND...: runs the command with its output in $out/NAME.log.
step() {
	name=$1
	shift
	printf '%-36s ' "$name"
	start=$(date +%s)
	if "$@" >"$out/$name.log" 2>&1; then
		status=PASS
	else
		status=FAIL
		failures=$((failures + 1))
	fi
	secs=$(($(date +%s) - start))
	echo "$status (${secs}s)"
	echo "$status $name (${secs}s)" >>"$summary"
}

jsrun=bench/bin/jsrun
[ -f bench/bin/jsrun.exe ] && jsrun=bench/bin/jsrun.exe

# --- The machine --------------------------------------------------------------------------
{
	echo "date: $(date)"
	echo "mode: $mode"
	echo "commit: $(git log -1 --format='%h %s')"
	echo "uname: $(uname -a)"
	[ -f /etc/os-release ] && grep -E '^(PRETTY_NAME|VERSION)=' /etc/os-release
	if command -v lscpu >/dev/null; then lscpu | grep -E 'Model name|Architecture|^CPU\(s\)|Flags' | cut -c1-200; fi
	grep -m1 'model name' /proc/cpuinfo 2>/dev/null
	command -v nproc >/dev/null && echo "cpus: $(nproc)"
	free -m 2>/dev/null | head -2
	echo "go: $(go version)"
	command -v getenforce >/dev/null && echo "selinux: $(getenforce)"
} >"$out/machine.txt" 2>&1
cat "$out/machine.txt"
echo

# --- Setup: test262 and WebKit (JetStream2) -------------------------------------------------
if [ ! -d test262 ] || [ ! -d webkit ]; then
	step setup sh scripts/setup.sh
fi
step build sh -c 'go build ./... && go vet ./... && sh bench/build-engines.sh'

# --- Correctness --------------------------------------------------------------------------------
step unit-tests go test -count=1 ./...
# The macro assembler's operations, through the same code as on the Mac.
step jit-masm go test -count=1 -v ./jit
# Generated code against the interpreter, on random programs (half of them
# with long branches).
step jit-fuzz env JIT_FUZZ_N=8000 go test -count=1 -v -run TestJITDifferential ./vm
step regex-fuzz env YARR_FUZZ_N=20000 go test -count=1 -v -run TestPrefiltersDifferential ./yarr
# Whether this machine lets the JIT make executable memory at all (SELinux's
# execmem can forbid it; jsc2go then quietly stays in the interpreter).
jitActive() {
	JSC2GO_JITLOG=1 "$jsrun" bench/vps/micro/jit.js >"$out/jit-active.txt" 2>&1
	grep -c 'jit: compiled' "$out/jit-active.txt"
	grep -q 'jit: compiled' "$out/jit-active.txt"
}
step jit-active jitActive
step leaks sh bench/vps/checks/leaks.sh "$jsrun"
# test262: expected 85615 of 85664 runs, and no failures JSC does not
# expect; the same with nearly everything compiled by the JIT.
# scripts/check262.sh fails on unexpected failures that also fail when run
# alone (slow tests can time out on a busy machine: flaky).
check262() {
	sh scripts/check262.sh "$out/$1.txt"
}
check262jit() (
	export JSC2GO_JITTHRESHOLD=1
	check262 test262-jit-everything-output
)
step test262 check262 test262-output
step test262-jit-everything check262jit

if [ "$mode" = quick ]; then
	echo
	cat "$summary"
	echo "results in $out"
	exit $failures
fi

# --- Speed --------------------------------------------------------------------------------------
step micro-jit sh -c "echo '# JIT on'; $jsrun bench/vps/micro/jit.js; echo '# JIT off'; JSC2GO_JIT=0 $jsrun bench/vps/micro/jit.js"
step micro-interpreter sh -c "echo '# JIT on'; $jsrun bench/vps/micro/interp.js; echo '# JIT off'; JSC2GO_JIT=0 $jsrun bench/vps/micro/interp.js"

js=webkit/PerformanceTests/JetStream2
# All JetStream2 JS benchmarks but the two that take minutes per iteration.
list=$(go run ./bench/vps/compare -list)
step jetstream-jit go run ./bench/jetstream -root $js -iterations 10 -timeout 5m -only "$list" -out "$out/jit.json"
step jetstream-nojit env JSC2GO_JIT=0 go run ./bench/jetstream -root $js -iterations 10 -timeout 5m -only "$list" -out "$out/nojit.json"
others=
if [ "$mode" = full ]; then
	step build-other-engines sh bench/build-engines.sh all
	step jetstream-others go run ./bench/jetstream -root $js -iterations 10 -timeout 5m -engines quickjs,goja,sobek,ramune -only "$list" -out "$out/others.json"
	others="-others $out/others.json"
	step jetstream-slow-jit go run ./bench/jetstream -root $js -iterations 3 -timeout 2h -only octane-zlib,float-mm.c -out "$out/slow-jit.json"
	step jetstream-slow-nojit env JSC2GO_JIT=0 go run ./bench/jetstream -root $js -iterations 3 -timeout 2h -only octane-zlib,float-mm.c -out "$out/slow-nojit.json"
fi
go run ./bench/vps/compare -run "$out/jit.json" -nojit "$out/nojit.json" $others | tee "$out/comparison.txt"

echo
cat "$summary"
echo "results in $out (send summary.txt, machine.txt, comparison.txt and the micro-*.log files)"
exit $failures
