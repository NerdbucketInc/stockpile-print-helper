package printers

import (
	"context"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/netpolicy"
)

// Found is a printer "Find printers" saw. Nothing is kept until a person
// keeps it.
type Found struct {
	Address netip.AddrPort
	Model   string // from ~HI, when it answered
	Zebra   bool
}

// FindTimeout bounds a whole search.
var FindTimeout = 20 * time.Second

// Find looks for printers on this computer's own networks: it tries ports
// 9100 and 631 on each private IPv4 network it's on (the /24 around its
// address, at most), 64 at a time, then asks each raw one what it is. It
// runs only when a person asks.
func Find(ctx context.Context) []Found {
	ctx, cancel := context.WithTimeout(ctx, FindTimeout)
	defer cancel()
	var targets []netip.AddrPort
	for _, p := range localNetworks() {
		for a := p.Masked().Addr().Next(); p.Contains(a); a = a.Next() {
			if netpolicy.AddressOK(a) != nil {
				continue
			}
			targets = append(targets, netip.AddrPortFrom(a, 9100), netip.AddrPortFrom(a, netpolicy.IPPPort))
		}
	}
	var (
		mu  sync.Mutex
		out []Found
		wg  sync.WaitGroup
	)
	sem := make(chan struct{}, 64)
	for _, t := range targets {
		select {
		case <-ctx.Done():
		case sem <- struct{}{}:
			wg.Add(1)
			go func(t netip.AddrPort) {
				defer wg.Done()
				defer func() { <-sem }()
				d := net.Dialer{Timeout: 400 * time.Millisecond}
				c, err := d.DialContext(ctx, "tcp", t.String())
				if err != nil {
					return
				}
				c.Close()
				f := Found{Address: t}
				if t.Port() != netpolicy.IPPPort {
					f.Model, f.Zebra, _ = Identify(ctx, t)
				}
				mu.Lock()
				out = append(out, f)
				mu.Unlock()
			}(t)
		}
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Address, out[j].Address
		if a.Addr() != b.Addr() {
			return a.Addr().Less(b.Addr())
		}
		return a.Port() < b.Port()
	})
	return out
}

// localNetworks: this computer's private IPv4 networks, each cut to a /24.
func localNetworks() []netip.Prefix {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	seen := map[netip.Prefix]bool{}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if !ip.Is4() || netpolicy.AddressOK(ip) != nil {
			continue
		}
		ones, _ := ipn.Mask.Size()
		if ones < 24 {
			ones = 24
		}
		p := netip.PrefixFrom(ip, ones).Masked()
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
