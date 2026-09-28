package main

import (
	"debug/pe"
	"encoding/binary"
	"testing"
)

// The Windows executables take their icon from these objects; the chat window
// loads icon group 1 from them, and Programs and Features points at the
// agent's icon 0. Without them every one of those shows Windows' blank icon,
// which is how the logo went missing before. Regenerate with `make icons`.
func TestWindowsResourceObjectsCarryTheIcon(t *testing.T) {
	for _, path := range []string{
		"rsrc_windows_amd64.syso", "rsrc_windows_arm64.syso",
		"../agent/rsrc_windows_amd64.syso", "../agent/rsrc_windows_arm64.syso",
	} {
		f, err := pe.Open(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		sec := f.Section(".rsrc")
		if sec == nil {
			t.Errorf("%s has no .rsrc section", path)
			f.Close()
			continue
		}
		data, err := sec.Data()
		f.Close()
		if err != nil || len(data) < 16 {
			t.Errorf("%s: unreadable .rsrc: %v", path, err)
			continue
		}
		if !hasResource(data, 14, 1) { // RT_GROUP_ICON, id 1
			t.Errorf("%s has no icon group 1", path)
		}
	}
}

// hasResource walks the first two levels of a resource directory: type, then
// numeric id.
func hasResource(d []byte, typ, id uint32) bool {
	entries := func(off uint32) [][2]uint32 {
		if int(off)+16 > len(d) {
			return nil
		}
		n := int(binary.LittleEndian.Uint16(d[off+12:])) + int(binary.LittleEndian.Uint16(d[off+14:]))
		out := [][2]uint32{}
		for k := 0; k < n && int(off)+16+8*k+8 <= len(d); k++ {
			e := d[int(off)+16+8*k:]
			out = append(out, [2]uint32{binary.LittleEndian.Uint32(e), binary.LittleEndian.Uint32(e[4:])})
		}
		return out
	}
	for _, t := range entries(0) {
		if t[0] == typ && t[1]&0x80000000 != 0 {
			for _, n := range entries(t[1] & 0x7fffffff) {
				if n[0] == id {
					return true
				}
			}
		}
	}
	return false
}
