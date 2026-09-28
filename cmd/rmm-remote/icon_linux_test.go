//go:build linux

package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestNetWMIconIsStraightARGBPerSize(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 0x11, G: 0x22, B: 0x33, A: 0x80}) // half-transparent
	img.SetNRGBA(1, 0, color.NRGBA{})                                   // fully transparent
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	got := netWMIcon([][]byte{buf.Bytes(), []byte("not a png")})
	// Premultiplying would turn 0x11 into 0x08: the icon would come out dark.
	want := []uint32{2, 1, 0x80112233, 0}
	if len(got) != len(want) {
		t.Fatalf("got %d values, want %d (a bad size must be skipped, not break the rest): %x", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("value %d = %#x, want %#x", i, got[i], want[i])
		}
	}
}

func TestEmbeddedIconShipsEverySizeAndFitsOneRequest(t *testing.T) {
	icon := embeddedIcon()
	sizes := []uint32{}
	for i := 0; i+1 < len(icon); {
		w, h := icon[i], icon[i+1]
		if w != h {
			t.Fatalf("icon %dx%d is not square", w, h)
		}
		sizes = append(sizes, w)
		i += 2 + int(w*h)
	}
	if len(sizes) != 3 || sizes[0] != 16 || sizes[1] != 32 || sizes[2] != 48 {
		t.Fatalf("embedded sizes = %v, want [16 32 48]", sizes)
	}
	// Core X request length is 16 bits of 4-byte units; past that the server
	// needs BIG-REQUESTS, which this client does not speak.
	req := changePropCardinals(1, 2, icon)
	if len(req)/4 > 0xffff || int(req[2])|int(req[3])<<8 != len(req)/4 {
		t.Fatalf("request of %d bytes does not fit or misstates its length", len(req))
	}
}
