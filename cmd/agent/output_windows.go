package main

import "golang.org/x/sys/windows"

// systemCodePage reports the code page a redirected child most likely wrote in.
//
// The console output page comes first: cmd and Windows PowerShell inherit it,
// and it is what `chcp` shows. It is 0 when the process has no console (the
// agent runs as a service), and then the ANSI page is the better guess — it is
// what Python's locale.getpreferredencoding() resolves to.
func systemCodePage() int {
	if cp, err := windows.GetConsoleOutputCP(); err == nil && cp != 0 {
		return int(cp)
	}
	return int(windows.GetACP())
}

// GetOEMCP is not wrapped by x/sys/windows.
var procGetOEMCP = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetOEMCP")

// consoleCodePage reports the page cmd and Windows PowerShell write their
// redirected output in.
//
// With a console of its own, the agent's children inherit it and its page.
// Without one (the agent runs as a service), Windows gives each console child a
// new console, which starts on the OEM page, not the ANSI one. On a pt-BR
// Windows 11 that is 850 against 1252: PowerShell reported
// [Console]::OutputEncoding as ibm850, and reading its bytes as 1252 turned
// "Instalação, café" into "Instala‡Æo, caf‚".
func consoleCodePage() int {
	if cp, err := windows.GetConsoleOutputCP(); err == nil && cp != 0 {
		return int(cp)
	}
	if procGetOEMCP.Find() == nil {
		if cp, _, _ := procGetOEMCP.Call(); cp != 0 {
			return int(cp)
		}
	}
	return systemCodePage()
}
