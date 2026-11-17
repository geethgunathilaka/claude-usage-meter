package ui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// Set UI_PREVIEW_DIR to write preview PNGs for a visual check.
func TestRender(t *testing.T) {
	w := RenderWidget(WidgetData{
		Bars:  []Bar{{"5h", 87, Alert}, {"Week", 74, Warn}, {"Fable", 50, OK}},
		Right: "resets 13:00",
	}, 1.25)
	if w.Bounds().Dx() < 300 || w.Bounds().Dy() != 50 {
		t.Fatalf("widget size %v", w.Bounds())
	}
	msg := RenderWidget(WidgetData{Message: "Add your claude.ai session key in Settings"}, 1)
	p := RenderPopup(PopupData{Title: "Session (5hr) at 75%", Body: "Resets 13:00 (in 1h 20m). Weekly 74% · Fable 50%.", Percent: 76, Level: Warn}, 1.25)
	if p.Bounds().Dx() < 400 {
		t.Fatalf("popup size %v", p.Bounds())
	}
	c := RenderCompact(WidgetData{Bars: []Bar{{"5h", 22, OK}, {"Week", 78, Warn}, {"Fable", 93, Alert}}}, 1.25)
	if c.Bounds().Dx() > 220 {
		t.Fatalf("compact meter too wide: %v", c.Bounds())
	}
	dir := os.Getenv("UI_PREVIEW_DIR")
	if dir == "" {
		return
	}
	for name, img := range map[string]*image.RGBA{"widget.png": w, "widget-msg.png": msg, "popup.png": p, "compact.png": c} {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		_ = png.Encode(f, img)
		f.Close()
	}
}
