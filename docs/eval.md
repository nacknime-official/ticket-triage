# Eval report

Live run of `typesafe/jev-1.14` (served as `typesafe/jev-1.14-20261014`) through `openrouter` on 2026-10-29, over the eval set in `testdata/eval/`.

> **Labels are unreviewed drafts.** An agent drafted every label and no human has reviewed them yet
> (`testdata/eval/README.md`), so every accuracy number here is provisional.

## Run

| Setting | Value |
|---|---|
| Classifier | `jev` (provider `openrouter`, model `typesafe/jev-1.14`) |
| Date | 2026-10-29 |
| Served model | `typesafe/jev-1.14-20261014` |
| Eval set | `bitext.jsonl` (30 rows, sha256 `848d196e9fec`), `generated.jsonl` (70 rows, sha256 `420bcf4501ea`) |
| Rows | 100 (en 50, uk 50) |
| Answered | 100; failed 0 |
| From cache | 5 |
| Thresholds | CategoryMinConfidence 0.60, UrgencyMinConfidence 0.50, ComplaintUncertainBand 0.35–0.65, RouteOtherToHuman true |
| Complaint decision | P(yes) ≥ 0.50 counts as a complaint |

## Accuracy

Share of answered rows whose answer matches the label. Priority is the urgency score rounded to
the nearest level; "within one level" also counts an answer one level off.

| Question | all | en | uk |
|---|---|---|---|
| Category | 94.0% (94/100) | 90.0% (45/50) | 98.0% (49/50) |
| Complaint | 64.0% (64/100) | 70.0% (35/50) | 58.0% (29/50) |
| Priority | 66.0% (66/100) | 58.0% (29/50) | 74.0% (37/50) |
| Priority, within one level | 99.0% (99/100) | 98.0% (49/50) | 100.0% (50/50) |

### By hard-case tag

A row can carry several tags.

| Tag | Rows | Category | Complaint | Priority |
|---|---|---|---|---|
| `surzhyk` | 6 | 100.0% (6/6) | 50.0% (3/6) | 66.7% (4/6) |
| `typos` | 8 | 100.0% (8/8) | 50.0% (4/8) | 62.5% (5/8) |
| `angry` | 10 | 100.0% (10/10) | 100.0% (10/10) | 60.0% (6/10) |
| `vague` | 7 | 85.7% (6/7) | 57.1% (4/7) | 85.7% (6/7) |
| `mixed_topics` | 5 | 100.0% (5/5) | 20.0% (1/5) | 80.0% (4/5) |
| `translit` | 1 | 100.0% (1/1) | 0.0% (0/1) | 100.0% (1/1) |
| `code_switching` | 1 | 100.0% (1/1) | 0.0% (0/1) | 0.0% (0/1) |

## Confusion matrices

Rows are the label, columns the answer; answered rows only.

### Category

#### Category all

| label \ answer | billing | technical | account | feature_request | other |
|---|---|---|---|---|---|
| billing | 28 | 0 | 0 | 0 | 0 |
| technical | 0 | 17 | 2 | 0 | 0 |
| account | 0 | 0 | 20 | 0 | 0 |
| feature_request | 0 | 0 | 0 | 10 | 0 |
| other | 3 | 1 | 0 | 0 | 19 |

#### Category en

| label \ answer | billing | technical | account | feature_request | other |
|---|---|---|---|---|---|
| billing | 17 | 0 | 0 | 0 | 0 |
| technical | 0 | 5 | 1 | 0 | 0 |
| account | 0 | 0 | 11 | 0 | 0 |
| feature_request | 0 | 0 | 0 | 3 | 0 |
| other | 3 | 1 | 0 | 0 | 9 |

#### Category uk

| label \ answer | billing | technical | account | feature_request | other |
|---|---|---|---|---|---|
| billing | 11 | 0 | 0 | 0 | 0 |
| technical | 0 | 12 | 1 | 0 | 0 |
| account | 0 | 0 | 9 | 0 | 0 |
| feature_request | 0 | 0 | 0 | 7 | 0 |
| other | 0 | 0 | 0 | 0 | 10 |

### Complaint

#### Complaint all

| label \ answer | false | true |
|---|---|---|
| false | 48 | 34 |
| true | 2 | 16 |

#### Complaint en

| label \ answer | false | true |
|---|---|---|
| false | 26 | 13 |
| true | 2 | 9 |

#### Complaint uk

| label \ answer | false | true |
|---|---|---|
| false | 22 | 21 |
| true | 0 | 7 |

### Priority

#### Priority all

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 23 | 8 | 0 | 0 |
| 1 normal | 8 | 20 | 11 | 0 |
| 2 high | 1 | 2 | 12 | 3 |
| 3 critical | 0 | 0 | 1 | 11 |

#### Priority en

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 10 | 4 | 0 | 0 |
| 1 normal | 7 | 11 | 6 | 0 |
| 2 high | 1 | 2 | 4 | 1 |
| 3 critical | 0 | 0 | 0 | 4 |

#### Priority uk

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 13 | 4 | 0 | 0 |
| 1 normal | 1 | 9 | 5 | 0 |
| 2 high | 0 | 0 | 8 | 2 |
| 3 critical | 0 | 0 | 1 | 7 |

## Decisions at the configured thresholds

`classifier.Decide` on each answer; a failed call goes to `needs_human` as in the processor.

| Status | Reason | all | en | uk |
|---|---|---|---|---|
| `classified` | — | 70.0% (70/100) | 64.0% (32/50) | 76.0% (38/50) |
| `needs_human` | `category_other` | 19.0% (19/100) | 18.0% (9/50) | 20.0% (10/50) |
| `needs_human` | `low_confidence` | 11.0% (11/100) | 18.0% (9/50) | 4.0% (2/50) |
| flag | `complaint_uncertain` | 16.0% (16/100) | 20.0% (10/50) | 12.0% (6/50) |

## Latency

Latency of the call behind each answered row, as the classifier measured it; a cached row keeps
the latency of its original call.

| Language | Answered | p50 | p95 | max |
|---|---|---|---|---|
| all | 100 | 222 ms | 480 ms | 953 ms |
| en | 50 | 245 ms | 515 ms | 846 ms |
| uk | 50 | 210 ms | 345 ms | 953 ms |

## Cost

What the calls behind these answers cost, failed calls included; a cached row counts the cost
recorded when it was first classified.

| Total | Per ticket | Input tokens | Output tokens | Cost source |
|---|---|---|---|---|
| $0.002544 | $0.00002544 | 60580 | 8710 | reported 100 |

## Threshold sweep

Each line decides every row again with `classifier.Decide` at other minimum confidences, the
remaining thresholds as configured. `needs_human` is the share of all rows sent to a person, failed calls
included; the accuracy columns score only the auto-classified rows. Bold marks the configured value.

### CategoryMinConfidence (UrgencyMinConfidence 0.50)

| CategoryMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 24.0% (24/100) | 76 | 92.1% (70/76) | 64.5% (49/76) | 56.6% (43/76) |
| 0.05 | 24.0% (24/100) | 76 | 92.1% (70/76) | 64.5% (49/76) | 56.6% (43/76) |
| 0.10 | 24.0% (24/100) | 76 | 92.1% (70/76) | 64.5% (49/76) | 56.6% (43/76) |
| 0.15 | 24.0% (24/100) | 76 | 92.1% (70/76) | 64.5% (49/76) | 56.6% (43/76) |
| 0.20 | 24.0% (24/100) | 76 | 92.1% (70/76) | 64.5% (49/76) | 56.6% (43/76) |
| 0.25 | 24.0% (24/100) | 76 | 92.1% (70/76) | 64.5% (49/76) | 56.6% (43/76) |
| 0.30 | 24.0% (24/100) | 76 | 92.1% (70/76) | 64.5% (49/76) | 56.6% (43/76) |
| 0.35 | 24.0% (24/100) | 76 | 92.1% (70/76) | 64.5% (49/76) | 56.6% (43/76) |
| 0.40 | 25.0% (25/100) | 75 | 93.3% (70/75) | 64.0% (48/75) | 56.0% (42/75) |
| 0.45 | 25.0% (25/100) | 75 | 93.3% (70/75) | 64.0% (48/75) | 56.0% (42/75) |
| 0.50 | 28.0% (28/100) | 72 | 97.2% (70/72) | 63.9% (46/72) | 56.9% (41/72) |
| 0.55 | 28.0% (28/100) | 72 | 97.2% (70/72) | 63.9% (46/72) | 56.9% (41/72) |
| **0.60** | 30.0% (30/100) | 70 | 98.6% (69/70) | 62.9% (44/70) | 55.7% (39/70) |
| 0.65 | 30.0% (30/100) | 70 | 98.6% (69/70) | 62.9% (44/70) | 55.7% (39/70) |
| 0.70 | 32.0% (32/100) | 68 | 98.5% (67/68) | 63.2% (43/68) | 55.9% (38/68) |
| 0.75 | 33.0% (33/100) | 67 | 98.5% (66/67) | 64.2% (43/67) | 55.2% (37/67) |
| 0.80 | 36.0% (36/100) | 64 | 98.4% (63/64) | 65.6% (42/64) | 56.2% (36/64) |
| 0.85 | 36.0% (36/100) | 64 | 98.4% (63/64) | 65.6% (42/64) | 56.2% (36/64) |
| 0.90 | 39.0% (39/100) | 61 | 100.0% (61/61) | 63.9% (39/61) | 57.4% (35/61) |
| 0.95 | 42.0% (42/100) | 58 | 100.0% (58/58) | 62.1% (36/58) | 60.3% (35/58) |
| 1.00 | 58.0% (58/100) | 42 | 100.0% (42/42) | 64.3% (27/42) | 59.5% (25/42) |

### UrgencyMinConfidence (CategoryMinConfidence 0.60)

| UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 25.0% (25/100) | 75 | 98.7% (74/75) | 61.3% (46/75) | 56.0% (42/75) |
| 0.05 | 25.0% (25/100) | 75 | 98.7% (74/75) | 61.3% (46/75) | 56.0% (42/75) |
| 0.10 | 25.0% (25/100) | 75 | 98.7% (74/75) | 61.3% (46/75) | 56.0% (42/75) |
| 0.15 | 25.0% (25/100) | 75 | 98.7% (74/75) | 61.3% (46/75) | 56.0% (42/75) |
| 0.20 | 25.0% (25/100) | 75 | 98.7% (74/75) | 61.3% (46/75) | 56.0% (42/75) |
| 0.25 | 26.0% (26/100) | 74 | 98.6% (73/74) | 60.8% (45/74) | 55.4% (41/74) |
| 0.30 | 26.0% (26/100) | 74 | 98.6% (73/74) | 60.8% (45/74) | 55.4% (41/74) |
| 0.35 | 26.0% (26/100) | 74 | 98.6% (73/74) | 60.8% (45/74) | 55.4% (41/74) |
| 0.40 | 26.0% (26/100) | 74 | 98.6% (73/74) | 60.8% (45/74) | 55.4% (41/74) |
| 0.45 | 27.0% (27/100) | 73 | 98.6% (72/73) | 61.6% (45/73) | 54.8% (40/73) |
| **0.50** | 30.0% (30/100) | 70 | 98.6% (69/70) | 62.9% (44/70) | 55.7% (39/70) |
| 0.55 | 38.0% (38/100) | 62 | 100.0% (62/62) | 64.5% (40/62) | 56.5% (35/62) |
| 0.60 | 40.0% (40/100) | 60 | 100.0% (60/60) | 66.7% (40/60) | 55.0% (33/60) |
| 0.65 | 49.0% (49/100) | 51 | 100.0% (51/51) | 68.6% (35/51) | 51.0% (26/51) |
| 0.70 | 55.0% (55/100) | 45 | 100.0% (45/45) | 68.9% (31/45) | 46.7% (21/45) |
| 0.75 | 59.0% (59/100) | 41 | 100.0% (41/41) | 73.2% (30/41) | 46.3% (19/41) |
| 0.80 | 65.0% (65/100) | 35 | 100.0% (35/35) | 82.9% (29/35) | 40.0% (14/35) |
| 0.85 | 69.0% (69/100) | 31 | 100.0% (31/31) | 87.1% (27/31) | 38.7% (12/31) |
| 0.90 | 74.0% (74/100) | 26 | 100.0% (26/26) | 84.6% (22/26) | 38.5% (10/26) |
| 0.95 | 78.0% (78/100) | 22 | 100.0% (22/22) | 86.4% (19/22) | 36.4% (8/22) |
| 1.00 | 98.0% (98/100) | 2 | 100.0% (2/2) | 100.0% (2/2) | 50.0% (1/2) |

### Best trade-offs on the full grid

Both thresholds over the 0.05 grid; a pair is listed when no other pair sends as few or fewer rows to
`needs_human` with as high or higher category accuracy on the auto-classified rows. Pairs with the same
result are shown once, with the lowest thresholds.

| CategoryMinConfidence | UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|---|
| 0.00 | 0.00 | 19.0% (19/100) | 81 | 92.6% (75/81) | 63.0% (51/81) | 56.8% (46/81) |
| 0.40 | 0.00 | 20.0% (20/100) | 80 | 93.8% (75/80) | 62.5% (50/80) | 56.2% (45/80) |
| 0.50 | 0.00 | 23.0% (23/100) | 77 | 97.4% (75/77) | 62.3% (48/77) | 57.1% (44/77) |
| 0.60 | 0.00 | 25.0% (25/100) | 75 | 98.7% (74/75) | 61.3% (46/75) | 56.0% (42/75) |
| 0.90 | 0.00 | 34.0% (34/100) | 66 | 100.0% (66/66) | 62.1% (41/66) | 57.6% (38/66) |

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

Answers are cached per ticket and model in `.cache/eval/results.jsonl` (git-ignored), so re-rendering
the report or re-running the sweep costs nothing. `-refresh` calls Jev again, for example after the adapter
or its questions change. `-limit 5` makes a small first run. `go run ./cmd/eval -h` lists every flag.
