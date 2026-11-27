//go:build !linux

package main

import "os/exec"

func configurePatchProcess(cmd *exec.Cmd) func() { return func() {} }
