package main

import (
	"encoding/binary"
	"image"
	"testing"
)

func solid(w, h int, r, g, b byte) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, b, 255
	}
	return img
}

// parseHeader reads flags, frame size and tile count from a chVideo payload.
func parseHeader(t *testing.T, p []byte) (key bool, w, h, tiles int) {
	t.Helper()
	if len(p) < videoHdrLen {
		t.Fatalf("payload too short: %d", len(p))
	}
	return p[0]&videoFlagKeyframe != 0,
		int(binary.BigEndian.Uint16(p[1:3])),
		int(binary.BigEndian.Uint16(p[3:5])),
		int(binary.BigEndian.Uint16(p[5:7]))
}

func TestEncoderFirstFrameIsKeyframe(t *testing.T) {
	e := newVideoEncoder(60)
	img := solid(320, 240, 10, 20, 30)
	p, err := e.encode(img)
	if err != nil {
		t.Fatal(err)
	}
	key, w, h, tiles := parseHeader(t, p)
	if !key || w != 320 || h != 240 || tiles != 1 {
		t.Fatalf("keyframe=%v w=%d h=%d tiles=%d, want true/320/240/1", key, w, h, tiles)
	}
}

func TestEncoderUnchangedFrameEmitsNothing(t *testing.T) {
	e := newVideoEncoder(60)
	img := solid(256, 256, 5, 5, 5)
	if _, err := e.encode(img); err != nil {
		t.Fatal(err)
	}
	// Same pixels again → no tiles changed → nil payload.
	p, err := e.encode(solid(256, 256, 5, 5, 5))
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("expected nil payload for unchanged frame, got %d bytes", len(p))
	}
}

func TestEncoderSendsOnlyChangedTiles(t *testing.T) {
	e := newVideoEncoder(60)
	const w, h = 320, 320 // 5×5 tiles of 64px
	if _, err := e.encode(solid(w, h, 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	// Flip one pixel inside the tile at column 3, row 2 (px 200,150).
	img := solid(w, h, 0, 0, 0)
	off := 150*img.Stride + 200*4
	img.Pix[off] = 255
	p, err := e.encode(img)
	if err != nil {
		t.Fatal(err)
	}
	key, _, _, tiles := parseHeader(t, p)
	if key {
		t.Fatal("delta frame should not be a keyframe")
	}
	if tiles != 1 {
		t.Fatalf("expected exactly 1 changed tile, got %d", tiles)
	}
	x := int(binary.BigEndian.Uint16(p[videoHdrLen : videoHdrLen+2]))
	y := int(binary.BigEndian.Uint16(p[videoHdrLen+2 : videoHdrLen+4]))
	if x != 192 || y != 128 { // tile origin of column 3 / row 2
		t.Fatalf("changed tile at (%d,%d), want (192,128)", x, y)
	}
}

// TestEncoderKeyframeStripes: a tall frame's keyframe is split into horizontal
// stripes (parallel encode + progressive paint) that exactly tile the screen.
func TestEncoderKeyframeStripes(t *testing.T) {
	e := newVideoEncoder(60)
	const w, h = 320, 600 // > 2*kfStripeH → striped
	p, err := e.encode(solid(w, h, 40, 80, 120))
	if err != nil {
		t.Fatal(err)
	}
	key, fw, fh, tiles := parseHeader(t, p)
	want := (h + kfStripeH - 1) / kfStripeH
	if !key || fw != w || fh != h || tiles != want {
		t.Fatalf("keyframe=%v w=%d h=%d tiles=%d, want true/%d/%d/%d", key, fw, fh, tiles, w, h, want)
	}
	// Walk the tiles: contiguous full-width stripes covering 0..h.
	off, nextY := videoHdrLen, 0
	for i := 0; i < tiles; i++ {
		x := int(binary.BigEndian.Uint16(p[off : off+2]))
		y := int(binary.BigEndian.Uint16(p[off+2 : off+4]))
		tw := int(binary.BigEndian.Uint16(p[off+4 : off+6]))
		th := int(binary.BigEndian.Uint16(p[off+6 : off+8]))
		jl := int(binary.BigEndian.Uint32(p[off+8 : off+12]))
		if x != 0 || y != nextY || tw != w || jl == 0 {
			t.Fatalf("stripe %d: x=%d y=%d w=%d jpegLen=%d, want 0/%d/%d/>0", i, x, y, tw, jl, nextY, w)
		}
		nextY += th
		off += tileHdrLen + jl
	}
	if nextY != h || off != len(p) {
		t.Fatalf("stripes cover %d px and %d payload bytes, want %d/%d", nextY, off, h, len(p))
	}
}

func TestEncoderForceKeyframe(t *testing.T) {
	e := newVideoEncoder(60)
	img := solid(128, 128, 7, 7, 7)
	if _, err := e.encode(img); err != nil {
		t.Fatal(err)
	}
	e.forceKeyframe()
	p, err := e.encode(solid(128, 128, 7, 7, 7)) // identical, but forced
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("forceKeyframe should emit a frame even when pixels are unchanged")
	}
	if key, _, _, tiles := parseHeader(t, p); !key || tiles != 1 {
		t.Fatalf("forced keyframe=%v tiles=%d, want true/1", key, tiles)
	}
}
