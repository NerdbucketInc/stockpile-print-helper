package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/api"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/config"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/journal"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/pages"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/printers"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/secret"
)

// fakeStockpile serves /api/helper/v1 the way Stockpile does, for one job.
type fakeStockpile struct {
	mu      sync.Mutex
	job     *api.Job
	body    []byte
	taken   bool
	results []api.Outcome
	hellos  []api.Hello
	revoked bool
}

func (f *fakeStockpile) snapshot() (taken bool, results []api.Outcome, hellos []api.Hello) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.taken, append([]api.Outcome{}, f.results...), append([]api.Hello{}, f.hellos...)
}

func (f *fakeStockpile) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sph_test" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		f.mu.Lock()
		revoked := f.revoked
		f.mu.Unlock()
		if revoked {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"revoked"}`))
			return
		}
		switch {
		case r.URL.Path == "/api/helper/v1/hello":
			var h api.Hello
			json.NewDecoder(r.Body).Decode(&h)
			f.mu.Lock()
			f.hellos = append(f.hellos, h)
			f.mu.Unlock()
			w.Write([]byte(`{"shop":{"name":"Ridgeline Arms"},"location":{"name":"Main Street"},"pollWaitSec":25}`))
		case r.URL.Path == "/api/helper/v1/jobs/next":
			f.mu.Lock()
			j := f.job
			if f.taken {
				j = nil
			}
			f.mu.Unlock()
			if j != nil {
				json.NewEncoder(w).Encode(map[string]any{"job": j})
				return
			}
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/take"):
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.taken {
				w.WriteHeader(409)
				return
			}
			f.taken = true
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(f.body)
		case strings.HasSuffix(r.URL.Path, "/result"):
			b, _ := io.ReadAll(r.Body)
			var o api.Outcome
			json.Unmarshal(b, &o)
			if strings.Contains(string(b), "jobId") || strings.Contains(string(b), `"code":""`) {
				t.Errorf("result body carries a field the server refuses: %s", b)
			}
			o.JobID = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/helper/v1/jobs/"), "/result")
			f.mu.Lock()
			f.results = append(f.results, o)
			f.mu.Unlock()
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	})
}

// fakePrinter records what it was asked to print.
type fakePrinter struct {
	mu   sync.Mutex
	got  [][]byte
	pgs  int
	resp printers.Outcome
}

func (p *fakePrinter) Info() api.Printer {
	return api.Printer{ID: "raw:192.168.1.50:9100", Name: "Zebra", Connection: "network-raw", Kind: "label", Formats: []string{"ZPL", "PNG"}, Status: "ready"}
}

func (p *fakePrinter) PrintRaw(_ context.Context, _ string, d []byte, _ int) printers.Outcome {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, append([]byte{}, d...))
	return p.resp
}

func (p *fakePrinter) PrintPages(_ context.Context, _ string, ps []pages.Page, _ int) printers.Outcome {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pgs += len(ps)
	return p.resp
}

func (p *fakePrinter) printed() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte{}, p.got...)
}

func setup(t *testing.T, f *fakeStockpile, p *fakePrinter) (*Agent, *config.Store) {
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := secret.Save(store.TokenPath(), "sph_test"); err != nil {
		t.Fatal(err)
	}
	j, _ := journal.Open(store.JournalPath())
	c, err := api.New(srv.URL, "sph_test", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Agent{
		Client: c, Store: store, Journal: j, Log: log.New(io.Discard, "", 0), Version: "test", OS: "test",
		Printers: func(context.Context, config.Settings) []printers.Printer { return []printers.Printer{p} },
		ReHello:  200 * time.Millisecond, PollWait: 0,
	}, store
}

func job(body []byte, format string) *api.Job {
	sum := sha256.Sum256(body)
	return &api.Job{ID: "job1", PrinterID: "raw:192.168.1.50:9100", Format: format, Title: "Shipping label — order #2291", Copies: 1, Bytes: len(body), SHA256: hex.EncodeToString(sum[:]), TakeBy: time.Now().Add(time.Minute).Format(time.RFC3339)}
}

func runFor(a *Agent, d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return a.Run(ctx)
}

func TestPrintsAndSaysSo(t *testing.T) {
	body := []byte("^XA^FDShip to Bob^FS^XZ")
	f := &fakeStockpile{job: job(body, "ZPL"), body: body}
	p := &fakePrinter{resp: printers.Outcome{State: "sent"}}
	a, _ := setup(t, f, p)
	runFor(a, 700*time.Millisecond)
	if got := p.printed(); len(got) != 1 || string(got[0]) != string(body) {
		t.Fatalf("printer got %q", got)
	}
	_, results, hellos := f.snapshot()
	if len(results) != 1 || results[0].State != "sent" || results[0].JobID != "job1" {
		t.Fatalf("results %+v", results)
	}
	if e, _ := a.Journal.Get("job1"); !e.Acked || e.Title == "" {
		t.Fatalf("journal %+v", e)
	}
	if len(hellos) == 0 || len(hellos[0].Printers) != 1 || hellos[0].Printers[0].Kind != "label" {
		t.Fatalf("hellos %+v", hellos)
	}
}

func TestRefusesASettingsChange(t *testing.T) {
	body := []byte("^XA^JUS^XZ")
	f := &fakeStockpile{job: job(body, "ZPL"), body: body}
	p := &fakePrinter{resp: printers.Outcome{State: "sent"}}
	a, _ := setup(t, f, p)
	runFor(a, 700*time.Millisecond)
	if len(p.printed()) != 0 {
		t.Fatal("the printer got it")
	}
	if _, results, _ := f.snapshot(); len(results) != 1 || results[0].State != "failed" || results[0].Code != "refused_command" {
		t.Fatalf("results %+v", results)
	}
}

func TestADocumentThatDidntArriveWholeIsNotPrinted(t *testing.T) {
	body := []byte("^XA^XZ")
	j := job(body, "ZPL")
	j.SHA256 = strings.Repeat("0", 64)
	f := &fakeStockpile{job: j, body: body}
	p := &fakePrinter{resp: printers.Outcome{State: "sent"}}
	a, _ := setup(t, f, p)
	runFor(a, 700*time.Millisecond)
	if _, results, _ := f.snapshot(); len(p.printed()) != 0 || len(results) != 1 || results[0].State != "not_received" {
		t.Fatalf("printed %d, results %+v", len(p.printed()), results)
	}
}

func TestPausedTakesNothing(t *testing.T) {
	body := []byte("^XA^XZ")
	f := &fakeStockpile{job: job(body, "ZPL"), body: body}
	p := &fakePrinter{resp: printers.Outcome{State: "sent"}}
	a, store := setup(t, f, p)
	store.Update(func(s *config.Settings) { s.Paused = true })
	runFor(a, 500*time.Millisecond)
	taken, _, hellos := f.snapshot()
	if taken || len(p.printed()) != 0 {
		t.Fatal("a paused helper took a job")
	}
	if len(hellos) == 0 || !hellos[0].Paused {
		t.Fatal("paused wasn't reported")
	}
}

func TestRemovedForgetsItsToken(t *testing.T) {
	f := &fakeStockpile{revoked: true}
	a, store := setup(t, f, &fakePrinter{})
	if err := runFor(a, 2*time.Second); !errors.Is(err, api.ErrRevoked) {
		t.Fatalf("Run = %v, want ErrRevoked", err)
	}
	if _, err := os.Stat(store.TokenPath()); !os.IsNotExist(err) {
		t.Fatal("the token is still saved")
	}
}

func TestAnUntoldOutcomeGoesWithTheNextReport(t *testing.T) {
	f := &fakeStockpile{}
	a, _ := setup(t, f, &fakePrinter{})
	a.Journal.Mark(journal.Entry{JobID: "earlier", Title: "Receipt", State: journal.Printing})
	j2, _ := journal.Open(a.Store.JournalPath()) // restarted after a crash mid-print
	a.Journal = j2
	runFor(a, 300*time.Millisecond)
	_, _, hellos := f.snapshot()
	if len(hellos) == 0 || len(hellos[0].Journal) != 1 || hellos[0].Journal[0].State != "uncertain" {
		t.Fatalf("hellos %+v", hellos)
	}
	if e, _ := a.Journal.Get("earlier"); !e.Acked {
		t.Fatal("not acknowledged after the report")
	}
}
