//go:build windows

// canvas.go —— 自绘像素画布
//
// 不用 GDI+ 的原因:Go 的 syscall.Syscall 在 x64 下只把参数放进整数寄存器,
// 而 GDI+ 的绘图函数参数是 float(走 XMM),直接调用会传垃圾值。
// 因此形状(圆角/渐变/描边)在 32bpp DIB 内存里逐像素合成并自带抗锯齿,
// 文字交给 GDI 的整数 API(CreateFontW / DrawTextW)。零依赖,外观可控。

package main

import (
	"math"
	"syscall"
	"unsafe"
)

var (
	procCreateDIBSection  = modGdi32.NewProc("CreateDIBSection")
	procGdiFlush          = modGdi32.NewProc("GdiFlush")
	procSetBkMode         = modGdi32.NewProc("SetBkMode")
	procSetTextColor      = modGdi32.NewProc("SetTextColor")
	procDrawTextW         = modUser32.NewProc("DrawTextW")
	procGetTextExtentP32W = modGdi32.NewProc("GetTextExtentPoint32W")
)

const (
	dibRGBColors = 0
	srcCopy      = 0x00CC0020
	transparent  = 1
	clearTypeQ   = 5 // CLEARTYPE_QUALITY

	dtLeft       = 0x0000
	dtCenter     = 0x0001
	dtRight      = 0x0002
	dtVCenter    = 0x0004
	dtSingleLine = 0x0020
	dtNoPrefix   = 0x0800
	dtEndEllipse = 0x8000

	alignLeft   = 0
	alignCenter = 1
	alignRight  = 2
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

type textSize struct{ cx, cy int32 }

// surface 是一块可读写的 32bpp 位图 + 一个已选中该位图的内存 DC。
type surface struct {
	dc  uintptr
	bmp uintptr
	old uintptr
	px  []byte // BGRA,自上而下
	w   int32
	h   int32
}

func newSurface(refDC uintptr, w, h int32) *surface {
	if w <= 0 || h <= 0 {
		return nil
	}
	dc, _, _ := procCreateCompatibleDC.Call(refDC)
	if dc == 0 {
		return nil
	}
	bi := bitmapInfo{}
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.Width = w
	bi.Header.Height = -h // 负高度 = 自上而下,避免逐行翻转
	bi.Header.Planes = 1
	bi.Header.BitCount = 32
	bi.Header.Compression = dibRGBColors
	var bits uintptr
	bmp, _, _ := procCreateDIBSection.Call(refDC, uintptr(unsafe.Pointer(&bi)), dibRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == 0 {
		procDeleteDC.Call(dc)
		return nil
	}
	old, _, _ := procSelectObject.Call(dc, bmp)
	return &surface{
		dc: dc, bmp: bmp, old: old, w: w, h: h,
		px: unsafe.Slice((*byte)(unsafe.Pointer(bits)), int(w)*int(h)*4),
	}
}

func (s *surface) free() {
	if s == nil {
		return
	}
	if s.old != 0 {
		procSelectObject.Call(s.dc, s.old)
	}
	if s.bmp != 0 {
		procDeleteObject.Call(s.bmp)
	}
	if s.dc != 0 {
		procDeleteDC.Call(s.dc)
	}
	s.px = nil
}

func (s *surface) blitTo(dst uintptr) {
	procBitBlt.Call(dst, 0, 0, uintptr(s.w), uintptr(s.h), s.dc, 0, 0, srcCopy)
}

// ---------------------------------------------------------------- 像素合成

// blend 按覆盖率 cov 把 RGB 颜色合成到 (x,y)。cov<=0 直接跳过(热路径最重要的剪枝)。
func (s *surface) blend(x, y int32, col uint32, cov float32) {
	if cov <= 0.004 {
		return
	}
	if x < 0 || y < 0 || x >= s.w || y >= s.h {
		return
	}
	if cov > 1 {
		cov = 1
	}
	i := (int(y)*int(s.w) + int(x)) * 4
	inv := 1 - cov
	d := s.px
	r := float32(col >> 16 & 0xFF)
	g := float32(col >> 8 & 0xFF)
	b := float32(col & 0xFF)
	d[i] = uint8(float32(d[i])*inv + b*cov)
	d[i+1] = uint8(float32(d[i+1])*inv + g*cov)
	d[i+2] = uint8(float32(d[i+2])*inv + r*cov)
	d[i+3] = 0xFF
}

func (s *surface) opaque(x, y int32, col uint32) {
	if x < 0 || y < 0 || x >= s.w || y >= s.h {
		return
	}
	i := (int(y)*int(s.w) + int(x)) * 4
	s.px[i] = uint8(col & 0xFF)
	s.px[i+1] = uint8(col >> 8 & 0xFF)
	s.px[i+2] = uint8(col >> 16 & 0xFF)
	s.px[i+3] = 0xFF
}

func lerpCol(c1, c2 uint32, t float32) uint32 {
	if t <= 0 {
		return c1
	}
	if t >= 1 {
		return c2
	}
	r := float32(c1>>16&0xFF) + (float32(c2>>16&0xFF)-float32(c1>>16&0xFF))*t
	g := float32(c1>>8&0xFF) + (float32(c2>>8&0xFF)-float32(c1>>8&0xFF))*t
	b := float32(c1&0xFF) + (float32(c2&0xFF)-float32(c1&0xFF))*t
	return uint32(r)<<16 | uint32(g)<<8 | uint32(b)
}

// sdRoundRect 是有符号距离:圆角矩形内部为负、外部为正。
func sdRoundRect(px, py, x, y, w, h, r float32) float32 {
	cx := x + w/2
	cy := y + h/2
	qx := float32(math.Abs(float64(px-cx))) - (w/2 - r)
	qy := float32(math.Abs(float64(py-cy))) - (h/2 - r)
	ax := float32(0)
	if qx > 0 {
		ax = qx
	}
	ay := float32(0)
	if qy > 0 {
		ay = qy
	}
	m := qx
	if qy > m {
		m = qy
	}
	if m > 0 {
		m = 0
	}
	return float32(math.Sqrt(float64(ax*ax+ay*ay))) + m - r
}

// FillRound 实心圆角矩形(带抗锯齿)。
func (s *surface) fillRound(r rectF, rad float32, col uint32, alpha float32) {
	if r.W <= 0 || r.H <= 0 || alpha <= 0.004 {
		return
	}
	if rad > r.H/2 {
		rad = r.H / 2
	}
	if rad > r.W/2 {
		rad = r.W / 2
	}
	x0 := int32(math.Floor(float64(r.X))) - 1
	y0 := int32(math.Floor(float64(r.Y))) - 1
	x1 := int32(math.Ceil(float64(r.X+r.W))) + 1
	y1 := int32(math.Ceil(float64(r.Y+r.H))) + 1
	for py := y0; py <= y1; py++ {
		fy := float32(py) + 0.5
		for px := x0; px <= x1; px++ {
			fx := float32(px) + 0.5
			d := sdRoundRect(fx, fy, r.X, r.Y, r.W, r.H, rad)
			s.blend(px, py, col, clamp01(0.5-d)*alpha)
		}
	}
}

// fillRoundGrad 竖向渐变圆角矩形。
func (s *surface) fillRoundGrad(r rectF, rad float32, c1, c2 uint32, alpha float32) {
	if r.W <= 0 || r.H <= 0 || alpha <= 0.004 {
		return
	}
	if rad > r.H/2 {
		rad = r.H / 2
	}
	x0 := int32(math.Floor(float64(r.X))) - 1
	y0 := int32(math.Floor(float64(r.Y))) - 1
	x1 := int32(math.Ceil(float64(r.X+r.W))) + 1
	y1 := int32(math.Ceil(float64(r.Y+r.H))) + 1
	for py := y0; py <= y1; py++ {
		fy := float32(py) + 0.5
		t := (fy - r.Y) / r.H
		col := lerpCol(c1, c2, t)
		for px := x0; px <= x1; px++ {
			fx := float32(px) + 0.5
			d := sdRoundRect(fx, fy, r.X, r.Y, r.W, r.H, rad)
			s.blend(px, py, col, clamp01(0.5-d)*alpha)
		}
	}
}

// strokeRound 圆角矩形描边(宽度 width,居中在边界上)。
func (s *surface) strokeRound(r rectF, rad float32, col uint32, alpha, width float32) {
	if r.W <= 0 || r.H <= 0 || width <= 0 || alpha <= 0.004 {
		return
	}
	half := width / 2
	x0 := int32(math.Floor(float64(r.X-half))) - 1
	y0 := int32(math.Floor(float64(r.Y-half))) - 1
	x1 := int32(math.Ceil(float64(r.X+r.W+half))) + 1
	y1 := int32(math.Ceil(float64(r.Y+r.H+half))) + 1
	for py := y0; py <= y1; py++ {
		fy := float32(py) + 0.5
		for px := x0; px <= x1; px++ {
			fx := float32(px) + 0.5
			d := sdRoundRect(fx, fy, r.X, r.Y, r.W, r.H, rad)
			if d < -half-1 || d > half+1 {
				continue
			}
			cov := clamp01(0.5 - (float32(math.Abs(float64(d))) - half))
			s.blend(px, py, col, cov*alpha)
		}
	}
}

func (s *surface) fillRect(r rectF, col uint32, alpha float32) {
	if alpha <= 0.004 {
		return
	}
	x0 := int32(math.Floor(float64(r.X)))
	y0 := int32(math.Floor(float64(r.Y)))
	x1 := int32(math.Ceil(float64(r.X + r.W)))
	y1 := int32(math.Ceil(float64(r.Y + r.H)))
	for py := y0; py < y1; py++ {
		if py < 0 || py >= s.h {
			continue
		}
		for px := x0; px < x1; px++ {
			if alpha >= 0.996 {
				s.opaque(px, py, col)
			} else {
				s.blend(px, py, col, alpha)
			}
		}
	}
}

// gradRect 竖向渐变矩形(不透明)。
func (s *surface) gradRect(r rectF, c1, c2 uint32) {
	y0 := int32(math.Floor(float64(r.Y)))
	y1 := int32(math.Ceil(float64(r.Y + r.H)))
	for py := y0; py < y1; py++ {
		t := (float32(py) + 0.5 - r.Y) / r.H
		col := lerpCol(c1, c2, t)
		for px := int32(r.X); px < int32(r.X+r.W); px++ {
			s.opaque(px, py, col)
		}
	}
}

// vline / hline 画 1px 分隔线(带 alpha)。
func (s *surface) hline(x, y, w float32, col uint32, alpha float32) {
	s.fillRect(rectF{x, y, w, 1}, col, alpha)
}

func (s *surface) vline(x, y, h float32, col uint32, alpha float32) {
	s.fillRect(rectF{x, y, 1, h}, col, alpha)
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ---------------------------------------------------------------- 文字

var fontCache = map[uint32]uintptr{}

func (s *surface) font(sizePx int32, bold bool) uintptr {
	key := uint32(sizePx)<<1 | 0
	if bold {
		key |= 1
	}
	if f, ok := fontCache[key]; ok {
		return f
	}
	weight := uintptr(400)
	if bold {
		weight = 600
	}
	f, _, _ := procCreateFontW.Call(
		uintptr(-int32(sizePx)), // 负值 = 字符高度(不含内边距),排版更可预测
		0, 0, 0, weight, 0, 0, 0,
		1, // DEFAULT_CHARSET
		0, 0, clearTypeQ, 0,
		utf16p("Microsoft YaHei UI"),
	)
	fontCache[key] = f
	return f
}

// text 在矩形内单行绘制文字(垂直居中 + 超出截断省略号)。
func (s *surface) text(str string, r rectF, sizePx int32, col uint32, align int, bold bool) {
	if str == "" {
		return
	}
	u := syscall.StringToUTF16(str)
	f := s.font(sizePx, bold)
	procSelectObject.Call(s.dc, f)
	procSetBkMode.Call(s.dc, transparent)
	procSetTextColor.Call(s.dc, uintptr(colorref(col)))
	flags := uintptr(dtSingleLine | dtVCenter | dtNoPrefix | dtEndEllipse)
	switch align {
	case alignCenter:
		flags |= dtCenter
	case alignRight:
		flags |= dtRight
	default:
		flags |= dtLeft
	}
	rc := rect{
		int32(math.Round(float64(r.X))), int32(math.Round(float64(r.Y))),
		int32(math.Round(float64(r.X + r.W))), int32(math.Round(float64(r.Y + r.H))),
	}
	procDrawTextW.Call(s.dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&rc)), flags)
}

func (s *surface) textWidth(str string, sizePx int32, bold bool) float32 {
	if str == "" {
		return 0
	}
	u := syscall.StringToUTF16(str)
	f := s.font(sizePx, bold)
	procSelectObject.Call(s.dc, f)
	var sz textSize
	procGetTextExtentP32W.Call(s.dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&sz)))
	return float32(sz.cx)
}

// colorref 把 0xRRGGBB 转成 GDI 的 COLORREF(0x00BBGGRR)。
func colorref(col uint32) uint32 {
	return (col&0xFF)<<16 | (col & 0xFF00) | (col >> 16 & 0xFF)
}
