#!/bin/sh
# Runs JetStream2's JavaScript benchmarks (from webkit/PerformanceTests).
#   bench/jetstream.sh                      jsc2go only
#   bench/jetstream.sh -engines jsc2go,quickjs,goja,sobek,ramune   (after build-engines.sh all)
# Extra flags go to the runner, e.g. -only richards,splay -iterations 5.
# octane-zlib and float-mm.c take minutes per iteration in an interpreter;
# run them separately with -iterations 3 -timeout 2h.
set -e
cd "$(dirname "$0")/.."
[ -x bench/bin/jsrun ] || [ -x bench/bin/jsrun.exe ] || sh bench/build-engines.sh
go run ./bench/jetstream -root webkit/PerformanceTests/JetStream2 -iterations 10 -timeout 5m "$@"
go run ./bench/jssummary bench/results.json
