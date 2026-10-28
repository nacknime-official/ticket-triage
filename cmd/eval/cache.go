package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/evalset"
)

// cacheKey identifies one classifier answer: the same ticket text sent to the
// same provider and configured model. The text hash makes an edited row miss.
type cacheKey struct {
	Provider   string
	Model      string
	ID         string
	TextSHA256 string
}

func newCacheKey(provider, model string, row evalset.Row) cacheKey {
	sum := sha256.Sum256([]byte(row.Subject + "\x00" + row.Body))
	return cacheKey{Provider: provider, Model: model, ID: row.ID, TextSHA256: hex.EncodeToString(sum[:])}
}

// cacheEntry is one line of the cache file. It holds the answer and the call
// metadata, never the ticket text.
type cacheEntry struct {
	Provider           string                `json:"provider"`
	Model              string                `json:"model"`
	ID                 string                `json:"id"`
	TextSHA256         string                `json:"text_sha256"`
	ComplaintP         float64               `json:"complaint_p"`
	Category           string                `json:"category"`
	CategoryConfidence float64               `json:"category_confidence"`
	Urgency            float64               `json:"urgency"`
	UrgencyConfidence  float64               `json:"urgency_confidence"`
	ServedModel        string                `json:"served_model"`
	HTTPStatus         int                   `json:"http_status"`
	LatencyNS          int64                 `json:"latency_ns"`
	InputTokens        int                   `json:"input_tokens"`
	OutputTokens       int                   `json:"output_tokens"`
	CostUSD            float64               `json:"cost_usd"`
	CostSource         classifier.CostSource `json:"cost_source"`
	RawResponse        jsontext.Value        `json:"raw_response,omitempty"`
}

func (e cacheEntry) key() cacheKey {
	return cacheKey{Provider: e.Provider, Model: e.Model, ID: e.ID, TextSHA256: e.TextSHA256}
}

func newCacheEntry(k cacheKey, r classifier.Result) cacheEntry {
	e := cacheEntry{
		Provider:           k.Provider,
		Model:              k.Model,
		ID:                 k.ID,
		TextSHA256:         k.TextSHA256,
		ComplaintP:         r.ComplaintP,
		Category:           r.Category,
		CategoryConfidence: r.CategoryConfidence,
		Urgency:            r.Urgency,
		UrgencyConfidence:  r.UrgencyConfidence,
		ServedModel:        r.Call.Model,
		HTTPStatus:         r.Call.HTTPStatus,
		LatencyNS:          r.Call.Latency.Nanoseconds(),
		InputTokens:        r.Call.InputTokens,
		OutputTokens:       r.Call.OutputTokens,
		CostUSD:            r.Call.CostUSD,
		CostSource:         r.Call.CostSource,
	}
	if raw := jsontext.Value(r.Call.RawResponse); raw.IsValid() {
		e.RawResponse = raw
	}
	return e
}

func (e cacheEntry) result() classifier.Result {
	return classifier.Result{
		ComplaintP:         e.ComplaintP,
		Category:           e.Category,
		CategoryConfidence: e.CategoryConfidence,
		Urgency:            e.Urgency,
		UrgencyConfidence:  e.UrgencyConfidence,
		Call: classifier.Call{
			Provider:     e.Provider,
			Model:        e.ServedModel,
			HTTPStatus:   e.HTTPStatus,
			Latency:      time.Duration(e.LatencyNS),
			InputTokens:  e.InputTokens,
			OutputTokens: e.OutputTokens,
			CostUSD:      e.CostUSD,
			CostSource:   e.CostSource,
			RawResponse:  e.RawResponse,
		},
	}
}

// cache holds successful classifier answers in a JSONL file, one entry per
// line, appended as each answer arrives so an interrupted run keeps what it
// paid for. A later line for the same key wins. It is safe for concurrent use.
type cache struct {
	mu      sync.Mutex
	f       *os.File
	entries map[cacheKey]cacheEntry
	// Skipped counts lines that did not decode, such as a line cut short by a
	// crash; they are ignored.
	Skipped int
}

// openCache loads the cache file at path, creating it and its directory if
// needed, and keeps it open for appending.
func openCache(path string) (*cache, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	c := &cache{f: f, entries: make(map[cacheKey]cacheEntry)}
	if err := c.load(f); err != nil {
		return nil, errors.Join(fmt.Errorf("cache: read %s: %w", path, err), f.Close())
	}
	return c, nil
}

func (c *cache) load(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e cacheEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			c.Skipped++
			continue
		}
		c.entries[e.key()] = e
	}
	return sc.Err()
}

func (c *cache) get(k cacheKey) (classifier.Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	return e.result(), ok
}

// put stores r under k and appends it to the file.
func (c *cache) put(k cacheKey, r classifier.Result) error {
	e := newCacheEntry(k, r)
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("cache: encode %s: %w", k.ID, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("cache: write %s: %w", k.ID, err)
	}
	c.entries[k] = e
	return nil
}

func (c *cache) Close() error {
	return c.f.Close()
}
