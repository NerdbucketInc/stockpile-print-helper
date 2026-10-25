//go:build windows

package ui

import (
	"context"
	"fmt"
	"image/color"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/nerdbucketinc/stockpile-print-helper/internal/app"
)

// The helper's window and tray icon, with Windows' own controls (user32,
// shell32, gdi32 through syscall — no UI toolkit). Everything it does goes
// through package app, the same steps as the command line. It opens no
// browser and starts no program: links are shown as text.

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")

	pRegisterClassExW         = user32.NewProc("RegisterClassExW")
	pCreateWindowExW          = user32.NewProc("CreateWindowExW")
	pDefWindowProcW           = user32.NewProc("DefWindowProcW")
	pGetMessageW              = user32.NewProc("GetMessageW")
	pTranslateMessage         = user32.NewProc("TranslateMessage")
	pDispatchMessageW         = user32.NewProc("DispatchMessageW")
	pIsDialogMessageW         = user32.NewProc("IsDialogMessageW")
	pPostQuitMessage          = user32.NewProc("PostQuitMessage")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pSendMessageW             = user32.NewProc("SendMessageW")
	pShowWindow               = user32.NewProc("ShowWindow")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pSetWindowTextW           = user32.NewProc("SetWindowTextW")
	pGetWindowTextW           = user32.NewProc("GetWindowTextW")
	pGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	pEnableWindow             = user32.NewProc("EnableWindow")
	pSetTimer                 = user32.NewProc("SetTimer")
	pMessageBoxW              = user32.NewProc("MessageBoxW")
	pDestroyWindow            = user32.NewProc("DestroyWindow")
	pLoadCursorW              = user32.NewProc("LoadCursorW")
	pCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	pAppendMenuW              = user32.NewProc("AppendMenuW")
	pTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	pDestroyMenu              = user32.NewProc("DestroyMenu")
	pGetCursorPos             = user32.NewProc("GetCursorPos")
	pCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	pDestroyIcon              = user32.NewProc("DestroyIcon")
	pFindWindowW              = user32.NewProc("FindWindowW")
	pEnumChildWindows         = user32.NewProc("EnumChildWindows")
	pIsWindowVisible          = user32.NewProc("IsWindowVisible")
	pSetWindowPos             = user32.NewProc("SetWindowPos")
	pGetWindowRect            = user32.NewProc("GetWindowRect")
	pGetClientRect            = user32.NewProc("GetClientRect")
	pShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
	pCreateMutexW             = kernel32.NewProc("CreateMutexW")
	pGetCurrentPackageName    = kernel32.NewProc("GetCurrentPackageFullName")
	pCreateFontW              = gdi32.NewProc("CreateFontW")
)

const (
	swpNoMove     = 0x2
	swpNoZOrder   = 0x4
	swpNoActivate = 0x10
)

// appmodelNoPackage is GetCurrentPackageFullName's answer when the program
// isn't running from an MSIX package.
const appmodelNoPackage = 15700

type rect struct{ Left, Top, Right, Bottom int32 }

// packaged reports whether the helper runs from its MSIX package (the
// Microsoft Store build), which starts at sign-in and updates by itself.
// The plain .exe from a GitHub release does neither.
func packaged() bool {
	if pGetCurrentPackageName.Find() != nil {
		return false
	}
	var n uint32
	r, _, _ := pGetCurrentPackageName.Call(uintptr(unsafe.Pointer(&n)), 0)
	return r != appmodelNoPackage
}

const (
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmSetFont     = 0x0030
	wmCommand     = 0x0111
	wmTimer       = 0x0113
	wmApp         = 0x8000
	wmTray        = wmApp + 1
	wmDone        = wmApp + 2
	wmShowMe      = wmApp + 3
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wsOverlapped  = 0x00000000
	wsCaption     = 0x00C00000
	wsSysMenu     = 0x00080000
	wsMinimizeBox = 0x00020000
	wsChild       = 0x40000000
	wsVisible     = 0x10000000
	wsTabStop     = 0x00010000
	wsBorder      = 0x00800000
	wsVScroll     = 0x00200000
	esUppercase   = 0x0008
	esAutoHScroll = 0x0080
	lbsNoSel      = 0x4000
	lbsNoIntegral = 0x0100
	lbAddString   = 0x0180
	lbResetCont   = 0x0184
	bsDefPush     = 0x00000001
	swHide        = 0
	swShow        = 5
	errAlreadyEx  = 183
	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nifMessage    = 0x1
	nifIcon       = 0x2
	nifTip        = 0x4
	mfString      = 0x0
	mfSeparator   = 0x800
	tpmReturnCmd  = 0x0100
	mbYesNo       = 0x4
	mbIconWarn    = 0x30
	mbIconInfo    = 0x40
	idYes         = 6
	idcArrow      = 32512
	colorWindow   = 5
)

// Control ids.
const (
	idStatus = 100 + iota
	idStatusSub
	idPairTitle
	idPairHelp
	idCode
	idPair
	idPairNote
	idPause
	idPrintersLbl
	idPrinters
	idFind
	idIP
	idAdd
	idFindNote
	idJobsLbl
	idJobs
	idJobsNote
	idStartup
	idUnpair
	idFooter
	idMsg
)

// Tray menu ids.
const (
	menuOpen = 900 + iota
	menuPause
	menuQuit
)

type wndClassEx struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	IconSm                             uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type notifyIconData struct {
	Size             uint32
	Wnd              uintptr
	ID, Flags        uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State, StateMask uint32
	Info             [256]uint16
	Version          uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUID             [16]byte
	BalloonIcon      uintptr
}

func u16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

const className = "StockpilePrintHelperWindow"

// window is the one window and its tray icon.
type window struct {
	app      *app.App
	hwnd     uintptr
	font     uintptr
	bold     uintptr
	ctl      map[int]uintptr
	icon     uintptr
	dot      string
	paused   bool
	mu       sync.Mutex
	done     func() // the last background step's result, run on the UI thread
	selftest string
	lastView string
}

var current *window

// Run shows the helper: its window (when not paired, or show is true) and
// its tray icon, and runs the agent while paired. It returns when Quit is
// chosen. A second copy just brings the first one's window up.
func Run(a *app.App, show bool, selftest string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if selftest == "" {
		h, _, err := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(u16(`Local\StockpilePrintHelper`))))
		if h != 0 && err == syscall.Errno(errAlreadyEx) {
			if other, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(u16(className))), 0); other != 0 {
				pPostMessageW.Call(other, wmShowMe, 0, 0)
			}
			return nil
		}
	}
	w := &window{app: a, ctl: map[int]uintptr{}, selftest: selftest}
	current = w
	if err := w.create(); err != nil {
		return err
	}
	a.Start()
	w.refresh()
	if !w.app.View(context.Background()).Paired || show || selftest != "" {
		w.show()
	}
	if selftest != "" {
		return w.runSelftest()
	}
	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if d, _, _ := pIsDialogMessageW.Call(w.hwnd, uintptr(unsafe.Pointer(&m))); d != 0 {
			continue
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	w.tray(nimDelete)
	a.Stop()
	return nil
}

func (w *window) create() error {
	inst, _, _ := pGetModuleHandleW.Call(0)
	cursor, _, _ := pLoadCursorW.Call(0, idcArrow)
	w.icon = makeIcon(32, nil)
	wc := wndClassEx{WndProc: syscall.NewCallback(wndProc), Instance: inst, Cursor: cursor, Background: colorWindow + 1, ClassName: u16(className), Icon: w.icon, IconSm: w.icon}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("couldn't register the window: %v", err)
	}
	h, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16(className))), uintptr(unsafe.Pointer(u16("Stockpile Print Helper"))),
		wsOverlapped|wsCaption|wsSysMenu|wsMinimizeBox, 200, 120, 520, 700, 0, 0, inst, 0)
	if h == 0 {
		return fmt.Errorf("couldn't make the window: %v", err)
	}
	w.hwnd = h
	w.font, _, _ = pCreateFontW.Call(uintptr(^uint32(16-1)), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(u16("Segoe UI"))))
	w.bold, _, _ = pCreateFontW.Call(uintptr(^uint32(18-1)), 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(u16("Segoe UI"))))
	type c struct {
		id         int
		class      string
		text       string
		x, y, w, h int
		style      uint32
		bold       bool
	}
	for _, k := range []c{
		// Heights fit the text: a line of this font is about 21 px, so a box
		// is 22 px (one line), 46 (two) or 68 (three). Too short a box cuts
		// off the last line of wrapped text. idMsg and idFooter move with the
		// view (fit).
		{idStatus, "STATIC", "", 16, 12, 470, 22, 0, true},
		{idStatusSub, "STATIC", "", 16, 38, 470, 46, 0, false},
		{idPairTitle, "STATIC", "Pair this computer with your shop", 16, 96, 470, 22, 0, true},
		{idPairHelp, "STATIC", "In Stockpile, go to Settings › Printing and click Get a pairing code. Type the code here.", 16, 122, 470, 46, 0, false},
		{idCode, "EDIT", "", 16, 174, 200, 26, wsBorder | wsTabStop | esUppercase | esAutoHScroll, false},
		{idPair, "BUTTON", "Pair", 226, 173, 90, 28, wsTabStop | bsDefPush, false},
		{idPairNote, "STATIC", "Until it's paired, this helper doesn't connect to anything.", 16, 210, 470, 46, 0, false},
		{idPause, "BUTTON", "Pause printing", 16, 96, 150, 28, wsTabStop, false},
		{idPrintersLbl, "STATIC", "PRINTERS THIS HELPER CAN USE", 16, 136, 470, 18, 0, true},
		{idPrinters, "LISTBOX", "", 16, 158, 470, 88, wsBorder | wsVScroll | lbsNoSel | lbsNoIntegral, false},
		{idFind, "BUTTON", "Find printers on the network", 16, 252, 210, 26, wsTabStop, false},
		{idIP, "EDIT", "", 236, 253, 150, 24, wsBorder | wsTabStop | esAutoHScroll, false},
		{idAdd, "BUTTON", "Add by IP", 392, 252, 94, 26, wsTabStop, false},
		{idFindNote, "STATIC", "Printers installed in Windows show up by themselves. Find looks on your network only when you click it. Add by IP: your own network only (10.x, 172.16–31.x, 192.168.x).", 16, 284, 470, 68, 0, false},
		{idJobsLbl, "STATIC", "RECENT JOBS", 16, 362, 470, 18, 0, true},
		{idJobs, "LISTBOX", "", 16, 384, 470, 100, wsBorder | wsVScroll | lbsNoSel | lbsNoIntegral, false},
		{idJobsNote, "STATIC", "The helper keeps no copy of any document. Only this list is kept (7 days).", 16, 490, 470, 46, 0, false},
		{idStartup, "STATIC", startupNote(), 16, 542, 470, 68, 0, false},
		{idUnpair, "BUTTON", "Unpair", 16, 616, 200, 28, wsTabStop, false},
		{idFooter, "STATIC", "", 16, 690, 470, 68, 0, false},
		{idMsg, "STATIC", "", 226, 616, 260, 68, 0, false},
	} {
		h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16(k.class))), uintptr(unsafe.Pointer(u16(k.text))),
			uintptr(wsChild|k.style), uintptr(k.x), uintptr(k.y), uintptr(k.w), uintptr(k.h), w.hwnd, uintptr(k.id), inst, 0)
		f := w.font
		if k.bold {
			f = w.bold
		}
		pSendMessageW.Call(h, wmSetFont, f, 1)
		w.ctl[k.id] = h
	}
	pSetTimer.Call(w.hwnd, 1, 1000, 0)
	w.tray(nimAdd)
	return nil
}

func makeIcon(size int, dot *color.RGBA) uintptr {
	b := IconPNG(size, dot)
	h, _, _ := pCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 1, 0x00030000, uintptr(size), uintptr(size), 0)
	return h
}

func (w *window) tray(op uintptr) {
	nid := notifyIconData{Wnd: w.hwnd, ID: 1, Flags: nifMessage | nifIcon | nifTip, CallbackMessage: wmTray, Icon: w.icon}
	nid.Size = uint32(unsafe.Sizeof(nid))
	tip, _ := syscall.UTF16FromString("Stockpile Print Helper")
	copy(nid.Tip[:], tip)
	pShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&nid)))
}

// startupNote says how the helper starts and updates: by itself from the
// Microsoft Store package, by hand for the plain .exe.
func startupNote() string {
	if packaged() {
		return "Starts when you sign in to Windows (turn that off in Windows Settings › Apps › Startup). Updates come through the Microsoft Store."
	}
	return "To start it when you sign in, put a shortcut to it in your Startup folder (Win+R, then shell:startup). For updates, download the newest release."
}

// fit puts the message and footer lines under the view that shows (not
// paired: the pairing steps; paired: printers and jobs) and sizes the
// window to it, so neither view leaves a gap or cuts anything off.
func (w *window) fit(paired bool) {
	msg, foot, client := [4]int{16, 262, 470, 46}, [4]int{16, 316, 470, 68}, 396
	if paired {
		msg, foot, client = [4]int{226, 616, 260, 68}, [4]int{16, 690, 470, 68}, 770
	}
	for id, r := range map[int][4]int{idMsg: msg, idFooter: foot} {
		pSetWindowPos.Call(w.ctl[id], 0, uintptr(r[0]), uintptr(r[1]), uintptr(r[2]), uintptr(r[3]), swpNoZOrder|swpNoActivate)
	}
	var wr, cr rect
	pGetWindowRect.Call(w.hwnd, uintptr(unsafe.Pointer(&wr)))
	pGetClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&cr)))
	frame := (wr.Bottom - wr.Top) - (cr.Bottom - cr.Top)
	pSetWindowPos.Call(w.hwnd, 0, 0, 0, uintptr(wr.Right-wr.Left), uintptr(int32(client)+frame), swpNoMove|swpNoZOrder|swpNoActivate)
}

func (w *window) set(id int, text string) {
	if h := w.ctl[id]; h != 0 {
		pSetWindowTextW.Call(h, uintptr(unsafe.Pointer(u16(text))))
	}
}

func (w *window) text(id int) string {
	h := w.ctl[id]
	n, _, _ := pGetWindowTextLengthW.Call(h)
	buf := make([]uint16, n+1)
	pGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

func (w *window) visible(on bool, ids ...int) {
	cmd := uintptr(swHide)
	if on {
		cmd = swShow
	}
	for _, id := range ids {
		pShowWindow.Call(w.ctl[id], cmd)
	}
}

func (w *window) enable(on bool, ids ...int) {
	v := uintptr(0)
	if on {
		v = 1
	}
	for _, id := range ids {
		pEnableWindow.Call(w.ctl[id], v)
	}
}

func (w *window) list(id int, rows []string) {
	h := w.ctl[id]
	pSendMessageW.Call(h, lbResetCont, 0, 0)
	for _, r := range rows {
		pSendMessageW.Call(h, lbAddString, 0, uintptr(unsafe.Pointer(u16(r))))
	}
}

func (w *window) show() {
	pShowWindow.Call(w.hwnd, swShow)
	pSetForegroundWindow.Call(w.hwnd)
}

var statusWords = map[string]string{"ready": "Ready", "paper_out": "Out of paper", "offline": "Offline", "paused": "Paused in Windows", "error": "Error"}
var connWords = map[string]string{"windows-usb": "Windows printer (USB)", "windows-network": "Windows printer (network)", "windows": "Windows printer", "network-raw": "Network label printer (raw)", "file": "File printer (development build)"}
var stateWords = map[string]string{"printed": "printed", "sent": "sent to the printer", "failed": "didn't print", "uncertain": "may have printed", "not_received": "didn't print", "taking": "taking…", "received": "printing…", "printing": "printing…"}

// refresh redraws from the app's view (once a second, and after each step).
func (w *window) refresh() {
	v := w.app.View(context.Background())
	key := fmt.Sprintf("%+v", v)
	if key == w.lastView {
		return
	}
	w.lastView = key
	paired := []int{idPause, idPrintersLbl, idPrinters, idFind, idIP, idAdd, idFindNote, idJobsLbl, idJobs, idJobsNote, idStartup, idUnpair}
	unpaired := []int{idPairTitle, idPairHelp, idCode, idPair, idPairNote}
	w.visible(true, idStatus, idStatusSub, idFooter)
	dot := DotGrey
	switch {
	case !v.Paired:
		w.visible(false, paired...)
		w.visible(true, unpaired...)
		w.fit(false)
		if v.Removed != "" {
			w.set(idStatus, "Removed from "+v.Removed)
			w.set(idStatusSub, "The shop's owner removed this computer in Stockpile. It isn't connected and won't print anything.")
		} else {
			w.set(idStatus, "Not paired")
			w.set(idStatusSub, "Pair it with your shop to start printing.")
		}
	default:
		w.visible(false, unpaired...)
		w.visible(true, paired...)
		w.fit(true)
		switch {
		case v.Paused:
			dot = DotAmber
			w.set(idStatus, "Paused — not printing")
			w.set(idStatusSub, "Anyone printing here is told it didn't print because this computer is paused. It stays paused after a restart.")
			w.set(idPause, "Resume printing")
		case v.Connected:
			dot = DotGreen
			w.set(idStatus, fmt.Sprintf("Connected — printing for %s · %s", v.Shop, v.Location))
			w.set(idStatusSub, "Paired as "+v.Computer+".")
			w.set(idPause, "Pause printing")
		default:
			dot = DotRed
			w.set(idStatus, "Can't reach Stockpile")
			why := "Check this computer's internet connection. Trying again…"
			if v.Problem == "" {
				why = "Connecting…"
				dot = DotGrey
			}
			w.set(idStatusSub, why)
			w.set(idPause, "Pause printing")
		}
		w.set(idUnpair, "Unpair from "+v.Shop)
		var ps []string
		for _, p := range v.Printers {
			ps = append(ps, fmt.Sprintf("%s — %s — %s", p.Name, connWords[p.Connection], statusWords[p.Status]))
		}
		if len(ps) == 0 {
			ps = []string{"No printers yet. Install one in Windows, or Find / Add by IP."}
		}
		w.list(idPrinters, ps)
		var js []string
		for _, e := range v.Recent {
			line := fmt.Sprintf("%s  %s — %s", e.At.Local().Format("Jan 2 3:04 PM"), e.Title, stateWords[e.State])
			if e.Message != "" && e.State != "printed" && e.State != "sent" {
				line += " (" + e.Message + ")"
			}
			js = append(js, line)
		}
		if len(js) == 0 {
			js = []string{"No jobs yet."}
		}
		w.list(idJobs, js)
	}
	w.set(idFooter, fmt.Sprintf("Version %s · Log folder: %s · What it does and doesn't, and its source: github.com/NerdbucketInc/stockpile-print-helper", w.app.Version, v.LogDir))
	var d *color.RGBA
	switch dot {
	case DotGreen:
		d = &DotGreen
	case DotAmber:
		d = &DotAmber
	case DotRed:
		d = &DotRed
	default:
		d = &DotGrey
	}
	name := fmt.Sprint(*d)
	if name != w.dot {
		w.dot = name
		old := w.icon
		w.icon = makeIcon(32, d)
		w.tray(nimModify)
		if old != 0 {
			pDestroyIcon.Call(old)
		}
	}
	w.paused = v.Paused
}

// background runs a slow step (pairing, finding printers) off the UI thread;
// finish runs back on it.
func (w *window) background(step func() func()) {
	w.enable(false, idPair, idFind, idAdd, idUnpair)
	go func() {
		finish := step()
		w.mu.Lock()
		w.done = finish
		w.mu.Unlock()
		pPostMessageW.Call(w.hwnd, wmDone, 0, 0)
	}()
}

func (w *window) say(text string) { w.set(idMsg, text); w.visible(true, idMsg) }

func (w *window) command(id int) {
	ctx := context.Background()
	switch id {
	case idPair:
		code := w.text(idCode)
		w.say("Pairing…")
		w.background(func() func() {
			p, err := w.app.Pair(ctx, "", code)
			return func() {
				if err != nil {
					w.say(err.Error())
					return
				}
				w.set(idCode, "")
				w.say("")
				w.visible(false, idMsg)
				w.app.Start()
				w.set(idStatusSub, fmt.Sprintf("Paired with %s · %s. Go back to Stockpile's Settings › Printing to pick which printer does what.", p.Shop.Name, p.Location.Name))
			}
		})
	case idPause, menuPause:
		w.app.SetPaused(!w.paused)
		w.lastView = ""
	case idFind:
		w.set(idFindNote, "Looking on this network for 20 seconds…")
		w.background(func() func() {
			found := w.app.Find(ctx)
			return func() {
				if len(found) == 0 {
					w.set(idFindNote, "No printers answered on this network. If yours is plugged into this computer, install it in Windows first.")
					return
				}
				var names []string
				for _, f := range found {
					names = append(names, f.Address.String())
				}
				w.set(idFindNote, "Found: "+strings.Join(names, ", ")+". Type one into the box beside Add by IP to keep it — nothing is used until you do.")
			}
		})
	case idAdd:
		target := strings.TrimSpace(w.text(idIP))
		w.background(func() func() {
			n, err := w.app.Keep(ctx, target, "", "")
			return func() {
				if err != nil {
					w.set(idFindNote, err.Error())
					return
				}
				w.set(idIP, "")
				w.set(idFindNote, "Kept: "+n.Name+". Stockpile can now offer it.")
				w.lastView = ""
			}
		})
	case idUnpair:
		v := w.app.View(ctx)
		if r, _, _ := pMessageBoxW.Call(w.hwnd, uintptr(unsafe.Pointer(u16(fmt.Sprintf("Unpair this computer? It stops printing for %s right away. To use it again you'll need a new pairing code from Settings › Printing.", v.Shop)))), uintptr(unsafe.Pointer(u16("Unpair"))), mbYesNo|mbIconWarn); r != idYes {
			return
		}
		w.background(func() func() {
			err := w.app.Unpair(ctx)
			return func() {
				if err != nil {
					w.say(err.Error())
				}
				w.lastView = ""
			}
		})
	case menuOpen:
		w.show()
	case menuQuit:
		pDestroyWindow.Call(w.hwnd)
	}
}

func (w *window) menu() {
	m, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(m)
	pause := "Pause printing"
	if w.paused {
		pause = "Resume printing"
	}
	pAppendMenuW.Call(m, mfString, menuOpen, uintptr(unsafe.Pointer(u16("Open"))))
	if w.app.View(context.Background()).Paired {
		pAppendMenuW.Call(m, mfString, menuPause, uintptr(unsafe.Pointer(u16(pause))))
	}
	pAppendMenuW.Call(m, mfSeparator, 0, 0)
	pAppendMenuW.Call(m, mfString, menuQuit, uintptr(unsafe.Pointer(u16("Quit (stops printing until you sign in again)"))))
	var pt struct{ X, Y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(w.hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(m, tpmReturnCmd, uintptr(pt.X), uintptr(pt.Y), 0, w.hwnd, 0)
	if cmd != 0 {
		w.command(int(cmd))
	}
}

func wndProc(hwnd uintptr, m uint32, wp, lp uintptr) uintptr {
	w := current
	if w == nil || hwnd != w.hwnd {
		r, _, _ := pDefWindowProcW.Call(hwnd, uintptr(m), wp, lp)
		return r
	}
	switch m {
	case wmCommand:
		w.command(int(wp & 0xffff))
		return 0
	case wmTimer:
		w.refresh()
		return 0
	case wmDone:
		w.mu.Lock()
		finish := w.done
		w.done = nil
		w.mu.Unlock()
		w.enable(true, idPair, idFind, idAdd, idUnpair)
		if finish != nil {
			finish()
		}
		w.refresh()
		return 0
	case wmTray:
		switch lp & 0xffff {
		case wmLButtonUp:
			w.show()
		case wmRButtonUp:
			w.menu()
		}
		return 0
	case wmShowMe:
		w.show()
		return 0
	case wmClose:
		// Closing the window keeps the helper running by the clock.
		pShowWindow.Call(hwnd, swHide)
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, uintptr(m), wp, lp)
	return r
}

// runSelftest lists what the window shows (visible controls' text and the
// list rows), for an automated check, then closes.
func (w *window) runSelftest() error {
	w.lastView = ""
	w.refresh()
	var out []string
	order := []int{idStatus, idStatusSub, idPairTitle, idPairHelp, idPair, idPairNote, idPause, idPrintersLbl, idFind, idAdd, idFindNote, idJobsLbl, idJobsNote, idStartup, idUnpair, idFooter}
	for _, id := range order {
		if v, _, _ := pIsWindowVisible.Call(w.ctl[id]); v != 0 {
			out = append(out, w.text(id))
		}
	}
	for _, id := range []int{idPrinters, idJobs} {
		if v, _, _ := pIsWindowVisible.Call(w.ctl[id]); v != 0 {
			out = append(out, "[list]")
		}
	}
	w.tray(nimDelete)
	pDestroyWindow.Call(w.hwnd)
	w.app.Stop()
	return writeSelftest(w.selftest, strings.Join(out, "\n"))
}
