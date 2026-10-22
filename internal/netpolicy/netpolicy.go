// Package netpolicy says which addresses the helper may print to: printers on
// the shop's own network only, on printer ports only. It is checked when a
// printer is added and again on every connect (the same rules as Stockpile's
// own printing/lan/address-policy.ts).
package netpolicy

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// RawPorts are raw (JetDirect) ports; IPPPort is IPP's.
const (
	RawPortMin = 9100
	RawPortMax = 9109
	IPPPort    = 631
)

var private = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"), // link-local
	netip.MustParsePrefix("fc00::/7"),       // IPv6 unique local
	netip.MustParsePrefix("fe80::/10"),      // IPv6 link-local
}

// AddressOK is nil for an address the helper may print to.
func AddressOK(a netip.Addr) error {
	a = a.Unmap()
	if !a.IsValid() {
		return fmt.Errorf("that isn't an address")
	}
	if a.IsLoopback() || a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return fmt.Errorf("%s isn't a printer on your network", a)
	}
	if a.Is4() && a.As4()[3] == 255 {
		return fmt.Errorf("%s is a broadcast address", a)
	}
	for _, p := range private {
		if p.Contains(a) {
			return nil
		}
	}
	return fmt.Errorf("%s isn't on your own network, so the helper won't print to it. Printer addresses start with 192.168., 10. or 172.16–31", a)
}

// PortOK is nil for a printer port.
func PortOK(port int) error {
	if (port >= RawPortMin && port <= RawPortMax) || port == IPPPort {
		return nil
	}
	return fmt.Errorf("port %d isn't a printer port (9100–9109, or 631)", port)
}

// ParseTarget reads "192.168.1.50" or "192.168.1.50:9101" (default port 9100).
func ParseTarget(s string) (netip.AddrPort, error) {
	s = strings.TrimSpace(s)
	host, portText, err := net.SplitHostPort(s)
	if err != nil {
		host, portText = s, strconv.Itoa(RawPortMin)
	}
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("type an address like 192.168.1.50")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("the port must be a number")
	}
	if err := AddressOK(a); err != nil {
		return netip.AddrPort{}, err
	}
	if err := PortOK(port); err != nil {
		return netip.AddrPort{}, err
	}
	return netip.AddrPortFrom(a.Unmap(), uint16(port)), nil
}
