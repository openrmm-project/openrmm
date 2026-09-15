//go:build windows

// Package sasbridge brokers secure attention through the installed service.
package sasbridge

import (
	"fmt"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"log"
	"runtime"
	"time"
	"unsafe"
)

const pipeName = `\\.\pipe\OpenRMM-SecureAttention-v1`

var sendSAS = windows.NewLazySystemDLL("sas.dll").NewProc("SendSAS")
var impersonate = windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")

func available() error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("SoftwareSASGeneration")
	if err != nil || (v != 1 && v != 3) {
		return fmt.Errorf("Windows policy does not allow services to send Ctrl+Alt+Delete")
	}
	return sendSAS.Find()
}

// Bound pipe operations, including unresponsive clients. Keep the OVERLAPPED
// alive until any cancelled operation completes.
func pending(h windows.Handle, start func(*windows.Overlapped) error) error {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	o := &windows.Overlapped{HEvent: event}
	err = start(o)
	if err != windows.ERROR_IO_PENDING {
		return err
	}
	status, waitErr := windows.WaitForSingleObject(event, 5000)
	var n uint32
	if waitErr != nil || status != windows.WAIT_OBJECT_0 {
		windows.CancelIoEx(h, o)
		windows.GetOverlappedResult(h, o, &n, true)
		return fmt.Errorf("secure attention service timed out")
	}
	return windows.GetOverlappedResult(h, o, &n, false)
}

func transfer(h windows.Handle, b []byte, write bool) error {
	return pending(h, func(o *windows.Overlapped) error {
		var n uint32
		if write {
			return windows.WriteFile(h, b, &n, o)
		}
		return windows.ReadFile(h, b, &n, o)
	})
}

// Request probes when fire=false. Success means dispatched, not confirmed:
// https://learn.microsoft.com/windows/win32/api/sas/nf-sas-sendsas returns void.
func Request(fire bool) error {
	h, err := dial()
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	b := []byte{0}
	if fire {
		b[0] = 1
	}
	if err := transfer(h, b, true); err != nil {
		return err
	}
	if err := transfer(h, b, false); err != nil {
		return err
	}
	_ = transfer(h, []byte{0}, true) // acknowledge before the server disconnects
	if b[0] != 1 {
		return fmt.Errorf("secure attention denied by Windows policy or service permissions")
	}
	return nil
}

// dial opens the broker pipe, waiting out a busy instance instead of reporting
// the feature as missing. The pipe serves one client at a time, so two sessions
// probing at once would otherwise leave the second one with Ctrl+Alt+Delete
// disabled for good — hello announces the capability once and never revises it.
func dial() (windows.Handle, error) {
	deadline := time.Now().Add(3 * time.Second)
	for {
		h, err := windows.CreateFile(windows.StringToUTF16Ptr(pipeName), windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IMPERSONATION, 0)
		if err == nil {
			return h, nil
		}
		if err != windows.ERROR_PIPE_BUSY || !time.Now().Before(deadline) {
			return 0, fmt.Errorf("secure attention service unavailable; update/restart the Windows agent: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Serve is started only by the Windows service. Its protected ACL admits
// local SYSTEM sidecars only, never regular users or network clients.
func Serve(done <-chan struct{}) error {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)")
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateNamedPipe(windows.StringToUTF16Ptr(pipeName), windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE, windows.PIPE_TYPE_BYTE|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 64, 64, 5000, sa)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(stop)
	go func() {
		<-done
		_ = windows.SetEvent(stop)
	}()
	for {
		connected, err := waitForClient(h, stop)
		if err != nil {
			// Staying up matters more than this one failure: the capability is
			// announced once per session, so a broker that gives up leaves
			// Ctrl+Alt+Delete greyed out until the service restarts.
			log.Printf("sasbridge: accept: %v", err)
			windows.DisconnectNamedPipe(h)
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if !connected {
			return nil // service stopping
		}
		serveRequest(h)
		windows.DisconnectNamedPipe(h)
	}
}

// waitForClient arms ConnectNamedPipe and blocks until a client arrives or the
// service stops. It deliberately does NOT time out. There is a single pipe
// instance, and a client that finds it unarmed fails outright — CreateFile does
// not queue — so every cancel-and-rearm cycle was a window in which the
// sidecar's probe failed and the viewer disabled Ctrl+Alt+Delete for the whole
// session.
func waitForClient(h, stop windows.Handle) (bool, error) {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(event)
	o := &windows.Overlapped{HEvent: event}
	switch err := windows.ConnectNamedPipe(h, o); err {
	case nil, windows.ERROR_PIPE_CONNECTED:
		return true, nil
	case windows.ERROR_IO_PENDING:
	default:
		return false, err
	}
	var n uint32
	signalled, err := windows.WaitForMultipleObjects([]windows.Handle{event, stop}, false, windows.INFINITE)
	if err != nil {
		return false, err
	}
	if signalled == windows.WAIT_OBJECT_0 {
		return true, windows.GetOverlappedResult(h, o, &n, false)
	}
	windows.CancelIoEx(h, o)
	windows.GetOverlappedResult(h, o, &n, true)
	return false, nil
}

func serveRequest(h windows.Handle) {
	b := []byte{0}
	if transfer(h, b, false) != nil {
		return
	}
	request := b[0]
	b[0] = 0
	if (request == 0 || request == 1) && available() == nil {
		// Authenticate the peer by impersonating it, then revert BEFORE sending.
		// SendSAS(FALSE) is the documented service path: Windows requires the
		// caller to be a LocalSystem service, judged by the calling thread. Under
		// an impersonation token taken from the interactive session that is no
		// longer true, and SendSAS — which returns void — generates nothing at
		// all: the request is acknowledged and the security screen never appears.
		runtime.LockOSThread()
		if ok, _, _ := impersonate.Call(uintptr(h)); ok != 0 {
			if err := windows.RevertToSelf(); err != nil {
				panic(err)
			}
			b[0] = 1
		}
		runtime.UnlockOSThread()
		if b[0] == 1 && request == 1 {
			sendSAS.Call(0) // as the service's own LocalSystem token, in session 0
		}
	}
	if transfer(h, b, true) == nil {
		_ = transfer(h, b, false)
	}
}
