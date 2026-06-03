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
