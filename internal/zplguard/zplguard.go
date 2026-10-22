// Package zplguard refuses label data that would change a printer for good.
//
// A label printer takes its settings commands in the same stream as the label
// it prints. A label from a carrier needs none of the commands below; they
// save settings, load firmware or files, reset the printer, or switch its
// language. The helper refuses a job that holds any of them, so whoever
// controls the server can't use a label to change a printer beyond that one
// label (SECURITY.md lists them).
package zplguard

import (
	"bytes"
	"fmt"
)

// Refused is one command the guard refuses, and why.
type Refused struct {
	Command string
	Why     string
}

// The commands refused, as they appear in the stream (case-insensitive).
// ZPL commands start with ^ or ~ (or the printer's changed prefix, which ^CC,
// ~CC, ^CT and ~CT set: those are refused too, so the prefix can't be
// changed to hide the rest).
var refused = []Refused{
	{"^JU", "saves or restores the printer's settings"},
	{"~JR", "resets the printer"},
	{"~JN", "turns head-test fault checking off"},
	{"~JO", "turns head-test fault checking on"},
	{"^JZ", "changes reprint-after-error"},
	{"~DY", "loads files or firmware"},
	{"~DN", "aborts a download"},
	{"^HZ", "reads printer information back"},
	{"~HB", "reads battery status back"},
	{"^NC", "changes the network settings"},
	{"^NI", "changes the network settings"},
	{"^NS", "changes the network settings"},
	{"^NT", "changes the network settings"},
	{"^ND", "changes the network settings"},
	{"^NB", "changes the network settings"},
	{"^NN", "changes the network settings"},
	{"^NP", "changes the network settings"},
	{"~NC", "changes the network settings"},
	{"~NT", "changes the network settings"},
	{"^KP", "sets a front-panel password"},
	{"^KL", "changes the front-panel language"},
	{"^SZ", "switches the printer's language"},
	{"^CC", "changes the command prefix"},
	{"~CC", "changes the command prefix"},
	{"^CT", "changes the control prefix"},
	{"~CT", "changes the control prefix"},
	{"^CD", "changes the delimiter"},
	{"~CD", "changes the delimiter"},
	{"~WC", "prints a configuration label"},
	{"~WR", "resets the wireless card"},
	{"~RO", "resets counters"},
}

// Commands that store or delete files: fine in the printer's memory (R:,
// gone at power-off — UPS labels load their logo that way and delete it
// after), refused anywhere that lasts (E: flash, B: and A: cards).
var lasting = []Refused{
	{"~DG", "stores a graphic in the printer's permanent memory"},
	{"~DB", "stores a font in the printer's permanent memory"},
	{"~DU", "stores a font in the printer's permanent memory"},
	{"~DS", "stores a font in the printer's permanent memory"},
	{"~DT", "stores a font in the printer's permanent memory"},
	{"^DF", "stores a format in the printer's permanent memory"},
	{"^ID", "deletes files from the printer's permanent memory"},
	{"^IS", "stores an image in the printer's permanent memory"},
	{"^HF", "reads a format back"},
}

// Other languages that ride in the same stream.
var refusedText = []Refused{
	{"! U1 setvar", "changes a printer setting (SGD)"},
	{"! U1 do", "runs a printer action (SGD)"},
	{"! U1 getvar", "reads a printer setting back (SGD)"},
	{"! U setvar", "changes a printer setting (SGD)"},
	{"! U do", "runs a printer action (SGD)"},
	{"{}{", "sends a JSON settings command (SGD)"},
	{"@PJL", "sends a printer job language command"},
	{"\x1b%-12345X", "switches the printer's language (UEL)"},
}

// Check returns nil when data holds none of the refused commands, else the
// first one found.
func Check(data []byte) error {
	up := bytes.ToUpper(data)
	for _, r := range refused {
		if bytes.Contains(up, []byte(r.Command)) {
			return &Error{Refused: r}
		}
	}
	for _, r := range lasting {
		for at := 0; ; {
			i := bytes.Index(up[at:], []byte(r.Command))
			if i < 0 {
				break
			}
			at += i + len(r.Command)
			// The device comes first ("E:LOGO.GRF"); none named = R: (memory).
			if at+1 < len(up) && up[at+1] == ':' && up[at] != 'R' {
				return &Error{Refused: r}
			}
		}
	}
	for _, r := range refusedText {
		if bytes.Contains(up, bytes.ToUpper([]byte(r.Command))) {
			return &Error{Refused: r}
		}
	}
	return nil
}

// Error is a refused job.
type Error struct{ Refused Refused }

func (e *Error) Error() string {
	return fmt.Sprintf("refused %q: it %s", e.Refused.Command, e.Refused.Why)
}

// Commands lists the refused commands (for SECURITY.md and the tests).
func Commands() []Refused {
	out := append([]Refused{}, refused...)
	out = append(out, lasting...)
	return append(out, refusedText...)
}
