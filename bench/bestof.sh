#!/bin/sh
# bestof.sh BIN SCRIPT [N]: runs BIN SCRIPT N times; prints the minimum of
# each "<name> <ms> ms" line.
bin=$1; script=$2; n=${3:-5}
for i in $(seq $n); do "$bin" "$script"; done | awk '{for (i = 2; i <= NF; i++) if ($(i+1) == "ms") { k = $1; v = $i + 0; if (!(k in m) || v < m[k]) m[k] = v } } END { for (k in m) printf "%s %d ms\n", k, m[k] }' | sort
