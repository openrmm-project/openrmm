//go:build !windows

package main

func startPlatformServices(done <-chan struct{}) {}
