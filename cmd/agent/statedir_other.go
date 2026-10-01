//go:build !windows

package main

// hardenStateDir is a no-op outside Windows: the state files are created 0600
// by writeDurableFile, and the installers refuse a directory that group or
// others can write to.
func hardenStateDir(string) {}
