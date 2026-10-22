package journal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCrashIsToldHonestly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	j.Mark(Entry{JobID: "a", Title: "Receipt — sale #1", State: Taking})
	j.Mark(Entry{JobID: "b", Title: "Label", State: Received})
	j.Mark(Entry{JobID: "c", Title: "Slip", State: Printing})
	j.Mark(Entry{JobID: "d", Title: "Done", State: Printed})
	j.Ack("d")
	// Power cut. Next start:
	j2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range j2.Unacked() {
		got[e.JobID] = e.State
	}
	want := map[string]string{"a": NotReceived, "b": NotReceived, "c": Uncertain}
	if len(got) != len(want) {
		t.Fatalf("unacked = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %s, want %s", k, got[k], v)
		}
	}
	if e, _ := j2.Get("a"); e.Title != "Receipt — sale #1" {
		t.Errorf("title lost: %+v", e)
	}
}

func TestNoContentsAndOldEntriesGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, _ := Open(path)
	j.Mark(Entry{JobID: "x", Title: "Label", State: Printed})
	j.now = func() time.Time { return time.Now().Add(-8 * 24 * time.Hour) }
	j.Mark(Entry{JobID: "old", Title: "Old", State: Printed})
	j2, _ := Open(path)
	if _, ok := j2.Get("old"); ok {
		t.Error("an entry older than 7 days was kept")
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "^XA") || strings.Contains(string(b), "png") {
		t.Errorf("the journal holds document contents: %s", b)
	}
	if n := len(j2.Recent(10)); n != 1 {
		t.Errorf("recent = %d, want 1", n)
	}
}
