//go:build !(((arm64 || amd64) && (darwin || linux)) || (amd64 && windows))

package jit

// Supported reports whether this platform can run generated code.
const Supported = false
