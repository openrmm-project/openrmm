//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
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
	procGetTokenInformation     = advapi32.NewProc("GetTokenInformation")
	procOpenProcessToken        = advapi32.NewProc("OpenProcessToken")
	procSetTokenInformation     = advapi32.NewProc("SetTokenInformation")
	procGetCurrentProcess       = kernel32.NewProc("GetCurrentProcess")
	procCreateProcessAsUser     = advapi32.NewProc("CreateProcessAsUserW")
	procCreateEnvironmentBlock  = userenv.NewProc("CreateEnvironmentBlock")
	procDestroyEnvironmentBlock = userenv.NewProc("DestroyEnvironmentBlock")
)

const (
	maximumAllowed        = 0x02000000
	securityImpersonation = 2
	tokenPrimary          = 1
	createUnicodeEnv      = 0x00000400
	createNoWindow        = 0x08000000
	invalidSessionID      = 0xFFFFFFFF
	tokenLinkedToken      = 19 // TOKEN_INFORMATION_CLASS
	tokenSessionID        = 12 // TokenSessionId
	tokenDuplicate        = 0x0002
	tokenQuery            = 0x0008
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

func launchSidecar(bin, relay, ticket string, extra ...string) error {
	// SendInput cannot synthesize the Windows Secure Attention Sequence. Allow
	// the LocalSystem remote module to use SendSAS unless an administrator has
	// explicitly disabled software SAS generation through policy.
	if err := ensureSoftwareSASPolicy(); err != nil {
		log.Printf("remote: Ctrl+Alt+Delete unavailable: %v", err)
	}
	if err := launchInUserSession(bin, relay, ticket, extra); err != nil {
		log.Printf("remote: user-session launch failed (%v); falling back to direct launch", err)
		cmd := exec.Command(bin, sidecarArgs(relay, ticket, extra)...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if e := cmd.Start(); e != nil {
			return e
		}
		go cmd.Wait()
	}
	return nil
}

const softwareSASPolicyPath = `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`

func ensureSoftwareSASPolicy() error {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, softwareSASPolicyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	current, _, err := key.GetIntegerValue("SoftwareSASGeneration")
	if err == registry.ErrNotExist { // not configured: allow services, but not unrelated applications
		return key.SetDWordValue("SoftwareSASGeneration", 1)
	}
	if err != nil {
		return err
	}
	switch current {
	case 1, 3: // services already allowed
		return nil
	default: // value 0 is an explicit administrator prohibition
		return fmt.Errorf("software Secure Attention Sequence is disabled by Windows policy")
	}
}

func launchInUserSession(bin, relay, ticket string, extra []string) error {
	sid, _, _ := procWTSGetActiveSession.Call()
	if sid == invalidSessionID {
		return fmt.Errorf("no active console session")
	}
	// Run the sidecar as SYSTEM (the agent's own token when installed as a
	// service) placed into the interactive session. SYSTEM integrity is required
	// to inject input into the UAC consent dialog (consent.exe runs as SYSTEM);
	// an elevated *user* is blocked by UIPI. Falls back below if not SYSTEM.
	hProc, _, _ := procGetCurrentProcess.Call()
	var hToken uintptr
	if r, _, e := procOpenProcessToken.Call(hProc, tokenDuplicate|tokenQuery, uintptr(unsafe.Pointer(&hToken))); r == 0 {
		return fmt.Errorf("OpenProcessToken: %v", e)
	}
	defer procCloseHandle.Call(hToken)

	var hDup uintptr
	if r, _, e := procDuplicateTokenEx.Call(hToken, maximumAllowed, 0, securityImpersonation, tokenPrimary, uintptr(unsafe.Pointer(&hDup))); r == 0 {
		return fmt.Errorf("DuplicateTokenEx: %v", e)
	}
	defer procCloseHandle.Call(hDup)

	// Move the duplicated token into the active console session.
	sessID := uint32(sid)
	if r, _, e := procSetTokenInformation.Call(hDup, tokenSessionID, uintptr(unsafe.Pointer(&sessID)), uintptr(unsafe.Sizeof(sessID))); r == 0 {
		return fmt.Errorf("SetTokenInformation(session): %v", e)
	}

	var env uintptr
	hasEnv := false
	if r, _, _ := procCreateEnvironmentBlock.Call(uintptr(unsafe.Pointer(&env)), hDup, 0); r != 0 {
		hasEnv = true
		defer procDestroyEnvironmentBlock.Call(env)
	}

	args := fmt.Sprintf("\"%s\" %s", bin, strings.Join(sidecarArgs(relay, ticket, extra), " "))
	cmdline, _ := syscall.UTF16PtrFromString(args)
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
