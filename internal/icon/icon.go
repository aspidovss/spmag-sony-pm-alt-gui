// Package icon draws the application icon (a camera with Wi-Fi arcs) so no
// binary image files are needed in the repository.
package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Accent colours.
var (
	Body   = color.NRGBA{0x1F, 0x2A, 0x37, 0xFF}
	Lens   = color.NRGBA{0x2E, 0x9C, 0xCA, 0xFF}
	Signal = color.NRGBA{0x2E, 0x9C, 0xCA, 0xFF}
	Busy   = color.NRGBA{0xF2, 0x9D, 0x38, 0xFF}
)

// Draw renders the icon at size×size. accent colours the lens and arcs.
func Draw(size int, accent color.NRGBA) *image.NRGBA {
	const ss = 4 // supersampling
	n := size * ss
	big := make([]color.NRGBA, n*n)
	s := float64(n)
	set := func(x, y int, c color.NRGBA) {
		if x >= 0 && y >= 0 && x < n && y < n {
			big[y*n+x] = c
		}
	}
	roundRect := func(x0, y0, x1, y1, r float64, c color.NRGBA) {
		for y := int(y0); y < int(y1); y++ {
			for x := int(x0); x < int(x1); x++ {
				fx, fy := float64(x)+.5, float64(y)+.5
				dx := math.Max(math.Max(x0+r-fx, fx-(x1-r)), 0)
				dy := math.Max(math.Max(y0+r-fy, fy-(y1-r)), 0)
				if dx*dx+dy*dy <= r*r {
					set(x, y, c)
				}
			}
		}
	}
	circle := func(cx, cy, r float64, c color.NRGBA) {
		for y := int(cy - r); y <= int(cy+r); y++ {
			for x := int(cx - r); x <= int(cx+r); x++ {
				fx, fy := float64(x)+.5-cx, float64(y)+.5-cy
				if fx*fx+fy*fy <= r*r {
					set(x, y, c)
				}
			}
		}
	}
	arc := func(cx, cy, r, w float64, c color.NRGBA) {
		for y := int(cy - r - w); y <= int(cy); y++ {
			for x := int(cx - r - w); x <= int(cx+r+w); x++ {
				fx, fy := float64(x)+.5-cx, float64(y)+.5-cy
				d := math.Hypot(fx, fy)
				ang := math.Atan2(-fy, fx) * 180 / math.Pi
				if math.Abs(d-r) <= w/2 && ang > 40 && ang < 140 {
					set(x, y, c)
				}
			}
		}
	}
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	// Wi-Fi arcs above the camera
	arc(s*0.5, s*0.40, s*0.30, s*0.075, accent)
	arc(s*0.5, s*0.40, s*0.17, s*0.075, accent)
	// camera body
	roundRect(s*0.30, s*0.36, s*0.52, s*0.46, s*0.03, Body) // prism hump
	roundRect(s*0.06, s*0.42, s*0.94, s*0.94, s*0.09, Body)
	circle(s*0.5, s*0.67, s*0.205, white)
	circle(s*0.5, s*0.67, s*0.16, accent)
	circle(s*0.44, s*0.61, s*0.045, white)
	roundRect(s*0.76, s*0.48, s*0.88, s*0.54, s*0.02, white)

	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for j := 0; j < ss; j++ {
				for i := 0; i < ss; i++ {
					c := big[(y*ss+j)*n+x*ss+i]
					fa := float64(c.A) / 255
					r += float64(c.R) * fa
					g += float64(c.G) * fa
					b += float64(c.B) * fa
					a += fa
				}
			}
			if a > 0 {
				out.SetNRGBA(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / (ss * ss) * 255)})
			}
		}
	}
	return out
}

// ICO encodes PNG-compressed icon images of the given sizes into .ico format.
func ICO(sizes []int, accent color.NRGBA) ([]byte, error) {
	var imgs [][]byte
	for _, sz := range sizes {
		var b bytes.Buffer
		if err := png.Encode(&b, Draw(sz, accent)); err != nil {
			return nil, err
		}
		imgs = append(imgs, b.Bytes())
	}
	var out bytes.Buffer
	w := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	w(uint16(0))
	w(uint16(1))
	w(uint16(len(sizes)))
	off := 6 + 16*len(sizes)
	for i, sz := range sizes {
		d := uint8(sz)
		if sz >= 256 {
			d = 0
		}
		w(d)
		w(d)
		w(uint8(0))
		w(uint8(0))
		w(uint16(1))
		w(uint16(32))
		w(uint32(len(imgs[i])))
		w(uint32(off))
		off += len(imgs[i])
	}
	for _, b := range imgs {
		out.Write(b)
	}
	return out.Bytes(), nil
}
