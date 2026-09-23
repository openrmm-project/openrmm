//go:build !darwin

package main

import "runtime"

func platformDetail() string { return runtime.GOOS + " " + runtime.GOARCH }
