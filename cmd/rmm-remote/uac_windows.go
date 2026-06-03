//go:build windows

package main

import (
	"log"
	"os/exec"
)

// UAC consent prompts normally render on the isolated "secure desktop", which a
// screen-capture/input tool cannot see or click. Setting PromptOnSecureDesktop=0
// moves them onto the interactive desktop, where the (elevated) sidecar can.
// This weakens UAC, so it is opt-in per session and restored on disconnect.
const uacKey = `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`

func (s *session) setUAC(disableSecure bool) {
	val := "1" // 1 = prompt on secure desktop (default, secure)
	if disableSecure {
		val = "0" // 0 = prompt on the interactive desktop (controllable)
	}
	cmd := exec.Command("reg", "add", uacKey, "/v", "PromptOnSecureDesktop", "/t", "REG_DWORD", "/d", val, "/f")
	if err := cmd.Run(); err != nil {
		log.Printf("uac: reg add failed: %v", err)
		s.sendJSON(map[string]any{"t": "uac", "enabled": s.uacOff, "err": err.Error()})
		return
	}
	s.mu.Lock()
	s.uacOff = disableSecure
	s.mu.Unlock()
	log.Printf("uac: secure-desktop prompts %s", map[bool]string{true: "disabled (controllable)", false: "restored"}[disableSecure])
	s.sendJSON(map[string]any{"t": "uac", "enabled": disableSecure})
}
