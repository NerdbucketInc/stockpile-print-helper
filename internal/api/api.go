// Package api is the helper's whole conversation with Stockpile: six HTTPS
// requests under /api/helper/v1, all made by the helper (it never listens).
//
//	POST pair            the owner's one-time code → this helper's device token
//	POST hello           its report: printers, paused, version, outcomes not yet told
//	GET  jobs/next       the long poll: the next job for it, or nothing
//	POST jobs/:id/take   the document, once
//	POST jobs/:id/result how it went
//	POST unpair          stop, for good
//
// There is nothing else: no request asks the helper to run, read, scan or
// change anything.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxDocument is the largest document taken (Stockpile's own cap).
const MaxDocument = 10 << 20

// ErrRevoked: Stockpile removed this helper (or it unpaired). It must stop
// and forget its token.
var ErrRevoked = errors.New("this print helper was removed in Stockpile")

// ErrUnauthorized: the token isn't known.
var ErrUnauthorized = errors.New("this print helper isn't paired")

// ErrGone: the job isn't this helper's to take any more.
var ErrGone = errors.New("that job isn't waiting for this helper any more")

// ErrBadCode: a pairing code that didn't work (unknown, used or expired).
var ErrBadCode = errors.New("that code didn't work. Codes work once and expire after 10 minutes. Get a new one in Stockpile: Settings › Printing › Get a pairing code")

// ErrRateLimited: too many pairing tries.
var ErrRateLimited = errors.New("too many tries. Wait a minute and try again")

// Client talks to one Stockpile server.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
	agent string
}

// New checks the server's address: https, or http only to this computer
// (a developer's local Stockpile).
func New(server, token, version string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("the server address isn't a URL")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, fmt.Errorf("the server must be https")
	}
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{base: u, token: token, http: hc, agent: "StockpilePrintHelper/" + version}, nil
}

// WithToken is the same client with a device token.
func (c *Client) WithToken(token string) *Client {
	n := *c
	n.token = token
	return &n
}

func (c *Client) url(path string) string { return c.base.String() + "/api/helper/v1" + path }

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.agent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&e)
		res.Body.Close()
		if e.Error == "revoked" {
			return nil, ErrRevoked
		}
		return nil, ErrUnauthorized
	}
	return res, nil
}

func decode(res *http.Response, out any) error {
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("Stockpile answered %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}

// Paired is what pairing hands back. The token is shown once.
type Paired struct {
	HelperID string `json:"helperId"`
	Token    string `json:"token"`
	Shop     Name   `json:"shop"`
	Location Name   `json:"location"`
}

// Name is a {name} object.
type Name struct {
	Name string `json:"name"`
}

// Pair uses an owner's pairing code.
func (c *Client) Pair(ctx context.Context, code, computer, os, version string) (*Paired, error) {
	res, err := c.do(ctx, http.MethodPost, "/pair", map[string]string{"code": code, "computer": computer, "os": os, "version": version})
	if err != nil {
		return nil, err
	}
	switch res.StatusCode {
	case http.StatusBadRequest:
		res.Body.Close()
		return nil, ErrBadCode
	case http.StatusTooManyRequests:
		res.Body.Close()
		return nil, ErrRateLimited
	}
	var p Paired
	if err := decode(res, &p); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(p.Token, "sph_") {
		return nil, fmt.Errorf("Stockpile's answer had no token")
	}
	return &p, nil
}

// Printer is one printer as the helper reports it.
type Printer struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Connection string   `json:"connection"`
	Kind       string   `json:"kind"`
	Formats    []string `json:"formats"`
	DPI        int      `json:"dpi,omitempty"`
	Papers     []string `json:"papers"`
	Status     string   `json:"status"`
}

// Outcome is one job's result, told in a hello's journal.
type Outcome struct {
	JobID   string `json:"jobId"`
	State   string `json:"state"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// Hello is the helper's report.
type Hello struct {
	Version  string    `json:"version"`
	OS       string    `json:"os"`
	Paused   bool      `json:"paused"`
	Printers []Printer `json:"printers"`
	Journal  []Outcome `json:"journal"`
}

// HelloReply names the shop and location (for the window).
type HelloReply struct {
	Shop        Name `json:"shop"`
	Location    Name `json:"location"`
	PollWaitSec int  `json:"pollWaitSec"`
}

// Hello sends the report.
func (c *Client) Hello(ctx context.Context, h Hello) (*HelloReply, error) {
	res, err := c.do(ctx, http.MethodPost, "/hello", h)
	if err != nil {
		return nil, err
	}
	var r HelloReply
	return &r, decode(res, &r)
}

// Job is an offer: what to print, on which of this helper's printers.
type Job struct {
	ID        string `json:"id"`
	PrinterID string `json:"printerId"`
	Format    string `json:"format"` // ZPL | PNG
	Title     string `json:"title"`
	Copies    int    `json:"copies"`
	Paper     string `json:"paper"`
	Bytes     int    `json:"bytes"`
	SHA256    string `json:"sha256"`
	TakeBy    string `json:"takeBy"`
}

// Next waits up to waitSec for a job. Nil, nil = nothing yet.
func (c *Client) Next(ctx context.Context, waitSec int) (*Job, error) {
	res, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/jobs/next?wait=%d", waitSec), nil)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusNoContent {
		res.Body.Close()
		return nil, nil
	}
	var r struct {
		Job *Job `json:"job"`
	}
	if err := decode(res, &r); err != nil {
		return nil, err
	}
	return r.Job, nil
}

// Take fetches the document, once, and checks it is what the offer said.
func (c *Client) Take(ctx context.Context, job *Job) ([]byte, string, error) {
	res, err := c.do(ctx, http.MethodPost, "/jobs/"+url.PathEscape(job.ID)+"/take", nil)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusConflict {
		return nil, "", ErrGone
	}
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("Stockpile answered %d to take", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxDocument+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > MaxDocument {
		return nil, "", fmt.Errorf("the document is bigger than %d bytes", MaxDocument)
	}
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if (job.SHA256 != "" && got != job.SHA256) || (res.Header.Get("X-Stockpile-Sha256") != "" && got != res.Header.Get("X-Stockpile-Sha256")) {
		return nil, "", fmt.Errorf("the document didn't arrive whole")
	}
	return body, res.Header.Get("Content-Type"), nil
}

// Result tells Stockpile how a job went.
func (c *Client) Result(ctx context.Context, o Outcome) error {
	body := struct {
		State   string `json:"state"`
		Code    string `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
	}{o.State, o.Code, o.Message}
	res, err := c.do(ctx, http.MethodPost, "/jobs/"+url.PathEscape(o.JobID)+"/result", body)
	if err != nil {
		return err
	}
	return decode(res, nil)
}

// Unpair cuts this helper off, for good.
func (c *Client) Unpair(ctx context.Context) error {
	res, err := c.do(ctx, http.MethodPost, "/unpair", nil)
	if err != nil {
		return err
	}
	return decode(res, nil)
}
