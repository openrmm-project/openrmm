//go:build windows

package main

import (
	"github.com/p1ngu/rmm/internal/sasbridge"
	"golang.org/x/sys/windows/svc"
	"log"
)

func startPlatformServices(done <-chan struct{}) {
	if ok, err := svc.IsWindowsService(); err != nil || !ok {
		return
	}
	go func() {
		if err := sasbridge.Serve(done); err != nil {
			log.Printf("remote: secure attention broker: %v", err)
		}
	}()
}
