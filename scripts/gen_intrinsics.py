# Generates vm/intrinsic_gen.go from webkit/Source/JavaScriptCore/runtime/Intrinsic.h.
# Run from the repository root: python scripts/gen_intrinsics.py
import re
BS = chr(92)
src = open('webkit/Source/JavaScriptCore/runtime/Intrinsic.h', encoding='utf-8').read().replace('\r', '')
i = src.index('#define JSC_FOR_EACH_INTRINSIC(macro)')
names = []
for line in src[i:].split('\n')[1:]:
    names += re.findall(r'macro\((\w+)\)', line)
    if not line.rstrip().endswith(BS):
        break
print(len(names), names[:2], names[-2:])
out = ['// Code generated from JavaScriptCore/runtime/Intrinsic.h; DO NOT EDIT.', '', 'package vm', '',
       "// Intrinsic is JSC's Intrinsic: what a builtin function is, which lets the",
       "// JIT run it itself (the thunks in jit_thunks.go).", 'type Intrinsic uint16', '', 'const (']
for k, n in enumerate(names):
    out.append('\t%s%s' % (n, ' Intrinsic = iota' if k == 0 else ''))
out += [')', '', 'var intrinsicNames = [...]string{'] + ['\t%s: "%s",' % (n, n) for n in names] + ['}', '',
        'func (i Intrinsic) String() string {', '\tif int(i) < len(intrinsicNames) {', '\t\treturn intrinsicNames[i]',
        '\t}', '\treturn "Intrinsic(?)"', '}']
open('vm/intrinsic_gen.go', 'w', newline='\n').write('\n'.join(out) + '\n')
