# Eval report

Live run of `typesafe/jev-1.14` (served as `typesafe/jev-1.14-20261014`) through `openrouter` on 2026-10-29, over the eval set in `testdata/eval/`.

> **Labels are unreviewed drafts.** An agent drafted every label and no human has reviewed them yet
> (`testdata/eval/README.md`), so every accuracy number here is provisional.

## Complaint criteria: before and after

Written by hand; `cmd/eval` does not generate this section, so a re-run drops it. "Before" is the report of
the same eval set and model at commit `a299635`, when `is_complaint` sent only the instruction "Does the
customer in `subject` and `body` express dissatisfaction or complain about the product, service, or a
charge?". "After" is this run (`-refresh`, every row called again): the question now sends the "Complaint"
labeling guidelines of `testdata/eval/README.md` as its `true` and `false` criteria. Labels, thresholds and
the category and urgency questions did not change.

| Complaint accuracy | before | after |
|---|---|---|
| all | 64.0% (64/100) | 92.0% (92/100) |
| en | 70.0% (35/50) | 96.0% (48/50) |
| uk | 58.0% (29/50) | 88.0% (44/50) |

Confusion, label \ answer, before → after:

| Language | false → false | false → true | true → false | true → true |
|---|---|---|---|---|
| all | 48 → 74 | 34 → 8 | 2 → 0 | 16 → 18 |
| en | 26 → 37 | 13 → 2 | 2 → 0 | 9 → 11 |
| uk | 22 → 37 | 21 → 6 | 0 → 0 | 7 → 7 |

| Tag | Rows | Complaint before | Complaint after |
|---|---|---|---|
| `surzhyk` | 6 | 50.0% (3/6) | 83.3% (5/6) |
| `typos` | 8 | 50.0% (4/8) | 87.5% (7/8) |
| `angry` | 10 | 100.0% (10/10) | 100.0% (10/10) |
| `vague` | 7 | 57.1% (4/7) | 85.7% (6/7) |
| `mixed_topics` | 5 | 20.0% (1/5) | 80.0% (4/5) |
| `translit` | 1 | 0.0% (0/1) | 100.0% (1/1) |
| `code_switching` | 1 | 0.0% (0/1) | 100.0% (1/1) |

The other questions did not regress: category stays at 94.0% (94/100; en 45/50, uk 49/50) with the same
confusion matrix, and priority moves from 66.0% to 67.0% (within one level 99.0% both times), inside the run
to run noise. The `complaint_uncertain` flag drops from 16 to 9 rows. Each call sends about 151 more input
tokens, so the cost per ticket rises from $0.00002544 to $0.00003179.

The 8 rows still answered "complaint" against a `false` label: six Ukrainian problem reports in a neutral
tone (`gen-uk-012` 502 for the whole team, `gen-uk-016` app crash, `gen-uk-017` slowness, `gen-uk-019` "the
site works strangely", `gen-uk-021` delayed webhooks, "not critical but inconvenient", `gen-uk-022` a
calendar bug plus a wish), most of them close to the threshold (P 0.51–0.71); `gen-en-006`, a calm report of
a data leak (0.72); and `bitext-7ba1793968c9` "i need assistance demanding compensations of my money"
(0.79), which the guidelines' "a demand for compensation" may make a complaint, so the reviewer should look
at its label.

## Run

| Setting | Value |
|---|---|
| Classifier | `jev` (provider `openrouter`, model `typesafe/jev-1.14`) |
| Date | 2026-10-29 |
| Served model | `typesafe/jev-1.14-20261014` |
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
| Category | 94.0% (94/100) | 90.0% (45/50) | 98.0% (49/50) |
| Complaint | 92.0% (92/100) | 96.0% (48/50) | 88.0% (44/50) |
| Priority | 67.0% (67/100) | 60.0% (30/50) | 74.0% (37/50) |
| Priority, within one level | 99.0% (99/100) | 98.0% (49/50) | 100.0% (50/50) |

### By hard-case tag

A row can carry several tags.

| Tag | Rows | Category | Complaint | Priority |
|---|---|---|---|---|
| `surzhyk` | 6 | 100.0% (6/6) | 83.3% (5/6) | 66.7% (4/6) |
| `typos` | 8 | 100.0% (8/8) | 87.5% (7/8) | 75.0% (6/8) |
| `angry` | 10 | 100.0% (10/10) | 100.0% (10/10) | 60.0% (6/10) |
| `vague` | 7 | 85.7% (6/7) | 85.7% (6/7) | 85.7% (6/7) |
| `mixed_topics` | 5 | 100.0% (5/5) | 80.0% (4/5) | 80.0% (4/5) |
| `translit` | 1 | 100.0% (1/1) | 100.0% (1/1) | 100.0% (1/1) |
| `code_switching` | 1 | 100.0% (1/1) | 100.0% (1/1) | 100.0% (1/1) |

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
| false | 74 | 8 |
| true | 0 | 18 |

#### Complaint en

| label \ answer | false | true |
|---|---|---|
| false | 37 | 2 |
| true | 0 | 11 |

#### Complaint uk

| label \ answer | false | true |
|---|---|---|
| false | 37 | 6 |
| true | 0 | 7 |

### Priority

#### Priority all

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 23 | 8 | 0 | 0 |
| 1 normal | 8 | 21 | 10 | 0 |
| 2 high | 1 | 2 | 12 | 3 |
| 3 critical | 0 | 0 | 1 | 11 |

#### Priority en

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 10 | 4 | 0 | 0 |
| 1 normal | 7 | 12 | 5 | 0 |
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
| `classified` | — | 70.0% (70/100) | 66.0% (33/50) | 74.0% (37/50) |
| `needs_human` | `category_other` | 19.0% (19/100) | 18.0% (9/50) | 20.0% (10/50) |
| `needs_human` | `low_confidence` | 11.0% (11/100) | 16.0% (8/50) | 6.0% (3/50) |
| flag | `complaint_uncertain` | 9.0% (9/100) | 4.0% (2/50) | 14.0% (7/50) |

## Latency

Latency of the call behind each answered row, as the classifier measured it; a cached row keeps
the latency of its original call.

| Language | Answered | p50 | p95 | max |
|---|---|---|---|---|
| all | 100 | 224 ms | 301 ms | 551 ms |
| en | 50 | 222 ms | 398 ms | 548 ms |
| uk | 50 | 224 ms | 276 ms | 551 ms |

## Cost

What the calls behind these answers cost, failed calls included; a cached row counts the cost
recorded when it was first classified.

| Total | Per ticket | Input tokens | Output tokens | Cost source |
|---|---|---|---|---|
| $0.003179 | $0.00003179 | 75680 | 8710 | reported 100 |

## Threshold sweep

Each line decides every row again with `classifier.Decide` at other minimum confidences, the
remaining thresholds as configured. `needs_human` is the share of all rows sent to a person, failed calls
included; the accuracy columns score only the auto-classified rows. Bold marks the configured value.

### CategoryMinConfidence (UrgencyMinConfidence 0.50)

| CategoryMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 25.0% (25/100) | 75 | 92.0% (69/75) | 64.0% (48/75) | 90.7% (68/75) |
| 0.05 | 25.0% (25/100) | 75 | 92.0% (69/75) | 64.0% (48/75) | 90.7% (68/75) |
| 0.10 | 25.0% (25/100) | 75 | 92.0% (69/75) | 64.0% (48/75) | 90.7% (68/75) |
| 0.15 | 25.0% (25/100) | 75 | 92.0% (69/75) | 64.0% (48/75) | 90.7% (68/75) |
| 0.20 | 25.0% (25/100) | 75 | 92.0% (69/75) | 64.0% (48/75) | 90.7% (68/75) |
| 0.25 | 25.0% (25/100) | 75 | 92.0% (69/75) | 64.0% (48/75) | 90.7% (68/75) |
| 0.30 | 25.0% (25/100) | 75 | 92.0% (69/75) | 64.0% (48/75) | 90.7% (68/75) |
| 0.35 | 25.0% (25/100) | 75 | 92.0% (69/75) | 64.0% (48/75) | 90.7% (68/75) |
| 0.40 | 25.0% (25/100) | 75 | 92.0% (69/75) | 64.0% (48/75) | 90.7% (68/75) |
| 0.45 | 27.0% (27/100) | 73 | 94.5% (69/73) | 63.0% (46/73) | 91.8% (67/73) |
| 0.50 | 28.0% (28/100) | 72 | 95.8% (69/72) | 62.5% (45/72) | 91.7% (66/72) |
| 0.55 | 28.0% (28/100) | 72 | 95.8% (69/72) | 62.5% (45/72) | 91.7% (66/72) |
| **0.60** | 30.0% (30/100) | 70 | 98.6% (69/70) | 62.9% (44/70) | 91.4% (64/70) |
| 0.65 | 31.0% (31/100) | 69 | 98.6% (68/69) | 62.3% (43/69) | 91.3% (63/69) |
| 0.70 | 31.0% (31/100) | 69 | 98.6% (68/69) | 62.3% (43/69) | 91.3% (63/69) |
| 0.75 | 34.0% (34/100) | 66 | 98.5% (65/66) | 63.6% (42/66) | 90.9% (60/66) |
| 0.80 | 37.0% (37/100) | 63 | 98.4% (62/63) | 65.1% (41/63) | 90.5% (57/63) |
| 0.85 | 37.0% (37/100) | 63 | 98.4% (62/63) | 65.1% (41/63) | 90.5% (57/63) |
| 0.90 | 38.0% (38/100) | 62 | 100.0% (62/62) | 64.5% (40/62) | 90.3% (56/62) |
| 0.95 | 43.0% (43/100) | 57 | 100.0% (57/57) | 61.4% (35/57) | 91.2% (52/57) |
| 1.00 | 60.0% (60/100) | 40 | 100.0% (40/40) | 62.5% (25/40) | 92.5% (37/40) |

### UrgencyMinConfidence (CategoryMinConfidence 0.60)

| UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 24.0% (24/100) | 76 | 98.7% (75/76) | 63.2% (48/76) | 90.8% (69/76) |
| 0.05 | 24.0% (24/100) | 76 | 98.7% (75/76) | 63.2% (48/76) | 90.8% (69/76) |
| 0.10 | 24.0% (24/100) | 76 | 98.7% (75/76) | 63.2% (48/76) | 90.8% (69/76) |
| 0.15 | 24.0% (24/100) | 76 | 98.7% (75/76) | 63.2% (48/76) | 90.8% (69/76) |
| 0.20 | 24.0% (24/100) | 76 | 98.7% (75/76) | 63.2% (48/76) | 90.8% (69/76) |
| 0.25 | 25.0% (25/100) | 75 | 98.7% (74/75) | 62.7% (47/75) | 90.7% (68/75) |
| 0.30 | 25.0% (25/100) | 75 | 98.7% (74/75) | 62.7% (47/75) | 90.7% (68/75) |
| 0.35 | 25.0% (25/100) | 75 | 98.7% (74/75) | 62.7% (47/75) | 90.7% (68/75) |
| 0.40 | 25.0% (25/100) | 75 | 98.7% (74/75) | 62.7% (47/75) | 90.7% (68/75) |
| 0.45 | 26.0% (26/100) | 74 | 98.6% (73/74) | 63.5% (47/74) | 90.5% (67/74) |
| **0.50** | 30.0% (30/100) | 70 | 98.6% (69/70) | 62.9% (44/70) | 91.4% (64/70) |
| 0.55 | 35.0% (35/100) | 65 | 100.0% (65/65) | 63.1% (41/65) | 90.8% (59/65) |
| 0.60 | 42.0% (42/100) | 58 | 100.0% (58/58) | 67.2% (39/58) | 89.7% (52/58) |
| 0.65 | 46.0% (46/100) | 54 | 100.0% (54/54) | 70.4% (38/54) | 90.7% (49/54) |
| 0.70 | 54.0% (54/100) | 46 | 100.0% (46/46) | 69.6% (32/46) | 91.3% (42/46) |
| 0.75 | 56.0% (56/100) | 44 | 100.0% (44/44) | 70.5% (31/44) | 90.9% (40/44) |
| 0.80 | 65.0% (65/100) | 35 | 100.0% (35/35) | 82.9% (29/35) | 88.6% (31/35) |
| 0.85 | 69.0% (69/100) | 31 | 100.0% (31/31) | 87.1% (27/31) | 87.1% (27/31) |
| 0.90 | 73.0% (73/100) | 27 | 100.0% (27/27) | 85.2% (23/27) | 85.2% (23/27) |
| 0.95 | 79.0% (79/100) | 21 | 100.0% (21/21) | 85.7% (18/21) | 85.7% (18/21) |
| 1.00 | 97.0% (97/100) | 3 | 100.0% (3/3) | 100.0% (3/3) | 100.0% (3/3) |

### Best trade-offs on the full grid

Both thresholds over the 0.05 grid; a pair is listed when no other pair sends as few or fewer rows to
`needs_human` with as high or higher category accuracy on the auto-classified rows. Pairs with the same
result are shown once, with the lowest thresholds.

| CategoryMinConfidence | UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|---|
| 0.00 | 0.00 | 19.0% (19/100) | 81 | 92.6% (75/81) | 64.2% (52/81) | 90.1% (73/81) |
| 0.45 | 0.00 | 21.0% (21/100) | 79 | 94.9% (75/79) | 63.3% (50/79) | 91.1% (72/79) |
| 0.50 | 0.00 | 22.0% (22/100) | 78 | 96.2% (75/78) | 62.8% (49/78) | 91.0% (71/78) |
| 0.60 | 0.00 | 24.0% (24/100) | 76 | 98.7% (75/76) | 63.2% (48/76) | 90.8% (69/76) |
| 0.90 | 0.00 | 32.0% (32/100) | 68 | 100.0% (68/68) | 64.7% (44/68) | 89.7% (61/68) |

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
