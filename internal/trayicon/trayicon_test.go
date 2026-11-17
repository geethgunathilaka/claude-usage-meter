package trayicon

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestRenderICO(t *testing.T) {
	for _, pct := range []float64{-1, 0, 7, 42, 97, 150} {
		ico := Render(pct, Warn)
		var hdr [3]uint16
		if err := binary.Read(bytes.NewReader(ico[:6]), binary.LittleEndian, &hdr); err != nil {
			t.Fatal(err)
		}
		if hdr[1] != 1 || int(hdr[2]) != len(sizes) {
			t.Fatalf("bad ico header %v", hdr)
		}
		for i := range sizes {
			off := binary.LittleEndian.Uint32(ico[6+16*i+12:])
			img, err := png.Decode(bytes.NewReader(ico[off:]))
			if err != nil {
				t.Fatalf("frame %d not a png: %v", i, err)
			}
			if img.Bounds().Dx() != sizes[i] {
				t.Fatalf("frame %d is %v, want %d", i, img.Bounds(), sizes[i])
			}
			if dir := os.Getenv("UI_PREVIEW_DIR"); dir != "" && pct == 97 {
				f, _ := os.Create(filepath.Join(dir, "tray-"+strconv.Itoa(sizes[i])+".png"))
				_ = png.Encode(f, img)
				f.Close()
			}
		}
	}
}
