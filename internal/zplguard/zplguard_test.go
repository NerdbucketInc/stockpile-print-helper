package zplguard

import (
	"errors"
	"strings"
	"testing"
)

// A carrier-style label: its logo loaded into the printer's memory (R:),
// recalled, and deleted after — allowed.
const upsLike = "^XA^LH0,0^PW812^LL1218^POI\n~DGR:UPSLOGO.GRF,00080,010,FFFF00\n^FO40,40^XGR:UPSLOGO.GRF,1,1^FS\n^FO40,200^A0N,40,40^FDSHIP TO: BOB^FS\n^BY3^FO40,300^BCN,120,Y,N,N^FD1Z999AA10123456784^FS\n^IDR:UPSLOGO.GRF^FS\n^PQ1^XZ\n"

func TestCarrierLabelsPass(t *testing.T) {
	for _, l := range []string{upsLike, "^XA^FO40,40^A0N,50,50^FDStockpile test page^FS^XZ", "^xa^fo10,10^fdlower case^fs^xz", "^XA^DFR:FORM.ZPL^FS^XZ"} {
		if err := Check([]byte(l)); err != nil {
			t.Errorf("refused a normal label: %v\n%s", err, l)
		}
	}
}

func TestLastingChangesRefused(t *testing.T) {
	cases := map[string]string{
		"^XA^JUS^XZ":                        "^JU",
		"^xa^jus^xz":                        "^JU",
		"~JR":                               "~JR",
		"~DGE:LOGO.GRF,4,1,FF":              "~DG",
		"~dgb:x.grf,4,1,ff":                 "~DG",
		"^XA^IDE:*.*^FS^XZ":                 "^ID",
		"^XA^DFE:FORM.ZPL^FS^XZ":            "^DF",
		"~DYE:FIRMWARE,B,X,100,,":           "~DY",
		"! U1 setvar \"device.reset\" \"\"": "! U1 setvar",
		"{}{\"device.reset\":null}":         "{}{",
		"@PJL SET COPIES=1":                 "@PJL",
		"^XA^NI1^XZ":                        "^NI",
		"^CC+":                              "^CC",
		"~CT*":                              "~CT",
		"\x1b%-12345X":                      "\x1b%-12345X",
	}
	for data, want := range cases {
		err := Check([]byte(data))
		if err == nil {
			t.Errorf("let %q through", data)
			continue
		}
		var ge *Error
		if !errors.As(err, &ge) || !strings.EqualFold(ge.Refused.Command, want) {
			t.Errorf("%q: refused for %v, want %q", data, err, want)
		}
	}
}

func TestEveryCommandListedIsRefused(t *testing.T) {
	for _, r := range Commands() {
		data := r.Command
		if strings.HasPrefix(data, "~D") || strings.HasPrefix(data, "^D") || strings.HasPrefix(data, "^I") || strings.HasPrefix(data, "^HF") {
			data += "E:X.GRF"
		}
		if Check([]byte(data)) == nil {
			t.Errorf("%q is listed but not refused", r.Command)
		}
	}
}
