//go:build !windows

package main

import (
	"os"
	"os/exec"
)

// attachToJob is a no-op outside Windows.
func attachToJob(*exec.Cmd) {}

// setProcAttr has nothing to set outside Windows.
func setProcAttr(*exec.Cmd) {}

// interrupt asks a child to stop gracefully.
func interrupt(p *os.Process) error { return p.Signal(os.Interrupt) }
