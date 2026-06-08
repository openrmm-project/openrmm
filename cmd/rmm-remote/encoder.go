package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"sync/atomic"
)

// Tile-delta video codec — CGO-free and OS-agnostic. It operates purely on a
// captured *image.RGBA, so any per-OS capturer (Windows GDI today, X11 next)
// feeds the SAME encoder without a fork. Each frame is split into a grid of
// tiles; only the tiles whose pixels changed since the previous frame are
// JPEG-encoded and sent. A keyframe sends the whole screen as a single JPEG.
// For the typical mostly-static desktop this cuts bandwidth by 1-2 orders of
// magnitude (and lets the frame rate rise without saturating the link).
//
// chVideo payload (after the 1-byte channel tag):
//	[1] flags       bit0 = keyframe
//	[2] frameW      uint16 BE (full virtual-desktop width)
//	[2] frameH      uint16 BE
//	[2] tileCount   uint16 BE
//	tileCount × tile:
//		[2] x  [2] y  [2] w  [2] h   uint16 BE (pixel rect on the frame)
//		[4] jpegLen                  uint32 BE
//		[jpegLen] JPEG bytes
const (
	tileSize   = 64
	videoFlagKeyframe = 0x01
	videoHdrLen       = 7
	tileHdrLen        = 12
)

// videoEncoder is single-goroutine except for forceKeyframe, which any goroutine
// may call (e.g. the reader, when a viewer (re)connects and asks for a refresh).
type videoEncoder struct {
	prev    []byte // previous frame pixels (copy), nil until first frame
	w, h    int
	quality int
	force   atomic.Bool
	out     bytes.Buffer
	jbuf    bytes.Buffer
}

func newVideoEncoder(quality int) *videoEncoder {
	e := &videoEncoder{quality: quality}
	e.force.Store(true)
	return e
}

// reset forces the next frame to be a full keyframe and drops the prior frame
// (call when the capture source changes — resolution, desktop, monitor).
func (e *videoEncoder) reset() { e.prev = nil; e.force.Store(true) }

// forceKeyframe makes the next encode() emit a full-screen keyframe.
func (e *videoEncoder) forceKeyframe() { e.force.Store(true) }

type tileRect struct{ x, y, w, h int }

// encode returns the chVideo payload (without the channel tag) for this frame,
// or nil if nothing changed since the previous frame.
func (e *videoEncoder) encode(img *image.RGBA) ([]byte, error) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	stride := img.Stride
	pix := img.Pix

	key := e.force.Swap(false)
	if e.prev == nil || e.w != w || e.h != h || len(e.prev) != len(pix) {
		key = true
		e.w, e.h = w, h
		e.prev = make([]byte, len(pix))
	}

	var tiles []tileRect
	if key {
		tiles = []tileRect{{0, 0, w, h}}
	} else {
		for ty := 0; ty < h; ty += tileSize {
			th := min(tileSize, h-ty)
			for tx := 0; tx < w; tx += tileSize {
				tw := min(tileSize, w-tx)
				if e.tileChanged(pix, stride, tx, ty, tw, th) {
					tiles = append(tiles, tileRect{tx, ty, tw, th})
				}
			}
		}
	}
	// Snapshot this frame for the next diff regardless of what we send.
	copy(e.prev, pix)

	if len(tiles) == 0 {
		return nil, nil
	}

	e.out.Reset()
	var hdr [videoHdrLen]byte
	if key {
		hdr[0] = videoFlagKeyframe
	}
	binary.BigEndian.PutUint16(hdr[1:3], uint16(w))
	binary.BigEndian.PutUint16(hdr[3:5], uint16(h))
	binary.BigEndian.PutUint16(hdr[5:7], uint16(len(tiles)))
	e.out.Write(hdr[:])

	var th [tileHdrLen]byte
	for _, t := range tiles {
		min0 := img.Rect.Min
		sub := img.SubImage(image.Rect(min0.X+t.x, min0.Y+t.y, min0.X+t.x+t.w, min0.Y+t.y+t.h))
		e.jbuf.Reset()
		if err := jpeg.Encode(&e.jbuf, sub, &jpeg.Options{Quality: e.quality}); err != nil {
			return nil, err
		}
		binary.BigEndian.PutUint16(th[0:2], uint16(t.x))
		binary.BigEndian.PutUint16(th[2:4], uint16(t.y))
		binary.BigEndian.PutUint16(th[4:6], uint16(t.w))
		binary.BigEndian.PutUint16(th[6:8], uint16(t.h))
		binary.BigEndian.PutUint32(th[8:12], uint32(e.jbuf.Len()))
		e.out.Write(th[:])
		e.out.Write(e.jbuf.Bytes())
	}
	return e.out.Bytes(), nil
}

// tileChanged reports whether any row of the tile differs from the prev frame.
func (e *videoEncoder) tileChanged(pix []byte, stride, tx, ty, tw, th int) bool {
	rowBytes := tw * 4
	for y := 0; y < th; y++ {
		off := (ty+y)*stride + tx*4
		if !bytes.Equal(pix[off:off+rowBytes], e.prev[off:off+rowBytes]) {
			return true
		}
	}
	return false
}
