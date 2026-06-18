//go:build windows

package main

import (
	"encoding/json"
	"io"
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
	Mod  string `json:"mod"`
}

// onList answers a remote directory listing (empty path => drive roots), so the
// viewer can offer a two-pane file browser instead of typing paths.
func (s *session) onList(data []byte) {
	var r getReq
	if json.Unmarshal(data, &r) != nil {
		return
	}
	s.sendListing(r.ID, r.Path)
}

// sendListing pushes a directory listing (empty path => drive roots) to the
// viewer. Also used to refresh after a file operation.
func (s *session) sendListing(id uint8, path string) {
	entries := []fsEntry{}
	if path == "" {
		for _, d := range logicalDrives() {
			entries = append(entries, fsEntry{Name: d, Dir: true})
		}
	} else {
		items, err := os.ReadDir(path)
		if err != nil {
			s.sendJSON(map[string]any{"t": "ferr", "id": id, "err": err.Error()})
			return
		}
		for _, it := range items {
			e := fsEntry{Name: it.Name(), Dir: it.IsDir()}
			if info, ie := it.Info(); ie == nil {
				e.Size = info.Size()
				e.Mod = info.ModTime().Format("2006-01-02 15:04")
			}
			entries = append(entries, e)
		}
	}
	s.sendJSON(map[string]any{"t": "flist", "id": id, "path": path, "entries": entries})
}

type opReq struct {
	ID   uint8  `json:"id"`
	Path string `json:"path"`
	Src  string `json:"src"`
	Dst  string `json:"dst"`
}

func (s *session) fileOp(data []byte, do func(opReq) error, refreshDir func(opReq) string) {
	var r opReq
	if json.Unmarshal(data, &r) != nil {
		return
	}
	if err := do(r); err != nil {
		s.sendJSON(map[string]any{"t": "ferr", "id": r.ID, "err": err.Error()})
		return
	}
	s.sendListing(r.ID, refreshDir(r))
}

func (s *session) onMkdir(data []byte) {
	s.fileOp(data, func(r opReq) error { return os.MkdirAll(r.Path, 0o755) }, func(r opReq) string { return filepath.Dir(r.Path) })
}
func (s *session) onDelete(data []byte) {
	s.fileOp(data, func(r opReq) error { return os.RemoveAll(r.Path) }, func(r opReq) string { return filepath.Dir(r.Path) })
}
func (s *session) onRename(data []byte) {
	s.fileOp(data, func(r opReq) error { return os.Rename(r.Src, r.Dst) }, func(r opReq) string { return filepath.Dir(r.Dst) })
}
func (s *session) onCopy(data []byte) {
	s.fileOp(data, func(r opReq) error { return copyPath(r.Src, r.Dst) }, func(r opReq) string { return filepath.Dir(r.Dst) })
}
func (s *session) onMove(data []byte) {
	s.fileOp(data, func(r opReq) error {
		if err := os.Rename(r.Src, r.Dst); err == nil {
			return nil
		}
		if err := copyPath(r.Src, r.Dst); err != nil { // cross-volume fallback
			return err
		}
		return os.RemoveAll(r.Src)
	}, func(r opReq) string { return filepath.Dir(r.Dst) })
}

// copyPath copies a file or directory tree from src to dst.
func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst)
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	items, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := copyPath(filepath.Join(src, it.Name()), filepath.Join(dst, it.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
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
