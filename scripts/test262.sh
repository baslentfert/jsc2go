#!/bin/sh
# Runs test262 with JSC's configuration (its skip list plus Temporal; no
# intl402) and prints the failures JSC does not expect as well, which
# should be none.
#
# Skipped files, as in JSC's config.yaml:
#   built-ins/ThrowTypeError/unique-per-realm-function-proto.js expects
#   Function.prototype.caller and .arguments to be %ThrowTypeError%; like
#   JSC (and SpiderMonkey) they are the legacy function reflection
#   accessors (f.caller works in sloppy functions).
set -e
cd "$(dirname "$0")/.."
out=${1:-test262-results.txt}
go run ./cmd/test262 -v \
	-skip-features decorators,source-phase-imports,import-defer,import-bytes,callable-boundary-realms,FinalizationRegistry.prototype.cleanupSome,await-dictionary,immutable-arraybuffer,error-stack-accessor,Temporal \
	-skip-files built-ins/ThrowTypeError/unique-per-realm-function-proto.js \
	-timeout 20s \
	test262/test/built-ins test262/test/language test262/test/annexB test262/test/harness \
	test262/test/staging/sm test262/test/staging/built-ins \
	test262/test/staging/explicit-resource-management test262/test/staging/top-level-await \
	> "$out" 2>&1 || true
tail -1 "$out"
echo "Failures JSC does not expect:"
awk -f scripts/summarize.awk "$out" | cut -f1 | tr '\134' '/' | sed 's| (strict)$||; s|^test262/||' | sort -u | comm -23 - scripts/jsc_expected.txt
