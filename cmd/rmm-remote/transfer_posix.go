//go:build linux || darwin

package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/gorilla/websocket"
)

// File transfer for POSIX remote sessions (Linux + macOS) — the counterpart of
// transfer_windows.go. It speaks the same wire protocol (JSON control messages +
// binary [chFile][id][bytes] chunks) so the same browser viewer drives all
// platforms. The only difference from Windows is path semantics: there are no
// drive letters, so an empty path lists "/". Pure os/filepath, no OS-specific
// syscalls, so it is shared verbatim. Free-path; gated by the remote session.

const maxTransferBytes = 2 << 30 // 2 GiB guard

// fsEntry is one directory entry sent to the viewer's file browser.
type fsEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
	Mod  string `json:"mod"`
}

type opReq struct {
	ID   uint8  `json:"id"`
	Path string `json:"path"`
	Src  string `json:"src"`
	Dst  string `json:"dst"`
}
type getReq struct {
	ID   uint8  `json:"id"`
	Path string `json:"path"`
}
type putReq struct {
	ID   uint8  `json:"id"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// fileXfer handles the file-transfer channel of a Linux remote session. send
// enqueues a frame on the media out channel (single-writer); uploads tracks
// in-flight viewer->agent transfers by id.
type fileXfer struct {
	send    func(mt int, b []byte)
	mu      sync.Mutex
	uploads map[uint8]*os.File
}

func newFileXfer(send func(mt int, b []byte)) *fileXfer {
	return &fileXfer{send: send, uploads: map[uint8]*os.File{}}
}

func (s *fileXfer) sendJSON(v any) {
	if b, e := json.Marshal(v); e == nil {
		s.send(websocket.TextMessage, b)
	}
}

// dispatch handles a JSON control message; returns false if t isn't a file op.
// Slow/streaming ops (listing, download, tree copy/move) run off-goroutine so
// they don't stall the reader; upload start/end stay synchronous so the file is
// registered before its binary chunks arrive.
func (s *fileXfer) dispatch(t string, data []byte) bool {
	switch t {
	case "fls":
		go s.onList(data)
	case "fget":
		go s.onDownload(data)
	case "fput":
		s.onUploadStart(data)
	case "fend":
		s.onUploadEnd(data)
	case "fmkdir":
		go s.fileOp(data, func(r opReq) error { return os.MkdirAll(r.Path, 0o755) }, func(r opReq) string { return filepath.Dir(r.Path) })
	case "fdel":
		go s.fileOp(data, func(r opReq) error { return os.RemoveAll(r.Path) }, func(r opReq) string { return filepath.Dir(r.Path) })
	case "frename":
		go s.fileOp(data, func(r opReq) error { return os.Rename(r.Src, r.Dst) }, func(r opReq) string { return filepath.Dir(r.Dst) })
	case "fcopy":
		go s.fileOp(data, func(r opReq) error { return copyPath(r.Src, r.Dst) }, func(r opReq) string { return filepath.Dir(r.Dst) })
	case "fmove":
		go s.fileOp(data, func(r opReq) error {
			if os.Rename(r.Src, r.Dst) == nil {
				return nil
			}
			if err := copyPath(r.Src, r.Dst); err != nil { // cross-device fallback
				return err
			}
			return os.RemoveAll(r.Src)
		}, func(r opReq) string { return filepath.Dir(r.Dst) })
	default:
		return false
	}
	return true
}

func (s *fileXfer) onList(data []byte) {
	var r getReq
	if json.Unmarshal(data, &r) != nil {
		return
	}
	s.sendListing(r.ID, r.Path)
}

func (s *fileXfer) sendListing(id uint8, path string) {
	if path == "" {
		path = "/" // Linux has no drive list; the filesystem root is the top.
	}
	items, err := os.ReadDir(path)
	if err != nil {
		s.sendJSON(map[string]any{"t": "ferr", "id": id, "err": err.Error()})
		return
	}
	entries := []fsEntry{}
	for _, it := range items {
		e := fsEntry{Name: it.Name(), Dir: it.IsDir()}
		if info, ie := it.Info(); ie == nil {
			e.Size = info.Size()
			e.Mod = info.ModTime().Format("2006-01-02 15:04")
			if info.Mode()&os.ModeSymlink != 0 {
				// Resolve symlinks so directory links are navigable.
				if st, se := os.Stat(filepath.Join(path, it.Name())); se == nil {
					e.Dir = st.IsDir()
					e.Size = st.Size()
				}
			}
		}
		entries = append(entries, e)
	}
	s.sendJSON(map[string]any{"t": "flist", "id": id, "path": path, "entries": entries})
}

func (s *fileXfer) fileOp(data []byte, do func(opReq) error, refreshDir func(opReq) string) {
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

func (s *fileXfer) onUploadStart(data []byte) {
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
	s.mu.Lock()
	s.uploads[r.ID] = f
	s.mu.Unlock()
}

func (s *fileXfer) onChunk(id uint8, chunk []byte) {
	s.mu.Lock()
	f := s.uploads[id]
	s.mu.Unlock()
	if f != nil {
		_, _ = f.Write(chunk)
	}
}

func (s *fileXfer) onUploadEnd(data []byte) {
	var r struct {
		ID uint8 `json:"id"`
	}
	if json.Unmarshal(data, &r) != nil {
		return
	}
	s.mu.Lock()
	f := s.uploads[r.ID]
	delete(s.uploads, r.ID)
	s.mu.Unlock()
	if f != nil {
		_ = f.Close()
		s.sendJSON(map[string]any{"t": "fend", "id": r.ID, "ok": true})
	}
}

func (s *fileXfer) onDownload(data []byte) {
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
