package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"runtime"
	"sync"
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
//
//	[1] flags       bit0 = keyframe
//	[2] frameW      uint16 BE (full virtual-desktop width)
//	[2] frameH      uint16 BE
//	[2] tileCount   uint16 BE
//	tileCount × tile:
//		[2] x  [2] y  [2] w  [2] h   uint16 BE (pixel rect on the frame)
//		[4] jpegLen                  uint32 BE
//		[jpegLen] JPEG bytes
const (
	tileSize          = 64
	videoFlagKeyframe = 0x01
	videoHdrLen       = 7
	tileHdrLen        = 12
)

// Relay messages are multiplexed: binary messages carry a 1-byte channel tag,
// text messages are JSON with a "t" field. Shared across OS capturers.
const (
	chVideo = 0x01 // agent -> viewer: [chVideo][tile-delta payload] (see encode)
	chFile  = 0x02 // either way:      [chFile][id][bytes]
)

// videoEncoder is single-goroutine except for forceKeyframe, which any goroutine
// may call (e.g. the reader, when a viewer (re)connects and asks for a refresh).
type videoEncoder struct {
	prev    []byte // previous frame pixels (copy), nil until first frame
	w, h    int
	quality int
	tile    int // delta-tile edge, px (RMM_REMOTE_TILE; default tileSize)
	force   atomic.Bool
	out     bytes.Buffer
	jbuf    bytes.Buffer
}

func newVideoEncoder(quality int) *videoEncoder {
	e := &videoEncoder{quality: quality, tile: encoderTileSize()}
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
		tiles = keyframeStripes(w, h)
	} else {
		ts := e.tile
		for ty := 0; ty < h; ty += ts {
			th := min(ts, h-ty)
			for tx := 0; tx < w; tx += ts {
				tw := min(ts, w-tx)
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

	jpegs, err := e.encodeJPEGs(img, tiles)
	if err != nil {
		return nil, err
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
	for i, t := range tiles {
		binary.BigEndian.PutUint16(th[0:2], uint16(t.x))
		binary.BigEndian.PutUint16(th[2:4], uint16(t.y))
		binary.BigEndian.PutUint16(th[4:6], uint16(t.w))
		binary.BigEndian.PutUint16(th[6:8], uint16(t.h))
		binary.BigEndian.PutUint32(th[8:12], uint32(len(jpegs[i])))
		e.out.Write(th[:])
		e.out.Write(jpegs[i])
	}
	return e.out.Bytes(), nil
}

// keyframeStripes splits a full-frame keyframe into horizontal stripes so the
// JPEG work spreads across cores (encodeJPEGs) and the viewer paints
// progressively instead of waiting on one whole-screen encode — the single
// biggest stall in the old pipeline. Small frames stay a single tile.
const kfStripeH = 128

func keyframeStripes(w, h int) []tileRect {
	if h <= 2*kfStripeH {
		return []tileRect{{0, 0, w, h}}
	}
	tiles := make([]tileRect, 0, (h+kfStripeH-1)/kfStripeH)
	for y := 0; y < h; y += kfStripeH {
		tiles = append(tiles, tileRect{0, y, w, min(kfStripeH, h-y)})
	}
	return tiles
}

// encodeJPEGs JPEG-encodes the tiles — in parallel when there are several (a
// keyframe's stripes, or a high-motion frame with many changed tiles). Result
// order matches tiles; the wire format is assembled by the caller.
func (e *videoEncoder) encodeJPEGs(img *image.RGBA, tiles []tileRect) ([][]byte, error) {
	min0 := img.Rect.Min
	sub := func(t tileRect) image.Image {
		return img.SubImage(image.Rect(min0.X+t.x, min0.Y+t.y, min0.X+t.x+t.w, min0.Y+t.y+t.h))
	}
	out := make([][]byte, len(tiles))
	if len(tiles) == 1 {
		e.jbuf.Reset()
		if err := jpeg.Encode(&e.jbuf, sub(tiles[0]), &jpeg.Options{Quality: e.quality}); err != nil {
			return nil, err
		}
		out[0] = e.jbuf.Bytes() // valid until the next encode() — consumed immediately
		return out, nil
	}
	workers := min(runtime.NumCPU(), len(tiles), 8)
	idx := make(chan int)
	errs := make([]error, len(tiles))
	var wg sync.WaitGroup
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf bytes.Buffer
			for i := range idx {
				buf.Reset()
				if err := jpeg.Encode(&buf, sub(tiles[i]), &jpeg.Options{Quality: e.quality}); err != nil {
					errs[i] = err
					continue
				}
				out[i] = append([]byte(nil), buf.Bytes()...)
			}
		}()
	}
	for i := range tiles {
		idx <- i
	}
	close(idx)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
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
