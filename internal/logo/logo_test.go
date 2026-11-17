package logo

import "testing"

func TestMask(t *testing.T) {
	path = parseSVG(builtin) // test the built-in icon, not a user's logo.svg
	parseOnce.Do(func() {})
	m := Mask(64)
	if m.Bounds().Dx() != 64 {
		t.Fatalf("size %v", m.Bounds())
	}
	filled, centre := 0, m.AlphaAt(32, 32).A
	for _, a := range m.Pix {
		if a > 128 {
			filled++
		}
	}
	// the spark covers a good part of its box and its centre is solid
	if filled < 64*64/10 || filled > 64*64*4/5 || centre < 200 {
		t.Fatalf("unexpected mask: filled=%d centre=%d", filled, centre)
	}
}
