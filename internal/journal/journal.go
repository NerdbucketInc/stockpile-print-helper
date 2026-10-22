// Package journal is the helper's crash-safe record of each job: its id, title,
// printer and how far it got — never its contents. Every step is flushed to
// disk before the next, so after a crash or power cut the helper can tell
// Stockpile honestly what happened:
//
//	taking / received  → nothing reached a printer: "not_received"
//	printing           → bytes may have reached it: "uncertain"
//	sent/printed/failed/uncertain/not_received → as is
//
// An outcome stays "unacknowledged" until Stockpile has taken it (a result
// call or a hello that carried it). Entries older than 7 days are dropped.
package journal

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// States a job passes through on this computer.
const (
	Taking      = "taking"
	Received    = "received"
	Printing    = "printing"
	Sent        = "sent"
	Printed     = "printed"
	Failed      = "failed"
	Uncertain   = "uncertain"
	NotReceived = "not_received"
)

// Keep is how long entries are kept.
const Keep = 7 * 24 * time.Hour

// Entry is one job's latest state.
type Entry struct {
	JobID   string    `json:"jobId"`
	Title   string    `json:"title,omitempty"`
	Printer string    `json:"printer,omitempty"`
	State   string    `json:"state"`
	Code    string    `json:"code,omitempty"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
	// Acked: Stockpile has this outcome.
	Acked bool `json:"acked,omitempty"`
}

// Final reports whether a state is an outcome (not a step on the way).
func Final(state string) bool {
	switch state {
	case Sent, Printed, Failed, Uncertain, NotReceived:
		return true
	}
	return false
}

// Journal is the file and its latest entry per job.
type Journal struct {
	mu      sync.Mutex
	path    string
	entries map[string]Entry
	now     func() time.Time
}

// Open reads the journal (a missing file is an empty one), settles any job a
// crash interrupted, and rewrites it without old entries.
func Open(path string) (*Journal, error) {
	j := &Journal{path: path, entries: map[string]Entry{}, now: time.Now}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.JobID != "" {
				j.entries[e.JobID] = e
			}
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	cut := j.now().Add(-Keep)
	for id, e := range j.entries {
		switch {
		case e.At.Before(cut):
			delete(j.entries, id)
		case e.State == Taking || e.State == Received:
			e.State, e.Code, e.Message, e.Acked = NotReceived, "", "The helper stopped before printing it", false
			j.entries[id] = e
		case e.State == Printing:
			e.State, e.Code, e.Message, e.Acked = Uncertain, "", "The helper stopped while printing it", false
			j.entries[id] = e
		}
	}
	return j, j.rewrite()
}

func (j *Journal) rewrite() error {
	tmp := j.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range j.sorted() {
		b, _ := json.Marshal(e)
		w.Write(append(b, '\n'))
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, j.path)
}

func (j *Journal) sorted() []Entry {
	out := make([]Entry, 0, len(j.entries))
	for _, e := range j.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].At.Before(out[b].At) })
	return out
}

// Mark records a job's new state, flushed to disk before it returns.
func (j *Journal) Mark(e Entry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if old, ok := j.entries[e.JobID]; ok {
		if e.Title == "" {
			e.Title = old.Title
		}
		if e.Printer == "" {
			e.Printer = old.Printer
		}
	}
	e.At = j.now()
	j.entries[e.JobID] = e
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// Ack notes that Stockpile has a job's outcome.
func (j *Journal) Ack(jobID string) error {
	j.mu.Lock()
	e, ok := j.entries[jobID]
	j.mu.Unlock()
	if !ok || e.Acked || !Final(e.State) {
		return nil
	}
	e.Acked = true
	return j.Mark(e)
}

// Unacked lists the outcomes Stockpile hasn't taken yet (at most 200).
func (j *Journal) Unacked() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []Entry
	for _, e := range j.sorted() {
		if Final(e.State) && !e.Acked {
			out = append(out, e)
		}
	}
	if len(out) > 200 {
		out = out[len(out)-200:]
	}
	return out
}

// Get returns a job's entry.
func (j *Journal) Get(jobID string) (Entry, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e, ok := j.entries[jobID]
	return e, ok
}

// Recent lists the newest n entries, newest first (the window's list).
func (j *Journal) Recent(n int) []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	all := j.sorted()
	out := make([]Entry, 0, n)
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, all[i])
	}
	return out
}
