package vm

import (
	"math"
	"sync"
	"time"
)

// Shared memory between agents. Every VM is an agent running on its own
// goroutine; SharedArrayBuffers share their data block, Atomics operations
// on shared memory and the waiter lists run inside one critical section
// (the spec's WaiterList critical sections), and a notify that wakes an
// async waiter of another agent resolves its promise through that agent's
// task queue.

var atomicsMu sync.Mutex

// waiterLists maps a memory location to its WaiterList.
var waiterLists = map[waiterKey][]*waiter{}

type waiterKey struct {
	data  *byte
	index int
}

// waiter is a Waiter Record: synchronous waiters block on ch, async ones
// are settled through promise in their agent vm.
type waiter struct {
	ch      chan struct{}
	vm      *VM
	promise *Object
}

func waiterKeyFor(t *typedArray, i int) waiterKey {
	b := t.buf()
	return waiterKey{data: &b.data[:1][0], index: t.byteOffset + i*t.kind.size()}
}

// lockShared enters the critical section if t views shared memory; it
// returns the function that leaves it.
func lockShared(t *typedArray) func() {
	if !t.buf().shared {
		return func() {}
	}
	atomicsMu.Lock()
	return atomicsMu.Unlock
}

func removeWaiterLocked(key waiterKey, w *waiter) bool {
	list := waiterLists[key]
	for i, x := range list {
		if x == w {
			if len(list) == 1 {
				delete(waiterLists, key)
			} else {
				waiterLists[key] = append(list[:i:i], list[i+1:]...)
			}
			return true
		}
	}
	return false
}

// waitSync is the blocking part of Atomics.wait (DoWait, mode sync).
func (vm *VM) waitSync(t *typedArray, i int, v Value, timeoutMs float64) string {
	atomicsMu.Lock()
	if !StrictEquals(t.getElement(i), v) {
		atomicsMu.Unlock()
		return "not-equal"
	}
	key := waiterKeyFor(t, i)
	w := &waiter{ch: make(chan struct{})}
	waiterLists[key] = append(waiterLists[key], w)
	atomicsMu.Unlock()

	var timeout <-chan time.Time
	if !math.IsInf(timeoutMs, 1) {
		timer := time.NewTimer(time.Duration(timeoutMs * float64(time.Millisecond)))
		defer timer.Stop()
		timeout = timer.C
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-w.ch:
			return "ok"
		case <-timeout:
			atomicsMu.Lock()
			removed := removeWaiterLocked(key, w)
			atomicsMu.Unlock()
			if removed {
				return "timed-out"
			}
			return "ok"
		case <-tick.C:
			if vm.interrupted.Load() {
				atomicsMu.Lock()
				removeWaiterLocked(key, w)
				atomicsMu.Unlock()
				panic(ErrInterrupted)
			}
		}
	}
}

// waitAsync is DoWait in async mode: it returns "not-equal" or
// "timed-out" for an immediate result, or "" after adding a waiter that
// settles p.
func (vm *VM) waitAsync(t *typedArray, i int, v Value, timeoutMs float64, p *Object) string {
	atomicsMu.Lock()
	defer atomicsMu.Unlock()
	if !StrictEquals(t.getElement(i), v) {
		return "not-equal"
	}
	if timeoutMs == 0 {
		return "timed-out"
	}
	key := waiterKeyFor(t, i)
	w := &waiter{vm: vm, promise: p}
	waiterLists[key] = append(waiterLists[key], w)
	vm.pendingAsyncWaiters++
	if !math.IsInf(timeoutMs, 1) {
		vm.addTimer(time.Duration(timeoutMs*float64(time.Millisecond)), func() {
			atomicsMu.Lock()
			removed := removeWaiterLocked(key, w)
			atomicsMu.Unlock()
			if removed {
				vm.pendingAsyncWaiters--
				vm.resolvePromise(p, Str("timed-out"))
			}
		})
	}
	return ""
}

// notifyWaiters is the waking part of Atomics.notify.
func (vm *VM) notifyWaiters(t *typedArray, i int, count float64) int {
	if len(t.buf().data) == 0 {
		return 0
	}
	key := waiterKeyFor(t, i)
	atomicsMu.Lock()
	var woken []*waiter
	for len(waiterLists[key]) > 0 && float64(len(woken)) < count {
		w := waiterLists[key][0]
		removeWaiterLocked(key, w)
		woken = append(woken, w)
	}
	atomicsMu.Unlock()
	for _, w := range woken {
		switch {
		case w.ch != nil:
			close(w.ch)
		case w.vm == vm:
			vm.pendingAsyncWaiters--
			vm.resolvePromise(w.promise, Str("ok"))
		default:
			target := w.vm
			target.PostTask(func() {
				target.pendingAsyncWaiters--
				target.resolvePromise(w.promise, Str("ok"))
			})
		}
	}
	return len(woken)
}

// PostTask queues f to run on vm's goroutine as a host task (during
// RunJobs). It may be called from any goroutine.
func (vm *VM) PostTask(f func()) {
	vm.tasksMu.Lock()
	vm.tasks = append(vm.tasks, f)
	vm.tasksMu.Unlock()
	select {
	case vm.taskSignal <- struct{}{}:
	default:
	}
}

func (vm *VM) takeTask() func() {
	vm.tasksMu.Lock()
	defer vm.tasksMu.Unlock()
	if len(vm.tasks) == 0 {
		return nil
	}
	f := vm.tasks[0]
	vm.tasks = vm.tasks[1:]
	return f
}

// SharedBlock is the shared data block of a SharedArrayBuffer, which can
// be handed to another agent.
type SharedBlock struct{ b *arrayBufferData }

// SharedBlockOf returns the data block of a SharedArrayBuffer.
func SharedBlockOf(v Value) (SharedBlock, bool) {
	if o := v.AsObject(); o != nil && o.class == ClassSharedArrayBuffer {
		return SharedBlock{bufferOf(o)}, true
	}
	return SharedBlock{}, false
}

// NewSharedArrayBuffer creates a SharedArrayBuffer of r over block.
func (r *Realm) NewSharedArrayBuffer(block SharedBlock) Value {
	o := r.newObject(r.SharedArrayBufferPrototype)
	o.class = ClassSharedArrayBuffer
	o.internal = block.b
	return ObjectValue(o)
}
