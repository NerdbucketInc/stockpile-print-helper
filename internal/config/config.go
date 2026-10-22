// Package config is the helper's own folder and settings. Everything it keeps
// lives in one folder (on Windows, inside the user's AppData — in the MSIX
// package's own LocalState, which uninstalling deletes):
//
//	device.bin     the device token, protected (package secret)
//	settings.json  the server, the pairing, paused, the network printers kept
//	journal.jsonl  job ids, titles, printers and results — never contents
//	logs/          7 days of plain-text log
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// NetworkPrinter is a printer someone kept at this computer ("Find printers"
// or "Add by IP address"). Stockpile can only ever pick from these and the
// printers installed in Windows; it can't name an address.
type NetworkPrinter struct {
	Address string `json:"address"` // "192.168.1.50:9100"
	Name    string `json:"name"`
	Kind    string `json:"kind"` // label | receipt | page
	ZPL     bool   `json:"zpl"`
}

// Settings are the helper's choices, saved as JSON.
type Settings struct {
	Server   string           `json:"server,omitempty"`
	HelperID string           `json:"helperId,omitempty"`
	Computer string           `json:"computer,omitempty"`
	Shop     string           `json:"shop,omitempty"`
	Location string           `json:"location,omitempty"`
	Paused   bool             `json:"paused"`
	Network  []NetworkPrinter `json:"network,omitempty"`
	// SinkDir: a development build's file printer (build tag devsink only).
	SinkDir string `json:"sinkDir,omitempty"`
}

// Dir is the helper's folder: --config, else the platform's.
func Dir(override string) string {
	if override != "" {
		return override
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "StockpilePrintHelper")
		}
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "stockpile-print-helper")
	}
	return ".stockpile-print-helper"
}

// Store reads and writes settings.json in a folder.
type Store struct {
	mu  sync.Mutex
	dir string
}

// Open makes the folder (only its owner can read it).
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string         { return s.dir }
func (s *Store) TokenPath() string   { return filepath.Join(s.dir, "device.bin") }
func (s *Store) JournalPath() string { return filepath.Join(s.dir, "journal.jsonl") }
func (s *Store) LogDir() string      { return filepath.Join(s.dir, "logs") }

// Load reads the settings (none yet = zero values).
func (s *Store) Load() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Settings
	b, err := os.ReadFile(filepath.Join(s.dir, "settings.json"))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(b, &out)
}

// Update changes the settings under the lock and saves them.
func (s *Store) Update(change func(*Settings)) (Settings, error) {
	cur, err := s.Load()
	if err != nil {
		return cur, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	change(&cur)
	b, _ := json.MarshalIndent(cur, "", "  ")
	tmp := filepath.Join(s.dir, "settings.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return cur, err
	}
	return cur, os.Rename(tmp, filepath.Join(s.dir, "settings.json"))
}
