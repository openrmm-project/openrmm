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
