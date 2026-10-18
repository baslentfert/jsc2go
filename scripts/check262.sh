#!/bin/sh
# check262.sh [OUTPUT]: runs test262 (scripts/test262.sh) and exits non-zero
# if a test fails that JSC does not expect to fail. Each unexpected failure is run again alone first:
# the slowest tests (staging/sm/Date/dst-offset-caching-*, which collect
# garbage thousands of times) can time out on a busy machine, and count as
# flaky if they then pass. Set JSC2GO_JITTHRESHOLD=1 to compile nearly
# everything with the JIT.
cd "$(dirname "$0")/.."
out=${1:-test262-results.txt}
sh scripts/test262.sh "$out" >"$out.summary" 2>&1
cat "$out.summary"
unexpected=$(sed -n '/Failures JSC does not expect/,$p' "$out.summary" | grep '^test/')
rm -f "$out.summary"
if ! grep -q ' 0 crash' "$out"; then
	echo "test262 crashed"
	exit 1
fi
status=0
for t in $unexpected; do
	if go run ./cmd/test262 -j 1 "test262/$t" 2>&1 | grep -q ' 0 fail, 0 crash, 0 timeout'; then
		echo "flaky (passes alone): $t"
	else
		echo "FAILS: $t"
		status=1
	fi
done
exit $status
