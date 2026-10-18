package vm

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
	"weak"

	"golang.org/x/text/collate"

	"github.com/baslentfert/jsc2go/jit"
	"github.com/baslentfert/jsc2go/yarr"
)

// VM is a JavaScript virtual machine (JSC's VM): it owns the realms, the
// job queue and the interpreter state. A VM is single-threaded.
type VM struct {
	// jitBuf is the memory the JIT compiles code in, kept between
	// compiles (jit.Masm.ReserveIn).
	jitBuf jit.Buffer
	realm  *Realm // the current realm

	// protoEpoch changes whenever an object that a cache case walked
	// through as a prototype (a watched object) changes its shape, its
	// shape's version or its prototype: cases with a chain whose epoch is
	// still current need not check the chain again (JSC's structure
	// transition watchpoints, as one global epoch like its MegamorphicCache
	// and invalidateStructureChainIntegrity). It starts at 1, so an empty
	// case (epoch 0) never matches.
	protoEpoch uint64
	// varInjection changes whenever sloppy eval gives an existing scope
	// var bindings of its own (JSC's var injection watchpoint): a global
	// resolve_scope that saw a scope chain without such bindings may use
	// its answer for the same scope again while it is unchanged.
	varInjection uint64
	// detachEpoch changes whenever an ArrayBuffer is detached: typed array
	// views made before keep a vector that may be stale (typedArray).
	detachEpoch uint64

	depth    int
	MaxDepth int // maximum JavaScript call depth before a RangeError

	jobs []func()

	// symbolRegistry is the GlobalSymbolRegistry, weak like JSC's
	// SymbolRegistry: a registered symbol nothing refers to can be made
	// again without anyone noticing.
	symbolRegistry       map[string]weak.Pointer[Symbol]
	symbolRegistryPurged int

	// weakQueue receives runtime cleanups of weak collections and
	// finalization registry targets; keptObjects are WeakRef targets kept
	// alive until the current job ends.
	weakQueue *weakQueue
	// sharedEphemerons are this VM's WeakMap and WeakSet entries for the
	// well-known symbols.
	sharedEphemerons sharedEphemerons
	keptObjects      map[Value]struct{}

	// collator is String.prototype.localeCompare's, made on first use.
	collator *collate.Collator

	// Print is called by the global print function (jsc shell compatible).
	Print func(string)

	// currentFrame is the innermost interpreter frame (for stack traces and
	// direct eval).
	currentFrame *Frame

	// AgentHooks are installed by test harnesses that support $262.agent.
	HostHooks HostHooks

	asyncEvaluationCount int64

	joinStack []*Object

	interrupted atomic.Bool
	// callLimit is the call depth generated code compares with: MaxDepth,
	// or -1 while interrupted, so that every call of generated code takes
	// the slow path, whose enterCall stops (recursion without loops),
	// with no instruction of its own.
	callLimit atomic.Int64

	pendingTailCall *tailCallRequest

	// regStack holds the registers of active frames (see allocRegs).
	regStack []Value
	sp       int
	// spDirty bounds the registers above sp that are not zero: generated
	// code leaves a returned callee's registers as they are (directCall);
	// allocRegs clears them for Go's frames, and callJIT and runSuspended
	// when generated code returns or stops, so that they keep nothing
	// alive. (A callee whose registers are in a replaced stack clears
	// them itself.)
	spDirty    int
	freeFrames []*Frame

	// jitFrames are the frames generated code calls generated code in, and
	// jitDepth how many are in use (jit_calls.go).
	jitFrames []*Frame
	jitDepth  int
	// jitSuspend is the state of the innermost callJIT running generated
	// code (jit_suspend.go), nil outside; jitCalls counts those calls and
	// jitStates keeps one state per level. jitFrameSets are the frame
	// arrays of generated code that runs while other code is suspended,
	// one per level of suspended code (jitSuspends). jitGraveyard keeps
	// code jettisoned while generated code may be suspended in it.
	jitSuspend   *jit.State
	jitCalls     int
	jitStates    []*jit.State
	jitFrameSets [][]*Frame
	jitSuspends  int
	jitSaved     []jitSaved
	jitGraveyard []*jitCode
	// jitPools are the objects generated code makes objects from
	// (jit_alloc.go).
	jitPools *[len(jitPoolCapacities)]jitObjectPool
	// jitStrings are generated code's Strings for string appends.
	jitStrings *jitStringPool
	// jitArrays are generated code's arrays for new_array.
	jitArrays *jitArrayPool
	// jitClosures are generated code's closures for new_func(_exp).
	jitClosures *jitClosurePool
	// jitScopes are generated code's scopes for create_lexical_environment.
	jitScopes *jitScopePool
	// jitRegExps are generated code's RegExp objects for new_reg_exp.
	jitRegExps *jitRegExpPool
	// jitSlots is the property storage generated code grows objects into.
	jitSlots *[len(jitSlotCapacities)]jitSlotPool
	// megaGet is the megamorphic get_by_id cache (made when first needed).
	megaGet *megaGet
	// megaPut is the megamorphic put_by_id cache (made when first needed).
	megaPut *megaPut
	// nativeCalls are the Calls of native functions, one per depth of
	// native calls (nativeDepth in use); see nativeCall.
	nativeCalls []*nativeCall
	nativeDepth int

	timers []*timer

	// CanBlock is the agent's [[CanBlock]] (Atomics.wait may suspend).
	CanBlock bool

	regexpCache map[string]*yarr.Pattern

	// rootShape is the empty shape all of this VM's objects start from.
	rootShape *shape

	// Host tasks posted from other goroutines (PostTask), and the number of
	// this agent's async waiters that another agent may still notify.
	tasksMu             sync.Mutex
	tasks               []func()
	taskSignal          chan struct{}
	pendingAsyncWaiters int
}

// HostHooks lets an embedder customize host-defined behavior.
type HostHooks struct {
	// LoadModule returns the source text of the module named by specifier
	// relative to referrer (a file name), plus its canonical name.
	LoadModule func(specifier, referrer string) (source, name string, err error)
	// PromiseRejectionTracker is HostPromiseRejectionTracker.
	PromiseRejectionTracker func(promise *Object, operation string)
	// ToJS, if set, converts Go values of the embedder's own types before
	// Realm.ToValue does (ok false: ToValue converts it).
	ToJS func(r *Realm, x reflect.Value) (v Value, ok bool)
	// FromJS, if set, converts to the embedder's own Go types before
	// ExportTo does (ok false: ExportTo converts it).
	FromJS func(v Value, t reflect.Type) (x reflect.Value, ok bool)
}

// New creates a VM with one realm.
func New() *VM {
	vm := &VM{protoEpoch: 1, MaxDepth: 2500, symbolRegistry: make(map[string]weak.Pointer[Symbol]), CanBlock: true, taskSignal: make(chan struct{}, 1), rootShape: newRootShape(&shapeTree{}), weakQueue: &weakQueue{}}
	vm.realm = vm.NewRealm()
	vm.setCallLimit()
	return vm
}

// Realm returns the current realm.
func (vm *VM) Realm() *Realm { return vm.realm }

// Exception is a thrown JavaScript value. The runtime throws by panicking
// with an *Exception; the interpreter recovers it at exception handlers and
// the embedding API turns it into an error.
type Exception struct {
	Value Value
	Stack string
}

func (e *Exception) Error() string {
	return "Uncaught " + describeThrown(e.Value)
}

// describeThrown formats a thrown value without running user code.
func describeThrown(v Value) string {
	if o := v.AsObject(); o != nil && o.class == ClassError {
		name := "Error"
		if p, ok := lookupDataProperty(o, StrKey("name")); ok && p.IsString() {
			name = p.StringRef().GoString()
		}
		msg := ""
		if p, ok := lookupDataProperty(o, StrKey("message")); ok && p.IsString() {
			msg = p.StringRef().GoString()
		}
		if msg == "" {
			return name
		}
		return name + ": " + msg
	}
	switch v.Kind() {
	case KindString:
		return v.StringRef().GoString()
	case KindObject:
		o := v.ObjectRef()
		name := "object"
		if c, ok := lookupDataProperty(o, StrKey("constructor")); ok && c.IsObject() {
			if n, ok := lookupDataProperty(c.ObjectRef(), StrKey("name")); ok && n.IsString() {
				name = n.StringRef().GoString()
			}
		}
		if m, ok := lookupDataProperty(o, StrKey("message")); ok && m.IsString() {
			return name + ": " + m.StringRef().GoString()
		}
		return "[object " + name + "]"
	case KindSymbol:
		return v.SymbolRef().DescriptiveString()
	}
	return safeToString(v)
}

// lookupDataProperty finds a data property along the prototype chain of
// ordinary objects without running user code.
func lookupDataProperty(o *Object, k PropertyKey) (Value, bool) {
	for ; o != nil && o.exotic == nil; o = o.proto {
		if p, ok := o.ordinaryGetOwnProperty(k); ok {
			if p.IsAccessor() {
				return Undefined, false
			}
			return p.Value, true
		}
	}
	return Undefined, false
}

// Throw throws v as a JavaScript exception.
func (vm *VM) Throw(v Value) {
	panic(&Exception{Value: v})
}

// ErrorKind names the native error constructors.
type ErrorKind uint8

const (
	ErrorKindError ErrorKind = iota
	ErrorKindEvalError
	ErrorKindRangeError
	ErrorKindReferenceError
	ErrorKindSyntaxError
	ErrorKindTypeError
	ErrorKindURIError
	ErrorKindAggregateError
	ErrorKindSuppressedError
	numberOfErrorKinds
)

var errorKindNames = [numberOfErrorKinds]string{"Error", "EvalError", "RangeError", "ReferenceError", "SyntaxError",
	"TypeError", "URIError", "AggregateError", "SuppressedError"}

func (k ErrorKind) String() string { return errorKindNames[k] }

// NewError creates an error object of the current realm.
func (vm *VM) NewError(kind ErrorKind, message string) *Object {
	return vm.realm.newError(kind, message)
}

func (r *Realm) newError(kind ErrorKind, message string) *Object {
	o := r.newObject(r.ErrorPrototypes[kind])
	o.class = ClassError
	if message != "" {
		o.putDirect(StrKey("message"), Str(message), Writable|Configurable)
	}
	return o
}

func (vm *VM) throwError(kind ErrorKind, format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	panic(&Exception{Value: ObjectValue(vm.NewError(kind, msg))})
}

func (vm *VM) throwTypeError(format string, args ...any) {
	vm.throwError(ErrorKindTypeError, format, args...)
}

func (vm *VM) throwRangeError(format string, args ...any) {
	vm.throwError(ErrorKindRangeError, format, args...)
}

func (vm *VM) throwReferenceError(format string, args ...any) {
	vm.throwError(ErrorKindReferenceError, format, args...)
}

func (vm *VM) throwSyntaxError(format string, args ...any) {
	vm.throwError(ErrorKindSyntaxError, format, args...)
}

// Try runs f and returns the JavaScript exception it throws, if any.
func (vm *VM) Try(f func()) (exc *Exception) {
	depth, frame, realm := vm.depth, vm.currentFrame, vm.realm
	mark := vm.jitMark()
	defer func() {
		if r := recover(); r != nil {
			vm.jitUnwindTo(mark)
			e, ok := r.(*Exception)
			if r == errStringTooLong {
				e, ok = vm.outOfMemory(), true
			}
			if !ok {
				panic(r)
			}
			vm.depth, vm.currentFrame, vm.realm = depth, frame, realm
			exc = e
		}
	}()
	f()
	return nil
}

// --- Jobs ------------------------------------------------------------------------------

// EnqueueJob adds a job (HostEnqueuePromiseJob).
func (vm *VM) EnqueueJob(job func()) { vm.jobs = append(vm.jobs, job) }

// RunJobs runs queued jobs and timers until none are left. It returns the
// first exception a job throws (and stops), or ErrInterrupted.
func (vm *VM) RunJobs() error {
	var exc *Exception
	if err := vm.guard(func() { exc = vm.runJobs() }); err != nil {
		return err
	}
	if exc != nil {
		return exc
	}
	return nil
}

func (vm *VM) runJobs() *Exception {
	for {
		vm.clearKeptObjects() // after the script or the task that ran last
		if exc := vm.runMicrotasks(); exc != nil {
			return exc
		}
		ran := false
		if exc := vm.Try(func() { ran = vm.runTimer() }); exc != nil {
			return exc
		}
		if !ran {
			return nil
		}
	}
}

func (vm *VM) runMicrotasks() *Exception {
	for len(vm.jobs) > 0 {
		job := vm.jobs[0]
		vm.jobs[0] = nil
		vm.jobs = vm.jobs[1:]
		exc := vm.Try(job)
		vm.clearKeptObjects()
		if exc != nil {
			return exc
		}
	}
	vm.jobs = nil
	return nil
}

var privateSymbolsMu sync.Mutex

// ErrInterrupted is the error RunScript, Eval, RunModule and RunJobs
// return when Interrupt stopped the code they ran (inside, it is the panic
// value that unwinds it; JavaScript code cannot catch it).
var ErrInterrupted = errors.New("vm: execution interrupted")

// Interrupt asks the running code to stop at the next loop iteration or
// function call; if none runs, the next code run stops at once. It may be
// called from another goroutine. The VM stays interrupted until
// ClearInterrupt.
func (vm *VM) Interrupt() {
	vm.interrupted.Store(true)
	vm.callLimit.Store(-1)
}

// ClearInterrupt lets code run again after Interrupt.
func (vm *VM) ClearInterrupt() {
	vm.interrupted.Store(false)
	vm.setCallLimit()
}

// setCallLimit sets callLimit from MaxDepth (which the embedder may
// change between runs), unless interrupted. (Interrupt stores the flag
// before the limit, and this reads it after storing: whichever runs last,
// an interrupt leaves -1.)
func (vm *VM) setCallLimit() {
	vm.callLimit.Store(int64(vm.MaxDepth))
	if vm.interrupted.Load() {
		vm.callLimit.Store(-1)
	}
}

// guard runs f and returns the JavaScript exception it throws, or
// ErrInterrupted if Interrupt stopped it. Either way the VM can be used
// again; an interrupt also drops the queued jobs and timers, which
// belonged to the code it stopped.
func (vm *VM) guard(f func()) (err error) {
	vm.setCallLimit()
	depth, frame, realm := vm.depth, vm.currentFrame, vm.realm
	mark := vm.jitMark()
	defer func() {
		if r := recover(); r != nil {
			if r != ErrInterrupted {
				panic(r)
			}
			vm.jitUnwindTo(mark)
			vm.depth, vm.currentFrame, vm.realm = depth, frame, realm
			clear(vm.jobs)
			vm.jobs = vm.jobs[:0]
			vm.timers = nil
			err = ErrInterrupted
		}
	}()
	if exc := vm.Try(f); exc != nil {
		return exc
	}
	return nil
}

// --- Timers ------------------------------------------------------------------------------

type timer struct {
	at        time.Time
	fn        func()
	cancelled bool
}

// addTimer schedules fn to run as a host task after d.
func (vm *VM) addTimer(d time.Duration, fn func()) *timer {
	t := &timer{at: time.Now().Add(d), fn: fn}
	vm.timers = append(vm.timers, t)
	return t
}

// runTimer runs one host task: a task posted by another agent, or the
// earliest timer, waiting for it if needed. While async waiters that other
// agents may notify are pending it waits for posted tasks. It reports
// whether a task ran.
func (vm *VM) runTimer() bool {
	if f := vm.takeTask(); f != nil {
		f()
		return true
	}
	best := -1
	for i, t := range vm.timers {
		if !t.cancelled && (best < 0 || t.at.Before(vm.timers[best].at)) {
			best = i
		}
	}
	if best < 0 {
		vm.timers = nil
		if vm.pendingAsyncWaiters == 0 {
			return false
		}
	}
	var due <-chan time.Time
	if best >= 0 {
		if d := time.Until(vm.timers[best].at); d > 0 {
			tm := time.NewTimer(d)
			defer tm.Stop()
			due = tm.C
		} else {
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			due = ch
		}
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-vm.taskSignal:
			if f := vm.takeTask(); f != nil {
				f()
				return true
			}
		case <-due:
			t := vm.timers[best]
			vm.timers = append(vm.timers[:best], vm.timers[best+1:]...)
			t.fn()
			return true
		case <-tick.C:
			if vm.interrupted.Load() {
				panic(ErrInterrupted)
			}
		}
	}
}

// outOfMemory is the exception for an allocation beyond the engine's limits.
func (vm *VM) outOfMemory() *Exception {
	return &Exception{Value: ObjectValue(vm.NewError(ErrorKindRangeError, "Out of memory"))}
}
