//go:build !windows

package main

func pendingReboot() (any, string) { return pendingRebootUnix() }
