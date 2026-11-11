//go:build !windows

package main

func inServiceMode() bool { return false }

func runAsService(func(string) string) error { return nil }

// platformOverrides has no registry outside Windows.
func platformOverrides() map[string]string { return nil }
