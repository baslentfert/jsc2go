# Generates vm/intrinsic_table_gen.go from the property tables in
# webkit/Source/JavaScriptCore/runtime (which builtin is which Intrinsic).
# Run from the repository root after gen_intrinsics.py: python scripts/gen_intrinsic_table.py
import re, os, collections
root = 'webkit/Source/JavaScriptCore/runtime'
BS = chr(92)

def owner_of(fname):
    base = fname[:-4]
    special = {'MathObject': 'Math', 'JSGlobalObject': 'globalThis', 'JSGlobalObjectFunctions': 'globalThis',
               'ObjectConstructor': 'Object', 'ReflectObject': 'Reflect', 'AtomicsObject': 'Atomics',
               'JSONObject': 'JSON', 'JSDataViewPrototype': 'DataView.prototype'}
    if base in special:
        return special[base]
    m = re.match(r'^(?:JS)?(\w+?)(Prototype|Constructor)$', base)
    if not m:
        return None
    name, kind = m.groups()
    return name + ('.prototype' if kind == 'Prototype' else '')

rows = []
for fname in sorted(os.listdir(root)):
    if not fname.endswith('.cpp') or fname.startswith('Intl') or fname.startswith('Temporal'):
        continue
    owner = owner_of(fname)
    if owner is None:
        continue
    src = open(os.path.join(root, fname), encoding='utf-8', errors='replace').read().replace('\r', '')
    # JSC_NATIVE_INTRINSIC_FUNCTION...("name"_s or builtinNames().xPublicName() or propertyNames->x, ..., XIntrinsic)
    for m in re.finditer(r'JSC_NATIVE_INTRINSIC_(?:FUNCTION|GETTER)\w*\((.*?)\);', src, re.S):
        args = m.group(1)
        intr = re.findall(r'(\w+Intrinsic)\b', args)
        if not intr:
            continue
        first = args.split(',')[0].strip()
        if 'PrivateName' in first:
            continue
        nm = re.match(r'"([^"]+)"_s', first) or re.search(r'builtinNames\(\)\.(\w+)PublicName', first) \
            or re.search(r'propertyNames->(\w+)', first)
        if not nm:
            continue
        rows.append((owner, nm.group(1), intr[-1]))
    # putDirectNativeFunction...(vm, globalObject, Identifier::fromString(vm, "abs"_s), 1, f, vis, AbsIntrinsic, ...)
    for m in re.finditer(r'putDirectNativeFunction\w*\((.*?)\);', src, re.S):
        args = m.group(1)
        intr = [x for x in re.findall(r'(\w+Intrinsic)\b', args) if x != 'NoIntrinsic']
        nm = re.search(r'"([^"]+)"_s', args) or re.search(r'propertyNames->(\w+)', args)
        if intr and nm:
            rows.append((owner, nm.group(1), intr[0]))
    # lut tables between @begin and @end
    for t in re.finditer(r'@begin (\w+)(.*?)@end', src, re.S):
        for line in t.group(2).split('\n'):
            parts = line.split()
            if len(parts) >= 5 and parts[-1].endswith('Intrinsic') and parts[-1] != 'NoIntrinsic':
                rows.append((owner, parts[0], parts[-1]))

intrinsics = set(re.findall(r'^\t(\w+)', open('vm/intrinsic_gen.go').read(), re.M))
seen = set()
out = []
for o, n, i in rows:
    if i not in intrinsics or (o, n) in seen:
        continue
    seen.add((o, n))
    out.append((o, n, i))
print(len(out))
for o, n, i in out:
    if any(k in i for k in ('CharCodeAt', 'Sqrt', 'Floor', 'ArrayPush', 'FromCharCode', 'IMul', 'Max')):
        print(o, n, i)
lines = ['// Code generated from the property tables of JavaScriptCore/runtime; DO NOT EDIT.', '', 'package vm', '',
         '// builtinIntrinsics are the builtin functions JSC marks with an Intrinsic:', '// the object they are a property of, the property and the Intrinsic.',
         'var builtinIntrinsics = []struct {', '\towner, name string', '\tintrinsic  Intrinsic', '}{']
for o, n, i in out:
    lines.append('\t{"%s", "%s", %s},' % (o, n, i))
lines.append('}')
open('vm/intrinsic_table_gen.go', 'w', newline='\n').write('\n'.join(lines) + '\n')
