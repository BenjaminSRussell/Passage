//go:build !windows

package main

func inServiceMode() bool { return false }

func runAsService(string, string, runFunc) error { return nil }

// platformOverrides has no registry outside Windows.
func platformOverrides() map[string]string { return nil }

// serviceRunning: there is no Passage service outside Windows.
func serviceRunning() bool { return false }
