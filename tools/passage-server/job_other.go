//go:build !windows

package main

import "os/exec"

// attachToJob is a no-op outside Windows.
func attachToJob(*exec.Cmd) {}
