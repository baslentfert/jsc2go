//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a command from opening a console window: the app has
// no console, so Windows would give the child one of its own (a window
// flashing up when app.js runs PowerShell).
func hideWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
