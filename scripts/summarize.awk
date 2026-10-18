/^(FAIL|CRASH|TIMEOUT) / { if (path != "") print path "\t" detail; path = $2; detail = ""; next }
/^    / { if (detail == "") { d = $0; sub(/^ +/, "", d); detail = d } ; next }
END { if (path != "") print path "\t" detail }
