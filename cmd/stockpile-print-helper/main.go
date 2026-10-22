// Command stockpile-print-helper is the Stockpile Print Helper: it connects
// out to Stockpile, takes print jobs for its one shop and location, prints
// them on this computer's printers, and says how each went. See README.md.
//
// This build is driven from the command line; the Windows window and tray
// icon wrap the same commands.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/agent"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/api"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/config"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/journal"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/netpolicy"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/printers"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/secret"
)

// Version is set at build time (-ldflags "-X main.Version=1.0.0").
var Version = "0.1.0-dev"

const usage = `Stockpile Print Helper %s

  stockpile-print-helper pair --server https://YOUR-STOCKPILE CODE
  stockpile-print-helper run
  stockpile-print-helper status
  stockpile-print-helper printers
  stockpile-print-helper find-printers
  stockpile-print-helper add-printer IP[:PORT] [--kind label|receipt|page] [--name NAME]
  stockpile-print-helper remove-printer IP[:PORT]
  stockpile-print-helper pause | resume
  stockpile-print-helper unpair
  stockpile-print-helper version

Every command takes --config DIR (default: this user's AppData folder).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, Version)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dir := fs.String("config", "", "the helper's folder")
	server := fs.String("server", "", "Stockpile's address (pair)")
	kind := fs.String("kind", "", "label, receipt or page (add-printer)")
	name := fs.String("name", "", "a name for it (add-printer)")
	sink := fs.String("file-sink", "", "development builds only: print pages and labels to this folder")
	fs.Parse(reorder(args))
	store, err := config.Open(config.Dir(*dir))
	if err != nil {
		fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch cmd {
	case "version":
		fmt.Println(Version)
	case "pair":
		pair(ctx, store, *server, fs.Arg(0))
	case "run":
		if *sink != "" {
			if !printers.DevSinkBuilt {
				fail(errors.New("--file-sink is only in development builds"))
			}
			store.Update(func(s *config.Settings) { s.SinkDir = *sink })
		}
		run(ctx, store)
	case "status":
		status(store)
	case "printers":
		s, _ := store.Load()
		for _, p := range printers.All(ctx, s) {
			i := p.Info()
			fmt.Printf("%-40s %-8s %-14s %-9s %s\n", i.Name, i.Kind, i.Connection, i.Status, strings.Join(i.Formats, ","))
		}
	case "find-printers":
		fmt.Println("Looking on this computer's own network for 20 seconds…")
		for _, f := range printers.Find(ctx) {
			what := "a printer"
			if f.Zebra {
				what = "a Zebra label printer (" + f.Model + ")"
			}
			fmt.Printf("%s  %s\n", f.Address, what)
		}
		fmt.Println("Nothing is kept until you add it: stockpile-print-helper add-printer ADDRESS")
	case "add-printer":
		addPrinter(ctx, store, fs.Arg(0), *kind, *name)
	case "remove-printer":
		ap, err := netpolicy.ParseTarget(fs.Arg(0))
		if err != nil {
			fail(err)
		}
		store.Update(func(s *config.Settings) {
			kept := s.Network[:0]
			for _, n := range s.Network {
				if n.Address != ap.String() {
					kept = append(kept, n)
				}
			}
			s.Network = kept
		})
		fmt.Println("Removed.")
	case "pause", "resume":
		store.Update(func(s *config.Settings) { s.Paused = cmd == "pause" })
		if cmd == "pause" {
			fmt.Println("Paused: anyone printing here is told it didn't print because this computer is paused.")
		} else {
			fmt.Println("Printing again.")
		}
	case "unpair":
		unpair(ctx, store)
	default:
		fmt.Fprintf(os.Stderr, usage, Version)
		os.Exit(2)
	}
}

// reorder lets flags come after the positional argument ("pair CODE --server …").
func reorder(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		rest = append(rest, args[i])
	}
	return append(flags, rest...)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func osName() string { return runtime.GOOS + "/" + runtime.GOARCH }

func computerName() string {
	if n, err := os.Hostname(); err == nil && n != "" {
		return strings.ToUpper(n)
	}
	return "Shop computer"
}

func pair(ctx context.Context, store *config.Store, server, code string) {
	if server == "" || code == "" {
		fail(errors.New("usage: stockpile-print-helper pair --server https://YOUR-STOCKPILE CODE"))
	}
	c, err := api.New(server, "", Version, nil)
	if err != nil {
		fail(err)
	}
	p, err := c.Pair(ctx, code, computerName(), osName(), Version)
	if err != nil {
		fail(err)
	}
	if err := secret.Save(store.TokenPath(), p.Token); err != nil {
		fail(err)
	}
	store.Update(func(s *config.Settings) {
		s.Server, s.HelperID, s.Computer, s.Shop, s.Location, s.Paused = strings.TrimRight(server, "/"), p.HelperID, computerName(), p.Shop.Name, p.Location.Name, false
	})
	fmt.Printf("Paired with %s · %s. Go back to Stockpile's Settings › Printing to pick which printer does what.\n", p.Shop.Name, p.Location.Name)
}

func client(store *config.Store) (*api.Client, config.Settings) {
	s, err := store.Load()
	if err != nil {
		fail(err)
	}
	token, err := secret.Load(store.TokenPath())
	if errors.Is(err, secret.ErrNone) || s.Server == "" {
		fail(errors.New("not paired: in Stockpile, Settings › Printing › Get a pairing code, then: stockpile-print-helper pair --server … CODE"))
	}
	if err != nil {
		fail(err)
	}
	c, err := api.New(s.Server, token, Version, nil)
	if err != nil {
		fail(err)
	}
	return c, s
}

func run(ctx context.Context, store *config.Store) {
	c, s := client(store)
	logger := openLog(store)
	j, err := journal.Open(store.JournalPath())
	if err != nil {
		fail(err)
	}
	logger.Printf("Stockpile Print Helper %s: printing for %s · %s.", Version, s.Shop, s.Location)
	a := &agent.Agent{Client: c, Store: store, Journal: j, Log: logger, Version: Version, OS: osName()}
	if err := a.Run(ctx); errors.Is(err, api.ErrRevoked) {
		logger.Printf("Removed from %s. It isn't connected and won't print anything. Pair it again to use it.", s.Shop)
		os.Exit(3)
	}
}

func status(store *config.Store) {
	s, _ := store.Load()
	if _, err := secret.Load(store.TokenPath()); err != nil || s.HelperID == "" {
		fmt.Println("Not paired. Until it's paired, this helper doesn't connect to anything.")
		return
	}
	state := "printing"
	if s.Paused {
		state = "paused"
	}
	fmt.Printf("Paired with %s · %s (%s), as %s. %s.\n", s.Shop, s.Location, s.Server, s.Computer, strings.ToUpper(state[:1])+state[1:])
	if j, err := journal.Open(store.JournalPath()); err == nil {
		for _, e := range j.Recent(10) {
			fmt.Printf("  %s  %-40s %-12s %s\n", e.At.Local().Format("Jan 2 3:04 PM"), e.Title, e.State, e.Message)
		}
	}
}

func addPrinter(ctx context.Context, store *config.Store, target, kind, name string) {
	ap, err := netpolicy.ParseTarget(target)
	if err != nil {
		fail(err)
	}
	model, zebra, err := printers.Identify(ctx, ap)
	if err != nil {
		fail(err)
	}
	if kind == "" && zebra {
		kind = "label"
	}
	if kind != "label" && kind != "receipt" && kind != "page" {
		fail(fmt.Errorf("something answered at %s but didn't say it's a label printer. What is it? Add it again with --kind label, receipt or page", ap))
	}
	if name == "" {
		name = strings.TrimSpace(model + " at " + ap.Addr().String())
	}
	n := config.NetworkPrinter{Address: ap.String(), Name: name, Kind: kind, ZPL: kind == "label"}
	store.Update(func(s *config.Settings) {
		for i, x := range s.Network {
			if x.Address == n.Address {
				s.Network[i] = n
				return
			}
		}
		s.Network = append(s.Network, n)
	})
	fmt.Printf("Kept: %s. Stockpile can now offer it.\n", n.Name)
}

func unpair(ctx context.Context, store *config.Store) {
	c, s := client(store)
	if err := c.Unpair(ctx); err != nil && !errors.Is(err, api.ErrRevoked) && !errors.Is(err, api.ErrUnauthorized) {
		fail(err)
	}
	secret.Forget(store.TokenPath())
	store.Update(func(x *config.Settings) { x.HelperID, x.Shop, x.Location = "", "", "" })
	fmt.Printf("Unpaired from %s. To use it again you'll need a new pairing code from Settings › Printing.\n", s.Shop)
}

// openLog writes to the console and a daily file, keeping 7 days. The log
// has job names, printers and results — never document contents.
func openLog(store *config.Store) *log.Logger {
	cut := time.Now().AddDate(0, 0, -7)
	if old, err := filepath.Glob(filepath.Join(store.LogDir(), "*.txt")); err == nil {
		for _, f := range old {
			if st, err := os.Stat(f); err == nil && st.ModTime().Before(cut) {
				os.Remove(f)
			}
		}
	}
	f, err := os.OpenFile(filepath.Join(store.LogDir(), time.Now().Format("2006-01-02")+".txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	var w io.Writer = os.Stdout
	if err == nil {
		w = io.MultiWriter(os.Stdout, f)
	}
	return log.New(w, "", log.LstdFlags)
}
