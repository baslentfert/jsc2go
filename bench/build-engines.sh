#!/bin/sh
# Builds the benchmark runners into bench/bin: jsrun (jsc2go) always, and
# with "all" also the runners for the engines jsc2go is compared with
# (goja, Sobek and modernc QuickJS in jsbench, Ramune in ramunebench).
# Only the latter makes Go download those engines.
set -e
cd "$(dirname "$0")/.."
mkdir -p bench/bin
go build -o bench/bin/ ./cmd/jsrun
if [ "$1" = "all" ]; then
	exe=$(go env GOEXE)
	(cd bench/engines && go build -o "../bin/jsbench$exe" . && go build -tags qjswasm -o "../bin/ramunebench$exe" ./ramunebench)
fi
ls bench/bin
