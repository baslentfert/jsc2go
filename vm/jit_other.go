//go:build !(((arm64 || amd64) && (darwin || linux)) || (amd64 && windows))

package vm

// No JIT tier on this platform: the interpreter runs everything.

const jitEnabled = false

type jitState struct{}

func (vm *VM) tryJIT(f *Frame, pc int) (int, bool) { return 0, false }
