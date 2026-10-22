// Package agent is the helper's one loop: report in, wait for a job, take it,
// print it, say how it went. Each step is journalled before the next, so a
// crash or power cut is told honestly (package journal).
package agent

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/nerdbucketinc/stockpile-print-helper/internal/api"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/config"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/journal"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/pages"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/printers"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/secret"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/zplguard"
)

// Agent runs one paired helper.
type Agent struct {
	Client  *api.Client
	Store   *config.Store
	Journal *journal.Journal
	Log     *log.Logger
	Version string
	OS      string
	// Printers lists what this helper may use (printers.All by default).
	Printers func(ctx context.Context, s config.Settings) []printers.Printer
	// ReHello: how often the report is sent again (printers come and go).
	ReHello time.Duration
	// PollWait: how long one poll is held (≤ 25 s).
	PollWait int
	// OnState, when set, hears whether Stockpile answered and what was
	// reported (the window shows it).
	OnState func(State)
}

// State is what the window shows about the connection.
type State struct {
	Connected bool
	Problem   string
	Printers  []api.Printer
	At        time.Time
}

func (a *Agent) report(st State) {
	if a.OnState != nil {
		st.At = time.Now()
		a.OnState(st)
	}
}

// Backoff after a failed request: 1, 2, 5, 10, then 30 s.
var Backoff = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}

// Run works until ctx ends, or Stockpile removes this helper (ErrRevoked:
// the token is forgotten).
func (a *Agent) Run(ctx context.Context) error {
	if a.Printers == nil {
		a.Printers = printers.All
	}
	if a.ReHello == 0 {
		a.ReHello = 60 * time.Second
	}
	if a.PollWait == 0 {
		a.PollWait = 25
	}
	fails := 0
	for ctx.Err() == nil {
		err := a.round(ctx)
		if errors.Is(err, api.ErrRevoked) || errors.Is(err, api.ErrUnauthorized) {
			a.Log.Printf("Stockpile says this helper isn't paired any more (%v). Forgetting its token.", err)
			secret.Forget(a.Store.TokenPath())
			a.Store.Update(func(s *config.Settings) { s.HelperID, s.Shop, s.Location = "", "", "" })
			return api.ErrRevoked
		}
		if err != nil && ctx.Err() == nil {
			a.report(State{Connected: false, Problem: err.Error()})
			wait := Backoff[min(fails, len(Backoff)-1)]
			fails++
			a.Log.Printf("Can't reach Stockpile (%v). Trying again in %s.", err, wait)
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
			continue
		}
		fails = 0
	}
	return ctx.Err()
}

// round: one report, then polls until it's time to report again.
func (a *Agent) round(ctx context.Context) error {
	s, err := a.Store.Load()
	if err != nil {
		return err
	}
	list := a.Printers(ctx, s)
	told := a.Journal.Unacked()
	h := api.Hello{Version: a.Version, OS: a.OS, Paused: s.Paused, Printers: infos(list), Journal: outcomes(told)}
	reply, err := a.Client.Hello(ctx, h)
	if err != nil {
		return err
	}
	a.report(State{Connected: true, Printers: h.Printers})
	for _, e := range told {
		a.Journal.Ack(e.JobID)
	}
	if reply.Shop.Name != s.Shop || reply.Location.Name != s.Location {
		a.Store.Update(func(x *config.Settings) { x.Shop, x.Location = reply.Shop.Name, reply.Location.Name })
	}
	until := time.Now().Add(a.ReHello)
	for time.Now().Before(until) && ctx.Err() == nil {
		job, err := a.Client.Next(ctx, a.PollWait)
		if err != nil {
			return err
		}
		now, _ := a.Store.Load()
		if now.Paused != s.Paused {
			return nil // report the change at once
		}
		if job == nil {
			continue
		}
		if now.Paused {
			a.Log.Printf("Paused: not taking %q.", job.Title)
			continue
		}
		a.handle(ctx, job, list)
	}
	return nil
}

func infos(list []printers.Printer) []api.Printer {
	out := make([]api.Printer, 0, len(list))
	for _, p := range list {
		out = append(out, p.Info())
	}
	return out
}

func outcomes(es []journal.Entry) []api.Outcome {
	out := make([]api.Outcome, 0, len(es))
	for _, e := range es {
		out = append(out, api.Outcome{JobID: e.JobID, State: e.State, Code: e.Code, Message: clip(e.Message)})
	}
	return out
}

func clip(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 280 {
		return s[:280]
	}
	return s
}

// handle takes one job and prints it. Every outcome is journalled first and
// told after; a failed tell is told again in the next report.
func (a *Agent) handle(ctx context.Context, job *api.Job, list []printers.Printer) {
	if t, err := time.Parse(time.RFC3339, job.TakeBy); err == nil && time.Now().After(t) {
		a.Log.Printf("%q came too late to print; leaving it.", job.Title)
		return
	}
	base := journal.Entry{JobID: job.ID, Title: job.Title, Printer: job.PrinterID}
	mark := func(state, code, msg string) {
		e := base
		e.State, e.Code, e.Message = state, code, msg
		if err := a.Journal.Mark(e); err != nil {
			a.Log.Printf("Couldn't write the journal: %v", err)
		}
	}
	mark(journal.Taking, "", "")
	body, _, err := a.Client.Take(ctx, job)
	switch {
	case errors.Is(err, api.ErrGone):
		mark(journal.NotReceived, "", "it wasn't waiting any more")
		a.Journal.Ack(job.ID) // Stockpile already knows
		return
	case err != nil:
		// No document here: nothing reached a printer.
		mark(journal.NotReceived, "", err.Error())
		a.tell(ctx, job.ID)
		return
	}
	defer clear(body) // printed and gone: nothing kept
	mark(journal.Received, "", "")
	out := a.print(ctx, job, body, list)
	mark(out.State, out.Code, out.Message)
	a.Log.Printf("%q on %s: %s %s", job.Title, job.PrinterID, out.State, out.Message)
	a.tell(ctx, job.ID)
}

func (a *Agent) print(ctx context.Context, job *api.Job, body []byte, list []printers.Printer) printers.Outcome {
	p := printers.ByID(list, job.PrinterID)
	if p == nil {
		return printers.Outcome{State: "failed", Code: "printer_gone", Message: "that printer isn't on this computer's list any more"}
	}
	copies := min(max(job.Copies, 1), 20)
	switch job.Format {
	case "ZPL":
		if !printers.Takes(p, printers.FormatZPL) {
			return printers.Outcome{State: "failed", Code: "format", Message: "this printer doesn't take label data"}
		}
		if err := zplguard.Check(body); err != nil {
			return printers.Outcome{State: "failed", Code: "refused_command", Message: err.Error()}
		}
		a.Journal.Mark(journal.Entry{JobID: job.ID, State: journal.Printing})
		return p.PrintRaw(ctx, job.Title, body, copies)
	case "PNG":
		if !printers.Takes(p, printers.FormatPNG) {
			return printers.Outcome{State: "failed", Code: "format", Message: "this printer doesn't print pages"}
		}
		ps, err := pages.Decode(body)
		if err != nil {
			return printers.Outcome{State: "failed", Code: "bad_document", Message: err.Error()}
		}
		a.Journal.Mark(journal.Entry{JobID: job.ID, State: journal.Printing})
		return p.PrintPages(ctx, job.Title, ps, copies)
	}
	return printers.Outcome{State: "failed", Code: "format", Message: "unknown kind of document"}
}

// tell sends a job's journalled outcome; on failure the next report carries it.
func (a *Agent) tell(ctx context.Context, jobID string) {
	e, ok := a.Journal.Get(jobID)
	if !ok || !journal.Final(e.State) {
		return
	}
	if err := a.Client.Result(ctx, api.Outcome{JobID: e.JobID, State: e.State, Code: e.Code, Message: clip(e.Message)}); err != nil {
		a.Log.Printf("Couldn't tell Stockpile how %q went (%v); it goes with the next report.", e.Title, err)
		return
	}
	a.Journal.Ack(jobID)
}
