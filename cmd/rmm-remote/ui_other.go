//go:build !darwin

package main

func runPlatformUI(desktop bool, run func() error) error { return run() }
