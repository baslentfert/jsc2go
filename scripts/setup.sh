#!/bin/sh
# Sets up a checkout: clones test262 and the parts of WebKit jsc2go uses as
# its reference (at the commits the port and its test results are based on),
# then builds and tests. Needs git and Go (see go.mod).
#   sh scripts/setup.sh           everything
#   sh scripts/setup.sh test262   only test262 (what tests and CI need)
set -e
cd "$(dirname "$0")/.."
TEST262_COMMIT=7ab7fafa0003f73fc85c1b95d88094d33f7eb8bd
WEBKIT_COMMIT=edbf6068f30a6cb59b135c6b647ec637e05d5576

if [ ! -d test262 ]; then
	# Only that commit. test262 must have LF line endings: line terminator
	# tests fail with CRLF.
	git -c core.autocrlf=false init -q test262
	git -C test262 config core.autocrlf false
	git -C test262 fetch -q --depth 1 https://github.com/tc39/test262.git $TEST262_COMMIT
	git -C test262 checkout -q FETCH_HEAD
fi
if [ "$1" = test262 ]; then
	exit 0
fi

if [ ! -d webkit ]; then
	# Only JavaScriptCore, WTF, JetStream2 and JSC's test262 expectations, and
	# file contents only as needed (blob:none).
	git clone --filter=blob:none --no-checkout --sparse https://github.com/WebKit/WebKit.git webkit
	git -C webkit sparse-checkout set Source/JavaScriptCore Source/WTF PerformanceTests/JetStream2 JSTests/test262/expectations.yaml JSTests/test262/config.yaml
	git -C webkit checkout -q $WEBKIT_COMMIT
fi

go build ./...
go test ./...
echo "Done. scripts/test262.sh runs the test262 suite; bench/jetstream.sh runs JetStream2."
