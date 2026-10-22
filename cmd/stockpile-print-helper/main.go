// Command stockpile-print-helper is the Stockpile Print Helper: it connects
// out to Stockpile, takes print jobs for its one shop and location, prints
// them on this computer's printers, and says how each went. See README.md.
//
// With no command (how Windows starts it at sign-in, and the Start menu) it
// shows its window when not paired, and otherwise sits by the clock. The
// commands below do the same steps from a command line.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/nerdbucketinc/stockpile-print-helper/internal/api"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/app"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/config"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/journal"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/netpolicy"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/printers"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/secret"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/ui"
)

// Set at build time: -ldflags "-X main.Version=1.0.0 -X main.Server=https://helper.example".
var (
	Version = "0.1.0-dev"
	// Server is the Stockpile the window pairs with.
	Server = ""
)

const usage = `Stockpile Print Helper %s

  stockpile-print-helper                 the window and tray icon (Windows)
  stockpile-print-helper pair [--server https://YOUR-STOCKPILE] CODE
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
	cmd, args := "window", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprintf(os.Stderr, usage, Version) }
	dir := fs.String("config", "", "the helper's folder")
	server := fs.String("server", "", "Stockpile's address (pair; the window: a development server)")
	kind := fs.String("kind", "", "label, receipt or page (add-printer)")
	name := fs.String("name", "", "a name for it (add-printer)")
	sink := fs.String("file-sink", "", "development builds only: print pages and labels to this folder")
	show := fs.Bool("show", false, "the window: show it even when paired")
	selftest := fs.String("ui-selftest", "", "the window: list what it shows to this file (or -), then close")
	fs.Parse(reorder(args))
	store, err := config.Open(config.Dir(*dir))
	if err != nil {
		fail(err)
	}
	if *sink != "" {
		if !printers.DevSinkBuilt {
			fail(errors.New("--file-sink is only in development builds"))
		}
		store.Update(func(s *config.Settings) { s.SinkDir = *sink })
	}
	a := &app.App{Store: store, Version: Version, Server: Server}
	if *server != "" {
		a.Server = *server
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch cmd {
	case "window":
		a.Log = app.OpenLog(store, nil)
		if err := ui.Run(a, *show, *selftest); err != nil {
			fail(err)
		}
	case "version":
		fmt.Println(Version)
	case "pair":
		a.Log = app.OpenLog(store, os.Stdout)
		p, err := a.Pair(ctx, *server, fs.Arg(0))
		if err != nil {
			fail(err)
		}
		fmt.Printf("Paired with %s · %s. Go back to Stockpile's Settings › Printing to pick which printer does what.\n", p.Shop.Name, p.Location.Name)
	case "run":
		a.Log = app.OpenLog(store, os.Stdout)
		if v := a.View(ctx); !v.Paired {
			fail(errors.New("not paired: in Stockpile, Settings › Printing › Get a pairing code, then: stockpile-print-helper pair --server … CODE"))
		}
		a.Start()
		select {
		case <-ctx.Done():
			a.Stop()
		case <-a.Done():
		}
		if _, err := secret.Load(store.TokenPath()); errors.Is(err, secret.ErrNone) {
			os.Exit(3)
		}
	case "status":
		status(store)
	case "printers":
		s, _ := store.Load()
		for _, p := range printers.All(ctx, s) {
			i := p.Info()
			fmt.Printf("%-40s %-8s %-15s %-9s %s\n", i.Name, i.Kind, i.Connection, i.Status, strings.Join(i.Formats, ","))
		}
	case "find-printers":
		fmt.Println("Looking on this computer's own network for 20 seconds…")
		for _, f := range a.Find(ctx) {
			what := "a printer"
			if f.Zebra {
				what = "a Zebra label printer (" + f.Model + ")"
			}
			fmt.Printf("%s  %s\n", f.Address, what)
		}
		fmt.Println("Nothing is kept until you add it: stockpile-print-helper add-printer ADDRESS")
	case "add-printer":
		n, err := a.Keep(ctx, fs.Arg(0), *kind, *name)
		if err != nil {
			fail(fmt.Errorf("%v (add it again with --kind label, receipt or page)", err))
		}
		fmt.Printf("Kept: %s. Stockpile can now offer it.\n", n.Name)
	case "remove-printer":
		ap, err := netpolicy.ParseTarget(fs.Arg(0))
		if err != nil {
			fail(err)
		}
		a.Forget(ap.String())
		fmt.Println("Removed.")
	case "pause", "resume":
		a.SetPaused(cmd == "pause")
		if cmd == "pause" {
			fmt.Println("Paused: anyone printing here is told it didn't print because this computer is paused.")
		} else {
			fmt.Println("Printing again.")
		}
	case "unpair":
		s, _ := store.Load()
		a.Log = app.OpenLog(store, nil)
		if err := a.Unpair(ctx); err != nil && !errors.Is(err, api.ErrRevoked) {
			fail(err)
		}
		fmt.Printf("Unpaired from %s. To use it again you'll need a new pairing code from Settings › Printing.\n", s.Shop)
	default:
		fs.Usage()
		os.Exit(2)
	}
}

// reorder lets flags come after the positional argument ("pair CODE --server …").
func reorder(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if !strings.Contains(args[i], "=") && args[i] != "--show" && args[i] != "-show" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
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

func status(store *config.Store) {
	s, _ := store.Load()
	if _, err := secret.Load(store.TokenPath()); err != nil || s.HelperID == "" {
		fmt.Println("Not paired. Until it's paired, this helper doesn't connect to anything.")
		return
	}
	state := "Printing"
	if s.Paused {
		state = "Paused"
	}
	fmt.Printf("Paired with %s · %s (%s), as %s. %s.\n", s.Shop, s.Location, s.Server, s.Computer, state)
	if j, err := journal.Open(store.JournalPath()); err == nil {
		for _, e := range j.Recent(10) {
			fmt.Printf("  %s  %-40s %-12s %s\n", e.At.Local().Format("Jan 2 3:04 PM"), e.Title, e.State, e.Message)
		}
	}
}
