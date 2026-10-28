// Command eval-import downloads the Bitext customer support dataset at a pinned revision into the git-ignored
// raw directory, samples rows deterministically, drafts labels for them, and writes them as eval rows.
//
// Run it from the repository root: go run ./cmd/eval-import. See testdata/eval/README.md.
package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nacknime-official/ticket-triage/internal/evalset"
)

const (
	defaultURL = "https://huggingface.co/datasets/bitext/Bitext-customer-support-llm-chatbot-training-dataset/" +
		"resolve/430d1a89bd93bd1fa23c16f29dd53e73f0087443/" +
		"Bitext_Sample_Customer_Support_Training_Dataset_27K_responses-v11.csv"
	defaultSHA256 = "6f81102b0100b97b8468eb04368033a23206bf1fde9d53500d5806ec1001a434"
	defaultSeed   = "t22"

	maxSubjectLength = 60
	downloadTimeout  = 5 * time.Minute
)

// intentRule drafts the labels for a Bitext intent and says how many of its rows to keep.
type intentRule struct {
	category string
	urgency  int
	rows     int
}

// rules lists the Bitext intents kept in the eval set. testdata/eval/README.md documents the same table.
var rules = map[string]intentRule{
	"create_account":           {"account", 0, 2},
	"delete_account":           {"account", 1, 2},
	"edit_account":             {"account", 1, 2},
	"recover_password":         {"account", 1, 3},
	"check_payment_methods":    {"billing", 0, 1},
	"check_refund_policy":      {"billing", 0, 1},
	"check_cancellation_fee":   {"billing", 0, 1},
	"check_invoice":            {"billing", 1, 1},
	"get_invoice":              {"billing", 1, 2},
	"get_refund":               {"billing", 1, 2},
	"track_refund":             {"billing", 1, 1},
	"payment_issue":            {"billing", 2, 3},
	"contact_customer_service": {"other", 0, 1},
	"delivery_period":          {"other", 0, 1},
	"newsletter_subscription":  {"other", 0, 1},
	"review":                   {"other", 0, 1},
	"contact_human_agent":      {"other", 1, 1},
	"track_order":              {"other", 1, 1},
	"cancel_order":             {"other", 1, 1},
	"complaint":                {"other", 1, 2},
}

// placeholders maps the Bitext {{...}} slots that occur in instructions to fixed invented values.
var placeholders = map[string]string{
	"Account Category": "Business",
	"Account Type":     "Premium",
	"Currency Symbol":  "$",
	"Delivery City":    "Denver",
	"Delivery Country": "Canada",
	"Invoice Number":   "INV-20931",
	"Order Number":     "#48213",
	"Person Name":      "Alex Morgan",
	"Refund Amount":    "$120",
}

var placeholderRE = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "eval-import:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("eval-import", flag.ContinueOnError)
	url := fs.String("url", defaultURL, "dataset CSV URL, pinned to a revision")
	sum := fs.String("sha256", defaultSHA256, "expected SHA-256 of the CSV")
	rawDir := fs.String("raw-dir", "testdata/eval/raw", "git-ignored directory for the downloaded CSV")
	out := fs.String("out", "testdata/eval/bitext.jsonl", "output JSONL file")
	seed := fs.String("seed", defaultSeed, "sampling seed; the same seed and CSV always give the same rows")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rawPath := filepath.Join(*rawDir, "bitext.csv")
	client := &http.Client{Timeout: downloadTimeout}
	if err := fetch(ctx, client, *url, *sum, rawPath); err != nil {
		return err
	}

	f, err := os.Open(rawPath)
	if err != nil {
		return err
	}
	defer f.Close()
	rows, err := buildRows(f, rules, *seed)
	if err != nil {
		return fmt.Errorf("%s: %w", rawPath, err)
	}

	var buf bytes.Buffer
	if err := evalset.Write(&buf, rows); err != nil {
		return err
	}
	if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote %d rows to %s\n", len(rows), *out)
	return err
}

// fetch downloads url to path unless path already holds a file with the wanted SHA-256. The download goes to
// a temporary file that is renamed only after its checksum matches.
func fetch(ctx context.Context, client *http.Client, url, wantSHA256, path string) error {
	if got, err := fileSHA256(path); err == nil && got == wantSHA256 {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename

	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA256 {
		return fmt.Errorf("download %s: SHA-256 is %s, want %s", url, got, wantSHA256)
	}
	return os.Rename(tmp.Name(), path)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// candidate is one Bitext row of a kept intent, with its sampling key.
type candidate struct {
	key         string
	flags       string
	instruction string
}

// buildRows reads the Bitext CSV and returns the sampled eval rows, ordered by intent and sampling key. For
// each intent in rules it keeps the distinct instructions with the smallest sha256(seed + "\x00" +
// instruction), which does not depend on the file order or the Go version.
func buildRows(r io.Reader, rules map[string]intentRule, seed string) ([]evalset.Row, error) {
	cr := csv.NewReader(r)
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if want := []string{"flags", "instruction", "category", "intent", "response"}; !slices.Equal(header, want) {
		return nil, fmt.Errorf("header is %v, want %v", header, want)
	}

	byIntent := make(map[string][]candidate)
	seen := make(map[string]bool)
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		flags, instruction, intent := rec[0], strings.TrimSpace(rec[1]), rec[3]
		if _, ok := rules[intent]; !ok || seen[instruction] {
			continue
		}
		seen[instruction] = true
		key := sha256.Sum256([]byte(seed + "\x00" + instruction))
		byIntent[intent] = append(byIntent[intent], candidate{hex.EncodeToString(key[:]), flags, instruction})
	}

	intents := make([]string, 0, len(rules))
	for intent := range rules {
		intents = append(intents, intent)
	}
	slices.Sort(intents)

	var rows []evalset.Row
	for _, intent := range intents {
		rule, cands := rules[intent], byIntent[intent]
		if len(cands) < rule.rows {
			return nil, fmt.Errorf("intent %s has %d distinct rows, want %d", intent, len(cands), rule.rows)
		}
		slices.SortFunc(cands, func(a, b candidate) int { return cmp.Compare(a.key, b.key) })
		for _, c := range cands[:rule.rows] {
			row, err := toRow(c, intent, rule)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func toRow(c candidate, intent string, rule intentRule) (evalset.Row, error) {
	var unknown []string
	body := placeholderRE.ReplaceAllStringFunc(c.instruction, func(m string) string {
		name := placeholderRE.FindStringSubmatch(m)[1]
		v, ok := placeholders[name]
		if !ok {
			unknown = append(unknown, name)
		}
		return v
	})
	if len(unknown) > 0 {
		return evalset.Row{}, fmt.Errorf("instruction %q: no value for placeholders %v", c.instruction, unknown)
	}

	id := sha256.Sum256([]byte(c.instruction))
	return evalset.Row{
		ID:               "bitext-" + hex.EncodeToString(id[:6]),
		Source:           evalset.SourceBitext,
		Lang:             "en",
		Subject:          subjectOf(body),
		Body:             body,
		ExpectedCategory: rule.category,
		// Bitext tags offensive language with W; profanity aimed at the company counts as a complaint.
		ExpectedComplaint: intent == "complaint" || strings.Contains(c.flags, "W"),
		ExpectedUrgency:   rule.urgency,
	}, nil
}

// subjectOf returns body cut at a word boundary to at most maxSubjectLength characters.
func subjectOf(body string) string {
	if utf8.RuneCountInString(body) <= maxSubjectLength {
		return body
	}
	runes := []rune(body)[:maxSubjectLength]
	if i := strings.LastIndexByte(string(runes), ' '); i > 0 {
		return strings.TrimRight(string(runes)[:i], " ,.;:!?") + "…"
	}
	return string(runes) + "…"
}
