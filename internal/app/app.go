// Package app is what the window and the tray drive: pair, run the agent,
// pause, find and keep printers, unpair. The same steps the command line
// takes, kept in one place so the window can't do anything the commands
// can't.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nerdbucketinc/stockpile-print-helper/internal/agent"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/api"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/config"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/journal"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/netpolicy"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/printers"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/secret"
)

// App is one helper on this computer.
type App struct {
	Store   *config.Store
	Version string
	// Server is Stockpile's address the window pairs with (set at build time).
	Server string
	Log    *log.Logger

	mu      sync.Mutex
	state   agent.State
	removed string // "" or the shop it was removed from
	cancel  context.CancelFunc
	done    chan struct{}
	journal *journal.Journal
	// The printers last listed here, when the agent isn't reporting (listing
	// Windows' printers isn't free: at most every 30 s).
	listed   []api.Printer
	listedAt time.Time
}

// View is everything the window shows.
type View struct {
	Paired    bool
	Shop      string
	Location  string
	Computer  string
	Paused    bool
	Connected bool
	Problem   string
	Removed   string
	Printers  []api.Printer
	Recent    []journal.Entry
	LogDir    string
}

// OSName is "windows/amd64" and so on.
func OSName() string { return runtime.GOOS + "/" + runtime.GOARCH }

// ComputerName is this computer's name, in capitals.
func ComputerName() string {
	if n, err := os.Hostname(); err == nil && n != "" {
		return strings.ToUpper(n)
	}
	return "Shop computer"
}

// OpenLog writes to w (if any) and a daily file in the helper's folder,
// keeping 7 days. Job names, printers and results — never contents.
func OpenLog(store *config.Store, w io.Writer) *log.Logger {
	cut := time.Now().AddDate(0, 0, -7)
	if old, err := filepath.Glob(filepath.Join(store.LogDir(), "*.txt")); err == nil {
		for _, f := range old {
			if st, err := os.Stat(f); err == nil && st.ModTime().Before(cut) {
				os.Remove(f)
			}
		}
	}
	f, err := os.OpenFile(filepath.Join(store.LogDir(), time.Now().Format("2006-01-02")+".txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	switch {
	case err == nil && w != nil:
		w = io.MultiWriter(w, f)
	case err == nil:
		w = f
	case w == nil:
		w = io.Discard
	}
	return log.New(w, "", log.LstdFlags)
}

// Pair uses an owner's code with server (the app's own when "").
func (a *App) Pair(ctx context.Context, server, code string) (*api.Paired, error) {
	if server == "" {
		server = a.Server
	}
	if server == "" {
		return nil, errors.New("this build doesn't know Stockpile's address")
	}
	if strings.TrimSpace(code) == "" {
		return nil, errors.New("type the 8-character code from Stockpile")
	}
	c, err := api.New(server, "", a.Version, nil)
	if err != nil {
		return nil, err
	}
	p, err := c.Pair(ctx, code, ComputerName(), OSName(), a.Version)
	if err != nil {
		return nil, err
	}
	if err := secret.Save(a.Store.TokenPath(), p.Token); err != nil {
		return nil, err
	}
	_, err = a.Store.Update(func(s *config.Settings) {
		s.Server, s.HelperID, s.Computer, s.Shop, s.Location, s.Paused = strings.TrimRight(server, "/"), p.HelperID, ComputerName(), p.Shop.Name, p.Location.Name, false
	})
	a.mu.Lock()
	a.removed = ""
	a.mu.Unlock()
	return p, err
}

// client is the paired helper's client, or an error when not paired.
func (a *App) client() (*api.Client, config.Settings, error) {
	s, err := a.Store.Load()
	if err != nil {
		return nil, s, err
	}
	token, err := secret.Load(a.Store.TokenPath())
	if errors.Is(err, secret.ErrNone) || s.Server == "" || s.HelperID == "" {
		return nil, s, secret.ErrNone
	}
	if err != nil {
		return nil, s, err
	}
	c, err := api.New(s.Server, token, a.Version, nil)
	return c, s, err
}

// Start runs the agent in the background while paired (no-op otherwise).
func (a *App) Start() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return
	}
	c, s, err := a.client()
	if err != nil {
		return
	}
	if a.journal == nil {
		j, err := journal.Open(a.Store.JournalPath())
		if err != nil {
			a.Log.Printf("Can't open the journal: %v", err)
			return
		}
		a.journal = j
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel, a.done = cancel, make(chan struct{})
	ag := &agent.Agent{Client: c, Store: a.Store, Journal: a.journal, Log: a.Log, Version: a.Version, OS: OSName(), OnState: func(st agent.State) {
		a.mu.Lock()
		a.state = st
		a.mu.Unlock()
	}}
	a.Log.Printf("Stockpile Print Helper %s: printing for %s · %s.", a.Version, s.Shop, s.Location)
	go func(done chan struct{}) {
		defer close(done)
		err := ag.Run(ctx)
		a.mu.Lock()
		a.cancel, a.state = nil, agent.State{}
		if errors.Is(err, api.ErrRevoked) {
			a.removed = s.Shop
			a.Log.Printf("Removed from %s. It isn't connected and won't print anything.", s.Shop)
		}
		a.mu.Unlock()
	}(a.done)
}

// Done closes when the running agent stops (removed, or Stop); nil when none runs.
func (a *App) Done() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.done
}

// Stop ends the agent and waits for it.
func (a *App) Stop() {
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// SetPaused pauses or resumes printing (the running agent reports it at once).
func (a *App) SetPaused(p bool) error {
	_, err := a.Store.Update(func(s *config.Settings) { s.Paused = p })
	return err
}

// Unpair tells Stockpile, forgets the token, and stops.
func (a *App) Unpair(ctx context.Context) error {
	c, _, err := a.client()
	if err == nil {
		if err := c.Unpair(ctx); err != nil && !errors.Is(err, api.ErrRevoked) && !errors.Is(err, api.ErrUnauthorized) {
			return err
		}
	}
	a.Stop()
	secret.Forget(a.Store.TokenPath())
	_, err = a.Store.Update(func(s *config.Settings) { s.HelperID, s.Shop, s.Location = "", "", "" })
	return err
}

// Find looks for printers on this computer's network (only when asked).
func (a *App) Find(ctx context.Context) []printers.Found { return printers.Find(ctx) }

// Keep adds a network printer by address. kind "" means "a Zebra, if it says
// so"; otherwise the person says what it is.
func (a *App) Keep(ctx context.Context, target, kind, name string) (config.NetworkPrinter, error) {
	ap, err := netpolicy.ParseTarget(target)
	if err != nil {
		return config.NetworkPrinter{}, err
	}
	model, zebra, err := printers.Identify(ctx, ap)
	if err != nil {
		return config.NetworkPrinter{}, err
	}
	if kind == "" && zebra {
		kind = "label"
	}
	if kind != "label" && kind != "receipt" && kind != "page" {
		return config.NetworkPrinter{}, fmt.Errorf("something answered at %s but didn't say it's a label printer. What kind of printer is it?", ap)
	}
	if name == "" {
		name = strings.TrimSpace(model + " at " + ap.Addr().String())
	}
	n := config.NetworkPrinter{Address: ap.String(), Name: name, Kind: kind, ZPL: kind == "label"}
	a.mu.Lock()
	a.listedAt = time.Time{}
	a.mu.Unlock()
	_, err = a.Store.Update(func(s *config.Settings) {
		for i, x := range s.Network {
			if x.Address == n.Address {
				s.Network[i] = n
				return
			}
		}
		s.Network = append(s.Network, n)
	})
	return n, err
}

// Forget removes a kept network printer.
func (a *App) Forget(address string) error {
	a.mu.Lock()
	a.listedAt = time.Time{}
	a.mu.Unlock()
	_, err := a.Store.Update(func(s *config.Settings) {
		kept := s.Network[:0]
		for _, n := range s.Network {
			if n.Address != address {
				kept = append(kept, n)
			}
		}
		s.Network = kept
	})
	return err
}

// View is the window's picture of things now.
func (a *App) View(ctx context.Context) View {
	s, _ := a.Store.Load()
	_, tokenErr := secret.Load(a.Store.TokenPath())
	a.mu.Lock()
	st, removed, j := a.state, a.removed, a.journal
	a.mu.Unlock()
	v := View{
		Paired: tokenErr == nil && s.HelperID != "", Shop: s.Shop, Location: s.Location, Computer: ComputerName(),
		Paused: s.Paused, Connected: st.Connected, Problem: st.Problem, Removed: removed, LogDir: a.Store.LogDir(),
	}
	v.Printers = st.Printers
	if v.Printers == nil {
		a.mu.Lock()
		if time.Since(a.listedAt) > 30*time.Second {
			a.listed = nil
			for _, p := range printers.All(ctx, s) {
				a.listed = append(a.listed, p.Info())
			}
			a.listedAt = time.Now()
		}
		v.Printers = a.listed
		a.mu.Unlock()
	}
	if j == nil {
		if jj, err := journal.Open(a.Store.JournalPath()); err == nil {
			a.mu.Lock()
			if a.journal == nil {
				a.journal = jj
			}
			j = a.journal
			a.mu.Unlock()
		}
	}
	if j != nil {
		v.Recent = j.Recent(50)
	}
	return v
}
