//go:build windows

package main

import "github.com/p1ngu/rmm/internal/sasbridge"

// secureAttentionAvailable verifies the prerequisites that can be checked
// without actually switching the endpoint to the Windows security screen.
func secureAttentionAvailable() error {
	return sasbridge.Request(false)
}

// sendSecureAttention invokes the OS-supported SAS path. SendInput deliberately
// cannot synthesize Ctrl+Alt+Delete, even when the sidecar is LocalSystem.
func sendSecureAttention() error {
	return sasbridge.Request(true)
}
