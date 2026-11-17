// Package ui draws the desktop usage bar and the alert pop-up as images
// (premultiplied RGBA), in Claude's palette. The Windows side only puts these
// images on screen, so everything visual lives here and can be tested anywhere.
package ui

import (
	"image"
	"image/color"
	"math"
	"os"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"claude-usage-meter/internal/logo"
)

// Claude palette.
var (
	Clay       = color.RGBA{0xD9, 0x77, 0x57, 0xff} // Claude accent
	Amber      = color.RGBA{0xF0, 0xA2, 0x3B, 0xff}
	Red        = color.RGBA{0xE5, 0x53, 0x4B, 0xff}
	Dark       = color.RGBA{0x26, 0x26, 0x24, 0xff}
	DarkTrack  = color.RGBA{0x48, 0x46, 0x41, 0xff}
	Cream      = color.RGBA{0xF5, 0xF4, 0xEE, 0xff}
	Muted      = color.RGBA{0xA8, 0xA6, 0x9E, 0xff}
	Paper      = color.RGBA{0xFA, 0xF9, 0xF5, 0xff}
	PaperLine  = color.RGBA{0xE6, 0xE3, 0xD8, 0xff}
	PaperTrack = color.RGBA{0xEC, 0xE9, 0xE0, 0xff}
	Ink        = color.RGBA{0x14, 0x14, 0x13, 0xff}
	InkMuted   = color.RGBA{0x5E, 0x5D, 0x59, 0xff}
)

// Level is how close a bar is to its limit.
type Level int

const (
	OK Level = iota
	Warn
	Alert
)

// LevelColor returns the fill colour for a level.
func LevelColor(l Level) color.RGBA {
	switch l {
	case Warn:
		return Amber
	case Alert:
		return Red
	}
	return Clay
}

// ---------- fonts ----------

var (
	fontOnce          sync.Once
	fRegular, fStrong *opentype.Font
	faceMu            sync.Mutex
	faces             = map[[2]int]font.Face{}
)

func loadFont(paths []string, fallback []byte) *opentype.Font {
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			if f, err := opentype.Parse(b); err == nil {
				return f
			}
		}
	}
	f, _ := opentype.Parse(fallback)
	return f
}

func fonts() {
	fontOnce.Do(func() {
		win := os.Getenv("WINDIR")
		if win == "" {
			win = `C:\Windows`
		}
		fRegular = loadFont([]string{win + `\Fonts\segoeui.ttf`}, goregular.TTF)
		fStrong = loadFont([]string{win + `\Fonts\seguisb.ttf`, win + `\Fonts\segoeuib.ttf`}, gobold.TTF)
	})
}

// face returns a cached font face; strong selects the semibold weight. px is the pixel size.
func face(strong bool, px float64) font.Face {
	fonts()
	k := [2]int{0, int(px * 10)}
	f := fRegular
	if strong {
		k[0], f = 1, fStrong
	}
	faceMu.Lock()
	defer faceMu.Unlock()
	if fc, ok := faces[k]; ok {
		return fc
	}
	fc, _ := opentype.NewFace(f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
	faces[k] = fc
	return fc
}

func textWidth(f font.Face, s string) float64 { return float64(font.MeasureString(f, s)) / 64 }

// drawText draws s with its vertical centre at cy.
func drawText(img *image.RGBA, f font.Face, x, cy float64, s string, c color.RGBA) {
	m := f.Metrics()
	base := cy + (float64(m.Ascent)-float64(m.Descent))/64/2
	d := &font.Drawer{Dst: img, Src: image.NewUniform(c), Face: f, Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(base * 64)}}
	d.DrawString(s)
}

// ---------- shapes (signed-distance, anti-aliased) ----------

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// blend composites colour c with coverage cov over the premultiplied pixel at x,y.
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

func sdRoundRect(px, py, x0, y0, x1, y1, r float64) float64 {
	hx, hy := (x1-x0)/2, (y1-y0)/2
	r = math.Min(r, math.Min(hx, hy))
	qx := math.Abs(px-(x0+hx)) - (hx - r)
	qy := math.Abs(py-(y0+hy)) - (hy - r)
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

func fillRoundRect(img *image.RGBA, x0, y0, x1, y1, r float64, c color.RGBA) {
	for y := int(y0) - 1; y <= int(y1)+1; y++ {
		for x := int(x0) - 1; x <= int(x1)+1; x++ {
			d := sdRoundRect(float64(x)+0.5, float64(y)+0.5, x0, y0, x1, y1, r)
			blend(img, x, y, c, clamp01(0.5-d))
		}
	}
}

func strokeRoundRect(img *image.RGBA, x0, y0, x1, y1, r, w float64, c color.RGBA) {
	for y := int(y0) - 1; y <= int(y1)+1; y++ {
		for x := int(x0) - 1; x <= int(x1)+1; x++ {
			d := sdRoundRect(float64(x)+0.5, float64(y)+0.5, x0, y0, x1, y1, r)
			blend(img, x, y, c, clamp01(0.5-d)-clamp01(0.5-(d+w)))
		}
	}
}

// softShadow approximates a blurred drop shadow around a rounded rectangle.
func softShadow(img *image.RGBA, x0, y0, x1, y1, r, spread float64, dy float64) {
	for y := int(y0 - spread); y <= int(y1+spread+dy); y++ {
		for x := int(x0 - spread); x <= int(x1+spread); x++ {
			d := sdRoundRect(float64(x)+0.5, float64(y)+0.5-dy, x0, y0, x1, y1, r)
			if d <= 0 {
				continue
			}
			t := 1 - d/spread
			if t > 0 {
				blend(img, x, y, color.RGBA{0, 0, 0, 255}, 0.22*t*t)
			}
		}
	}
}

// drawLogo draws the Claude logo (from claude.svg) centred at cx,cy, size pixels wide.
func drawLogo(img *image.RGBA, cx, cy, size float64, c color.RGBA) {
	n := int(math.Round(size))
	m := logo.Mask(n)
	x0, y0 := int(math.Round(cx-float64(n)/2)), int(math.Round(cy-float64(n)/2))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if a := m.AlphaAt(x, y).A; a > 0 {
				blend(img, x0+x, y0+y, c, float64(a)/255)
			}
		}
	}
}

func bar(img *image.RGBA, x, cy, w, h, pct float64, track, fill color.RGBA) {
	fillRoundRect(img, x, cy-h/2, x+w, cy+h/2, h/2, track)
	f := clamp01(pct / 100)
	if f > 0 {
		fw := math.Max(h, w*f)
		fillRoundRect(img, x, cy-h/2, x+fw, cy+h/2, h/2, fill)
	}
}

// ---------- desktop usage bar ----------

// Bar is one usage figure on the desktop bar.
type Bar struct {
	Label   string
	Percent float64 // < 0 = unknown
	Level   Level
}

// WidgetData is what the desktop bar shows.
type WidgetData struct {
	Bars    []Bar
	Right   string // e.g. "resets 13:00"
	Message string // shown instead of bars when there is no data
}

func pctLabel(p float64) string {
	if p < 0 {
		return "—"
	}
	return itoa(int(math.Floor(p))) + "%"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// RenderWidget draws the desktop usage bar at the given UI scale (1.0 = 96 DPI).
func RenderWidget(d WidgetData, s float64) *image.RGBA {
	H := 40 * s
	pad := 14 * s
	logo := 9.5 * s
	barW, barH := 58*s, 6*s
	fLabel := face(false, 12.5*s)
	fPct := face(true, 13*s)
	fRight := face(false, 12*s)

	w := pad + logo*2 + 12*s
	type grp struct{ lw, pw float64 }
	var gs []grp
	if d.Message != "" {
		w += textWidth(fLabel, d.Message) + pad
	} else {
		for _, b := range d.Bars {
			g := grp{textWidth(fLabel, b.Label), math.Max(textWidth(fPct, "100%"), textWidth(fPct, pctLabel(b.Percent)))}
			gs = append(gs, g)
			w += g.lw + 6*s + barW + 7*s + g.pw + 16*s
		}
		if d.Right != "" {
			w += textWidth(fRight, d.Right) + 2*s
		}
		w += pad - 16*s + 4*s
	}
	W := math.Ceil(w)
	img := image.NewRGBA(image.Rect(0, 0, int(W), int(math.Ceil(H))))
	fillRoundRect(img, 0, 0, W, H, H/2, color.RGBA{Dark.R, Dark.G, Dark.B, 238})
	strokeRoundRect(img, 0, 0, W, H, H/2, 1*s, color.RGBA{255, 255, 255, 30})

	cy := H / 2
	x := pad
	drawLogo(img, x+logo, cy, logo*2.1, Clay)
	x += logo*2 + 12*s
	if d.Message != "" {
		drawText(img, fLabel, x, cy, d.Message, Cream)
		return img
	}
	for i, b := range d.Bars {
		g := gs[i]
		drawText(img, fLabel, x, cy, b.Label, Muted)
		x += g.lw + 6*s
		bar(img, x, cy, barW, barH, b.Percent, DarkTrack, LevelColor(b.Level))
		x += barW + 7*s
		drawText(img, fPct, x, cy, pctLabel(b.Percent), Cream)
		x += g.pw + 16*s
	}
	if d.Right != "" {
		drawText(img, fRight, x, cy, d.Right, Muted)
	}
	return img
}

// ring draws a progress ring centred at cx,cy (clockwise from 12 o'clock).
func ring(img *image.RGBA, cx, cy, rOuter, width, pct float64, track, fill color.RGBA) {
	rInner := rOuter - width
	frac := clamp01(pct / 100)
	if pct < 0 {
		frac = 0
	}
	for y := int(cy - rOuter - 1); y <= int(cy+rOuter+1); y++ {
		for x := int(cx - rOuter - 1); x <= int(cx+rOuter+1); x++ {
			px, py := float64(x)+0.5-cx, float64(y)+0.5-cy
			d := math.Hypot(px, py)
			cov := clamp01(rOuter+0.5-d) * clamp01(d-(rInner-0.5))
			if cov <= 0 {
				continue
			}
			ang := math.Atan2(px, -py) / (2 * math.Pi)
			if ang < 0 {
				ang++
			}
			c := track
			if ang <= frac {
				c = fill
			}
			blend(img, x, y, c, cov)
		}
	}
}

// RenderCompact draws the small meter: the logo and one mini ring per limit,
// with the percentage inside the ring and a short label underneath.
func RenderCompact(d WidgetData, s float64) *image.RGBA {
	pad := 9 * s
	logoW := 15 * s
	rr := 14 * s // ring radius
	gap := 7 * s
	H := pad + rr*2 + 3*s + 10*s + pad*0.7
	fNum := face(true, 10.5*s)
	fLbl := face(false, 9*s)

	n := len(d.Bars)
	if d.Message != "" || n == 0 {
		// no data: just the logo
		W := math.Ceil(pad*2 + logoW)
		img := image.NewRGBA(image.Rect(0, 0, int(W), int(math.Ceil(H))))
		fillRoundRect(img, 0, 0, W, H, 12*s, color.RGBA{Dark.R, Dark.G, Dark.B, 238})
		drawLogo(img, W/2, H/2, logoW, Clay)
		return img
	}
	W := math.Ceil(pad + logoW + gap + float64(n)*(rr*2) + float64(n-1)*gap + pad)
	img := image.NewRGBA(image.Rect(0, 0, int(W), int(math.Ceil(H))))
	fillRoundRect(img, 0, 0, W, H, 12*s, color.RGBA{Dark.R, Dark.G, Dark.B, 238})
	strokeRoundRect(img, 0, 0, W, H, 12*s, 1*s, color.RGBA{255, 255, 255, 28})

	ringCY := pad + rr
	drawLogo(img, pad+logoW/2, ringCY, logoW, Clay)
	x := pad + logoW + gap
	for _, b := range d.Bars {
		cx := x + rr
		ring(img, cx, ringCY, rr, 3.2*s, b.Percent, DarkTrack, LevelColor(b.Level))
		num := "—"
		if b.Percent >= 0 {
			num = itoa(int(math.Floor(b.Percent)))
		}
		drawText(img, fNum, cx-textWidth(fNum, num)/2, ringCY, num, Cream)
		lbl := b.Label
		drawText(img, fLbl, cx-textWidth(fLbl, lbl)/2, ringCY+rr+3*s+5*s, lbl, Muted)
		x += rr*2 + gap
	}
	return img
}

// ---------- alert pop-up ----------

// PopupData is what an alert card shows.
type PopupData struct {
	Title   string
	Body    string
	Percent float64
	Level   Level
}

func wrap(f font.Face, s string, maxW float64, maxLines int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		try := word
		if cur != "" {
			try = cur + " " + word
		}
		if textWidth(f, try) <= maxW || cur == "" {
			cur = try
			continue
		}
		lines = append(lines, cur)
		cur = word
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] += "…"
	}
	return lines
}

// PopupMargin is the transparent shadow margin around the card, at scale 1.
const PopupMargin = 16

// RenderPopup draws an alert card (with its shadow margin) at the given scale.
func RenderPopup(p PopupData, s float64) *image.RGBA {
	m := PopupMargin * s
	cw, ch := 360*s, 118*s
	img := image.NewRGBA(image.Rect(0, 0, int(math.Ceil(cw+2*m)), int(math.Ceil(ch+2*m))))
	x0, y0, x1, y1 := m, m, m+cw, m+ch
	r := 14 * s
	softShadow(img, x0, y0, x1, y1, r, m*0.9, 3*s)
	fillRoundRect(img, x0, y0, x1, y1, r, Paper)
	strokeRoundRect(img, x0, y0, x1, y1, r, 1*s, PaperLine)
	// accent strip, clipped to the card's rounded shape
	for y := int(y0); y <= int(y1); y++ {
		for x := int(x0); x <= int(x0+4*s)+1; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			card := clamp01(0.5 - sdRoundRect(px, py, x0, y0, x1, y1, r))
			strip := clamp01(x0 + 4*s - float64(x))
			blend(img, x, y, LevelColor(p.Level), math.Min(card, strip))
		}
	}

	drawLogo(img, x0+30*s, y0+30*s, 28*s, Clay)
	tx := x0 + 56*s
	fTitle := face(true, 15*s)
	fBody := face(false, 12.5*s)
	drawText(img, fTitle, tx, y0+26*s, p.Title, Ink)
	drawText(img, face(false, 16*s), x1-24*s, y0+22*s, "×", InkMuted)
	for i, line := range wrap(fBody, p.Body, x1-tx-18*s, 2) {
		drawText(img, fBody, tx, y0+50*s+float64(i)*18*s, line, InkMuted)
	}
	fPct := face(true, 13.5*s)
	pl := pctLabel(p.Percent)
	pw := textWidth(fPct, pl)
	by := y1 - 22*s
	bar(img, tx, by, x1-tx-26*s-pw, 7*s, p.Percent, PaperTrack, LevelColor(p.Level))
	drawText(img, fPct, x1-18*s-pw, by, pl, LevelColor(p.Level))
	return img
}
