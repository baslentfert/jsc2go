package ident

import (
	"runtime"
	"strings"
	"sync"
	"weak"
)

// The atom table (JSC's AtomStringTable). An identifier is a pointer to an
// atom; the table maps contents to atoms so that equal contents give the
// same pointer.
//
// Atoms are allocated in blocks of 64, and the table refers to the blocks
// weakly: once no identifier points into a block, the block is collected
// and a runtime cleanup removes its names from the table. Go's
// unique.Make, used before, made a weak handle for every string, and the
// runtime inserts those into a sorted per-span list: interning many new
// names (objects used as dictionaries) spent most of its time there.
// Per block that cost is paid once for 64 names. A block is kept as long
// as any of its atoms is, so dead names stay interned at most until their
// block's other names die too.

type atom struct{ s string }

const atomsPerBlock = 64

type atomBlock struct {
	atoms [atomsPerBlock]atom
}

// atomRef is a table entry: an atom as its block and index.
type atomRef struct {
	block weak.Pointer[atomBlock]
	i     uint8
}

// blockNames are the names of a block, for its cleanup (which must not
// refer to the block itself).
type blockNames struct {
	ref   weak.Pointer[atomBlock]
	names [atomsPerBlock]string
}

// The table is sharded by a hash of the name; each shard is a plain map
// (compact for the garbage collector, unlike sync.Map's node per entry)
// read under a shared lock.
const atomShards = 64

type atomShard struct {
	mu   sync.RWMutex
	m    map[string]atomRef
	peak int      // most entries m held since it was made
	_    [32]byte // keep shards on separate cache lines
}

var (
	shards [atomShards]atomShard

	allocMu    sync.Mutex
	curBlock   *atomBlock
	curNames   *blockNames
	curBlockAt int // atoms used in curBlock
)

func shardOf(s string) *atomShard {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint32(s[i])) * 16777619
	}
	return &shards[h%atomShards]
}

func (sh *atomShard) lookup(s string) *atom {
	sh.mu.RLock()
	r, ok := sh.m[s]
	sh.mu.RUnlock()
	if ok {
		if b := r.block.Value(); b != nil {
			return &b.atoms[r.i]
		}
	}
	return nil
}

func intern(s string) *atom {
	sh := shardOf(s)
	if a := sh.lookup(s); a != nil {
		return a
	}
	allocMu.Lock()
	defer allocMu.Unlock()
	if a := sh.lookup(s); a != nil {
		return a
	}
	if curBlock == nil || curBlockAt == atomsPerBlock {
		curBlock = new(atomBlock)
		curNames = &blockNames{ref: weak.Make(curBlock)}
		curBlockAt = 0
		runtime.AddCleanup(curBlock, removeNames, curNames)
	}
	s = strings.Clone(s) // the caller's string may be part of a larger one
	i := curBlockAt
	curBlockAt++
	a := &curBlock.atoms[i]
	a.s = s
	curNames.names[i] = s
	sh.mu.Lock()
	if sh.m == nil {
		sh.m = make(map[string]atomRef)
	}
	sh.m[s] = atomRef{curNames.ref, uint8(i)}
	sh.peak = max(sh.peak, len(sh.m))
	sh.mu.Unlock()
	return a
}

// removeNames drops a collected block's names, unless a name was interned
// again since (in another block).
func removeNames(n *blockNames) {
	allocMu.Lock() // orders the reads after the writes of intern
	defer allocMu.Unlock()
	for i, s := range n.names {
		sh := shardOf(s)
		sh.mu.Lock()
		if r, ok := sh.m[s]; ok && r == (atomRef{n.ref, uint8(i)}) {
			delete(sh.m, s)
			// A map keeps its memory on delete: copy it once it has
			// shrunk to a quarter.
			if sh.peak > 1024 && len(sh.m) < sh.peak/4 {
				m := make(map[string]atomRef, len(sh.m)) // maps.Clone keeps the size
				for k, v := range sh.m {
					m[k] = v
				}
				sh.m = m
				sh.peak = len(sh.m)
			}
		}
		sh.mu.Unlock()
	}
}
