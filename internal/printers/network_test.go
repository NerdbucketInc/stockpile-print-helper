package printers

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/nerdbucketinc/stockpile-print-helper/internal/config"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/netpolicy"
)

// A private address of this machine: the helper prints only to those, so the
// fake printer listens there (no test-only hole in the address rules).
func privateAddr(t *testing.T) netip.Addr {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok && ip.Unmap().Is4() && netpolicy.AddressOK(ip.Unmap()) == nil {
				return ip.Unmap()
			}
		}
	}
	t.Skip("no private IPv4 address on this machine")
	return netip.Addr{}
}

// fakeZebra listens on a printer port; status is its ~HS answer ("" = none).
func fakeZebra(t *testing.T, status string) (netip.AddrPort, chan string) {
	ip := privateAddr(t)
	var ln net.Listener
	for port := 9100; port <= 9109 && ln == nil; port++ {
		ln, _ = net.Listen("tcp", netip.AddrPortFrom(ip, uint16(port)).String())
	}
	if ln == nil {
		t.Skip("no free printer port")
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 10)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				buf := make([]byte, 3)
				n, _ := io.ReadFull(c, buf)
				head := string(buf[:n])
				switch head {
				case "~HS":
					if status != "" {
						c.Write([]byte(status))
					}
					return
				case "~HI":
					c.Write([]byte("\x02ZD620-203dpi,V84.20.18Z,8,8176KB\x03\r\n"))
					return
				}
				rest, _ := io.ReadAll(c)
				got <- head + string(rest)
			}(c)
		}
	}()
	return netip.MustParseAddrPort(ln.Addr().String()), got
}

const hsReady = "\x02030,0,0,1218,000,0,0,0,000,0,0,0\x03\r\n\x02000,0,0,0,0,2,4,0,00000000,1,000\x03\r\n\x021234,0\x03\r\n"
const hsPaperOut = "\x02030,1,0,1218,000,0,0,0,000,0,0,0\x03\r\n\x02000,0,0,0,0,2,4,0,00000000,1,000\x03\r\n\x021234,0\x03\r\n"

func TestRawSendsTheLabel(t *testing.T) {
	addr, got := fakeZebra(t, hsReady)
	p, err := NewNetwork(config.NetworkPrinter{Address: addr.String(), Kind: "label", ZPL: true})
	if err != nil {
		t.Fatal(err)
	}
	out := p.PrintRaw(context.Background(), "Label", []byte("^XA^FDhi^FS^XZ"), 2)
	if out.State != "sent" {
		t.Fatalf("outcome %+v", out)
	}
	if s := <-got; s != "^XA^FDhi^FS^XZ^XA^FDhi^FS^XZ" {
		t.Fatalf("printer got %q", s)
	}
}

func TestOutOfPaperSendsNothing(t *testing.T) {
	addr, got := fakeZebra(t, hsPaperOut)
	p, _ := NewNetwork(config.NetworkPrinter{Address: addr.String(), Kind: "label", ZPL: true})
	out := p.PrintRaw(context.Background(), "Label", []byte("^XA^XZ"), 1)
	if out.State != "failed" || out.Code != "paper_out" {
		t.Fatalf("outcome %+v", out)
	}
	select {
	case s := <-got:
		t.Fatalf("the label was sent anyway: %q", s)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestNoAnswerIsFailedNotUncertain(t *testing.T) {
	ip := privateAddr(t)
	p, _ := NewNetwork(config.NetworkPrinter{Address: netip.AddrPortFrom(ip, 9109).String(), Kind: "label"})
	ConnectTimeout = time.Second
	out := p.PrintRaw(context.Background(), "Label", []byte("^XA^XZ"), 1)
	if out.State != "failed" {
		t.Fatalf("outcome %+v", out)
	}
}

func TestIdentifyAZebra(t *testing.T) {
	addr, _ := fakeZebra(t, "")
	model, zebra, err := Identify(context.Background(), addr)
	if err != nil || !zebra || !strings.HasPrefix(model, "ZD620") {
		t.Fatalf("got %q %v %v", model, zebra, err)
	}
	if _, _, err := Identify(context.Background(), netip.MustParseAddrPort("8.8.8.8:9100")); err == nil {
		t.Fatal("identified a public address")
	}
}

func TestReadHS(t *testing.T) {
	split := func(s string) [][]byte {
		var out [][]byte
		for _, l := range strings.Split(s, "\x03\r\n") {
			if l != "" {
				out = append(out, []byte(strings.Trim(l, "\x02")))
			}
		}
		return out
	}
	if why := readHS(split(hsReady)); why != "" {
		t.Errorf("ready read as %q", why)
	}
	if why := readHS(split(hsPaperOut)); why != "out of paper" {
		t.Errorf("paper out read as %q", why)
	}
	if why := readHS(split("\x02030,0,1,1218\x03\r\n")); why != "paused" {
		t.Errorf("paused read as %q", why)
	}
}

func TestKinds(t *testing.T) {
	for driver, want := range map[string]string{"ZDesigner ZD421-203dpi ZPL": "label", "EPSON TM-T88V Receipt5": "receipt", "Brother HL-L2350D series": "page", "Star TSP100 Cutter (TSP143)": "receipt", "Zebra ZP 500 (EPL)": "label"} {
		if k, _ := KindOf(driver, driver); k != want {
			t.Errorf("%s: %s, want %s", driver, k, want)
		}
	}
	if _, zpl := KindOf("Zebra ZP 500 (EPL)", "Zebra GK420t (Network)"); zpl {
		t.Error("an EPL printer was offered ZPL")
	}
	if _, zpl := KindOf("ZDesigner ZD421-203dpi ZPL", "x"); !zpl {
		t.Error("a ZPL printer wasn't")
	}
	if !Skipped("Microsoft Print to PDF", "PORTPROMPT:") || Skipped("Brother", "USB001") {
		t.Error("virtual printers")
	}
}
