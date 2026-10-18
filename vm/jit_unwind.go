package vm

// What catching an exception thrown while generated code was suspended
// puts back (jit_suspend.go): suspensions keep no deferred function, so
// whoever catches an exception and goes on unwinds the suspensions and
// calls into generated code made since it started.

// jitSaved is what a suspension set aside: the frames of the suspended
// chain and the current frame.
type jitSaved struct {
	frames  []*Frame
	depth   int
	current *Frame
}

// jitMark is how far generated code was suspended and called (callJIT).
type jitMark struct{ suspends, calls int }

func (vm *VM) jitMark() jitMark { return jitMark{vm.jitSuspends, vm.jitCalls} }

// jitUnwindTo ends the suspensions and calls made since mark m: the frames
// of each suspended chain are freed as finishNative frees them, innermost
// first.
func (vm *VM) jitUnwindTo(m jitMark) {
	for vm.jitSuspends > m.suspends {
		vm.jitSuspends--
		sv := &vm.jitSaved[vm.jitSuspends]
		for i := sv.depth - 1; i >= 0; i-- {
			g := sv.frames[i]
			if g.stackBase >= 0 {
				clear(g.regs)
				vm.sp = g.stackBase
			}
			vm.depth--
			*g = Frame{}
		}
		vm.jitFrames, vm.jitDepth, vm.currentFrame = sv.frames, 0, sv.current
		*sv = jitSaved{}
	}
	if vm.jitCalls > m.calls {
		vm.jitCalls = m.calls
		vm.jitSuspend = nil
		if m.calls > 0 {
			vm.jitSuspend = vm.jitStates[m.calls-1]
		}
		if m.calls == 0 {
			vm.jitGraveyard = nil
		}
	}
}
