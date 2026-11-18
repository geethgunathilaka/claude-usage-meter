//go:build windows

package main

// Desktop overlays: the always-on-top usage bar and the alert pop-up.
// Both are layered, click-through-free popup windows painted with per-pixel alpha
// from images rendered by internal/ui. All window calls run on one locked OS thread.

import (
	"image"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	pRegisterClassExW              = user32.NewProc("RegisterClassExW")
	pCreateWindowExW               = user32.NewProc("CreateWindowExW")
	pDefWindowProcW                = user32.NewProc("DefWindowProcW")
	pGetMessageW                   = user32.NewProc("GetMessageW")
	pTranslateMessage              = user32.NewProc("TranslateMessage")
	pDispatchMessageW              = user32.NewProc("DispatchMessageW")
	pShowWindow                    = user32.NewProc("ShowWindow")
	pUpdateLayeredWindow           = user32.NewProc("UpdateLayeredWindow")
	pGetDC                         = user32.NewProc("GetDC")
	pReleaseDC                     = user32.NewProc("ReleaseDC")
	pSetTimer                      = user32.NewProc("SetTimer")
	pReleaseCapture                = user32.NewProc("ReleaseCapture")
	pSendMessageW                  = user32.NewProc("SendMessageW")
	pGetWindowRect                 = user32.NewProc("GetWindowRect")
	pSetWindowPos                  = user32.NewProc("SetWindowPos")
	pSystemParametersInfoW         = user32.NewProc("SystemParametersInfoW")
	pGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	pLoadCursorW                   = user32.NewProc("LoadCursorW")
	pMessageBeep                   = user32.NewProc("MessageBeep")
	pGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pSetThreadDpiAwarenessContext  = user32.NewProc("SetThreadDpiAwarenessContext")
	pGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
	pIsWindowVisible               = user32.NewProc("IsWindowVisible")
	pGetWindowLongW                = user32.NewProc("GetWindowLongW")
	pSetWindowLongW                = user32.NewProc("SetWindowLongW")

	pCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	pDeleteDC           = gdi32.NewProc("DeleteDC")
	pCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	pSelectObject       = gdi32.NewProc("SelectObject")
	pDeleteObject       = gdi32.NewProc("DeleteObject")
)

const (
	wsPopup         = 0x80000000
	wsExLayered     = 0x00080000
	wsExToolWindow  = 0x00000080
	wsExTopmost     = 0x00000008
	wsExNoActivate  = 0x08000000
	swHide          = 0
	swShowNoActive  = 4
	wmTimer         = 0x0113
	wmLButtonDown   = 0x0201
	wmMouseActivate = 0x0021
	wmNCLButtonDown = 0x00A1
	htCaption       = 2
	maNoActivate    = 3
	ulwAlpha        = 2
	acSrcAlpha      = 1
	spiGetWorkArea  = 0x0030
	smCxScreen      = 0
	swpNoMove       = 0x0002
	swpNoSize       = 0x0001
	swpNoActivate   = 0x0010
	mbIconWarning   = 0x30

	wmWindowPosChanging = 0x0046
	swpNoZOrder         = 0x0004
	hwndBottom          = uintptr(1)
	hwndTopmost         = ^uintptr(0) // -1
	hwndNoTopmost       = ^uintptr(1) // -2
	gwlExStyle          = ^uintptr(19) // -20
	wsExTransparent     = 0x00000020
)

// windowPos mirrors WINDOWPOS.
type windowPos struct {
	Hwnd            uintptr
	HwndInsertAfter uintptr
	X, Y, CX, CY    int32
	Flags           uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type point struct{ X, Y int32 }
type size struct{ CX, CY int32 }
type rect struct{ Left, Top, Right, Bottom int32 }

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type blendFunction struct{ BlendOp, BlendFlags, SourceConstantAlpha, AlphaFormat byte }

type bitmapInfo struct {
	Size          uint32
	Width, Height int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
	Colors        [1]uint32
}

// enableDPIAwareness makes Windows give us real pixels so the overlays are sharp.
// The process-wide call can be refused if something already set a mode, so the
// overlay thread also sets its own context (see run), which Windows always honours.
func enableDPIAwareness() {
	if pSetProcessDpiAwarenessContext.Find() == nil {
		if r, _, _ := pSetProcessDpiAwarenessContext.Call(^uintptr(3)); r == 0 { // PER_MONITOR_AWARE_V2 (-4)
			pSetProcessDpiAwarenessContext.Call(^uintptr(2)) // PER_MONITOR_AWARE (-3)
		}
	}
}

// enableThreadDPIAwareness makes windows created on the calling thread per-monitor DPI aware.
func enableThreadDPIAwareness() {
	if pSetThreadDpiAwarenessContext.Find() == nil {
		if r, _, _ := pSetThreadDpiAwarenessContext.Call(^uintptr(3)); r == 0 {
			pSetThreadDpiAwarenessContext.Call(^uintptr(2))
		}
	}
}

func uiScale() float64 {
	if pGetDpiForSystem.Find() == nil {
		if dpi, _, _ := pGetDpiForSystem.Call(); dpi > 0 {
			return float64(dpi) / 96
		}
	}
	return 1
}

func beep() { pMessageBeep.Call(mbIconWarning) }

// present paints a premultiplied RGBA image onto a layered window at x,y.
func present(hwnd uintptr, img *image.RGBA, x, y int32) { presentAlpha(hwnd, img, x, y, 255) }

// presentAlpha is present with an overall opacity (0-255) on top of the per-pixel alpha.
func presentAlpha(hwnd uintptr, img *image.RGBA, x, y int32, alpha byte) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w == 0 || h == 0 {
		return
	}
	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen)
	mem, _, _ := pCreateCompatibleDC.Call(screen)
	defer pDeleteDC.Call(mem)
	bi := bitmapInfo{Size: 40, Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	var bits unsafe.Pointer
	hbmp, _, _ := pCreateDIBSection.Call(screen, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbmp == 0 || bits == nil {
		return
	}
	defer pDeleteObject.Call(hbmp)
	dst := unsafe.Slice((*byte)(bits), w*h*4)
	for yy := 0; yy < h; yy++ {
		row := img.Pix[yy*img.Stride : yy*img.Stride+w*4]
		o := yy * w * 4
		for xx := 0; xx < w*4; xx += 4 { // RGBA (premultiplied) -> BGRA (premultiplied)
			dst[o+xx], dst[o+xx+1], dst[o+xx+2], dst[o+xx+3] = row[xx+2], row[xx+1], row[xx], row[xx+3]
		}
	}
	old, _, _ := pSelectObject.Call(mem, hbmp)
	defer pSelectObject.Call(mem, old)
	pos := point{x, y}
	sz := size{int32(w), int32(h)}
	src := point{}
	bf := blendFunction{SourceConstantAlpha: alpha, AlphaFormat: acSrcAlpha}
	pUpdateLayeredWindow.Call(hwnd, screen, uintptr(unsafe.Pointer(&pos)), uintptr(unsafe.Pointer(&sz)), mem,
		uintptr(unsafe.Pointer(&src)), 0, uintptr(unsafe.Pointer(&bf)), ulwAlpha)
}

func workArea() rect {
	var r rect
	pSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
	return r
}

// overlay owns the two windows and the thread they live on.
type overlay struct {
	scale float64

	mu      sync.Mutex
	queue   []func()
	widget  uintptr
	popup   uintptr
	popupAt time.Time // when the pop-up should hide
	ticks   int

	widgetW, widgetH int32
	widgetX, widgetY int32
	autoPos          bool
	onTop            bool // meter is topmost + click-through
	dragging         bool // a drag finished; keep its position until settings catch up

	onMoved func(x, y int) // called (on a new goroutine) after the user drags the bar
}

var theOverlay *overlay

func startOverlay(onMoved func(x, y int)) *overlay {
	o := &overlay{scale: uiScale(), onMoved: onMoved, autoPos: true}
	theOverlay = o
	ready := make(chan struct{})
	go o.run(ready)
	<-ready
	return o
}

// do runs f on the overlay thread at the next tick.
func (o *overlay) do(f func()) {
	o.mu.Lock()
	o.queue = append(o.queue, f)
	o.mu.Unlock()
}

func (o *overlay) run(ready chan struct{}) {
	runtime.LockOSThread()
	enableThreadDPIAwareness()
	inst := windows.Handle(0)
	_ = windows.GetModuleHandleEx(0, nil, &inst)
	cls, _ := windows.UTF16PtrFromString("ClaudeUsageMeterOverlay")
	cur, _, _ := pLoadCursorW.Call(0, 32512) // IDC_ARROW
	wc := wndClassEx{WndProc: windows.NewCallback(wndProc), Instance: inst, ClassName: cls, Cursor: windows.Handle(cur)}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	title, _ := windows.UTF16PtrFromString("Claude Usage")
	mk := func(ex uintptr) uintptr {
		h, _, _ := pCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)), wsPopup,
			0, 0, 10, 10, 0, 0, uintptr(inst), 0)
		return h
	}
	// The usage bar lives on the desktop layer: never topmost, kept at the bottom of the
	// window stack (see wmWindowPosChanging), so app windows always cover it.
	o.widget = mk(wsExLayered | wsExToolWindow | wsExNoActivate)
	// Alert pop-ups are short-lived warnings and do appear above other windows.
	o.popup = mk(wsExLayered | wsExToolWindow | wsExTopmost | wsExNoActivate)
	if pGetDpiForWindow.Find() == nil { // the real DPI of the window, now that the thread is DPI aware
		if dpi, _, _ := pGetDpiForWindow.Call(o.widget); dpi > 0 {
			o.scale = float64(dpi) / 96
		}
	}
	pSetTimer.Call(o.widget, 1, 150, 0)
	close(ready)
	var m winMsg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	o := theOverlay
	if o == nil {
		r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}
	switch msg {
	case wmWindowPosChanging:
		if hwnd == o.widget && lp != 0 {
			// pin the meter to its layer whatever else tries to move it in the stack:
			// topmost when "on top" is enabled, otherwise the bottom (desktop layer)
			wp := *(**windowPos)(unsafe.Pointer(&lp)) // lParam is a *WINDOWPOS
			if wp.Flags&swpNoZOrder == 0 {
				if o.onTop {
					wp.HwndInsertAfter = hwndTopmost
				} else {
					wp.HwndInsertAfter = hwndBottom
				}
			}
		}
	case wmTimer:
		if hwnd == o.widget {
			o.tick()
		}
		return 0
	case wmMouseActivate:
		return maNoActivate
	case wmLButtonDown:
		if hwnd == o.popup {
			pShowWindow.Call(o.popup, swHide)
			return 0
		}
		if hwnd == o.widget {
			// let Windows move the window as if its caption were dragged
			pReleaseCapture.Call()
			pSendMessageW.Call(hwnd, wmNCLButtonDown, htCaption, 0)
			var r rect
			pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
			if r.Left != o.widgetX || r.Top != o.widgetY {
				o.widgetX, o.widgetY, o.dragging = r.Left, r.Top, true
				if o.onMoved != nil {
					go o.onMoved(int(r.Left), int(r.Top))
				}
			}
			return 0
		}
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func (o *overlay) tick() {
	o.mu.Lock()
	q := o.queue
	o.queue = nil
	o.mu.Unlock()
	for _, f := range q {
		f()
	}
	if !o.popupAt.IsZero() && time.Now().After(o.popupAt) {
		pShowWindow.Call(o.popup, swHide)
		o.popupAt = time.Time{}
	}
	o.ticks++
	if o.ticks%40 == 0 { // every ~6 s, re-assert the layer: topmost, or the bottom of the stack
		if v, _, _ := pIsWindowVisible.Call(o.widget); v != 0 {
			after := hwndBottom
			if o.onTop {
				after = hwndTopmost
			}
			pSetWindowPos.Call(o.widget, after, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
		}
	}
}

// WidgetOptions controls where and how the meter is shown.
type WidgetOptions struct {
	Position string // top-left, top-right, top-center, bottom-left, bottom-right, custom
	X, Y     int    // used when Position is "custom"
	OnTop    bool   // above other apps, click-through
	Opacity  float64
}

// ShowWidget shows the usage meter.
func (o *overlay) ShowWidget(img *image.RGBA, opt WidgetOptions) {
	o.do(func() {
		w, h := int32(img.Bounds().Dx()), int32(img.Bounds().Dy())
		wa := workArea()
		m := int32(12 * o.scale)
		top := wa.Top + m
		if opt.OnTop {
			// floating over apps: stay below title bars so window buttons are never covered
			top = wa.Top + int32(56*o.scale)
		}
		if opt.Position != "custom" {
			o.dragging = false
		}
		switch opt.Position {
		case "custom":
			if !o.dragging {
				o.widgetX, o.widgetY = int32(opt.X), int32(opt.Y)
			}
		case "top-left":
			o.widgetX, o.widgetY = wa.Left+m, top
		case "top-center":
			// window buttons sit in the corners, so the centre can hug the top edge even when on top
			o.widgetX, o.widgetY = (wa.Left+wa.Right-w)/2, wa.Top+int32(4*o.scale)
		case "bottom-left":
			o.widgetX, o.widgetY = wa.Left+m, wa.Bottom-h-m
		case "bottom-right":
			o.widgetX, o.widgetY = wa.Right-w-m, wa.Bottom-h-m
		default: // top-right
			o.widgetX, o.widgetY = wa.Right-w-m, top
		}
		o.widgetW, o.widgetH = w, h

		// on top = topmost + click-through; otherwise the desktop layer
		// only the click-through bit is changed here; topmost is managed with SetWindowPos
		ex, _, _ := pGetWindowLongW.Call(o.widget, gwlExStyle)
		want := ex &^ wsExTransparent
		if opt.OnTop {
			want |= wsExTransparent
		}
		if want != ex {
			pSetWindowLongW.Call(o.widget, gwlExStyle, want)
		}
		o.onTop = opt.OnTop

		alpha := byte(255)
		if opt.OnTop {
			a := opt.Opacity
			if a < 0.3 || a > 1 {
				a = 0.85
			}
			alpha = byte(a * 255)
		}
		presentAlpha(o.widget, img, o.widgetX, o.widgetY, alpha)
		pShowWindow.Call(o.widget, swShowNoActive)
		if opt.OnTop {
			pSetWindowPos.Call(o.widget, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
		} else {
			pSetWindowPos.Call(o.widget, hwndNoTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
			pSetWindowPos.Call(o.widget, hwndBottom, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
		}
	})
}

// HideWidget hides the usage bar.
func (o *overlay) HideWidget() { o.do(func() { pShowWindow.Call(o.widget, swHide) }) }

// ShowPopup shows an alert card in the bottom-right corner for d.
func (o *overlay) ShowPopup(img *image.RGBA, d time.Duration) {
	o.do(func() {
		wa := workArea()
		w, h := int32(img.Bounds().Dx()), int32(img.Bounds().Dy())
		m := int32(4 * o.scale)
		present(o.popup, img, wa.Right-w-m, wa.Bottom-h-m)
		pShowWindow.Call(o.popup, swShowNoActive)
		pSetWindowPos.Call(o.popup, ^uintptr(0), 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
		o.popupAt = time.Now().Add(d)
	})
}
