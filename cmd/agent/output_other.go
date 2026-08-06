//go:build !windows

package main

// systemCodePage has no meaning off Windows: those systems are UTF-8 in
// practice, so output that fails the UTF-8 check is either genuinely binary or
// from a program with an unusual locale. Reporting 0 selects the Windows-1252
// fallback, which is byte-preserving for the Latin-1 range rather than
// discarding the byte outright.
func systemCodePage() int { return 0 }
