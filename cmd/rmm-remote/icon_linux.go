//go:build linux

package main

// The chat window's icon. Windows reads the icon from the executable
// (rsrc_windows_*.syso); an X11 window has none unless it says so, and without
// _NET_WM_ICON the window manager shows a generic one in the title bar, the
// panel and the Alt+Tab list — the host user sees a nameless window asking
// them to talk to somebody.
//
// The PNGs are the product logo cut down by `make icons`. Decoding them needs
// only the standard library, which keeps the sidecar free of CGO.

import (
	"bytes"
	"embed"
	"image"
	"image/draw"
	"image/png"
	"log"
)

//go:embed icon/openrmm-16.png icon/openrmm-32.png icon/openrmm-48.png
var iconFS embed.FS

var iconFiles = []string{"icon/openrmm-16.png", "icon/openrmm-32.png", "icon/openrmm-48.png"}

// netWMIcon returns the _NET_WM_ICON value: for each size, width, height and
// then width*height pixels as ARGB, not premultiplied, row by row. Sizes that
// fail to decode are skipped; nil means no icon at all.
func netWMIcon(pngs [][]byte) []uint32 {
	var out []uint32
	for _, b := range pngs {
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			log.Printf("chat: icon: %v", err)
			continue
		}
		bounds := img.Bounds()
		// NRGBA is straight (non-premultiplied) alpha, which is what EWMH wants.
		n := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
		draw.Draw(n, n.Bounds(), img, bounds.Min, draw.Src)
		w, h := n.Bounds().Dx(), n.Bounds().Dy()
		out = append(out, uint32(w), uint32(h))
		for i := 0; i < len(n.Pix); i += 4 {
			r, g, b, a := uint32(n.Pix[i]), uint32(n.Pix[i+1]), uint32(n.Pix[i+2]), uint32(n.Pix[i+3])
			out = append(out, a<<24|r<<16|g<<8|b)
		}
	}
	return out
}

func embeddedIcon() []uint32 {
	var pngs [][]byte
	for _, name := range iconFiles {
		if b, err := iconFS.ReadFile(name); err == nil {
			pngs = append(pngs, b)
		}
	}
	return netWMIcon(pngs)
}

// changePropCardinals replaces a window property with a CARDINAL[] value.
func changePropCardinals(win, prop uint32, vals []uint32) []byte {
	body := 24 + 4*len(vals)
	req := make([]byte, body)
	req[0] = opChangeProperty
	req[1] = 0 // Replace
	le.PutUint16(req[2:], uint16(body/4))
	le.PutUint32(req[4:], win)
	le.PutUint32(req[8:], prop)
	le.PutUint32(req[12:], xaCardinal)
	req[16] = 32 // format
	le.PutUint32(req[20:], uint32(len(vals)))
	for i, v := range vals {
		le.PutUint32(req[24+4*i:], v)
	}
	return req
}
