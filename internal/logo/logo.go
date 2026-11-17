// Package logo rasterises the app's icon into alpha masks at any size.
//
// The built-in icon is a generic gauge (gauge.svg). To use a different icon,
// put an SVG named logo.svg in %APPDATA%\ClaudeUsageMeter (or next to the .exe).
// It must be a single-path SVG with a 1200x1200 viewBox using absolute
// M, L, C and Z commands, like the built-in one.
package logo

import (
	_ "embed"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/vector"
)

//go:embed gauge.svg
var builtin string

// viewBox of the icon
const box = 1200.0

type cmd struct {
	op  byte
	arg []float32
}

var (
	parseOnce sync.Once
	path      []cmd
	cacheMu   sync.Mutex
	cache     = map[int]*image.Alpha{}
)

// customPaths are checked in order for a user-supplied logo.svg.
func customPaths() []string {
	var out []string
	if dir, err := os.UserConfigDir(); err == nil {
		out = append(out, filepath.Join(dir, "ClaudeUsageMeter", "logo.svg"))
	}
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Join(filepath.Dir(exe), "logo.svg"))
	}
	return out
}

func parseSVG(svg string) []cmd {
	m := regexp.MustCompile(`\sd="([^"]+)"`).FindStringSubmatch(svg)
	if m == nil {
		return nil
	}
	var out []cmd
	var cur *cmd
	for _, tok := range strings.Fields(m[1]) {
		if len(tok) == 1 && strings.ContainsAny(tok, "MLCZmlcz") {
			out = append(out, cmd{op: strings.ToUpper(tok)[0]})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if v, err := strconv.ParseFloat(tok, 32); err == nil {
			cur.arg = append(cur.arg, float32(v))
		}
	}
	return out
}

func parse() {
	for _, p := range customPaths() {
		if b, err := os.ReadFile(p); err == nil {
			if c := parseSVG(string(b)); len(c) > 0 {
				path = c
				return
			}
		}
	}
	path = parseSVG(builtin)
}

// Mask returns the icon as an alpha mask of size x size pixels (cached).
func Mask(size int) *image.Alpha {
	if size < 1 {
		size = 1
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if a, ok := cache[size]; ok {
		return a
	}
	parseOnce.Do(parse)
	s := float32(size) / box
	r := vector.NewRasterizer(size, size)
	for _, c := range path {
		a := c.arg
		switch c.op {
		case 'M':
			for i := 0; i+1 < len(a); i += 2 {
				if i == 0 {
					r.MoveTo(a[0]*s, a[1]*s)
				} else {
					r.LineTo(a[i]*s, a[i+1]*s)
				}
			}
		case 'L':
			for i := 0; i+1 < len(a); i += 2 {
				r.LineTo(a[i]*s, a[i+1]*s)
			}
		case 'C':
			for i := 0; i+5 < len(a); i += 6 {
				r.CubeTo(a[i]*s, a[i+1]*s, a[i+2]*s, a[i+3]*s, a[i+4]*s, a[i+5]*s)
			}
		case 'Z':
			r.ClosePath()
		}
	}
	mask := image.NewAlpha(image.Rect(0, 0, size, size))
	r.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	cache[size] = mask
	return mask
}
