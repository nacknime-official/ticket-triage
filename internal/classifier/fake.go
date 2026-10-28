package classifier

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"
	"sync"
)

// Fake call metadata.
const (
	FakeProvider = "fake"
	FakeModel    = "fake-keywords-1"
)

// Fake is a deterministic keyword classifier over the default taxonomy, for
// tests and local runs without a key. It knows English and Ukrainian keywords.
// The same Input always gives the same Result. The zero value is ready to use.
type Fake struct {
	// Errs scripts failures: call n returns Errs[n] while n < len(Errs), and a
	// nil entry classifies normally. Later calls always classify. Set it
	// before the first call.
	Errs []error

	mu    sync.Mutex
	calls int
}

var _ Classifier = (*Fake)(nil)

// Calls returns how many times Classify was called.
func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Classify implements Classifier. A done ctx gives an ErrTransient, as a
// timeout of a real call does.
func (f *Fake) Classify(ctx context.Context, in Input) (Result, error) {
	f.mu.Lock()
	n := f.calls
	f.calls++
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return Result{Call: Call{Provider: FakeProvider}}, fmt.Errorf("%w: %w", ErrTransient, err)
	}
	if n < len(f.Errs) && f.Errs[n] != nil {
		return Result{Call: Call{Provider: FakeProvider}}, f.Errs[n]
	}
	return classifyKeywords(in)
}

// Keywords are matched as substrings of the lower-cased subject and body, so
// Ukrainian entries are stems that cover the inflected forms.
var (
	categoryKeywords = []struct {
		category string
		words    []string
	}{
		{CategoryBilling, []string{
			"invoice", "refund", "charge", "payment", "billing", "subscription", "pricing", "price", "receipt",
			"оплат", "платіж", "платеж", "рахун", "списа", "гроші", "грош", "підписк", "тариф", "ціна", "ціни",
			"повернення коштів", "грн",
		}},
		{CategoryTechnical, []string{
			"error", "bug", "crash", "outage", "not working", "doesn't work", "does not work", "broken",
			"timeout", "slow", "api", "500", "freez",
			"помилк", "баг", "не працю", "збій", "вилітає", "гальму", "повільн", "лежить", "зависа",
		}},
		{CategoryAccount, []string{
			"login", "log in", "password", "sign in", "account", "profile", "2fa", "locked out", "access",
			"пароль", "логін", "увійти", "вхід", "акаунт", "обліков", "профіл", "доступ", "заблокува",
		}},
		{CategoryFeatureRequest, []string{
			"feature", "would be nice", "please add", "could you add", "suggest", "wish", "dark mode",
			"додайте", "додати", "пропоную", "було б добре", "було б зручно", "функці", "хотілось би", "хотілося б",
		}},
	}
	urgencyKeywords = []struct {
		level float64
		words []string
	}{
		// Checked from the highest level down; the first level with a hit wins.
		{3, []string{
			"outage", "security", "hacked", "breach", "data loss", "lost all", "lawsuit", "lawyer", "legal", "court",
			"суд", "злам", "юрист", "адвокат", "витік", "втратили дані", "втрачені дані", "безпек",
		}},
		{2, []string{
			"urgent", "asap", "immediately", "blocked", "cannot", "can't", "unable", "charged twice", "double charge",
			"терміново", "негайно", "не можу", "двічі", "блокує",
		}},
		{0, []string{
			"no rush", "whenever", "just wondering", "curious", "would be nice", "suggest",
			"не терміново", "коли буде час", "цікаво", "пропоную", "було б добре",
		}},
	}
	complaintKeywords = []string{
		"unacceptable", "terrible", "awful", "worst", "angry", "disappointed", "frustrat", "ridiculous",
		"complain", "again", "scam", "!!!",
		"скарг", "жахлив", "обурен", "неприпустим", "знову", "розчарован", "кошмар", "ганьба", "вдруге", "шахра",
	}
)

// Fake confidences. Ties between categories give low category confidence;
// urgency without a keyword falls back to normal with moderate confidence.
const (
	fakeOtherConfidence      = 0.7
	fakeUrgencyHitConfidence = 0.8
	fakeUrgencyDefault       = 1
	fakeUrgencyDefaultConf   = 0.6
)

// fakeAnswer is the raw response of the fake; it holds no ticket text.
type fakeAnswer struct {
	ComplaintP         float64        `json:"complaint_p"`
	ComplaintHits      int            `json:"complaint_hits"`
	Category           string         `json:"category"`
	CategoryConfidence float64        `json:"category_confidence"`
	CategoryHits       map[string]int `json:"category_hits"`
	Urgency            float64        `json:"urgency"`
	UrgencyConfidence  float64        `json:"urgency_confidence"`
}

func classifyKeywords(in Input) (Result, error) {
	text := strings.ToLower(in.Subject + "\n" + in.Body)

	a := fakeAnswer{
		Category:           CategoryOther,
		CategoryConfidence: fakeOtherConfidence,
		CategoryHits:       make(map[string]int),
		Urgency:            fakeUrgencyDefault,
		UrgencyConfidence:  fakeUrgencyDefaultConf,
	}

	best, total := 0, 0
	for _, c := range categoryKeywords {
		n := countHits(text, c.words)
		a.CategoryHits[c.category] = n
		total += n
		// Strictly greater, so a tie goes to the earlier category.
		if n > best {
			best, a.Category = n, c.category
		}
	}
	if best > 0 {
		a.CategoryConfidence = float64(best) / float64(total)
	}

	for _, u := range urgencyKeywords {
		if countHits(text, u.words) > 0 {
			a.Urgency, a.UrgencyConfidence = u.level, fakeUrgencyHitConfidence
			break
		}
	}

	a.ComplaintHits = countHits(text, complaintKeywords)
	switch a.ComplaintHits {
	case 0:
		a.ComplaintP = 0.1
	case 1:
		a.ComplaintP = 0.5
	default:
		a.ComplaintP = 0.9
	}

	raw, err := json.Marshal(a, json.Deterministic(true))
	if err != nil {
		return Result{}, fmt.Errorf("classifier: fake: encode raw response: %w", err)
	}
	return Result{
		ComplaintP:         a.ComplaintP,
		Category:           a.Category,
		CategoryConfidence: a.CategoryConfidence,
		Urgency:            a.Urgency,
		UrgencyConfidence:  a.UrgencyConfidence,
		Call: Call{
			Provider:    FakeProvider,
			Model:       FakeModel,
			CostSource:  CostEstimated,
			RawResponse: raw,
		},
	}, nil
}

// countHits counts the distinct words that occur in text.
func countHits(text string, words []string) int {
	n := 0
	for _, w := range words {
		if strings.Contains(text, w) {
			n++
		}
	}
	return n
}
