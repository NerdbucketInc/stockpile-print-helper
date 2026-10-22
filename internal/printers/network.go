package printers

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/nerdbucketinc/stockpile-print-helper/internal/api"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/config"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/netpolicy"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/pages"
)

// Timeouts for a raw (port 9100) printer.
var (
	ConnectTimeout = 5 * time.Second
	WriteTimeout   = 30 * time.Second
	CloseGrace     = 2 * time.Second
	StatusTimeout  = 2 * time.Second
)

// network is a raw (JetDirect, port 9100) printer kept at this computer.
type network struct {
	cfg  config.NetworkPrinter
	addr netip.AddrPort
}

// NewNetwork checks a kept printer's address again (the same rules as when
// it was added).
func NewNetwork(n config.NetworkPrinter) (Printer, error) {
	ap, err := netpolicy.ParseTarget(n.Address)
	if err != nil {
		return nil, err
	}
	return &network{cfg: n, addr: ap}, nil
}

func (n *network) Info() api.Printer {
	formats := []string{}
	if n.cfg.ZPL {
		formats = append(formats, FormatZPL)
	}
	name := n.cfg.Name
	if name == "" {
		name = "Printer at " + n.addr.Addr().String()
	}
	return api.Printer{
		ID:         "raw:" + n.addr.String(),
		Name:       name,
		Connection: "network-raw",
		Kind:       n.cfg.Kind,
		Formats:    formats,
		DPI:        203,
		Papers:     []string{},
		Status:     "ready",
	}
}

func (n *network) PrintPages(context.Context, string, []pages.Page, int) Outcome {
	return Outcome{State: "failed", Code: "format", Message: "a raw network printer can't print pages"}
}

// PrintRaw: a Zebra is asked its status first (~HS) — out of paper, paused
// or the head open means nothing is sent. Then the bytes, as they are.
// Refused or no answer before a byte went: failed. Dropped while writing:
// uncertain. All written and closed: sent (a raw printer can't say more).
func (n *network) PrintRaw(ctx context.Context, title string, data []byte, copies int) Outcome {
	if err := netpolicy.AddressOK(n.addr.Addr()); err != nil {
		return Outcome{State: "failed", Code: "printer_gone", Message: err.Error()}
	}
	if n.cfg.ZPL {
		if why := zebraStatus(ctx, n.addr); why != "" {
			return Outcome{State: "failed", Code: codeFor(why), Message: why}
		}
	}
	d := net.Dialer{Timeout: ConnectTimeout}
	conn, err := d.DialContext(ctx, "tcp", n.addr.String())
	if err != nil {
		return Outcome{State: "failed", Code: "offline", Message: "the printer didn't answer"}
	}
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
	if copies < 1 {
		copies = 1
	}
	for i := 0; i < copies; i++ {
		if _, err := conn.Write(data); err != nil {
			return Outcome{State: "uncertain", Message: "the connection dropped while sending"}
		}
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
	conn.SetReadDeadline(time.Now().Add(CloseGrace))
	buf := make([]byte, 512)
	for {
		if _, err := conn.Read(buf); err != nil {
			break // closed, or the grace ran out: the bytes went
		}
	}
	return Outcome{State: "sent"}
}

func codeFor(why string) string {
	if strings.Contains(why, "paper") {
		return "paper_out"
	}
	return "offline"
}

// zebraStatus asks a ZPL printer "~HS" and reads its answer's flags. No
// answer (not every printer gives one) is not a problem: "" is "go ahead".
func zebraStatus(ctx context.Context, addr netip.AddrPort) string {
	d := net.Dialer{Timeout: ConnectTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr.String())
	if err != nil {
		return "" // the print itself will say it's unreachable
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(StatusTimeout))
	if _, err := conn.Write([]byte("~HS")); err != nil {
		return ""
	}
	r := bufio.NewReader(conn)
	var lines [][]byte
	for len(lines) < 2 {
		line, err := r.ReadBytes('\x03')
		if err != nil {
			break
		}
		lines = append(lines, bytes.Trim(line, "\x02\x03\r\n"))
	}
	return readHS(lines)
}

// readHS reads the ~HS answer's first two strings:
//
//	1: aaa,b,c,dddd,eee,f,…  b = paper out, c = paused
//	2: mmm,n,o,p,…           o = head up (open), p = ribbon out
func readHS(lines [][]byte) string {
	if len(lines) >= 1 {
		f := bytes.Split(lines[0], []byte(","))
		if len(f) >= 3 {
			if string(bytes.TrimSpace(f[1])) == "1" {
				return "out of paper"
			}
			if string(bytes.TrimSpace(f[2])) == "1" {
				return "paused"
			}
		}
	}
	if len(lines) >= 2 {
		f := bytes.Split(lines[1], []byte(","))
		if len(f) >= 4 {
			if string(bytes.TrimSpace(f[2])) == "1" {
				return "head open"
			}
			if string(bytes.TrimSpace(f[3])) == "1" {
				return "out of ribbon"
			}
		}
	}
	return ""
}

// Identify asks a printer at an address what it is ("~HI": a Zebra answers
// with its model). It never sends anything that prints.
func Identify(ctx context.Context, addr netip.AddrPort) (model string, zebra bool, err error) {
	if err := netpolicy.AddressOK(addr.Addr()); err != nil {
		return "", false, err
	}
	if err := netpolicy.PortOK(int(addr.Port())); err != nil {
		return "", false, err
	}
	d := net.Dialer{Timeout: ConnectTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr.String())
	if err != nil {
		return "", false, fmt.Errorf("no printer answered at %s", addr)
	}
	defer conn.Close()
	if addr.Port() == netpolicy.IPPPort {
		return "", false, nil
	}
	conn.SetDeadline(time.Now().Add(StatusTimeout))
	if _, err := conn.Write([]byte("~HI")); err != nil {
		return "", false, nil
	}
	line, err := bufio.NewReader(conn).ReadBytes('\x03')
	if err != nil && !errors.Is(err, net.ErrClosed) && len(line) == 0 {
		return "", false, nil
	}
	f := strings.Split(strings.Trim(string(line), "\x02\x03\r\n "), ",")
	if len(f) >= 2 && f[0] != "" {
		return strings.TrimSpace(f[0]), true, nil
	}
	return "", false, nil
}
