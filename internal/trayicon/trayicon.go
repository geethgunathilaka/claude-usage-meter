// Package trayicon draws the tray icon: a rounded badge in the level colour
// (Claude clay, amber, red) with the percentage in large bold digits, or the
// Claude logo when there is no data. Frames are rendered at every size Windows
// uses for tray icons so nothing is scaled down, and encoded as a .ico.
package trayicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"strconv"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"claude-usage-meter/internal/logo"
)

// Level selects the badge colour.
type Level int

const (
	OK Level = iota
	Warn
	Alert
	Unknown
)

var (
	colDark  = color.RGBA{0x26, 0x26, 0x24, 0xff}
	colText  = color.RGBA{0xff, 0xff, 0xff, 0xff}
	colClay  = color.RGBA{0xd9, 0x77, 0x57, 0xff}
	colLevel = map[Level]color.RGBA{
		OK:      colClay,
		Warn:    {0xe8, 0x92, 0x1e, 0xff},
		Alert:   {0xd9, 0x3a, 0x32, 0xff},
		Unknown: colDark,
	}
)

// Sizes Windows picks from for the notification area at 100%-300% scaling.
var sizes = []int{16, 20, 24, 32, 40, 48, 64}

var (
	fontOnce sync.Once
	boldFont *opentype.Font
)

func bold() *opentype.Font {
	fontOnce.Do(func() {
		win := os.Getenv("WINDIR")
		if win == "" {
			win = `C:\Windows`
		}
		for _, p := range []string{win + `\Fonts\segoeuib.ttf`, win + `\Fonts\seguisb.ttf`} {
			if b, err := os.ReadFile(p); err == nil {
				if f, err := opentype.Parse(b); err == nil {
					boldFont = f
					return
				}
			}
		}
		boldFont, _ = opentype.Parse(gobold.TTF)
	})
	return boldFont
}

// Render returns .ico bytes. percent < 0 means unknown (shows the Claude logo).
func Render(percent float64, level Level) []byte {
	var frames []*image.RGBA
	for _, s := range sizes {
		frames = append(frames, draw(s, percent, level))
	}
	return encodeICO(frames...)
}

func label(percent float64) string {
	switch {
	case percent < 0:
		return ""
	case percent >= 100:
		return "!"
	}
	return strconv.Itoa(int(percent))
}

func draw(size int, percent float64, level Level) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	r := s * 0.24
	bg := colLevel[level]
	if percent < 0 {
		bg = colDark
	}
	fillRoundRect(img, 0, 0, s, s, r, bg)

	txt := label(percent)
	if txt == "" {
		drawLogo(img, s*0.78, colClay)
		return img
	}
	// Largest bold size whose digits fit in ~94% of the width and ~78% of the height.
	f := bold()
	px := s * 1.05
	var fc font.Face
	var bounds fixed.Rectangle26_6
	for ; px > 4; px -= 0.5 {
		fc, _ = opentype.NewFace(f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
		bounds, _ = font.BoundString(fc, txt)
		w := float64(bounds.Max.X-bounds.Min.X) / 64
		h := float64(bounds.Max.Y-bounds.Min.Y) / 64
		if w <= s*0.94 && h <= s*0.78 {
			break
		}
		fc.Close()
	}
	w := float64(bounds.Max.X-bounds.Min.X) / 64
	h := float64(bounds.Max.Y-bounds.Min.Y) / 64
	x := (s-w)/2 - float64(bounds.Min.X)/64
	y := (s-h)/2 - float64(bounds.Min.Y)/64
	d := &font.Drawer{Dst: img, Src: image.NewUniform(colText), Face: fc,
		Dot: fixed.Point26_6{X: fixed.Int26_6(math.Round(x * 64)), Y: fixed.Int26_6(math.Round(y * 64))}}
	d.DrawString(txt)
	fc.Close()
	return img
}

func blend(img *image.RGBA, x, y int, c color.RGBA, cov float64) {
	if cov <= 0 || !(image.Point{x, y}.In(img.Rect)) {
		return
	}
	a := float64(c.A) / 255 * cov
	i := img.PixOffset(x, y)
	p := img.Pix[i : i+4 : i+4]
	p[0] = uint8(float64(c.R)*a + float64(p[0])*(1-a))
	p[1] = uint8(float64(c.G)*a + float64(p[1])*(1-a))
	p[2] = uint8(float64(c.B)*a + float64(p[2])*(1-a))
	p[3] = uint8(255*a + float64(p[3])*(1-a))
}

func fillRoundRect(img *image.RGBA, x0, y0, x1, y1, r float64, c color.RGBA) {
	hx, hy := (x1-x0)/2, (y1-y0)/2
	for y := int(y0); y < int(math.Ceil(y1)); y++ {
		for x := int(x0); x < int(math.Ceil(x1)); x++ {
			qx := math.Abs(float64(x)+0.5-(x0+hx)) - (hx - r)
			qy := math.Abs(float64(y)+0.5-(y0+hy)) - (hy - r)
			d := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
			blend(img, x, y, c, math.Max(0, math.Min(1, 0.5-d)))
		}
	}
}

// drawLogo draws the Claude logo centred in the icon, size pixels wide.
func drawLogo(img *image.RGBA, size float64, c color.RGBA) {
	n := int(math.Round(size))
	if n < 4 {
		return
	}
	m := logo.Mask(n)
	off := (img.Bounds().Dx() - n) / 2
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if a := m.AlphaAt(x, y).A; a > 0 {
				blend(img, off+x, off+y, c, float64(a)/255)
			}
		}
	}
}

func encodeICO(frames ...*image.RGBA) []byte {
	var pngs [][]byte
	for _, f := range frames {
		var b bytes.Buffer
		_ = png.Encode(&b, f)
		pngs = append(pngs, b.Bytes())
	}
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(frames))})
	offset := 6 + 16*len(frames)
	for i, f := range frames {
		w := f.Bounds().Dx()
		dim := uint8(w)
		if w >= 256 {
			dim = 0
		}
		_ = binary.Write(&out, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(pngs[i])), uint32(offset)})
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		out.Write(p)
	}
	return out.Bytes()
}
