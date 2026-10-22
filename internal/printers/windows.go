//go:build windows

package printers

import (
	"context"
	"fmt"
	"math"
	"syscall"
	"time"
	"unsafe"

	"github.com/nerdbucketinc/stockpile-print-helper/internal/api"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/pages"
)

// Windows printing: the printers installed in Windows (EnumPrinters), raw
// label data through the spooler (the RAW datatype), and PNG pages drawn
// with GDI (StretchDIBits) — no PDF reader, no other program. Only Windows'
// own DLLs; no cgo.

var (
	winspool = syscall.NewLazyDLL("winspool.drv")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")

	procEnumPrintersW     = winspool.NewProc("EnumPrintersW")
	procOpenPrinterW      = winspool.NewProc("OpenPrinterW")
	procClosePrinter      = winspool.NewProc("ClosePrinter")
	procStartDocPrinterW  = winspool.NewProc("StartDocPrinterW")
	procStartPagePrinter  = winspool.NewProc("StartPagePrinter")
	procWritePrinter      = winspool.NewProc("WritePrinter")
	procEndPagePrinter    = winspool.NewProc("EndPagePrinter")
	procEndDocPrinter     = winspool.NewProc("EndDocPrinter")
	procGetJobW           = winspool.NewProc("GetJobW")
	procSetJobW           = winspool.NewProc("SetJobW")
	procCreateDCW         = gdi32.NewProc("CreateDCW")
	procCreateICW         = gdi32.NewProc("CreateICW")
	procDeleteDC          = gdi32.NewProc("DeleteDC")
	procGetDeviceCaps     = gdi32.NewProc("GetDeviceCaps")
	procStartDocW         = gdi32.NewProc("StartDocW")
	procEndDoc            = gdi32.NewProc("EndDoc")
	procAbortDoc          = gdi32.NewProc("AbortDoc")
	procStartPage         = gdi32.NewProc("StartPage")
	procEndPage           = gdi32.NewProc("EndPage")
	procStretchDIBits     = gdi32.NewProc("StretchDIBits")
	procSetStretchBltMode = gdi32.NewProc("SetStretchBltMode")
)

const (
	printerEnumLocal       = 0x2
	printerEnumConnections = 0x4

	attrKeepPrintedJobs = 0x100
	attrWorkOffline     = 0x400

	statusPaused        = 0x1
	statusError         = 0x2
	statusPaperJam      = 0x8
	statusPaperOut      = 0x10
	statusOffline       = 0x80
	statusNotAvailable  = 0x1000
	statusDoorOpen      = 0x400000
	statusUserInterv    = 0x100000
	statusPaperProblem  = 0x40
	jobStatusError      = 0x2
	jobStatusOffline    = 0x20
	jobStatusPaperOut   = 0x40
	jobStatusPrinted    = 0x80
	jobStatusDeleted    = 0x100
	jobStatusUserInterv = 0x400
	jobStatusComplete   = 0x1000
	jobControlDelete    = 5

	capHorzRes     = 8
	capVertRes     = 10
	capLogPixelsX  = 88
	capLogPixelsY  = 90
	stretchHalfone = 4
	srcCopy        = 0x00CC0020
)

type printerInfo2 struct {
	ServerName, PrinterName, ShareName, PortName, DriverName, Comment, Location *uint16
	DevMode                                                                     uintptr
	SepFile, PrintProcessor, Datatype, Parameters                               *uint16
	SecurityDescriptor                                                          uintptr
	Attributes, Priority, DefaultPriority, StartTime, UntilTime, Status, Jobs   uint32
	AveragePPM                                                                  uint32
}

type jobInfo1 struct {
	JobID                                                  uint32
	PrinterName, MachineName, UserName, Document, Datatype *uint16
	StatusText                                             *uint16
	Status, Priority, Position, TotalPages, PagesPrinted   uint32
	Submitted                                              [8]uint16
}

type docInfo1 struct {
	DocName, OutputFile, Datatype *uint16
}

type docInfoW struct {
	Size     int32
	DocName  *uint16
	Output   *uint16
	Datatype *uint16
	Type     uint32
}

func utf16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func str(p *uint16) string {
	if p == nil {
		return ""
	}
	return syscall.UTF16ToString(unsafe.Slice(p, indexNul(p)))
}

func indexNul(p *uint16) int {
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; n++ {
		ptr = unsafe.Add(ptr, 2)
	}
	return n
}

// windowsPrinter is a printer installed in Windows.
type windowsPrinter struct {
	name, port, driver string
	kind               string
	zpl                bool
	dpi                int
	status             uint32
	attrs              uint32
}

func installed(context.Context) []Printer {
	var needed, returned uint32
	flags := uintptr(printerEnumLocal | printerEnumConnections)
	procEnumPrintersW.Call(flags, 0, 2, 0, 0, uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if needed == 0 {
		return nil
	}
	buf := make([]byte, needed)
	r, _, _ := procEnumPrintersW.Call(flags, 0, 2, uintptr(unsafe.Pointer(&buf[0])), uintptr(needed), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if r == 0 {
		return nil
	}
	infos := unsafe.Slice((*printerInfo2)(unsafe.Pointer(&buf[0])), returned)
	var out []Printer
	for _, in := range infos {
		name, port, driver := str(in.PrinterName), str(in.PortName), str(in.DriverName)
		if name == "" || Skipped(name, port) {
			continue
		}
		kind, zpl := KindOf(driver, name)
		out = append(out, &windowsPrinter{name: name, port: port, driver: driver, kind: kind, zpl: zpl, dpi: dpiOf(name), status: in.Status, attrs: in.Attributes})
	}
	return out
}

func dpiOf(name string) int {
	dc, _, _ := procCreateICW.Call(uintptr(unsafe.Pointer(utf16("WINSPOOL"))), uintptr(unsafe.Pointer(utf16(name))), 0, 0)
	if dc == 0 {
		return 0
	}
	defer procDeleteDC.Call(dc)
	d, _, _ := procGetDeviceCaps.Call(dc, capLogPixelsX)
	return int(d)
}

// KeepsCopies: Windows is set to keep a copy of every job printed here.
func (w *windowsPrinter) KeepsCopies() bool { return w.attrs&attrKeepPrintedJobs != 0 }

func (w *windowsPrinter) Info() api.Printer {
	formats := []string{FormatPNG}
	if w.zpl {
		formats = append([]string{FormatZPL}, formats...)
	}
	conn := "windows"
	switch {
	case len(w.port) >= 3 && (w.port[:3] == "USB" || w.port[:3] == "usb"):
		conn = "windows-usb"
	case len(w.port) >= 3 && (w.port[:3] == "IP_" || w.port[:3] == "WSD"):
		conn = "windows-network"
	}
	return api.Printer{ID: "win:" + w.name, Name: w.name, Connection: conn, Kind: w.kind, Formats: formats, DPI: w.dpi, Papers: []string{}, Status: statusWord(w.status, w.attrs)}
}

func statusWord(s, attrs uint32) string {
	switch {
	case s&(statusPaperOut|statusPaperProblem) != 0:
		return "paper_out"
	case s&(statusOffline|statusNotAvailable) != 0 || attrs&attrWorkOffline != 0:
		return "offline"
	case s&statusPaused != 0:
		return "paused"
	case s&(statusError|statusPaperJam|statusDoorOpen|statusUserInterv) != 0:
		return "error"
	}
	return "ready"
}

func (w *windowsPrinter) open() (uintptr, error) {
	var h uintptr
	r, _, err := procOpenPrinterW.Call(uintptr(unsafe.Pointer(utf16(w.name))), uintptr(unsafe.Pointer(&h)), 0)
	if r == 0 {
		return 0, fmt.Errorf("Windows couldn't open %s: %v", w.name, err)
	}
	return h, nil
}

// PrintRaw sends label data through the spooler as it is (RAW datatype).
func (w *windowsPrinter) PrintRaw(ctx context.Context, title string, data []byte, copies int) Outcome {
	h, err := w.open()
	if err != nil {
		return Outcome{State: "failed", Code: "printer_gone", Message: err.Error()}
	}
	defer procClosePrinter.Call(h)
	di := docInfo1{DocName: utf16(title), Datatype: utf16("RAW")}
	job, _, err := procStartDocPrinterW.Call(h, 1, uintptr(unsafe.Pointer(&di)))
	if job == 0 {
		return Outcome{State: "failed", Code: "format", Message: fmt.Sprintf("this printer's Windows driver can't take label data: %v", err)}
	}
	if copies < 1 {
		copies = 1
	}
	procStartPagePrinter.Call(h)
	for i := 0; i < copies; i++ {
		var written uint32
		r, _, _ := procWritePrinter.Call(h, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&written)))
		if r == 0 || int(written) != len(data) {
			procEndPagePrinter.Call(h)
			procEndDocPrinter.Call(h)
			return Outcome{State: "uncertain", Message: "the spooler stopped taking the label partway"}
		}
	}
	procEndPagePrinter.Call(h)
	procEndDocPrinter.Call(h)
	return watch(ctx, h, uint32(job))
}

// PrintPages draws each PNG page with GDI at its size on paper, centred in
// the printable area (scaled down only if it doesn't fit).
func (w *windowsPrinter) PrintPages(ctx context.Context, title string, ps []pages.Page, copies int) Outcome {
	dc, _, err := procCreateDCW.Call(uintptr(unsafe.Pointer(utf16("WINSPOOL"))), uintptr(unsafe.Pointer(utf16(w.name))), 0, 0)
	if dc == 0 {
		return Outcome{State: "failed", Code: "printer_gone", Message: fmt.Sprintf("Windows couldn't open %s: %v", w.name, err)}
	}
	defer procDeleteDC.Call(dc)
	di := docInfoW{DocName: utf16(title)}
	di.Size = int32(unsafe.Sizeof(di))
	job, _, err := procStartDocW.Call(dc, uintptr(unsafe.Pointer(&di)))
	if int32(job) <= 0 {
		return Outcome{State: "failed", Code: "spooler", Message: fmt.Sprintf("Windows didn't start the print: %v", err)}
	}
	cap := func(i uintptr) float64 { v, _, _ := procGetDeviceCaps.Call(dc, i); return float64(int32(v)) }
	dpiX, dpiY, horz, vert := cap(capLogPixelsX), cap(capLogPixelsY), cap(capHorzRes), cap(capVertRes)
	procSetStretchBltMode.Call(dc, stretchHalfone)
	if copies < 1 {
		copies = 1
	}
	started := false
	for c := 0; c < copies; c++ {
		for _, pg := range ps {
			if r, _, _ := procStartPage.Call(dc); int32(r) <= 0 {
				return w.abort(dc, started)
			}
			started = true
			wPx, hPx := pg.WidthMM/25.4*dpiX, pg.HeightMM/25.4*dpiY
			if s := math.Min(horz/wPx, vert/hPx); s < 1 {
				wPx, hPx = wPx*s, hPx*s
			}
			x, y := (horz-wPx)/2, (vert-hPx)/2
			if !drawGray(dc, pg, int32(x), int32(y), int32(wPx), int32(hPx)) {
				return w.abort(dc, started)
			}
			if r, _, _ := procEndPage.Call(dc); int32(r) <= 0 {
				return w.abort(dc, started)
			}
		}
	}
	if r, _, _ := procEndDoc.Call(dc); int32(r) <= 0 {
		return Outcome{State: "uncertain", Message: "Windows didn't finish the print"}
	}
	h, err := w.open()
	if err != nil {
		return Outcome{State: "sent"}
	}
	defer procClosePrinter.Call(h)
	return watch(ctx, h, uint32(job))
}

func (w *windowsPrinter) abort(dc uintptr, started bool) Outcome {
	procAbortDoc.Call(dc)
	if started {
		return Outcome{State: "uncertain", Message: "Windows stopped partway through the print"}
	}
	return Outcome{State: "failed", Code: "spooler", Message: "Windows didn't take the print"}
}

type bitmapInfo struct {
	Size          uint32
	Width, Height int32
	Planes, Bits  uint16
	Compression   uint32
	SizeImage     uint32
	XPels, YPels  int32
	ClrUsed       uint32
	ClrImportant  uint32
	Colors        [256]uint32
}

// drawGray hands an 8-bit grey page to the printer driver.
func drawGray(dc uintptr, pg pages.Page, x, y, w, h int32) bool {
	img := pg.Gray
	width, height := img.Rect.Dx(), img.Rect.Dy()
	stride := (width + 3) &^ 3
	bits := make([]byte, stride*height)
	for row := 0; row < height; row++ {
		copy(bits[row*stride:row*stride+width], img.Pix[row*img.Stride:row*img.Stride+width])
	}
	bi := bitmapInfo{Size: 40, Width: int32(width), Height: -int32(height), Planes: 1, Bits: 8, ClrUsed: 256}
	for i := range bi.Colors {
		v := uint32(i)
		bi.Colors[i] = v | v<<8 | v<<16
	}
	r, _, _ := procStretchDIBits.Call(dc, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, uintptr(width), uintptr(height),
		uintptr(unsafe.Pointer(&bits[0])), uintptr(unsafe.Pointer(&bi)), 0, srcCopy)
	return int32(r) > 0
}

// watch follows a spooled job for up to a minute: printed; gone from the
// queue (handed to the printer); stuck on paper out or an error for 20 s —
// cancelled, so nothing prints later and "Print again" is safe; else sent.
func watch(ctx context.Context, h uintptr, job uint32) Outcome {
	deadline := time.Now().Add(60 * time.Second)
	var stuckSince time.Time
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) {
		var needed uint32
		r, _, _ := procGetJobW.Call(h, uintptr(job), 1, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)))
		if r == 0 {
			return Outcome{State: "sent"} // left the queue: handed to the printer
		}
		ji := (*jobInfo1)(unsafe.Pointer(&buf[0]))
		switch {
		case ji.Status&(jobStatusPrinted|jobStatusComplete) != 0:
			return Outcome{State: "printed"}
		case ji.Status&jobStatusDeleted != 0:
			return Outcome{State: "failed", Code: "spooler", Message: "the job was cancelled in Windows"}
		case ji.Status&(jobStatusPaperOut|jobStatusOffline|jobStatusError|jobStatusUserInterv) != 0:
			if stuckSince.IsZero() {
				stuckSince = time.Now()
			} else if time.Since(stuckSince) > 20*time.Second {
				procSetJobW.Call(h, uintptr(job), 0, 0, jobControlDelete)
				code, why := "offline", "the printer is offline or showing an error"
				if ji.Status&jobStatusPaperOut != 0 {
					code, why = "paper_out", "the printer is out of paper"
				}
				return Outcome{State: "failed", Code: code, Message: why + "; the job was cancelled so it won't print later"}
			}
		default:
			stuckSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return Outcome{State: "sent"}
		case <-time.After(500 * time.Millisecond):
		}
	}
	return Outcome{State: "sent"}
}
