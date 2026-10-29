# Eval report

> **Fake classifier: a pipeline check, not a quality number.** This report was produced with
> the deterministic keyword fake (`internal/classifier/fake.go`), not with Jev. It shows that the eval
> pipeline runs end to end; its accuracy says nothing about Jev. The live Jev report is
> `docs/eval.md`.

> **Labels are unreviewed drafts.** An agent drafted every label and no human has reviewed them yet
> (`testdata/eval/README.md`), so every accuracy number here is provisional.

## Run

| Setting | Value |
|---|---|
| Classifier | `fake` (provider `fake`, model `fake-keywords-1`) |
| Date | — |
| Served model | `fake-keywords-1` |
| Eval set | `bitext.jsonl` (30 rows, sha256 `848d196e9fec`), `generated.jsonl` (70 rows, sha256 `420bcf4501ea`) |
| Rows | 100 (en 50, uk 50) |
| Answered | 100; failed 0 |
| From cache | 0 |
| Thresholds | CategoryMinConfidence 0.60, UrgencyMinConfidence 0.50, ComplaintUncertainBand 0.35–0.65, RouteOtherToHuman true |
| Complaint decision | P(yes) ≥ 0.50 counts as a complaint |

## Accuracy

Share of answered rows whose answer matches the label. Priority is the urgency score rounded to
the nearest level; "within one level" also counts an answer one level off.

| Question | all | en | uk |
|---|---|---|---|
| Category | 72.0% (72/100) | 78.0% (39/50) | 66.0% (33/50) |
| Complaint | 89.0% (89/100) | 88.0% (44/50) | 90.0% (45/50) |
| Priority | 50.0% (50/100) | 54.0% (27/50) | 46.0% (23/50) |
| Priority, within one level | 92.0% (92/100) | 92.0% (46/50) | 92.0% (46/50) |

### By hard-case tag

A row can carry several tags.

| Tag | Rows | Category | Complaint | Priority |
|---|---|---|---|---|
| `surzhyk` | 6 | 66.7% (4/6) | 83.3% (5/6) | 16.7% (1/6) |
| `typos` | 8 | 75.0% (6/8) | 100.0% (8/8) | 62.5% (5/8) |
| `angry` | 10 | 60.0% (6/10) | 50.0% (5/10) | 50.0% (5/10) |
| `vague` | 7 | 85.7% (6/7) | 100.0% (7/7) | 57.1% (4/7) |
| `mixed_topics` | 5 | 80.0% (4/5) | 100.0% (5/5) | 60.0% (3/5) |
| `translit` | 1 | 0.0% (0/1) | 100.0% (1/1) | 0.0% (0/1) |
| `code_switching` | 1 | 100.0% (1/1) | 100.0% (1/1) | 100.0% (1/1) |

## Confusion matrices

Rows are the label, columns the answer; answered rows only.

### Category

#### Category all

| label \ answer | billing | technical | account | feature_request | other |
|---|---|---|---|---|---|
| billing | 21 | 0 | 0 | 0 | 7 |
| technical | 3 | 10 | 0 | 0 | 6 |
| account | 1 | 0 | 17 | 0 | 2 |
| feature_request | 2 | 1 | 0 | 3 | 4 |
| other | 1 | 0 | 1 | 0 | 21 |

#### Category en

| label \ answer | billing | technical | account | feature_request | other |
|---|---|---|---|---|---|
| billing | 11 | 0 | 0 | 0 | 6 |
| technical | 1 | 4 | 0 | 0 | 1 |
| account | 0 | 0 | 10 | 0 | 1 |
| feature_request | 0 | 0 | 0 | 2 | 1 |
| other | 1 | 0 | 0 | 0 | 12 |

#### Category uk

| label \ answer | billing | technical | account | feature_request | other |
|---|---|---|---|---|---|
| billing | 10 | 0 | 0 | 0 | 1 |
| technical | 2 | 6 | 0 | 0 | 5 |
| account | 1 | 0 | 7 | 0 | 1 |
| feature_request | 2 | 1 | 0 | 1 | 3 |
| other | 0 | 0 | 1 | 0 | 9 |

### Complaint

#### Complaint all

| label \ answer | false | true |
|---|---|---|
| false | 82 | 0 |
| true | 11 | 7 |

#### Complaint en

| label \ answer | false | true |
|---|---|---|
| false | 39 | 0 |
| true | 6 | 5 |

#### Complaint uk

| label \ answer | false | true |
|---|---|---|
| false | 43 | 0 |
| true | 5 | 2 |

### Priority

#### Priority all

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 1 | 30 | 0 | 0 |
| 1 normal | 0 | 38 | 0 | 1 |
| 2 high | 0 | 11 | 7 | 0 |
| 3 critical | 0 | 7 | 1 | 4 |

#### Priority en

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 1 | 13 | 0 | 0 |
| 1 normal | 0 | 23 | 0 | 1 |
| 2 high | 0 | 6 | 2 | 0 |
| 3 critical | 0 | 3 | 0 | 1 |

#### Priority uk

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 0 | 17 | 0 | 0 |
| 1 normal | 0 | 15 | 0 | 0 |
| 2 high | 0 | 5 | 5 | 0 |
| 3 critical | 0 | 4 | 1 | 3 |

## Decisions at the configured thresholds

`classifier.Decide` on each answer; a failed call goes to `needs_human` as in the processor.

| Status | Reason | all | en | uk |
|---|---|---|---|---|
| `classified` | — | 55.0% (55/100) | 56.0% (28/50) | 54.0% (27/50) |
| `needs_human` | `category_other` | 40.0% (40/100) | 42.0% (21/50) | 38.0% (19/50) |
| `needs_human` | `low_confidence` | 5.0% (5/100) | 2.0% (1/50) | 8.0% (4/50) |
| flag | `complaint_uncertain` | 5.0% (5/100) | 8.0% (4/50) | 2.0% (1/50) |

## Latency

Latency of the call behind each answered row, as the classifier measured it; a cached row keeps
the latency of its original call. The fake makes no call and reports 0.

| Language | Answered | p50 | p95 | max |
|---|---|---|---|---|
| all | 100 | 0 ms | 0 ms | 0 ms |
| en | 50 | 0 ms | 0 ms | 0 ms |
| uk | 50 | 0 ms | 0 ms | 0 ms |

## Cost

What the calls behind these answers cost, failed calls included; a cached row counts the cost
recorded when it was first classified.

| Total | Per ticket | Input tokens | Output tokens | Cost source |
|---|---|---|---|---|
| $0.000000 | $0.00000000 | 0 | 0 | estimated 100 |

## Threshold sweep

Each line decides every row again with `classifier.Decide` at other minimum confidences, the
remaining thresholds as configured. `needs_human` is the share of all rows sent to a person, failed calls
included; the accuracy columns score only the auto-classified rows. Bold marks the configured value.

### CategoryMinConfidence (UrgencyMinConfidence 0.50)

| CategoryMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 40.0% (40/100) | 60 | 85.0% (51/60) | 51.7% (31/60) | 90.0% (54/60) |
| 0.05 | 40.0% (40/100) | 60 | 85.0% (51/60) | 51.7% (31/60) | 90.0% (54/60) |
| 0.10 | 40.0% (40/100) | 60 | 85.0% (51/60) | 51.7% (31/60) | 90.0% (54/60) |
| 0.15 | 40.0% (40/100) | 60 | 85.0% (51/60) | 51.7% (31/60) | 90.0% (54/60) |
| 0.20 | 40.0% (40/100) | 60 | 85.0% (51/60) | 51.7% (31/60) | 90.0% (54/60) |
| 0.25 | 40.0% (40/100) | 60 | 85.0% (51/60) | 51.7% (31/60) | 90.0% (54/60) |
| 0.30 | 40.0% (40/100) | 60 | 85.0% (51/60) | 51.7% (31/60) | 90.0% (54/60) |
| 0.35 | 40.0% (40/100) | 60 | 85.0% (51/60) | 51.7% (31/60) | 90.0% (54/60) |
| 0.40 | 40.0% (40/100) | 60 | 85.0% (51/60) | 51.7% (31/60) | 90.0% (54/60) |
| 0.45 | 40.0% (40/100) | 60 | 85.0% (51/60) | 51.7% (31/60) | 90.0% (54/60) |
| 0.50 | 40.0% (40/100) | 60 | 85.0% (51/60) | 51.7% (31/60) | 90.0% (54/60) |
| 0.55 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| **0.60** | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.65 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.70 | 48.0% (48/100) | 52 | 90.4% (47/52) | 51.9% (27/52) | 92.3% (48/52) |
| 0.75 | 48.0% (48/100) | 52 | 90.4% (47/52) | 51.9% (27/52) | 92.3% (48/52) |
| 0.80 | 49.0% (49/100) | 51 | 90.2% (46/51) | 51.0% (26/51) | 92.2% (47/51) |
| 0.85 | 49.0% (49/100) | 51 | 90.2% (46/51) | 51.0% (26/51) | 92.2% (47/51) |
| 0.90 | 49.0% (49/100) | 51 | 90.2% (46/51) | 51.0% (26/51) | 92.2% (47/51) |
| 0.95 | 49.0% (49/100) | 51 | 90.2% (46/51) | 51.0% (26/51) | 92.2% (47/51) |
| 1.00 | 49.0% (49/100) | 51 | 90.2% (46/51) | 51.0% (26/51) | 92.2% (47/51) |

### UrgencyMinConfidence (CategoryMinConfidence 0.60)

| UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.05 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.10 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.15 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.20 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.25 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.30 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.35 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.40 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.45 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| **0.50** | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.55 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.60 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.65 | 88.0% (88/100) | 12 | 91.7% (11/12) | 91.7% (11/12) | 91.7% (11/12) |
| 0.70 | 88.0% (88/100) | 12 | 91.7% (11/12) | 91.7% (11/12) | 91.7% (11/12) |
| 0.75 | 88.0% (88/100) | 12 | 91.7% (11/12) | 91.7% (11/12) | 91.7% (11/12) |
| 0.80 | 88.0% (88/100) | 12 | 91.7% (11/12) | 91.7% (11/12) | 91.7% (11/12) |
| 0.85 | 100.0% (100/100) | 0 | — | — | — |
| 0.90 | 100.0% (100/100) | 0 | — | — | — |
| 0.95 | 100.0% (100/100) | 0 | — | — | — |
| 1.00 | 100.0% (100/100) | 0 | — | — | — |

### Best trade-offs on the full grid

Both thresholds over the 0.05 grid; a pair is listed when no other pair sends as few or fewer rows to
`needs_human` with as high or higher category accuracy on the auto-classified rows. Pairs with the same
result are shown once, with the lowest thresholds.

| CategoryMinConfidence | UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|---|
| 0.00 | 0.00 | 40.0% (40/100) | 60 | 85.0% (51/60) | 51.7% (31/60) | 90.0% (54/60) |
| 0.55 | 0.00 | 45.0% (45/100) | 55 | 89.1% (49/55) | 50.9% (28/55) | 90.9% (50/55) |
| 0.70 | 0.00 | 48.0% (48/100) | 52 | 90.4% (47/52) | 51.9% (27/52) | 92.3% (48/52) |
| 0.55 | 0.65 | 88.0% (88/100) | 12 | 91.7% (11/12) | 91.7% (11/12) | 91.7% (11/12) |

## Reproduce

From the repository root. The fake run needs no key and makes no network call:

```sh
go run ./cmd/eval  # writes docs/eval-fake.md
```

The live run calls Jev once per row (about 100 calls, a fraction of a cent at the README §4.4 price)
with the key from `~/.config/ticket-triage/.env`, stops if the calls cost more than `-max-cost` ($0.05),
and writes `docs/eval.md`:

```sh
go run ./cmd/eval -classifier jev -env-file ~/.config/ticket-triage/.env \
  -jsonl ~/.cache/ticket-triage/eval-results.jsonl
```

Answers are cached per ticket, model and hash of the Jev questions in `.cache/eval/results.jsonl`
(git-ignored), so re-rendering the report or re-running the sweep costs nothing, and a question change
calls Jev again for every row. `-refresh` calls Jev again regardless, for example after another adapter
change. `-limit 5` makes a small first run. `go run ./cmd/eval -h` lists every flag.
