//go:build windows

package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/gorilla/websocket"
)

// File transfer rides the relay alongside video and chat. Control messages are
// JSON (fput/fget/fmeta/fend/ferr); file bytes are binary [chFile][id][data].
// The transfer is free-path (the technician chose flexibility over a sandbox);
// it is still gated by the remote session itself and every transfer is logged.

const maxTransferBytes = 2 << 30 // 2 GiB guard

var procGetLogicalDrives = kernel32.NewProc("GetLogicalDrives")

// logicalDrives returns the available drive roots ("C:\\", ...).
func logicalDrives() []string {
	mask, _, _ := procGetLogicalDrives.Call()
	var out []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, string(rune('A'+i))+":\\")
		}
	}
	return out
}

type fsEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

// onList answers a remote directory listing (empty path => drive roots), so the
// viewer can offer a RustDesk-style file browser instead of typing paths.
func (s *session) onList(data []byte) {
	var r getReq
	if json.Unmarshal(data, &r) != nil {
		return
	}
	entries := []fsEntry{}
	if r.Path == "" {
		for _, d := range logicalDrives() {
			entries = append(entries, fsEntry{Name: d, Dir: true})
		}
	} else {
		items, err := os.ReadDir(r.Path)
		if err != nil {
			s.sendJSON(map[string]any{"t": "ferr", "id": r.ID, "err": err.Error()})
			return
		}
		for _, it := range items {
			var sz int64
			if info, e := it.Info(); e == nil {
				sz = info.Size()
			}
			entries = append(entries, fsEntry{Name: it.Name(), Dir: it.IsDir(), Size: sz})
		}
	}
	s.sendJSON(map[string]any{"t": "flist", "id": r.ID, "path": r.Path, "entries": entries})
}

type putReq struct {
	ID   uint8  `json:"id"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type getReq struct {
	ID   uint8  `json:"id"`
	Path string `json:"path"`
}

// onUploadStart opens the destination file for a viewer->agent upload.
func (s *session) onUploadStart(data []byte) {
	var r putReq
	if json.Unmarshal(data, &r) != nil || r.Path == "" {
		return
	}
	if r.Size > maxTransferBytes {
		s.sendJSON(map[string]any{"t": "ferr", "id": r.ID, "err": "file too large"})
		return
	}
	if dir := filepath.Dir(r.Path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.Create(r.Path)
	if err != nil {
		s.sendJSON(map[string]any{"t": "ferr", "id": r.ID, "err": err.Error()})
		return
	}
	log.Printf("transfer: receiving upload -> %s", r.Path)
	s.mu.Lock()
	s.uploads[r.ID] = f
	s.mu.Unlock()
}

func (s *session) onUploadChunk(id uint8, chunk []byte) {
	s.mu.Lock()
	f := s.uploads[id]
	s.mu.Unlock()
	if f != nil {
		_, _ = f.Write(chunk)
	}
}

func (s *session) onUploadEnd(id uint8) {
	s.mu.Lock()
	f := s.uploads[id]
	delete(s.uploads, id)
	s.mu.Unlock()
	if f != nil {
		_ = f.Close()
		log.Printf("transfer: upload %d complete", id)
		s.sendJSON(map[string]any{"t": "fend", "id": id, "ok": true})
	}
}

// onDownload streams a remote file to the viewer (agent->viewer).
func (s *session) onDownload(data []byte) {
	var r getReq
	if json.Unmarshal(data, &r) != nil || r.Path == "" {
		return
	}
	f, err := os.Open(r.Path)
	if err != nil {
		s.sendJSON(map[string]any{"t": "ferr", "id": r.ID, "err": err.Error()})
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		s.sendJSON(map[string]any{"t": "ferr", "id": r.ID, "err": "not a file"})
		return
	}
	log.Printf("transfer: sending download <- %s (%d bytes)", r.Path, fi.Size())
	s.sendJSON(map[string]any{"t": "fmeta", "id": r.ID, "name": filepath.Base(r.Path), "size": fi.Size()})

	buf := make([]byte, 64*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			frame := make([]byte, 0, n+2)
			frame = append(frame, chFile, r.ID)
			frame = append(frame, buf[:n]...)
			s.send(websocket.BinaryMessage, frame)
		}
		if rerr != nil {
			break
		}
	}
	s.sendJSON(map[string]any{"t": "fend", "id": r.ID, "ok": true})
}
