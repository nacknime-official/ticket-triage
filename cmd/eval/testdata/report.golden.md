# Eval report

Live run of `typesafe/jev-1.14` (served as `typesafe/jev-1.14-20261014`) through `openrouter` on 2026-10-28, over the eval set in `testdata/eval/`.

> **Labels are unreviewed drafts.** An agent drafted every label and no human has reviewed them yet
> (`testdata/eval/README.md`), so every accuracy number here is provisional.

## Run

| Setting | Value |
|---|---|
| Classifier | `jev` (provider `openrouter`, model `typesafe/jev-1.14`) |
| Date | 2026-10-28 |
| Served model | `typesafe/jev-1.14-20261014` |
| Eval set | `a.jsonl` (3 rows, sha256 `0123456789ab`), `b.jsonl` (3 rows, sha256 `fedcba987654`) |
| Rows | 6 (en 3, uk 3) |
| Answered | 5; failed 1 |
| From cache | 1 |
| Thresholds | CategoryMinConfidence 0.60, UrgencyMinConfidence 0.50, ComplaintUncertainBand 0.35–0.65, RouteOtherToHuman true |
| Complaint decision | P(yes) ≥ 0.50 counts as a complaint |

Failed calls are left out of accuracy, confusion matrices and latency, and count as
`needs_human` in the decisions and the sweep.

| Error | Rows |
|---|---|
| `classifier: transient error: jev: status 503` | 1 |

## Accuracy

Share of answered rows whose answer matches the label. Priority is the urgency score rounded to
the nearest level; "within one level" also counts an answer one level off.

| Question | all | en | uk |
|---|---|---|---|
| Category | 80.0% (4/5) | 100.0% (3/3) | 50.0% (1/2) |
| Complaint | 100.0% (5/5) | 100.0% (3/3) | 100.0% (2/2) |
| Priority | 80.0% (4/5) | 100.0% (3/3) | 50.0% (1/2) |
| Priority, within one level | 100.0% (5/5) | 100.0% (3/3) | 100.0% (2/2) |

### By hard-case tag

A row can carry several tags.

| Tag | Rows | Category | Complaint | Priority |
|---|---|---|---|---|
| `surzhyk` | 1 | 100.0% (1/1) | 100.0% (1/1) | 0.0% (0/1) |
| `typos` | 1 | 100.0% (1/1) | 100.0% (1/1) | 0.0% (0/1) |
| `angry` | 1 | 100.0% (1/1) | 100.0% (1/1) | 100.0% (1/1) |
| `vague` | 1 | 0.0% (0/1) | 100.0% (1/1) | 100.0% (1/1) |

## Confusion matrices

Rows are the label, columns the answer; answered rows only.

### Category

#### Category all

| label \ answer | billing | technical | account | feature_request | other |
|---|---|---|---|---|---|
| billing | 1 | 0 | 0 | 0 | 0 |
| technical | 0 | 1 | 0 | 0 | 0 |
| account | 0 | 0 | 1 | 0 | 0 |
| feature_request | 0 | 0 | 0 | 1 | 0 |
| other | 1 | 0 | 0 | 0 | 0 |

#### Category en

| label \ answer | billing | technical | account | feature_request | other |
|---|---|---|---|---|---|
| billing | 1 | 0 | 0 | 0 | 0 |
| technical | 0 | 1 | 0 | 0 | 0 |
| account | 0 | 0 | 0 | 0 | 0 |
| feature_request | 0 | 0 | 0 | 1 | 0 |
| other | 0 | 0 | 0 | 0 | 0 |

#### Category uk

| label \ answer | billing | technical | account | feature_request | other |
|---|---|---|---|---|---|
| billing | 0 | 0 | 0 | 0 | 0 |
| technical | 0 | 0 | 0 | 0 | 0 |
| account | 0 | 0 | 1 | 0 | 0 |
| feature_request | 0 | 0 | 0 | 0 | 0 |
| other | 1 | 0 | 0 | 0 | 0 |

### Complaint

#### Complaint all

| label \ answer | false | true |
|---|---|---|
| false | 3 | 0 |
| true | 0 | 2 |

#### Complaint en

| label \ answer | false | true |
|---|---|---|
| false | 2 | 0 |
| true | 0 | 1 |

#### Complaint uk

| label \ answer | false | true |
|---|---|---|
| false | 1 | 0 |
| true | 0 | 1 |

### Priority

#### Priority all

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 1 | 0 | 0 | 0 |
| 1 normal | 0 | 1 | 0 | 0 |
| 2 high | 0 | 1 | 1 | 0 |
| 3 critical | 0 | 0 | 0 | 1 |

#### Priority en

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 1 | 0 | 0 | 0 |
| 1 normal | 0 | 0 | 0 | 0 |
| 2 high | 0 | 0 | 1 | 0 |
| 3 critical | 0 | 0 | 0 | 1 |

#### Priority uk

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 0 | 0 | 0 | 0 |
| 1 normal | 0 | 1 | 0 | 0 |
| 2 high | 0 | 1 | 0 | 0 |
| 3 critical | 0 | 0 | 0 | 0 |

## Decisions at the configured thresholds

`classifier.Decide` on each answer; a failed call goes to `needs_human` as in the processor.

| Status | Reason | all | en | uk |
|---|---|---|---|---|
| `classified` | — | 33.3% (2/6) | 66.7% (2/3) | 0.0% (0/3) |
| `needs_human` | `classifier_unavailable` | 16.7% (1/6) | 0.0% (0/3) | 33.3% (1/3) |
| `needs_human` | `low_confidence` | 50.0% (3/6) | 33.3% (1/3) | 66.7% (2/3) |
| flag | `complaint_uncertain` | 16.7% (1/6) | 0.0% (0/3) | 33.3% (1/3) |

## Latency

Latency of the call behind each answered row, as the classifier measured it; a cached row keeps
the latency of its original call.

| Language | Answered | p50 | p95 | max |
|---|---|---|---|---|
| all | 5 | 140 ms | 450 ms | 450 ms |
| en | 3 | 110 ms | 140 ms | 140 ms |
| uk | 2 | 300 ms | 450 ms | 450 ms |

## Cost

What the calls behind these answers cost, failed calls included; a cached row counts the cost
recorded when it was first classified.

| Total | Per ticket | Input tokens | Output tokens | Cost source |
|---|---|---|---|---|
| $0.000095 | $0.00001583 | 2250 | 0 | unknown 1, reported 5 |

## Threshold sweep

Each line decides every row again with `classifier.Decide` at other minimum confidences, the
remaining thresholds as configured. `needs_human` is the share of all rows sent to a person, failed calls
included; the accuracy columns score only the auto-classified rows. Bold marks the configured value.

### CategoryMinConfidence (UrgencyMinConfidence 0.50)

| CategoryMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 33.3% (2/6) | 4 | 75.0% (3/4) | 100.0% (4/4) | 100.0% (4/4) |
| 0.05 | 33.3% (2/6) | 4 | 75.0% (3/4) | 100.0% (4/4) | 100.0% (4/4) |
| 0.10 | 33.3% (2/6) | 4 | 75.0% (3/4) | 100.0% (4/4) | 100.0% (4/4) |
| 0.15 | 33.3% (2/6) | 4 | 75.0% (3/4) | 100.0% (4/4) | 100.0% (4/4) |
| 0.20 | 33.3% (2/6) | 4 | 75.0% (3/4) | 100.0% (4/4) | 100.0% (4/4) |
| 0.25 | 33.3% (2/6) | 4 | 75.0% (3/4) | 100.0% (4/4) | 100.0% (4/4) |
| 0.30 | 33.3% (2/6) | 4 | 75.0% (3/4) | 100.0% (4/4) | 100.0% (4/4) |
| 0.35 | 33.3% (2/6) | 4 | 75.0% (3/4) | 100.0% (4/4) | 100.0% (4/4) |
| 0.40 | 33.3% (2/6) | 4 | 75.0% (3/4) | 100.0% (4/4) | 100.0% (4/4) |
| 0.45 | 33.3% (2/6) | 4 | 75.0% (3/4) | 100.0% (4/4) | 100.0% (4/4) |
| 0.50 | 33.3% (2/6) | 4 | 75.0% (3/4) | 100.0% (4/4) | 100.0% (4/4) |
| 0.55 | 50.0% (3/6) | 3 | 100.0% (3/3) | 100.0% (3/3) | 100.0% (3/3) |
| **0.60** | 66.7% (4/6) | 2 | 100.0% (2/2) | 100.0% (2/2) | 100.0% (2/2) |
| 0.65 | 66.7% (4/6) | 2 | 100.0% (2/2) | 100.0% (2/2) | 100.0% (2/2) |
| 0.70 | 66.7% (4/6) | 2 | 100.0% (2/2) | 100.0% (2/2) | 100.0% (2/2) |
| 0.75 | 83.3% (5/6) | 1 | 100.0% (1/1) | 100.0% (1/1) | 100.0% (1/1) |
| 0.80 | 83.3% (5/6) | 1 | 100.0% (1/1) | 100.0% (1/1) | 100.0% (1/1) |
| 0.85 | 100.0% (6/6) | 0 | — | — | — |
| 0.90 | 100.0% (6/6) | 0 | — | — | — |
| 0.95 | 100.0% (6/6) | 0 | — | — | — |
| 1.00 | 100.0% (6/6) | 0 | — | — | — |

### UrgencyMinConfidence (CategoryMinConfidence 0.60)

| UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 50.0% (3/6) | 3 | 100.0% (3/3) | 66.7% (2/3) | 100.0% (3/3) |
| 0.05 | 50.0% (3/6) | 3 | 100.0% (3/3) | 66.7% (2/3) | 100.0% (3/3) |
| 0.10 | 50.0% (3/6) | 3 | 100.0% (3/3) | 66.7% (2/3) | 100.0% (3/3) |
| 0.15 | 50.0% (3/6) | 3 | 100.0% (3/3) | 66.7% (2/3) | 100.0% (3/3) |
| 0.20 | 50.0% (3/6) | 3 | 100.0% (3/3) | 66.7% (2/3) | 100.0% (3/3) |
| 0.25 | 50.0% (3/6) | 3 | 100.0% (3/3) | 66.7% (2/3) | 100.0% (3/3) |
| 0.30 | 50.0% (3/6) | 3 | 100.0% (3/3) | 66.7% (2/3) | 100.0% (3/3) |
| 0.35 | 50.0% (3/6) | 3 | 100.0% (3/3) | 66.7% (2/3) | 100.0% (3/3) |
| 0.40 | 50.0% (3/6) | 3 | 100.0% (3/3) | 66.7% (2/3) | 100.0% (3/3) |
| 0.45 | 50.0% (3/6) | 3 | 100.0% (3/3) | 66.7% (2/3) | 100.0% (3/3) |
| **0.50** | 66.7% (4/6) | 2 | 100.0% (2/2) | 100.0% (2/2) | 100.0% (2/2) |
| 0.55 | 66.7% (4/6) | 2 | 100.0% (2/2) | 100.0% (2/2) | 100.0% (2/2) |
| 0.60 | 83.3% (5/6) | 1 | 100.0% (1/1) | 100.0% (1/1) | 100.0% (1/1) |
| 0.65 | 100.0% (6/6) | 0 | — | — | — |
| 0.70 | 100.0% (6/6) | 0 | — | — | — |
| 0.75 | 100.0% (6/6) | 0 | — | — | — |
| 0.80 | 100.0% (6/6) | 0 | — | — | — |
| 0.85 | 100.0% (6/6) | 0 | — | — | — |
| 0.90 | 100.0% (6/6) | 0 | — | — | — |
| 0.95 | 100.0% (6/6) | 0 | — | — | — |
| 1.00 | 100.0% (6/6) | 0 | — | — | — |

### Best trade-offs on the full grid

Both thresholds over the 0.05 grid; a pair is listed when no other pair sends as few or fewer rows to
`needs_human` with as high or higher category accuracy on the auto-classified rows. Pairs with the same
result are shown once, with the lowest thresholds.

| CategoryMinConfidence | UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|---|
| 0.00 | 0.00 | 16.7% (1/6) | 5 | 80.0% (4/5) | 80.0% (4/5) | 100.0% (5/5) |
| 0.55 | 0.00 | 33.3% (2/6) | 4 | 100.0% (4/4) | 75.0% (3/4) | 100.0% (4/4) |

## Reproduce

From the repository root. The fake run needs no key and makes no network call:

```sh
go run ./cmd/eval  # writes docs/eval-fake.md
```

The live run calls Jev once per row (about 100 calls, a fraction of a cent at the PRD §4.4 price)
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
