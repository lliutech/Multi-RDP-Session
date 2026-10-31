//go:build windows

// gui.go —— 自绘界面(纯 Win32,无第三方依赖)
//
// 一个窗口 + 三个页面(主界面 / 远程设置 / 运行日志),所有控件都是画出来的:
// 每次重绘时重建鼠标命中区,因此不存在原生控件的配色与缩放问题。
// 文本一律走 T(zh, en),支持中英切换。

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	modUser32  = syscall.NewLazyDLL("user32.dll")
	modGdi32   = syscall.NewLazyDLL("gdi32.dll")
	modShell32 = syscall.NewLazyDLL("shell32.dll")
	modDwmapi  = syscall.NewLazyDLL("dwmapi.dll")

	procRegisterClassExW   = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW    = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW     = modUser32.NewProc("DefWindowProcW")
	procGetMessageW        = modUser32.NewProc("GetMessageW")
	procTranslateMessage   = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW   = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage    = modUser32.NewProc("PostQuitMessage")
	procPostMessageW       = modUser32.NewProc("PostMessageW")
	procSetWindowTextW     = modUser32.NewProc("SetWindowTextW")
	procGetClientRect      = modUser32.NewProc("GetClientRect")
	procBeginPaint         = modUser32.NewProc("BeginPaint")
	procEndPaint           = modUser32.NewProc("EndPaint")
	procInvalidateRect     = modUser32.NewProc("InvalidateRect")
	procShowWindow         = modUser32.NewProc("ShowWindow")
	procSetTimer           = modUser32.NewProc("SetTimer")
	procLoadCursorW        = modUser32.NewProc("LoadCursorW")
	procSetCursor          = modUser32.NewProc("SetCursor")
	procSetCapture         = modUser32.NewProc("SetCapture")
	procReleaseCapture     = modUser32.NewProc("ReleaseCapture")
	procTrackMouseEvent    = modUser32.NewProc("TrackMouseEvent")
	procAdjustWindowRect   = modUser32.NewProc("AdjustWindowRect")
	procDestroyWindow      = modUser32.NewProc("DestroyWindow")
	procGetDpiForWindow    = modUser32.NewProc("GetDpiForWindow")
	procGetDpiForSystem    = modUser32.NewProc("GetDpiForSystem")
	procSetDpiAwarenessCtx = modUser32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware = modUser32.NewProc("SetProcessDPIAware")
	procOpenClipboard      = modUser32.NewProc("OpenClipboard")
	procEmptyClipboard     = modUser32.NewProc("EmptyClipboard")
	procSetClipboardData   = modUser32.NewProc("SetClipboardData")
	procCloseClipboard     = modUser32.NewProc("CloseClipboard")
	procShellExecuteExW    = modShell32.NewProc("ShellExecuteExW")
	procCreateFontW        = modGdi32.NewProc("CreateFontW")
	procCreateCompatibleDC = modGdi32.NewProc("CreateCompatibleDC")
	procSelectObject       = modGdi32.NewProc("SelectObject")
	procDeleteObject       = modGdi32.NewProc("DeleteObject")
	procDeleteDC           = modGdi32.NewProc("DeleteDC")
	procBitBlt             = modGdi32.NewProc("BitBlt")
	procDwmSetWindowAttr   = modDwmapi.NewProc("DwmSetWindowAttribute")
	procGlobalAlloc        = modKernel32.NewProc("GlobalAlloc")
	procGlobalLock         = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock       = modKernel32.NewProc("GlobalUnlock")
	procGlobalFree         = modKernel32.NewProc("GlobalFree")
	procGetModuleHandleW   = modKernel32.NewProc("GetModuleHandleW")
	procGetConsoleWindow   = modKernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcList = modKernel32.NewProc("GetConsoleProcessList")
)

// ---------------------------------------------------------------- 主题

const (
	colBg       = 0x0A0C11
	colBgGlow   = 0x121722
	colCard     = 0x141924
	colCardHi   = 0x1B2130
	colCardAct  = 0x222B3D
	colBorder   = 0x232A38
	colBorderHi = 0x354056
	colText     = 0xE9EDF6
	colTextDim  = 0x8C97AC
	colTextMute = 0x5B6478
	colAccent   = 0x4D8DFF
	colAccentD  = 0x2A63D8
	colOK       = 0x34D399
	colWarn     = 0xF5B942
	colErr      = 0xF0656B
	colViolet   = 0x9B8CFB
)

// ---------------------------------------------------------------- 常量

const (
	wsOverlapped  = 0x00CF0000
	wsCaption     = 0x00C00000
	wsSysMenu     = 0x00080000
	wsMinimizeBox = 0x00020000
	wsMaximizeBox = 0x00010000
	wsThickFrame  = 0x00040000
	exAppWindow   = 0x00040000

	wmDestroy      = 0x0002
	wmSize         = 0x0005
	wmPaint        = 0x000F
	wmClose        = 0x0010
	wmEraseBkgnd   = 0x0014
	wmSetCursor    = 0x0020
	wmGetMinMaxInf = 0x0024
	wmKeyDown      = 0x0100
	wmChar         = 0x0102
	wmTimer        = 0x0113
	wmMouseMove    = 0x0200
	wmLButtonDown  = 0x0201
	wmLButtonUp    = 0x0202
	wmMouseWheel   = 0x020A
	wmMouseLeave   = 0x02A3

	swShow = 5
	swHide = 0

	idcArrow = 32512
	idcHand  = 32649
)

const (
	pageMain = iota
	pageSettings
	pageLog
)

const (
	idNone = iota
	idUnlock
	idRestore
	idRefresh
	idAddGrp
	idSetPwd
	idHideBuiltin
	idCopyLog
	idSaveLog
	idClearLog
	idEnableRdp
	idElevate
	idLang
	idNavMain
	idNavSettings
	idNavLog
	idSetRestoreAll
	idSetRefresh
	idModalOK
	idModalCancel
	idRowBase    = 1000
	idSetCtlBase = 2000
	idSetRstBase = 2200
)

const (
	mkConfirm = iota
	mkPassword
	mkNumber
)

type rect struct{ L, T, R, B int32 }

type rectF struct{ X, Y, W, H float32 }

func rf(x, y, w, h float32) rectF { return rectF{x, y, w, h} }

func (r rectF) right() float32  { return r.X + r.W }
func (r rectF) bottom() float32 { return r.Y + r.H }

func (r rectF) contains(x, y float32) bool {
	return x >= r.X && x <= r.X+r.W && y >= r.Y && y <= r.Y+r.H
}

type hitRegion struct {
	id int
	r  rectF
}

type paintStruct struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     rect
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

type minMaxInfo struct {
	PtReserved [2]int32
	PtMaxSize  [2]int32
	PtMaxPos   [2]int32
	PtMinTrack [2]int32
	PtMaxTrack [2]int32
}

type trackMouseEvent struct {
	CbSize      uint32
	DwFlags     uint32
	HwndTrack   uintptr
	DwHoverTime uint32
}

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     syscall.Handle
	HIcon         syscall.Handle
	HCursor       syscall.Handle
	HbrBackground syscall.Handle
	LpszMenuName  uintptr
	LpszClassName uintptr
	HIconSm       syscall.Handle
}

type msgStruct struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// ---------------------------------------------------------------- 运行时状态

type modalState struct {
	kind   int
	title  string
	body   string
	okText string
	input  []rune
	onOK   func(string)
}

type opResult struct {
	label string
	err   error
}

type logLine struct {
	text string
	lv   int
	head bool
	at   time.Time
}

type settingVal struct {
	v  uint32
	ok bool
}

type app struct {
	hwnd     uintptr
	surf     *surface
	sc       float32
	elevated bool

	page int

	accts    []acctInfo
	accReady bool
	st       sysStatus

	hideBuiltin bool
	busy        bool
	busyLabel   string

	selName   string
	accScroll float32

	setVals   map[string]settingVal
	setOrig   map[string]dwordState
	setScroll float32

	logScroll float32
	logFollow bool
	logUnread bool

	hits     []hitRegion
	hover    int
	press    int
	mx, my   float32
	tracking bool

	modal   *modalState
	toast   string
	toastLv int
	toastAt time.Time

	logCh  chan logEntry
	accCh  chan []acctInfo
	stCh   chan sysStatus
	doneCh chan opResult

	lg *Logger

	wrap      []logLine
	wrapFrom  int
	wrapWidth float32

	startAt time.Time
}

var (
	A      *app
	cbWndP uintptr
)

func (a *app) s(v float32) float32 { return v * a.sc }

func (a *app) invalidate() { procInvalidateRect.Call(a.hwnd, 0, 0) }

// ---------------------------------------------------------------- 入口

func runGUI() {
	runtime.LockOSThread()

	if os.Getenv("RDPMULTI_NO_ELEVATE") != "1" && !isElevated() && selfElevate() {
		return
	}

	if h, _, _ := procGetConsoleWindow.Call(); h != 0 {
		var buf [2]uint32
		if n, _, _ := procGetConsoleProcList.Call(uintptr(unsafe.Pointer(&buf[0])), 2); n <= 1 {
			procShowWindow.Call(h, swHide)
		}
	}

	if r, _, _ := procSetDpiAwarenessCtx.Call(^uintptr(3)); r == 0 {
		procSetProcessDPIAware.Call()
	}

	loadLangPref()
	switch os.Getenv("RDPMULTI_LANG") {
	case "en":
		setLangTransient(langEN)
	case "zh":
		setLangTransient(langZH)
	}

	A = &app{
		hideBuiltin: true,
		logFollow:   true,
		logCh:       make(chan logEntry, 4096),
		accCh:       make(chan []acctInfo, 8),
		stCh:        make(chan sysStatus, 8),
		doneCh:      make(chan opResult, 8),
		elevated:    isElevated(),
		startAt:     time.Now(),
		setVals:     map[string]settingVal{},
		setOrig:     map[string]dwordState{},
	}
	A.lg = NewLogger(false, true)
	A.lg.SetSink(func(e logEntry) {
		select {
		case A.logCh <- e:
		default:
		}
	})

	cbWndP = syscall.NewCallback(wndProc)

	hInst, _, _ := procGetModuleHandleW.Call(0)
	wc := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		LpfnWndProc:   cbWndP,
		HInstance:     syscall.Handle(hInst),
		HCursor:       loadCursor(idcArrow),
		HbrBackground: 0,
		LpszClassName: utf16p("rdpmultiUI"),
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	dpiSys := float32(96)
	if d, _, _ := procGetDpiForSystem.Call(); d > 0 {
		dpiSys = float32(d)
	}
	sc0 := dpiSys / 96
	if sc0 < 1 {
		sc0 = 1
	}
	const baseW, baseH = 980.0, 720.0
	rc := rect{0, 0, int32(baseW * sc0), int32(baseH * sc0)}
	style := uintptr(wsOverlapped | wsCaption | wsSysMenu | wsMinimizeBox | wsMaximizeBox | wsThickFrame)
	procAdjustWindowRect.Call(uintptr(unsafe.Pointer(&rc)), style, 0)

	hwnd, _, _ := procCreateWindowExW.Call(exAppWindow, utf16p("rdpmultiUI"),
		utf16p(T("RDP Multi — 多账户并发会话解锁", "RDP Multi — concurrent multi-user sessions")),
		style, 0x80000000, 0x80000000, uintptr(rc.R-rc.L), uintptr(rc.B-rc.T), 0, 0, hInst, 0)
	if hwnd == 0 {
		return
	}
	A.hwnd = hwnd

	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	A.sc = float32(dpi) / 96.0
	if A.sc < 1 {
		A.sc = 1
	}

	dark := int32(1)
	procDwmSetWindowAttr.Call(hwnd, 20, uintptr(unsafe.Pointer(&dark)), 4)
	procDwmSetWindowAttr.Call(hwnd, 19, uintptr(unsafe.Pointer(&dark)), 4)

	procSetTimer.Call(hwnd, 1, 55, 0)
	procShowWindow.Call(hwnd, swShow)

	A.lg.Say(T("[>>] RDP Multi 启动 | 用户 %s | %s | 管理员=%v",
		"[>>] RDP Multi started | user %s | %s | admin=%v"),
		currentUserName(), orDefault(os.Getenv("SESSIONNAME"), "?"), A.elevated)
	if A.lg.Path() != "" {
		A.lg.Say(T("     日志文件: %s", "     log file: %s"), A.lg.Path())
	} else {
		A.lg.Say(T("[!!] 日志文件创建失败(ProgramData 不可写),仅保留内存日志",
			"[!!] could not create a log file (ProgramData not writable); keeping memory log only"))
	}
	if !A.elevated {
		A.lg.Say(T("[!!] 未获得管理员权限:解锁/还原/远程设置都将被拒绝(需要 UAC 提权)",
			"[!!] not elevated: unlock / restore / settings will be refused (requires UAC)"))
	}

	A.reloadSettings()
	A.refreshAll(false)
	A.lg.Say(T("[>>] 正在枚举账户并探测密码(空密码试登录一次,有密码的账户失败计数 +1)",
		"[>>] enumerating accounts and probing passwords (one blank-password logon each)"))
	A.installTestHooks()

	var m msgStruct
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	A.lg.Close()
}

func loadCursor(id uintptr) syscall.Handle {
	h, _, _ := procLoadCursorW.Call(0, id)
	return syscall.Handle(h)
}

// ---------------------------------------------------------------- 窗口过程

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	if A == nil {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}
	switch msg {
	case wmPaint:
		var ps paintStruct
		hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		A.present(hdc)
		procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0
	case wmEraseBkgnd:
		return 1
	case wmSize:
		A.invalidate()
		return 0
	case wmTimer:
		A.pump()
		return 0
	case customClickMsg:
		A.onClick(int(wp))
		return 0
	case wmMouseMove:
		A.onMouseMove(int32(int16(lp&0xFFFF)), int32(int16((lp>>16)&0xFFFF)))
		return 0
	case wmLButtonDown:
		A.onMouseDown(int32(int16(lp&0xFFFF)), int32(int16((lp>>16)&0xFFFF)))
		return 0
	case wmLButtonUp:
		A.onMouseUp()
		return 0
	case wmMouseLeave:
		A.tracking = false
		if A.hover != idNone {
			A.hover = idNone
			A.invalidate()
		}
		return 0
	case wmMouseWheel:
		A.onWheel(int32(int16((wp >> 16) & 0xFFFF)))
		return 0
	case wmSetCursor:
		if A.hover != idNone {
			procSetCursor.Call(uintptr(loadCursor(idcHand)))
			return 1
		}
		procSetCursor.Call(uintptr(loadCursor(idcArrow)))
		return 1
	case wmKeyDown:
		return A.onKeyDown(wp)
	case wmChar:
		A.onChar(rune(wp))
		return 0
	case wmGetMinMaxInf:
		mmi := (*minMaxInfo)(unsafe.Pointer(lp))
		rc := rect{0, 0, int32(A.s(880)), int32(A.s(560))}
		procAdjustWindowRect.Call(uintptr(unsafe.Pointer(&rc)),
			uintptr(wsOverlapped|wsCaption|wsSysMenu|wsMinimizeBox|wsMaximizeBox|wsThickFrame), 0)
		mmi.PtMinTrack[0] = rc.R - rc.L
		mmi.PtMinTrack[1] = rc.B - rc.T
		return 0
	case wmClose:
		if A.busy {
			A.toastMsg(T("操作进行中,请等待完成后再关闭", "An operation is running, please wait"), lvWarn)
			return 0
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

// ---------------------------------------------------------------- 绘制

func (a *app) present(hdc uintptr) {
	var rc rect
	procGetClientRect.Call(a.hwnd, uintptr(unsafe.Pointer(&rc)))
	w, h := rc.R, rc.B
	if w <= 0 || h <= 0 {
		return
	}
	if a.surf == nil || a.surf.w != w || a.surf.h != h {
		if a.surf != nil {
			a.surf.free()
		}
		a.surf = newSurface(hdc, w, h)
	}
	if a.surf == nil {
		return
	}
	procGdiFlush.Call()
	a.draw(a.surf)
	a.surf.blitTo(hdc)
}

func (a *app) draw(c *surface) {
	a.hits = a.hits[:0]
	W, H := float32(c.w), float32(c.h)

	c.fillRect(rf(0, 0, W, H), colBg, 1)
	c.gradRect(rf(0, 0, W, a.s(110)), colBgGlow, colBg)

	pad := a.s(16)
	x := pad
	w := W - pad*2
	y := a.s(9)

	a.drawHeader(c, x, y, w)
	y += a.s(32)
	a.drawNav(c, x, y, w)
	y += a.s(32)

	statusH := a.s(24)
	contentH := H - y - statusH - a.s(4)
	if contentH < a.s(80) {
		contentH = a.s(80)
	}

	switch a.page {
	case pageSettings:
		a.drawSettings(c, x, y, w, contentH)
	case pageLog:
		a.drawLogPage(c, x, y, w, contentH)
	default:
		a.drawMain(c, x, y, w, contentH)
	}

	a.drawStatusBar(c, 0, H-statusH, W, statusH)

	if a.modal != nil {
		a.hits = nil
		a.drawModal(c, W, H)
	}
	a.drawToast(c, W, H)
}

func (a *app) hit(id int, r rectF) (hover, press bool) {
	a.hits = append(a.hits, hitRegion{id, r})
	return a.hover == id, a.press == id && a.hover == id
}

// button 画按钮。style: 0=ghost 1=secondary 2=primary
func (a *app) button(c *surface, id int, r rectF, label string, style int, enabled bool) {
	hov, prs := a.hit(id, r)
	rad := a.s(8)
	fs := int32(a.s(12))
	switch style {
	case 2:
		c1, c2 := uint32(colAccent), uint32(colAccentD)
		if !enabled {
			c1, c2 = 0x333B4C, 0x2B3242
		} else if prs {
			c1, c2 = 0x3E7BE8, 0x2455BE
		} else if hov {
			c1, c2 = 0x5E9AFF, 0x336FE6
		}
		c.fillRoundGrad(r, rad, c1, c2, 1)
		c.text(label, r, fs, 0xFFFFFF, alignCenter, true)
	case 1:
		bg := uint32(colCardHi)
		if !enabled {
			bg = colCard
		} else if prs {
			bg = colCardAct
		} else if hov {
			bg = 0x232C3D
		}
		c.fillRound(r, rad, bg, 1)
		c.strokeRound(r, rad, colBorderHi, 0.9, 1)
		col := uint32(colText)
		if !enabled {
			col = colTextMute
		}
		c.text(label, r, fs, col, alignCenter, false)
	default:
		bg := uint32(0)
		if hov && enabled {
			bg = colCardHi
		}
		if bg != 0 {
			c.fillRound(r, rad, bg, 1)
		}
		col := uint32(colTextDim)
		if !enabled {
			col = colTextMute
		} else if hov {
			col = colText
		}
		c.text(label, r, fs, col, alignCenter, false)
	}
}

func (a *app) switchCtl(c *surface, id int, r rectF, on bool) {
	hov, _ := a.hit(id, r)
	track := uint32(0x323B4E)
	if on {
		track = colAccent
	}
	if hov {
		track = lerpCol(track, 0xFFFFFF, 0.12)
	}
	c.fillRound(r, r.H/2, track, 1)
	knobX := r.X + a.s(2)
	if on {
		knobX = r.right() - r.H + a.s(2)
	}
	c.fillRound(rf(knobX, r.Y+a.s(2), r.H-a.s(4), r.H-a.s(4)), (r.H-a.s(4))/2, 0xF2F5FB, 1)
}

func (a *app) pill(c *surface, r rectF, label string, col uint32) {
	c.fillRound(r, r.H/2, col, 0.14)
	c.strokeRound(r, r.H/2, col, 0.28, 1)
	c.text(label, r, int32(a.s(10)), col, alignCenter, false)
}

func (a *app) drawHeader(c *surface, x, y, w float32) {
	c.text("RDP Multi", rf(x, y, a.s(200), a.s(24)), int32(a.s(17)), colText, alignLeft, true)
	tw := c.textWidth("RDP Multi", int32(a.s(17)), true)
	c.text(T("多账户并发会话解锁", "concurrent multi-user sessions"),
		rf(x+tw+a.s(9), y+a.s(2), a.s(220), a.s(22)), int32(a.s(10.5)), colTextMute, alignLeft, false)

	bw := a.s(38)
	a.button(c, idLang, rf(x+w-bw, y+a.s(2), bw, a.s(21)), langBadge(), 0, true)

	label, col := T("检测中", "detecting"), uint32(colTextDim)
	switch {
	case a.st.State == psUnlocked:
		label, col = T("已解锁", "unlocked"), colOK
	case a.st.State == psUnlockable:
		label, col = T("未解锁 · 可修补", "locked · patchable"), colWarn
	case a.st.State == psUnsupported:
		label, col = T("不支持", "unsupported"), colErr
	}
	pw := c.textWidth(label, int32(a.s(11)), true) + a.s(26)
	pr := rf(x+w-bw-a.s(8)-pw, y+a.s(2), pw, a.s(21))
	c.fillRound(pr, pr.H/2, col, 0.13)
	c.strokeRound(pr, pr.H/2, col, 0.30, 1)
	c.fillRound(rf(pr.X+a.s(8), pr.Y+pr.H/2-a.s(2.5), a.s(5), a.s(5)), a.s(2.5), col, 1)
	c.text(label, rf(pr.X+a.s(16), pr.Y, pw-a.s(20), pr.H), int32(a.s(11)), col, alignLeft, true)
}

func (a *app) drawNav(c *surface, x, y, w float32) {
	items := []struct {
		id    int
		page  int
		label string
	}{
		{idNavMain, pageMain, T("主界面", "Main")},
		{idNavSettings, pageSettings, T("远程设置", "Settings")},
		{idNavLog, pageLog, T("运行日志", "Log")},
	}
	bx := x
	h := a.s(25)
	for _, it := range items {
		tw := c.textWidth(it.label, int32(a.s(11.5)), false)
		bw := tw + a.s(24)
		r := rf(bx, y, bw, h)
		hov, prs := a.hit(it.id, r)
		active := a.page == it.page
		switch {
		case active:
			c.fillRound(r, a.s(7), colAccent, 0.18)
			c.strokeRound(r, a.s(7), colAccent, 0.34, 1)
		case prs:
			c.fillRound(r, a.s(7), colCardAct, 1)
		case hov:
			c.fillRound(r, a.s(7), colCardHi, 1)
		}
		col := uint32(colTextDim)
		if active {
			col = colText
		}
		c.text(it.label, r, int32(a.s(11.5)), col, alignCenter, active)
		if it.page == pageLog && a.logUnread {
			c.fillRound(rf(r.right()-a.s(7), r.Y+a.s(4), a.s(5), a.s(5)), a.s(2.5), colErr, 1)
		}
		bx += bw + a.s(5)
	}

	if a.accReady {
		usable, total := a.accountStats()
		c.text(fmt.Sprintf(T("%d/%d 账户可远程", "%d/%d accounts can RDP"), usable, total),
			rf(x, y, w, h), int32(a.s(10.5)), colTextMute, alignRight, false)
	}
}

// ---------------------------------------------------------------- 主界面

func (a *app) drawMain(c *surface, x, y, w, h float32) {
	top := y
	if a.banner() != nil {
		a.drawBanner(c, x, y, w)
		y += a.s(36)
	}
	a.drawInfoCard(c, x, y, w, a.s(64))
	y += a.s(64)

	a.drawActions(c, x, y, w, a.s(46))
	y += a.s(46)

	accH := top + h - y
	if accH < a.s(140) {
		accH = a.s(140)
	}
	a.drawAccounts(c, x, y, w, accH)
}

type bannerInfo struct {
	text   string
	lv     int
	action int
	actTxt string
}

func (a *app) banner() *bannerInfo {
	if !a.elevated {
		return &bannerInfo{T("没有管理员权限,解锁与还原都会被拒绝",
			"Not elevated: unlock and restore will be refused"), lvErr, idElevate,
			T("以管理员重启", "Relaunch as admin")}
	}
	if a.st.OsShort != "" && !a.st.RDPEnabled {
		return &bannerInfo{T("远程桌面尚未开启,远程设备无法连接本机",
			"Remote Desktop is off; no device can connect"), lvWarn, idEnableRdp,
			T("开启远程桌面", "Enable RDP")}
	}
	if a.st.State == psUnsupported && a.st.Detail != "" {
		return &bannerInfo{T("无法自动补丁:", "Cannot patch automatically: ") + a.st.Detail, lvErr, idNone, ""}
	}
	if a.accReady {
		if usable, _ := a.accountStats(); usable == 0 {
			return &bannerInfo{T("没有可用于远程登录的账户(需 启用 + 有密码 + 远程权限)",
				"No account can log on remotely (enabled + password + RDP permission required)"), lvWarn, idNone, ""}
		}
	}
	if sessionIsRDP() {
		return &bannerInfo{T("当前在远程会话中:替换文件后需重启系统才会生效",
			"Inside an RDP session: the file swap takes effect after a reboot"), lvInfo, idNone, ""}
	}
	return nil
}

func (a *app) drawBanner(c *surface, x, y, w float32) {
	bn := a.banner()
	if bn == nil {
		return
	}
	col := uint32(colWarn)
	switch bn.lv {
	case lvErr:
		col = colErr
	case lvInfo:
		col = colAccent
	}
	r := rf(x, y, w, a.s(30))
	c.fillRound(r, a.s(8), col, 0.10)
	c.strokeRound(r, a.s(8), col, 0.26, 1)
	c.fillRound(rf(r.X, r.Y+a.s(7), a.s(3), r.H-a.s(14)), a.s(1.5), col, 0.9)
	c.text(bn.text, rf(r.X+a.s(13), r.Y, r.W-a.s(180), r.H), int32(a.s(11)), col, alignLeft, false)
	if bn.action != idNone {
		bw := c.textWidth(bn.actTxt, int32(a.s(11)), false) + a.s(20)
		a.button(c, bn.action, rf(r.right()-bw-a.s(5), r.Y+a.s(4), bw, r.H-a.s(8)), bn.actTxt, 1, true)
	}
}

func (a *app) drawInfoCard(c *surface, x, y, w, h float32) {
	r := rf(x, y, w, h)
	c.fillRound(r, a.s(10), colCard, 1)
	c.strokeRound(r, a.s(10), colBorder, 0.9, 1)

	cw := w / 3
	for i := 1; i < 3; i++ {
		c.vline(x+cw*float32(i), y+a.s(10), h-a.s(20), colBorder, 0.8)
	}
	lbl := func(i int) rectF {
		return rf(x+cw*float32(i)+a.s(13), y+a.s(7), cw-a.s(24), a.s(13))
	}
	val := func(i int) rectF {
		return rf(x+cw*float32(i)+a.s(13), y+a.s(21), cw-a.s(24), a.s(19))
	}
	sub := func(i int) rectF {
		return rf(x+cw*float32(i)+a.s(13), y+a.s(40), cw-a.s(24), a.s(13))
	}

	c.text(T("操作系统", "Operating system"), lbl(0), int32(a.s(9.5)), colTextMute, alignLeft, false)
	c.text(orDefault(a.st.OsShort, T("读取中…", "reading…")), val(0), int32(a.s(12)), colText, alignLeft, true)
	c.text(orDefault(a.st.OsSub, T("Build 读取中", "reading build")), sub(0), int32(a.s(9)), colTextMute, alignLeft, false)

	c.text(T("远程桌面服务", "Remote Desktop service"), lbl(1), int32(a.s(9.5)), colTextMute, alignLeft, false)
	c.text(orDefault(a.st.TermsrvVer, T("读取中…", "reading…")), val(1), int32(a.s(12)), colText, alignLeft, true)
	c.text(orDefault(a.st.PatchText, T("补丁点检测中…", "scanning patch sites…")), sub(1), int32(a.s(9)), colTextMute, alignLeft, false)

	c.text(T("并发状态", "Session state"), lbl(2), int32(a.s(9.5)), colTextMute, alignLeft, false)
	stateTxt, stateCol := T("未解锁", "locked"), uint32(colWarn)
	switch a.st.State {
	case psUnlocked:
		stateTxt, stateCol = T("已解锁 · 可双会话", "unlocked · two sessions"), colOK
	case psUnsupported:
		stateTxt, stateCol = T("不支持", "unsupported"), colErr
	}
	c.text(stateTxt, val(2), int32(a.s(12)), stateCol, alignLeft, true)
	c.text(orDefault(a.st.PolicyText, T("策略读取中", "reading policy")), sub(2), int32(a.s(9)), colTextMute, alignLeft, false)
}

func (a *app) drawActions(c *surface, x, y, w, h float32) {
	bh := a.s(34)
	by := y + (h-bh)/2

	unlocked := a.st.State == psUnlocked
	// 解锁后主按钮变为"一键还原",解锁按钮禁用;未解锁时相反。
	if unlocked {
		a.button(c, idRestore, rf(x, by, a.s(140), bh), T("一键还原", "Restore"), 2, !a.busy)
		a.button(c, idUnlock, rf(x+a.s(148), by, a.s(104), bh), T("一键解锁", "Unlock"), 1, false)
	} else {
		a.button(c, idUnlock, rf(x, by, a.s(140), bh), T("一键解锁", "Unlock"), 2, !a.busy)
		a.button(c, idRestore, rf(x+a.s(148), by, a.s(104), bh), T("一键还原", "Restore"), 1, !a.busy && a.st.HasState)
	}
	a.button(c, idRefresh, rf(x+a.s(260), by, a.s(82), bh), T("重新体检", "Re-check"), 1, !a.busy)

	hint := T("本机账户 A 保持登录,mstsc 用账户 B 连接,互不踢出",
		"Keep account A signed in locally, connect with account B over mstsc")
	if unlocked {
		hint = T("已解锁:两个账户可并行登录;需要恢复时点左侧「一键还原」",
			"Unlocked: both accounts can run at once; use Restore to undo")
	}
	if a.busy {
		hint = fmt.Sprintf(T("正在执行:%s(详情见「运行日志」)", "Running: %s (see Log)"), a.busyLabel)
	}
	c.text(hint, rf(x+a.s(350), by, w-a.s(356), bh), int32(a.s(10)), colTextMute, alignLeft, false)
}

// ---------------------------------------------------------------- 账户区

func (a *app) visibleAccounts() []acctInfo {
	if !a.hideBuiltin {
		return a.accts
	}
	out := make([]acctInfo, 0, len(a.accts))
	for _, x := range a.accts {
		if !x.BuiltIn {
			out = append(out, x)
		}
	}
	return out
}

func (a *app) accountStats() (usable, total int) {
	for _, x := range a.accts {
		if x.Disabled {
			continue
		}
		total++
		if x.RemoteOK && x.Blank != nil && !*x.Blank {
			usable++
		}
	}
	return
}

func (a *app) drawAccounts(c *surface, x, y, w, h float32) {
	r := rf(x, y, w, h)
	c.fillRound(r, a.s(10), colCard, 1)
	c.strokeRound(r, a.s(10), colBorder, 0.9, 1)

	c.text(T("账户", "Accounts"), rf(x+a.s(13), y+a.s(7), a.s(110), a.s(18)), int32(a.s(12)), colText, alignLeft, true)
	atw := c.textWidth(T("账户", "Accounts"), int32(a.s(12)), true)
	hidden := 0
	for _, acc := range a.accts {
		if acc.BuiltIn {
			hidden++
		}
	}
	subTxt := fmt.Sprintf(T("%d 个账户", "%d accounts"), len(a.accts))
	if a.hideBuiltin && hidden > 0 {
		subTxt = fmt.Sprintf(T("%d 个账户 · 已隐藏 %d 个内置", "%d accounts · %d built-in hidden"),
			len(a.accts), hidden)
	}
	c.text(subTxt, rf(x+a.s(13)+atw+a.s(10), y+a.s(8), w-a.s(260), a.s(16)), int32(a.s(10)), colTextMute, alignLeft, false)

	swW, swH := a.s(28), a.s(15)
	swR := rf(x+w-a.s(138), y+a.s(9), swW, swH)
	a.hit(idHideBuiltin, rf(swR.X-a.s(48), swR.Y-a.s(5), swW+a.s(90), swH+a.s(10)))
	a.switchCtl(c, idHideBuiltin, swR, a.hideBuiltin)
	c.text(T("隐藏内置账户", "Hide built-in"), rf(swR.right()+a.s(6), swR.Y-a.s(4), a.s(82), swH+a.s(8)),
		int32(a.s(10)), colTextDim, alignLeft, false)

	hy := y + a.s(27)
	cols := a.accountColumns(x, w)
	for i, name := range []string{T("账户名", "Account"), T("状态", "State"), T("密码", "Password"), T("远程权限", "RDP access")} {
		c.text(name, rf(cols[i], hy, a.s(90), a.s(13)), int32(a.s(9)), colTextMute, alignLeft, false)
	}
	c.hline(x+a.s(13), hy+a.s(15), w-a.s(26), colBorder, 0.7)

	listTop := hy + a.s(20)
	listBot := r.bottom() - a.s(36)
	listH := listBot - listTop
	if listH < a.s(28) {
		listH = a.s(28)
	}

	footY := r.bottom() - a.s(31)
	c.hline(x+a.s(13), footY-a.s(4), w-a.s(26), colBorder, 0.7)
	label := T("先在列表中选中一个账户", "Select an account first")
	if acc := a.findSelected(); acc != nil {
		label = fmt.Sprintf(T("已选中:%s", "Selected: %s"), acc.Name)
	}
	c.text(label, rf(x+a.s(13), footY, w-a.s(260), a.s(26)), int32(a.s(10)), colTextMute, alignLeft, false)
	sel := a.findSelected() != nil
	bw := a.s(96)
	a.button(c, idSetPwd, rf(r.right()-a.s(13)-bw, footY, bw, a.s(24)), T("设置密码", "Set password"), 1, sel && !a.busy)
	a.button(c, idAddGrp, rf(r.right()-a.s(13)-bw*2-a.s(6), footY, bw, a.s(24)), T("加入远程组", "Add to group"), 1, sel && !a.busy)

	list := a.visibleAccounts()
	if !a.accReady {
		c.text(T("正在枚举账户…", "Enumerating accounts…"), rf(listTop+a.s(4), listTop, w, listH),
			int32(a.s(11)), colTextMute, alignLeft, false)
		return
	}
	if len(list) == 0 {
		c.text(T("没有可显示的账户(内置账户已被隐藏)", "Nothing to show (built-in accounts are hidden)"),
			rf(listTop+a.s(4), listTop, w, listH), int32(a.s(11)), colTextMute, alignLeft, false)
		return
	}

	rowH := a.s(30)
	totalH := rowH * float32(len(list))
	maxScroll := totalH - listH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if a.accScroll > maxScroll {
		a.accScroll = maxScroll
	}
	if a.accScroll < 0 {
		a.accScroll = 0
	}

	for i, acc := range list {
		ry := listTop + rowH*float32(i) - a.accScroll
		if ry+rowH < listTop || ry > listBot {
			continue
		}
		rowPad := float32(0)
		if maxScroll > 0 {
			rowPad = a.s(8)
		}
		rowR := rf(x+a.s(7), ry+a.s(1), w-a.s(14)-rowPad, rowH-a.s(2))
		hov, _ := a.hit(idRowBase+i, rowR)

		selected := a.selName != "" && strings.EqualFold(a.selName, acc.Name)
		switch {
		case selected:
			c.fillRound(rowR, a.s(7), colAccent, 0.16)
			c.strokeRound(rowR, a.s(7), colAccent, 0.35, 1)
		case hov:
			c.fillRound(rowR, a.s(7), colCardHi, 1)
		}

		nameCol := uint32(colText)
		if acc.Disabled {
			nameCol = colTextMute
		}
		c.text(acc.Name, rf(cols[0], ry, a.s(140), rowH), int32(a.s(11.5)), nameCol, alignLeft, true)
		if acc.BuiltIn {
			lbl := T("内置", "built-in")
			bw := c.textWidth(lbl, int32(a.s(9)), false) + a.s(12)
			a.pill(c, rf(cols[0]+c.textWidth(acc.Name, int32(a.s(11.5)), true)+a.s(6), ry+rowH/2-a.s(8), bw, a.s(16)),
				lbl, colViolet)
		}

		stTxt, stCol := T("启用", "enabled"), uint32(colOK)
		if acc.Disabled {
			stTxt, stCol = T("禁用", "disabled"), colTextMute
		}
		a.pill(c, rf(cols[1], ry+rowH/2-a.s(8), a.s(54), a.s(16)), stTxt, stCol)

		pwdTxt, pwdCol := T("未探测", "unknown"), uint32(colTextMute)
		pwdW := a.s(54)
		if acc.Blank != nil {
			if *acc.Blank {
				pwdTxt, pwdCol = T("空密码", "blank"), colErr
			} else {
				pwdTxt, pwdCol = T("已设置", "set"), colOK
			}
		} else if acc.Disabled {
			pwdTxt = T("跳过", "skipped")
		} else if acc.Locked {
			pwdTxt, pwdCol = T("已锁定", "locked out"), colWarn
			pwdW = a.s(66)
		}
		a.pill(c, rf(cols[2], ry+rowH/2-a.s(8), pwdW, a.s(16)), pwdTxt, pwdCol)

		remTxt, remCol := T("无", "no"), uint32(colTextMute)
		if acc.RemoteOK {
			remTxt, remCol = T("允许", "yes"), colAccent
		}
		a.pill(c, rf(cols[3], ry+rowH/2-a.s(8), a.s(46), a.s(16)), remTxt, remCol)
	}

	if maxScroll > 0 {
		sbH := listH * (listH / totalH)
		if sbH < a.s(18) {
			sbH = a.s(18)
		}
		sbY := listTop + (listH-sbH)*(a.accScroll/maxScroll)
		c.fillRound(rf(r.right()-a.s(5), sbY, a.s(3), sbH), a.s(1.5), colBorderHi, 0.9)
	}
}

func (a *app) accountColumns(x, w float32) [4]float32 {
	return [4]float32{x + a.s(13), x + w*0.50, x + w*0.64, x + w*0.78}
}

func (a *app) findSelected() *acctInfo {
	if a.selName == "" {
		return nil
	}
	for i := range a.accts {
		if strings.EqualFold(a.accts[i].Name, a.selName) {
			return &a.accts[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------- 远程设置页

func (a *app) reloadSettings() {
	st := loadState()
	ss := loadSettingsStore()
	for _, d := range settingsDefs {
		id := settingID(d.Key, d.Name)
		v, ok := readSetting(d.Key, d.Name)
		a.setVals[id] = settingVal{v, ok}

		var ds dwordState
		var has bool
		if st != nil {
			if m, ok2 := st.Policy[d.Key]; ok2 {
				if d2, ok3 := m[d.Name]; ok3 {
					ds, has = d2, true
				}
			}
		}
		if !has {
			ds, has = ss.Items[id]
		}
		if has {
			a.setOrig[id] = ds
		} else {
			delete(a.setOrig, id)
		}
	}
}

func (a *app) settingText(d settingDef) string {
	v := a.setVals[settingID(d.Key, d.Name)]
	if !v.ok {
		return T("未设置", "not set")
	}
	switch d.Kind {
	case skBool, skBoolInv:
		if boolOn(d, v.v) {
			return T("开", "on")
		}
		return T("关", "off")
	case skEnum:
		return enumLabel(d, v.v)
	default:
		n := strconv.FormatUint(uint64(v.v), 10)
		if d.Ms && v.v > 0 {
			return n + " ms"
		}
		if u := d.unit(); u != "" {
			return n + " " + u
		}
		return n
	}
}

func (a *app) settingModified(d settingDef) bool {
	id := settingID(d.Key, d.Name)
	orig, has := a.setOrig[id]
	if !has {
		return false
	}
	cur, curOK := a.setVals[id]
	if orig.Existed != curOK {
		return true
	}
	if !orig.Existed {
		return false
	}
	return orig.Value != cur.v
}

func (a *app) drawSettings(c *surface, x, y, w, h float32) {
	r := rf(x, y, w, h)
	c.fillRound(r, a.s(10), colCard, 1)
	c.strokeRound(r, a.s(10), colBorder, 0.9, 1)

	c.text(T("远程设置", "Remote settings"), rf(x+a.s(13), y+a.s(7), a.s(160), a.s(18)),
		int32(a.s(12)), colText, alignLeft, true)
	tw := c.textWidth(T("远程设置", "Remote settings"), int32(a.s(12)), true)
	c.text(T("改动即时生效;橙色标记的项可单独还原", "Changes apply immediately; orange items have a saved original"),
		rf(x+a.s(13)+tw+a.s(10), y+a.s(8), w-a.s(300)-tw, a.s(16)), int32(a.s(10)), colTextMute, alignLeft, false)

	bw := a.s(78)
	a.button(c, idSetRestoreAll, rf(r.right()-a.s(13)-bw, y+a.s(6), bw, a.s(22)),
		T("全部还原", "Restore all"), 1, !a.busy && len(a.setOrig) > 0)
	a.button(c, idSetRefresh, rf(r.right()-a.s(13)-bw-a.s(56), y+a.s(6), a.s(50), a.s(22)),
		T("刷新", "Refresh"), 1, !a.busy)

	listTop := y + a.s(32)
	listBot := r.bottom() - a.s(6)
	listH := listBot - listTop
	if listH < a.s(50) {
		return
	}
	c.hline(x+a.s(1), listTop, w-a.s(2), colBorder, 0.8)

	// 布局:分组标题 26 + 每行 58,超出可滚动
	const secH, rowH = 26.0, 58.0
	secIdx := []int{secConn, secSession, secRedirect}
	totalH := a.s(float32(len(secIdx))*secH + float32(len(settingsDefs))*rowH)

	maxScroll := totalH - listH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if a.setScroll > maxScroll {
		a.setScroll = maxScroll
	}
	if a.setScroll < 0 {
		a.setScroll = 0
	}

	cur := listTop - a.setScroll
	idx := 0
	for _, sec := range secIdx {
		if cur+a.s(secH) > listTop && cur < listBot {
			c.text(strings.ToUpper(sectionTitle(sec)), rf(x+a.s(13), cur+a.s(6), w-a.s(30), a.s(16)),
				int32(a.s(10)), colTextMute, alignLeft, true)
			c.hline(x+a.s(13), cur+a.s(secH)-a.s(4), w-a.s(26), colBorder, 0.5)
		}
		cur += a.s(secH)

		for idx < len(settingsDefs) && settingsDefs[idx].Sec == sec {
			ry := cur
			if ry+a.s(rowH) > listTop && ry < listBot {
				a.drawSettingRow(c, x, w, r, settingsDefs[idx], idx, ry)
			}
			cur += a.s(rowH)
			idx++
		}
	}

	if maxScroll > 0 {
		sbH := listH * (listH / totalH)
		if sbH < a.s(24) {
			sbH = a.s(24)
		}
		sbY := listTop + (listH-sbH)*(a.setScroll/maxScroll)
		c.fillRound(rf(r.right()-a.s(5), sbY, a.s(4), sbH), a.s(2), colBorderHi, 1)
	}
}

func (a *app) drawSettingRow(c *surface, x, w float32, r rectF, d settingDef, idx int, ry float32) {
	id := settingID(d.Key, d.Name)
	modified := a.settingModified(d)
	ctlW := a.s(118)
	rstW := a.s(58)
	textW := w - ctlW - rstW - a.s(56)

	c.text(d.label(), rf(x+a.s(13), ry+a.s(9), textW, a.s(19)), int32(a.s(12)), colText, alignLeft, false)
	c.text(d.desc(), rf(x+a.s(13), ry+a.s(31), textW, a.s(16)), int32(a.s(10.5)), colTextMute, alignLeft, false)

	ctlR := rf(r.right()-a.s(13)-rstW-a.s(10)-ctlW, ry+a.s(16), ctlW, a.s(26))
	switch d.Kind {
	case skBool, skBoolInv:
		cur := a.setVals[id]
		on := cur.ok && boolOn(d, cur.v)
		a.switchCtl(c, idSetCtlBase+idx, rf(ctlR.right()-a.s(34), ctlR.Y+a.s(4), a.s(34), a.s(18)), on)
		c.text(a.settingText(d), rf(ctlR.X, ctlR.Y, ctlW-a.s(42), ctlR.H),
			int32(a.s(11)), colTextDim, alignRight, false)
	default:
		hov, prs := a.hit(idSetCtlBase+idx, ctlR)
		bg := uint32(0)
		if prs {
			bg = colCardAct
		} else if hov {
			bg = colCardHi
		}
		if bg != 0 {
			c.fillRound(ctlR, a.s(7), bg, 1)
		}
		c.strokeRound(ctlR, a.s(7), colBorderHi, 0.8, 1)
		c.text(a.settingText(d), ctlR, int32(a.s(11.5)), colText, alignCenter, false)
	}

	if _, has := a.setOrig[id]; has {
		rr := rf(r.right()-a.s(13)-rstW, ry+a.s(16), rstW, a.s(26))
		a.button(c, idSetRstBase+idx, rr, T("还原", "Restore"), 1, !a.busy)
		if modified {
			c.strokeRound(rr, a.s(8), colWarn, 0.55, 1)
		}
	} else {
		c.text("—", rf(r.right()-a.s(13)-rstW, ry+a.s(16), rstW, a.s(26)),
			int32(a.s(11.5)), colTextMute, alignRight, false)
	}

	c.hline(x+a.s(13), ry+a.s(58), w-a.s(26), colBorder, 0.4)
}

// ---------------------------------------------------------------- 日志页

func (a *app) drawLogPage(c *surface, x, y, w, h float32) {
	r := rf(x, y, w, h)
	c.fillRound(r, a.s(10), colCard, 1)
	c.strokeRound(r, a.s(10), colBorder, 0.9, 1)

	c.text(T("运行日志", "Log"), rf(x+a.s(13), y+a.s(7), a.s(70), a.s(18)), int32(a.s(12)), colText, alignLeft, true)
	tw := c.textWidth(T("运行日志", "Log"), int32(a.s(12)), true)
	c.text(fmt.Sprintf(T("%d 行 · 同时落盘 %s", "%d lines · also written to %s"), a.lg.Count(),
		orDefault(a.lg.Path(), T("(未落盘)", "(not persisted)"))),
		rf(x+a.s(13)+tw+a.s(10), y+a.s(8), w-a.s(200)-tw, a.s(16)), int32(a.s(9.5)), colTextMute, alignLeft, false)

	bw := a.s(52)
	bx := r.right() - a.s(13) - bw
	a.button(c, idClearLog, rf(bx, y+a.s(5), bw, a.s(21)), T("清空", "Clear"), 0, true)
	bx -= bw + a.s(3)
	a.button(c, idCopyLog, rf(bx, y+a.s(5), bw, a.s(21)), T("复制", "Copy"), 0, true)
	bx -= bw + a.s(3)
	a.button(c, idSaveLog, rf(bx, y+a.s(5), bw, a.s(21)), T("导出", "Export"), 0, true)

	bodyTop := y + a.s(31)
	bodyBot := r.bottom() - a.s(5)
	bodyH := bodyBot - bodyTop
	if bodyH < a.s(20) {
		return
	}
	c.hline(x+a.s(1), bodyTop, w-a.s(2), colBorder, 0.8)

	availW := w - a.s(22) - a.s(52)
	a.ensureWrap(c, availW)

	lineH := a.s(14)
	totalH := float32(len(a.wrap)) * lineH
	maxScroll := totalH - bodyH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if a.logFollow {
		a.logScroll = maxScroll
	}
	if a.logScroll > maxScroll {
		a.logScroll = maxScroll
	}
	if a.logScroll < 0 {
		a.logScroll = 0
	}

	first := int(a.logScroll / lineH)
	last := first + int(bodyH/lineH) + 2
	if last > len(a.wrap) {
		last = len(a.wrap)
	}
	fs := int32(a.s(10))
	for i := first; i < last; i++ {
		ln := a.wrap[i]
		ly := bodyTop + float32(i)*lineH - a.logScroll
		if len([]rune(ln.text)) == 0 {
			continue
		}
		col := uint32(colTextDim)
		switch ln.lv {
		case lvOK:
			col = colOK
		case lvWarn:
			col = colWarn
		case lvErr:
			col = colErr
		case lvStep:
			col = colAccent
		case lvAction:
			col = colViolet
		}
		if ln.head {
			c.text(ln.at.Format("15:04:05"), rf(x+a.s(13), ly, a.s(48), lineH), fs, colTextMute, alignLeft, false)
		}
		c.text(ln.text, rf(x+a.s(13)+a.s(52), ly, availW, lineH), fs, col, alignLeft, false)
	}

	if maxScroll > 0 {
		sbH := bodyH * (bodyH / totalH)
		if sbH < a.s(18) {
			sbH = a.s(18)
		}
		sbY := bodyTop + (bodyH-sbH)*(a.logScroll/maxScroll)
		c.fillRound(rf(r.right()-a.s(5), sbY, a.s(3), sbH), a.s(1.5), colBorderHi, 0.9)
	}
}

func (a *app) ensureWrap(c *surface, width float32) {
	if width != a.wrapWidth {
		a.wrapWidth = width
		a.wrap = a.wrap[:0]
		a.wrapFrom = 0
	}
	total := a.lg.Total()
	if a.wrapFrom >= total {
		return
	}
	entries := a.lg.From(a.wrapFrom)
	a.wrapFrom = total
	fs := int32(a.s(10))
	for _, e := range entries {
		if strings.TrimSpace(e.Text) == "" {
			a.wrap = append(a.wrap, logLine{text: "", lv: e.Level, head: true, at: e.At})
			continue
		}
		segs := wrapText(c, e.Text, width, fs)
		for i, s := range segs {
			a.wrap = append(a.wrap, logLine{text: s, lv: e.Level, head: i == 0, at: e.At})
		}
	}
	if len(a.wrap) > 6000 {
		a.wrap = a.wrap[len(a.wrap)-6000:]
	}
}

func wrapText(c *surface, s string, width float32, fs int32) []string {
	if c.textWidth(s, fs, false) <= width {
		return []string{s}
	}
	var out []string
	var cur []rune
	for _, r := range s {
		cur = append(cur, r)
		if c.textWidth(string(cur), fs, false) > width {
			line := string(cur[:len(cur)-1])
			if line == "" {
				line = string(cur)
				cur = cur[:0]
			} else {
				cur = cur[len(cur)-1:]
			}
			out = append(out, line)
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// ---------------------------------------------------------------- 状态栏 / toast

func (a *app) drawStatusBar(c *surface, x, y, w, h float32) {
	c.fillRect(rf(x, y, w, h), 0x0C0F15, 0.9)
	c.hline(x, y, w, colBorder, 0.7)

	dot := uint32(colOK)
	txt := T("就绪", "Ready")
	switch {
	case a.busy:
		dot = colAccent
		txt = fmt.Sprintf(T("执行中:%s", "Running: %s"), a.busyLabel)
	case a.st.State == psUnsupported:
		dot = colErr
		txt = T("当前系统不支持自动补丁", "This build is not supported for auto-patching")
	case a.st.State == psUnlocked:
		dot = colOK
		txt = T("已解锁 · 双会话可用", "Unlocked · two sessions available")
	case a.st.State == psUnlockable:
		dot = colWarn
		txt = T("待解锁", "Patchable")
	}
	c.fillRound(rf(x+a.s(17), y+h/2-a.s(3), a.s(6), a.s(6)), a.s(3), dot, 1)
	c.text(txt, rf(x+a.s(29), y, a.s(420), h), int32(a.s(10)), colTextDim, alignLeft, false)

	perm := T("普通权限", "Standard user")
	permCol := uint32(colWarn)
	if a.elevated {
		perm = T("管理员权限", "Administrator")
		permCol = colOK
	}
	pw := c.textWidth(perm, int32(a.s(10)), false)
	c.text(perm, rf(x+w-a.s(17)-pw, y, pw, h), int32(a.s(10)), permCol, alignRight, false)
	c.fillRound(rf(x+w-a.s(23)-pw, y+h/2-a.s(3), a.s(6), a.s(6)), a.s(3), permCol, 1)
}

func (a *app) drawToast(c *surface, W, H float32) {
	if a.toast == "" || time.Since(a.toastAt) > 4*time.Second {
		return
	}
	col := uint32(colOK)
	switch a.toastLv {
	case lvWarn:
		col = colWarn
	case lvErr:
		col = colErr
	}
	fs := int32(a.s(11))
	tw := c.textWidth(a.toast, fs, false) + a.s(32)
	if tw > W-a.s(50) {
		tw = W - a.s(50)
	}
	r := rf(W-a.s(16)-tw, H-a.s(24)-a.s(6)-a.s(32), tw, a.s(32))
	c.fillRound(r, a.s(8), 0x1A2030, 0.98)
	c.strokeRound(r, a.s(8), col, 0.5, 1)
	c.fillRound(rf(r.X+a.s(10), r.Y+r.H/2-a.s(3), a.s(6), a.s(6)), a.s(3), col, 1)
	c.text(a.toast, rf(r.X+a.s(21), r.Y, r.W-a.s(28), r.H), fs, colText, alignLeft, false)
}

// ---------------------------------------------------------------- 模态层

func (a *app) drawModal(c *surface, W, H float32) {
	c.fillRect(rf(0, 0, W, H), 0x05070B, 0.62)

	m := a.modal
	cw := a.s(420)
	ch := a.s(158)
	if m.kind != mkConfirm {
		ch = a.s(188)
	}
	cr := rf((W-cw)/2, (H-ch)/2, cw, ch)
	c.fillRound(cr, a.s(12), colCard, 1)
	c.strokeRound(cr, a.s(12), colBorderHi, 0.9, 1)

	c.text(m.title, rf(cr.X+a.s(18), cr.Y+a.s(15), cw-a.s(36), a.s(22)),
		int32(a.s(13)), colText, alignLeft, true)

	bodyW := cw - a.s(36)
	lines := wrapText(c, m.body, bodyW, int32(a.s(10.5)))
	by := cr.Y + a.s(42)
	for _, ln := range lines {
		if by > cr.Y+ch-a.s(74) {
			break
		}
		c.text(ln, rf(cr.X+a.s(18), by, bodyW, a.s(17)), int32(a.s(10.5)), colTextDim, alignLeft, false)
		by += a.s(17)
	}

	if m.kind != mkConfirm {
		ir := rf(cr.X+a.s(18), cr.bottom()-a.s(88), cw-a.s(36), a.s(28))
		c.fillRound(ir, a.s(7), 0x0F131B, 1)
		c.strokeRound(ir, a.s(7), colBorderHi, 0.9, 1)
		shown := string(m.input)
		if m.kind == mkPassword {
			shown = strings.Repeat("•", len(m.input))
		}
		c.text(shown, rf(ir.X+a.s(9), ir.Y, ir.W-a.s(18), ir.H), int32(a.s(11.5)), colText, alignLeft, false)
		if (time.Since(a.startAt).Milliseconds()/530)%2 == 0 {
			cx := ir.X + a.s(9) + c.textWidth(shown, int32(a.s(11.5)), false)
			c.fillRect(rf(cx, ir.Y+a.s(6), a.s(1.5), ir.H-a.s(12)), colAccent, 0.9)
		}
	}

	by2 := cr.bottom() - a.s(46)
	bw := a.s(80)
	bh := a.s(28)
	a.button(c, idModalCancel, rf(cr.right()-a.s(18)-bw, by2, bw, bh), T("取消", "Cancel"), 1, true)
	a.button(c, idModalOK, rf(cr.right()-a.s(18)-bw*2-a.s(7), by2, bw, bh), m.okText, 2, true)
}

// ---------------------------------------------------------------- 输入事件

func (a *app) hitAt(x, y float32) int {
	for i := len(a.hits) - 1; i >= 0; i-- {
		if a.hits[i].r.contains(x, y) {
			return a.hits[i].id
		}
	}
	return idNone
}

func (a *app) onMouseMove(x, y int32) {
	a.mx, a.my = float32(x), float32(y)
	if !a.tracking {
		a.tracking = true
		tme := trackMouseEvent{CbSize: uint32(unsafe.Sizeof(trackMouseEvent{})), DwFlags: 2, HwndTrack: a.hwnd}
		procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
	}
	if id := a.hitAt(a.mx, a.my); id != a.hover {
		a.hover = id
		a.invalidate()
	}
}

func (a *app) onMouseDown(x, y int32) {
	a.mx, a.my = float32(x), float32(y)
	id := a.hitAt(a.mx, a.my)
	if id == idNone {
		return
	}
	a.press = id
	procSetCapture.Call(a.hwnd)
	a.invalidate()
}

func (a *app) onMouseUp() {
	procReleaseCapture.Call()
	id := a.press
	a.press = idNone
	a.invalidate()
	if id == idNone {
		return
	}
	if a.hitAt(a.mx, a.my) != id {
		return
	}
	a.onClick(id)
}

func (a *app) onWheel(delta int32) {
	step := a.s(46) * float32(delta) / 120.0
	switch a.page {
	case pageLog:
		a.logScroll -= step
		a.logFollow = false
		if a.logScroll <= 0 {
			a.logScroll = 0
			a.logFollow = true
		}
	case pageSettings:
		a.setScroll -= step
		if a.setScroll < 0 {
			a.setScroll = 0
		}
	default:
		a.accScroll -= step
		if a.accScroll < 0 {
			a.accScroll = 0
		}
	}
	a.invalidate()
}

func (a *app) onKeyDown(vk uintptr) uintptr {
	if a.modal != nil {
		switch vk {
		case 13:
			a.modalConfirm()
			return 0
		case 27:
			a.modalClose()
			return 0
		case 8:
			if a.modal.kind != mkConfirm && len(a.modal.input) > 0 {
				a.modal.input = a.modal.input[:len(a.modal.input)-1]
				a.invalidate()
			}
			return 0
		}
	}
	return 0
}

func (a *app) onChar(r rune) {
	if a.modal == nil || a.modal.kind == mkConfirm {
		return
	}
	if r == '\r' || r == '\n' || r == 8 || r == 27 {
		return
	}
	if a.modal.kind == mkNumber && (r < '0' || r > '9') {
		return
	}
	if len(a.modal.input) < 64 {
		a.modal.input = append(a.modal.input, r)
		a.invalidate()
	}
}

// ---------------------------------------------------------------- 动作

func (a *app) navTo(p int) {
	if a.page == p {
		return
	}
	a.page = p
	if p == pageLog {
		a.logUnread = false
		a.logFollow = true
	}
	if p == pageSettings {
		a.reloadSettings()
	}
	name := map[int]string{
		pageMain:     T("主界面", "Main"),
		pageSettings: T("远程设置", "Settings"),
		pageLog:      T("运行日志", "Log"),
	}[p]
	a.lg.Say(T("[>>] 切换到「%s」", "[>>] switched to \"%s\""), name)
	a.invalidate()
}

func (a *app) onClick(id int) {
	switch {
	case id == idNavMain:
		a.navTo(pageMain)
		return
	case id == idNavSettings:
		a.navTo(pageSettings)
		return
	case id == idNavLog:
		a.navTo(pageLog)
		return
	case id == idLang:
		toggleLang()
		procSetWindowTextW.Call(a.hwnd, utf16p(T("RDP Multi — 多账户并发会话解锁",
			"RDP Multi — concurrent multi-user sessions")))
		a.lg.Say(T("[>>] 界面语言:中文", "[>>] UI language: English"))
		a.invalidate()
		return
	case id == idHideBuiltin:
		a.hideBuiltin = !a.hideBuiltin
		sel := T("隐藏", "hide")
		if !a.hideBuiltin {
			sel = T("显示", "show")
		}
		a.lg.Say(T("[>>] 账户列表:%s内置账户", "[>>] account list: %s built-in accounts"), sel)
		a.invalidate()
		return
	case id == idClearLog:
		a.lg.Clear()
		a.wrap = a.wrap[:0]
		a.wrapFrom = 0
		a.logFollow = true
		a.lg.Say(T("[>>] 日志窗口已清空(磁盘日志文件不受影响)",
			"[>>] log view cleared (the file on disk is untouched)"))
		a.invalidate()
		return
	case id == idCopyLog:
		a.copyLog()
		return
	case id == idSaveLog:
		a.exportLog()
		return
	case id == idSetRefresh:
		if a.busy {
			a.lg.Say(T("[!!] 忽略[刷新]:已有操作在进行(%s)", "[!!] Refresh ignored: %s is running"), a.busyLabel)
			return
		}
		a.reloadSettings()
		a.lg.Say(T("[>>] 远程设置:已重新读取当前值(%d 项)", "[>>] settings reloaded (%d items)"), len(settingsDefs))
		a.invalidate()
		return
	case id == idSetRestoreAll:
		a.restoreAllSettings()
		return
	case id == idRefresh:
		if a.busy {
			a.lg.Say(T("[!!] 忽略[重新体检]:已有操作在进行(%s)", "[!!] Re-check ignored: %s is running"), a.busyLabel)
			return
		}
		a.lg.Say(T("[>>] 手动触发重新体检(将重新探测账户密码)",
			"[>>] manual re-check (passwords will be probed again)"))
		a.refreshAll(true)
		return
	case id == idElevate:
		a.relaunchElevated()
		return
	case id == idEnableRdp:
		if a.busy {
			a.lg.Say(T("[!!] 忽略[开启远程桌面]:已有操作在进行", "[!!] Ignored: another operation is running"))
			return
		}
		a.runOp(T("开启远程桌面", "Enable Remote Desktop"), func() error {
			if err := regSetDWORD(tsKey, "fDenyTSConnections", 0); err != nil {
				return fmt.Errorf(T("写入 fDenyTSConnections 失败: %v", "writing fDenyTSConnections failed: %v"), err)
			}
			a.lg.Say(T("[OK] 远程桌面已开启(fDenyTSConnections=0)",
				"[OK] Remote Desktop enabled (fDenyTSConnections=0)"))
			return nil
		})
		return
	case id == idUnlock:
		if a.busy {
			a.lg.Say(T("[!!] 忽略[一键解锁]:已有操作在进行(%s)", "[!!] Unlock ignored: %s is running"), a.busyLabel)
			return
		}
		if a.st.State == psUnlocked {
			a.lg.Say(T("[!!] 忽略[一键解锁]:termsrv.dll 已是解锁状态,无需重复操作",
				"[!!] Unlock ignored: termsrv.dll is already patched"))
			a.toastMsg(T("当前已解锁,无需重复操作", "Already unlocked"), lvWarn)
			return
		}
		a.lg.Say(T("[>>] 点击[一键解锁],等待确认", "[>>] Unlock clicked, waiting for confirmation"))
		a.openConfirm(T("确认解锁", "Confirm unlock"),
			T("将写入策略注册表并补丁 termsrv.dll(CDefPolicy 三处许可闸门,共 6 字节)。\n"+
				"原始 DLL 与注册表值会自动备份,可随时一键还原。\n"+
				"过程中会重启远程桌面服务(若在远程会话内,可能断开连接)。",
				"This writes the policy registry values and patches termsrv.dll\n"+
					"(three CDefPolicy license gates, 6 bytes in total).\n"+
					"The original DLL and registry values are backed up automatically.\n"+
					"The Remote Desktop service restarts and may drop an active RDP session."),
			T("确认解锁", "Unlock"), func() {
				a.runOp(T("一键解锁", "Unlock"), func() error { return runUnlock(unlockOpts{}, a.lg.Say) })
			})
		return
	case id == idRestore:
		if a.busy {
			a.lg.Say(T("[!!] 忽略[一键还原]:已有操作在进行(%s)", "[!!] Restore ignored: %s is running"), a.busyLabel)
			return
		}
		if !a.st.HasState {
			a.lg.Say(T("[!!] 忽略[一键还原]:未找到操作记录(state.json 不存在)",
				"[!!] Restore ignored: no state.json found"))
			a.toastMsg(T("没有可还原的记录", "Nothing to restore"), lvWarn)
			return
		}
		a.lg.Say(T("[>>] 点击[一键还原],等待确认", "[>>] Restore clicked, waiting for confirmation"))
		a.openConfirm(T("确认还原", "Confirm restore"),
			T("将还原 termsrv.dll 备份、注册表原值,以及远程设置里改过的项。\n系统回到解锁前状态。",
				"This restores the termsrv.dll backup, the original registry values\n"+
					"and any item changed on the Settings page."),
			T("确认还原", "Restore"), func() {
				a.runOp(T("一键还原", "Restore"), func() error { return runRestore(false, a.lg.Say) })
			})
		return
	case id == idAddGrp:
		acc := a.findSelected()
		if acc == nil {
			a.lg.Say(T("[!!] 忽略[加入远程组]:未选中账户", "[!!] Ignored: no account selected"))
			a.toastMsg(T("请先在列表中选中一个账户", "Select an account first"), lvWarn)
			return
		}
		if a.busy {
			a.lg.Say(T("[!!] 忽略[加入远程组]:已有操作在进行", "[!!] Ignored: another operation is running"))
			return
		}
		name := acc.Name
		a.lg.Say(T("[>>] 点击[加入远程组]:%s", "[>>] add to RDP group: %s"), name)
		a.runOp(T("加入远程组", "Add to RDP group"), func() error {
			if err := addToRemoteGroup(name); err != nil {
				return err
			}
			a.lg.Say(T("[OK] %s 已加入 Remote Desktop Users", "[OK] %s added to Remote Desktop Users"), name)
			return nil
		})
		return
	case id == idSetPwd:
		acc := a.findSelected()
		if acc == nil {
			a.lg.Say(T("[!!] 忽略[设置密码]:未选中账户", "[!!] Ignored: no account selected"))
			a.toastMsg(T("请先在列表中选中一个账户", "Select an account first"), lvWarn)
			return
		}
		if a.busy {
			a.lg.Say(T("[!!] 忽略[设置密码]:已有操作在进行", "[!!] Ignored: another operation is running"))
			return
		}
		name := acc.Name
		a.lg.Say(T("[>>] 点击[设置密码]:%s", "[>>] set password: %s"), name)
		a.openInput(mkPassword, T("设置密码", "Set password")+" — "+name,
			fmt.Sprintf(T("为账户「%s」设置新密码(不能为空,RDP 不允许空密码):",
				"New password for \"%s\" (cannot be empty; RDP rejects blank passwords):"), name),
			T("确定", "OK"), "", func(pwd string) {
				if pwd == "" {
					a.lg.Say(T("[!!] 取消[设置密码]:密码为空", "[!!] Set password cancelled: empty password"))
					a.toastMsg(T("密码不能为空", "Password cannot be empty"), lvWarn)
					return
				}
				a.runOp(T("设置密码", "Set password"), func() error {
					if err := netUserSetPassword(name, pwd); err != nil {
						return err
					}
					notBlank := false
					pwdProbeCache[strings.ToLower(name)] = pwdProbe{blank: &notBlank,
						desc: T("已设置密码", "password set")}
					a.lg.Say(T("[OK] 账户 %s 密码已更新(请在远程端用新密码登录)",
						"[OK] password updated for %s"), name)
					return nil
				})
			})
		return
	case id == idModalOK:
		a.modalConfirm()
		return
	case id == idModalCancel:
		a.modalClose()
		return
	case id >= idSetRstBase && id < idSetRstBase+200:
		a.restoreOneSetting(id - idSetRstBase)
		return
	case id >= idSetCtlBase && id < idSetCtlBase+200:
		a.onSettingControl(id - idSetCtlBase)
		return
	case id >= idRowBase:
		list := a.visibleAccounts()
		idx := id - idRowBase
		if idx >= 0 && idx < len(list) {
			a.selName = list[idx].Name
			a.lg.Say(T("[>>] 选中账户:%s", "[>>] account selected: %s"), a.selName)
			a.invalidate()
		}
	}
}

// ---------------------------------------------------------------- 远程设置动作

func (a *app) requireElevated(what string) bool {
	if a.elevated {
		return true
	}
	a.lg.Say(T("[X] %s 被拒绝:当前进程没有管理员权限",
		"[X] %s refused: this process is not elevated"), what)
	a.toastMsg(T("需要管理员权限", "Administrator rights required"), lvErr)
	return false
}

func (a *app) onSettingControl(i int) {
	if i < 0 || i >= len(settingsDefs) {
		return
	}
	if a.busy {
		a.lg.Say(T("[!!] 忽略远程设置操作:已有任务在运行", "[!!] Settings change ignored: a task is running"))
		return
	}
	d := settingsDefs[i]
	if !a.requireElevated(d.label()) {
		return
	}
	cur, curOK := a.setVals[settingID(d.Key, d.Name)]

	switch d.Kind {
	case skBool, skBoolInv:
		on := curOK && boolOn(d, cur.v)
		a.applySettingValue(d, boolValue(d, !on))
	case skEnum:
		next := d.Enum[0].V
		for j, o := range d.Enum {
			if !curOK || cur.v == o.V {
				next = d.Enum[(j+1)%len(d.Enum)].V
				break
			}
		}
		a.applySettingValue(d, next)
	default:
		init := ""
		if curOK {
			init = strconv.FormatUint(uint64(cur.v), 10)
		}
		a.openInput(mkNumber, d.label(),
			fmt.Sprintf(T("%s(单位:%s,范围 %d ~ %d)", "%s (unit: %s, range %d ~ %d)"),
				d.desc(), orDefault(d.unit(), T("整数", "integer")), d.Min, d.Max),
			T("保存", "Save"), init, func(s string) {
				n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
				if err != nil {
					a.lg.Say(T("[X] 输入不是合法数字: %q", "[X] not a valid number: %q"), s)
					a.toastMsg(T("请输入数字", "Enter a number"), lvErr)
					return
				}
				if uint32(n) < d.Min || uint32(n) > d.Max {
					a.lg.Say(T("[X] 输入超出范围 %d~%d: %d", "[X] value out of range %d~%d: %d"), d.Min, d.Max, n)
					a.toastMsg(T("数值超出范围", "Value out of range"), lvErr)
					return
				}
				a.applySettingValue(d, uint32(n))
			})
	}
}

func (a *app) applySettingValue(d settingDef, v uint32) {
	old, oldOK, recorded, err := applySetting(d, v)
	oldTxt := T("未设置", "not set")
	if oldOK {
		oldTxt = strconv.FormatUint(uint64(old), 10)
	}
	if err != nil {
		a.lg.Say(T("[X] 远程设置 · %s 写入失败: %v", "[X] setting \"%s\" write failed: %v"), d.label(), err)
		a.toastMsg(T("写入失败,详见日志", "Write failed, see log"), lvErr)
		return
	}
	a.lg.Say(T("[OK] 远程设置 · %s:%s → %d(%s\\%s)", "[OK] setting \"%s\": %s -> %d (%s\\%s)"),
		d.label(), oldTxt, v, d.Key, d.Name)
	if recorded {
		a.lg.Say(T("     已记录原值 %s,可单独还原", "     original value %s recorded; can be restored individually"), oldTxt)
	}
	a.reloadSettings()
	a.invalidate()
	a.toastMsg(fmt.Sprintf(T("%s 已更新", "%s updated"), d.label()), lvOK)
}

func (a *app) restoreOneSetting(i int) {
	if i < 0 || i >= len(settingsDefs) {
		return
	}
	if a.busy {
		a.lg.Say(T("[!!] 忽略还原操作:已有任务在运行", "[!!] Restore ignored: a task is running"))
		return
	}
	d := settingsDefs[i]
	if !a.requireElevated(d.label()) {
		return
	}
	ds, had, err := restoreSetting(d.Key, d.Name)
	if !had {
		a.lg.Say(T("[!!] 远程设置 · %s 没有原值记录,无需还原",
			"[!!] setting \"%s\" has no saved original"), d.label())
		a.toastMsg(T("该项没有可还原的记录", "This item has nothing to restore"), lvWarn)
		return
	}
	if err != nil {
		a.lg.Say(T("[X] 远程设置 · %s 还原失败: %v", "[X] setting \"%s\" restore failed: %v"), d.label(), err)
		a.toastMsg(T("还原失败,详见日志", "Restore failed, see log"), lvErr)
		return
	}
	if ds.Existed {
		a.lg.Say(T("[OK] 远程设置 · %s 已还原为 %d", "[OK] setting \"%s\" restored to %d"), d.label(), ds.Value)
	} else {
		a.lg.Say(T("[OK] 远程设置 · %s 已还原(该值原不存在,已删除)",
			"[OK] setting \"%s\" restored (the value did not exist, so it was removed)"), d.label())
	}
	a.reloadSettings()
	a.invalidate()
	a.toastMsg(fmt.Sprintf(T("%s 已还原", "%s restored"), d.label()), lvOK)
}

func (a *app) restoreAllSettings() {
	if a.busy {
		a.lg.Say(T("[!!] 忽略[全部还原]:已有任务在运行", "[!!] Restore all ignored: a task is running"))
		return
	}
	if len(a.setOrig) == 0 {
		a.lg.Say(T("[!!] 远程设置:没有任何可还原的记录", "[!!] settings: nothing to restore"))
		a.toastMsg(T("没有可还原的记录", "Nothing to restore"), lvWarn)
		return
	}
	if !a.requireElevated(T("还原全部设置", "restore all settings")) {
		return
	}
	n, errs := restoreAllSettings()
	a.lg.Say(T("[OK] 远程设置:已还原 %d 项", "[OK] settings restored: %d items"), n)
	for _, e := range errs {
		a.lg.Say(T("[X] 还原失败: %s", "[X] restore failed: %s"), e)
	}
	a.reloadSettings()
	a.invalidate()
	a.toastMsg(fmt.Sprintf(T("已还原 %d 项设置", "Restored %d settings"), n), lvOK)
}

// ---------------------------------------------------------------- 其他动作

func (a *app) toastMsg(text string, lv int) {
	a.toast = text
	a.toastLv = lv
	a.toastAt = time.Now()
	a.invalidate()
}

func (a *app) openConfirm(title, body, okText string, onOK func()) {
	a.modal = &modalState{kind: mkConfirm, title: title, body: body, okText: okText,
		onOK: func(string) { onOK() }}
	a.lg.Say(T("[?] 等待用户确认:%s", "[?] waiting for confirmation: %s"), title)
	a.invalidate()
}

func (a *app) openInput(kind int, title, body, okText, init string, onOK func(string)) {
	a.modal = &modalState{kind: kind, title: title, body: body, okText: okText, onOK: onOK,
		input: []rune(init)}
	a.invalidate()
}

func (a *app) modalConfirm() {
	m := a.modal
	if m == nil {
		return
	}
	a.modal = nil
	a.invalidate()
	if m.onOK != nil {
		m.onOK(string(m.input))
	}
}

func (a *app) modalClose() {
	m := a.modal
	if m == nil {
		return
	}
	a.modal = nil
	a.lg.Say(T("[!!] 用户取消了操作:%s", "[!!] cancelled by user: %s"), m.title)
	a.toastMsg(T("已取消", "Cancelled"), lvWarn)
	a.invalidate()
}

func (a *app) runOp(label string, fn func() error) {
	if a.busy {
		a.lg.Say(T("[!!] 忽略操作 %s:已有任务在运行(%s)", "[!!] %s ignored: %s is running"), label, a.busyLabel)
		return
	}
	a.busy = true
	a.busyLabel = label
	a.lg.Say(T("[>>] 开始执行:%s", "[>>] running: %s"), label)
	a.invalidate()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.doneCh <- opResult{label, fmt.Errorf(T("内部异常: %v", "internal error: %v"), r)}
			}
		}()
		a.doneCh <- opResult{label, fn()}
	}()
}

func (a *app) refreshAll(forceProbe bool) {
	go func() {
		a.stCh <- gatherStatus()
		accts, err := analyzeAccounts(forceProbe)
		if err != nil {
			a.accCh <- nil
			a.lg.Say(T("[!!] 账户枚举失败: %v", "[!!] account enumeration failed: %v"), err)
			a.lg.Say(T("     常见原因:进程令牌受限、安全软件拦截、NetUserEnum 无权限。",
				"     usual causes: restricted token, security software, NetUserEnum denied."))
			return
		}
		a.accCh <- accts
	}()
}

func (a *app) pump() {
	if a.toast != "" && time.Since(a.toastAt) > 4*time.Second {
		a.toast = ""
	}
	dirty := false
	for {
		select {
		case e := <-a.logCh:
			dirty = true
			if a.page != pageLog && (e.Level == lvErr || e.Level == lvWarn) {
				a.logUnread = true
			}
		case accts := <-a.accCh:
			a.accts = accts
			a.accReady = true
			dirty = true
		case st := <-a.stCh:
			a.st = st
			dirty = true
		case res := <-a.doneCh:
			a.busy = false
			a.busyLabel = ""
			dirty = true
			if res.err != nil {
				a.lg.Say(T("[X] %s 失败: %v", "[X] %s failed: %v"), res.label, res.err)
				a.toastMsg(fmt.Sprintf(T("%s 失败,详见「运行日志」", "%s failed, see Log"), res.label), lvErr)
			} else {
				a.lg.Say(T("[OK] %s 完成", "[OK] %s finished"), res.label)
				a.toastMsg(fmt.Sprintf(T("%s 成功", "%s succeeded"), res.label), lvOK)
			}
			a.refreshAll(false)
			a.reloadSettings()
		default:
			if dirty {
				a.invalidate()
			}
			return
		}
	}
}

func (a *app) copyLog() {
	text := a.lg.Plain()
	if text == "" {
		a.toastMsg(T("没有可复制的日志", "Nothing to copy"), lvWarn)
		return
	}
	if err := setClipboard(text); err != nil {
		a.lg.Say(T("[X] 复制到剪贴板失败: %v", "[X] clipboard copy failed: %v"), err)
		a.toastMsg(T("复制失败", "Copy failed"), lvErr)
		return
	}
	a.lg.Say(T("[OK] 日志已复制到剪贴板(%d 行)", "[OK] log copied to clipboard (%d lines)"), a.lg.Count())
	a.toastMsg(T("日志已复制", "Log copied"), lvOK)
}

func (a *app) exportLog() {
	dir := filepath.Join(os.Getenv("USERPROFILE"), "Desktop")
	if _, err := os.Stat(dir); err != nil {
		dir = os.TempDir()
	}
	p := filepath.Join(dir, "rdpmulti-log-"+time.Now().Format("20060102-150405")+".txt")
	if err := os.WriteFile(p, []byte(a.lg.Plain()), 0644); err != nil {
		a.lg.Say(T("[X] 导出日志失败: %v", "[X] log export failed: %v"), err)
		a.toastMsg(T("导出失败", "Export failed"), lvErr)
		return
	}
	a.lg.Say(T("[OK] 日志已导出: %s", "[OK] log exported: %s"), p)
	a.toastMsg(T("已导出到桌面", "Exported to Desktop"), lvOK)
}

func setClipboard(text string) error {
	const gmemMoveable = 0x0002
	const cfUnicodeText = 13
	u := syscall.StringToUTF16(text)
	u = append(u, 0)
	size := uintptr(len(u) * 2)
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("GlobalLock failed")
	}
	dst := unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u))
	copy(dst, u)
	procGlobalUnlock.Call(h)

	if r, _, _ := procOpenClipboard.Call(a0()); r == 0 {
		return fmt.Errorf("OpenClipboard failed")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}

func a0() uintptr {
	if A == nil {
		return 0
	}
	return A.hwnd
}

func (a *app) relaunchElevated() {
	a.lg.Say(T("[>>] 请求以管理员权限重启(UAC)", "[>>] requesting elevation (UAC)"))
	if selfElevate() {
		a.lg.Say(T("[OK] 已启动提权实例,当前窗口即将关闭", "[OK] elevated instance launched, closing this window"))
		procDestroyWindow.Call(a.hwnd)
		return
	}
	a.lg.Say(T("[!!] 提权未完成:UAC 被取消或系统拒绝,当前仍以普通权限运行",
		"[!!] elevation not granted: UAC cancelled or denied"))
	a.toastMsg(T("提权被取消", "Elevation cancelled"), lvWarn)
}

func netUserSetPassword(name, pwd string) error {
	type userInfo1003 struct{ Password uintptr }
	r, _, _ := procNetUserSetInfo.Call(0, utf16p(name), 1003,
		uintptr(unsafe.Pointer(&userInfo1003{Password: utf16p(pwd)})), 0)
	if r != 0 {
		return fmt.Errorf(T("设置密码失败(NERR %d):%s", "setting the password failed (NERR %d): %s"),
			r, nerrText(uint32(r)))
	}
	return nil
}

func nerrText(code uint32) string {
	switch code {
	case 5:
		return T("拒绝访问", "access denied")
	case 2221:
		return T("找不到该用户", "user not found")
	case 2245:
		return T("密码不满足复杂度策略", "password does not meet the complexity policy")
	case 2246:
		return T("密码太短", "password too short")
	case 2247:
		return T("密码已被使用过", "password was used before")
	case 2248:
		return T("密码不符合密码策略要求", "password does not meet the password policy")
	case 2249:
		return T("密码历史限制", "password history restriction")
	}
	return T("未知错误", "unknown error")
}

func addToRemoteGroup(name string) error {
	out, code := runCmd("net", "localgroup", "Remote Desktop Users", name, "/add")
	if code == 0 {
		return nil
	}
	if strings.Contains(out, "1378") || strings.Contains(strings.ToLower(out), "already") {
		return nil
	}
	return fmt.Errorf("net localgroup exited with %d: %s", code, firstLine(out))
}

func selfElevate() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	var sei shellExecuteInfoW
	sei.CbSize = uint32(unsafe.Sizeof(sei))
	sei.FMask = 0x40
	sei.LpVerb = utf16p("runas")
	sei.LpFile = utf16p(exe)
	sei.NShow = 1
	r, _, _ := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei)))
	return r != 0
}

type shellExecuteInfoW struct {
	CbSize       uint32
	FMask        uint32
	Hwnd         uintptr
	LpVerb       uintptr
	LpFile       uintptr
	LpParameters uintptr
	LpDirectory  uintptr
	NShow        int32
	HInstApp     uintptr
	LpIDList     uintptr
	LpClass      uintptr
	HkeyClass    uintptr
	DwHotKey     uint32
	HIcon        uintptr
	HProcess     uintptr
}

// ---------------------------------------------------------------- 测试钩子

// installTestHooks 支持自动化验收(不影响正常使用):
//
//	RDPMULTI_SHOT=<png>          渲染一帧存成 PNG 后退出
//	RDPMULTI_TEST_CLICKS=id,id   依次模拟点击(间隔 700ms)
func (a *app) installTestHooks() {
	shot := os.Getenv("RDPMULTI_SHOT")
	clicks := os.Getenv("RDPMULTI_TEST_CLICKS")
	if shot == "" && clicks == "" {
		return
	}
	var seq []int
	if clicks != "" {
		for _, s := range strings.Split(clicks, ",") {
			var v int
			if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &v); err == nil {
				seq = append(seq, v)
			}
		}
	}
	go func() {
		time.Sleep(900 * time.Millisecond)
		for _, id := range seq {
			a.postClick(id)
			time.Sleep(700 * time.Millisecond)
		}
		if shot != "" {
			time.Sleep(900 * time.Millisecond)
			a.snapshotPNG(shot)
			procPostMessageW.Call(a.hwnd, wmClose, 0, 0)
		}
	}()
}

const customClickMsg = 0x0400 + 17

func (a *app) postClick(id int) {
	procPostMessageW.Call(a.hwnd, customClickMsg, uintptr(id), 0)
}

func (a *app) snapshotPNG(path string) {
	if a.surf == nil {
		return
	}
	w, h := int(a.surf.w), int(a.surf.h)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			img.SetRGBA(x, y, color.RGBA{
				R: a.surf.px[i+2], G: a.surf.px[i+1], B: a.surf.px[i], A: 255,
			})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	png.Encode(f, img)
}
