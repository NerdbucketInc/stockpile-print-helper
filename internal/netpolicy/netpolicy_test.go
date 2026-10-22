package netpolicy

import "testing"

func TestTargets(t *testing.T) {
	ok := map[string]string{
		"192.168.1.50":       "192.168.1.50:9100",
		"192.168.1.50:9101":  "192.168.1.50:9101",
		" 10.0.0.7:631 ":     "10.0.0.7:631",
		"172.16.4.4":         "172.16.4.4:9100",
		"172.31.255.1":       "172.31.255.1:9100",
		"[fd00::1]:9100":     "[fd00::1]:9100",
		"::ffff:192.168.0.9": "192.168.0.9:9100",
	}
	for in, want := range ok {
		got, err := ParseTarget(in)
		if err != nil || got.String() != want {
			t.Errorf("%q: got %v %v, want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"8.8.8.8", "127.0.0.1", "::1", "0.0.0.0", "224.0.0.1", "192.168.1.255", "172.32.0.1", "192.168.1.50:22", "192.168.1.50:8080", "printer.local", "", "192.168.1.50:x", "::ffff:127.0.0.1"} {
		if _, err := ParseTarget(in); err == nil {
			t.Errorf("%q was allowed", in)
		}
	}
}
