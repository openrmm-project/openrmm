//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// When the agent runs as a service it lives in session 0, which cannot capture
// the interactive desktop. CreateProcessAsUser launches the media sidecar in
// the logged-in user's session instead. If that fails (e.g. the agent is run
// interactively, or no user is logged in), we fall back to a direct launch.

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	wtsapi32 = syscall.NewLazyDLL("wtsapi32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")
	userenv  = syscall.NewLazyDLL("userenv.dll")

	procWTSGetActiveSession     = kernel32.NewProc("WTSGetActiveConsoleSessionId")
	procCloseHandle             = kernel32.NewProc("CloseHandle")
	procWTSQueryUserToken       = wtsapi32.NewProc("WTSQueryUserToken")
	procDuplicateTokenEx        = advapi32.NewProc("DuplicateTokenEx")
	procCreateProcessAsUser     = advapi32.NewProc("CreateProcessAsUserW")
	procCreateEnvironmentBlock  = userenv.NewProc("CreateEnvironmentBlock")
	procDestroyEnvironmentBlock = userenv.NewProc("DestroyEnvironmentBlock")
)

const (
	maximumAllowed         = 0x02000000
	securityImpersonation  = 2
	tokenPrimary           = 1
	createUnicodeEnv       = 0x00000400
	createNoWindow         = 0x08000000
	invalidSessionID       = 0xFFFFFFFF
)

type startupInfoW struct {
	cb              uint32
	_               uint32
	lpReserved      uintptr
	lpDesktop       uintptr
	lpTitle         uintptr
	dwX             uint32
	dwY             uint32
	dwXSize         uint32
	dwYSize         uint32
	dwXCountChars   uint32
	dwYCountChars   uint32
	dwFillAttribute uint32
	dwFlags         uint32
	wShowWindow     uint16
	cbReserved2     uint16
	lpReserved2     uintptr
	hStdInput       uintptr
	hStdOutput      uintptr
	hStdError       uintptr
}

type processInformation struct {
	hProcess    uintptr
	hThread     uintptr
	dwProcessID uint32
	dwThreadID  uint32
}

func launchSidecar(bin, relay string) error {
	if err := launchInUserSession(bin, relay); err != nil {
		log.Printf("remote: user-session launch failed (%v); falling back to direct launch", err)
		cmd := exec.Command(bin, "-relay", relay)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if e := cmd.Start(); e != nil {
			return e
		}
		go cmd.Wait()
	}
	return nil
}

func launchInUserSession(bin, relay string) error {
	sid, _, _ := procWTSGetActiveSession.Call()
	if sid == invalidSessionID {
		return fmt.Errorf("no active console session")
	}
	var hToken uintptr
	if r, _, e := procWTSQueryUserToken.Call(sid, uintptr(unsafe.Pointer(&hToken))); r == 0 {
		return fmt.Errorf("WTSQueryUserToken: %v", e)
	}
	defer procCloseHandle.Call(hToken)

	var hDup uintptr
	if r, _, e := procDuplicateTokenEx.Call(hToken, maximumAllowed, 0, securityImpersonation, tokenPrimary, uintptr(unsafe.Pointer(&hDup))); r == 0 {
		return fmt.Errorf("DuplicateTokenEx: %v", e)
	}
	defer procCloseHandle.Call(hDup)

	var env uintptr
	hasEnv := false
	if r, _, _ := procCreateEnvironmentBlock.Call(uintptr(unsafe.Pointer(&env)), hDup, 0); r != 0 {
		hasEnv = true
		defer procDestroyEnvironmentBlock.Call(env)
	}

	cmdline, _ := syscall.UTF16PtrFromString(fmt.Sprintf("\"%s\" -relay %s", bin, relay))
	desktop, _ := syscall.UTF16PtrFromString("winsta0\\default")
	si := startupInfoW{lpDesktop: uintptr(unsafe.Pointer(desktop))}
	si.cb = uint32(unsafe.Sizeof(si))
	var pi processInformation
	flags := uintptr(createUnicodeEnv | createNoWindow)
	envArg := uintptr(0)
	if hasEnv {
		envArg = env
	}
	r, _, e := procCreateProcessAsUser.Call(
		hDup, 0, uintptr(unsafe.Pointer(cmdline)),
		0, 0, 0, flags, envArg, 0,
		uintptr(unsafe.Pointer(&si)), uintptr(unsafe.Pointer(&pi)))
	if r == 0 {
		return fmt.Errorf("CreateProcessAsUser: %v", e)
	}
	procCloseHandle.Call(pi.hProcess)
	procCloseHandle.Call(pi.hThread)
	log.Printf("remote: sidecar launched in session %d", sid)
	return nil
}
