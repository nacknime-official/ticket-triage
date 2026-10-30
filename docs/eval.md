# Eval report

Live run of `typesafe/jev-1.14` (served as `typesafe/jev-1.14-20261014`) through `openrouter` on 2026-10-29, over the eval set in `testdata/eval/`.

> **Labels are unreviewed drafts.** An agent drafted every label and no human has reviewed them yet
> (`testdata/eval/README.md`), so every accuracy number here is provisional.

## Complaint examples: before and after

Written by hand; `cmd/eval` does not generate this section, so a re-run drops it. "Before" is a live run of
the same eval set and model on the same day at commit `87939f8`, with the complaint guidelines as they were
then (`-cache` empty, every row called). "After" is this run, every row called again (the questions hash
changed, so the cache missed). The "Complaint" labeling guidelines of `testdata/eval/README.md`, which
`is_complaint` sends word for word, gained two "Not a complaint" sentences:

- A calm question about how to get a refund or compensation is not a complaint.
- A calm, factual report of an error, a crash or something not working is not a complaint in any language,
  even when it stops work, with a Ukrainian example and the same report with dissatisfaction as the contrast.

The category and urgency questions, the thresholds and every label did not change. The label of
`bitext-7ba1793968c9` "i need assistance demanding compensations of my money" (Bitext `get_refund`) was
checked and stays `false`, like its neighbour `bitext-b07d1c5688a4` "is it possible to demand a rebate of my
money?": both are calm requests for help getting money back, and the demand for compensation rule covers a
demand made with dissatisfaction. The urgency before and after of the previous change is in this file at
commit `87939f8`; that run had complaint at 93.0% and this day's "before" run 92.0% with the same questions
and labels, run to run noise.

| Complaint accuracy | before | after |
|---|---|---|
| all | 92.0% (92/100) | 95.0% (95/100) |
| en | 96.0% (48/50) | 96.0% (48/50) |
| uk | 88.0% (44/50) | 94.0% (47/50) |

Confusion, label \ answer, before → after:

| all | false | true |
|---|---|---|
| false | 74 → 77 | 8 → 5 |
| true | 0 → 0 | 18 → 18 |

| en | false | true |
|---|---|---|
| false | 37 → 37 | 2 → 2 |
| true | 0 → 0 | 11 → 11 |

| uk | false | true |
|---|---|---|
| false | 37 → 40 | 6 → 3 |
| true | 0 → 0 | 7 → 7 |

Complaints answered `false` stay at 0, so the new sentences did not make Jev miss a complaint. By tag, complaint
accuracy rises for `surzhyk` (5/6 to 6/6) and `mixed_topics` (4/5 to 5/5); `typos` (7/8) and `vague` (6/7)
stay. The `complaint_uncertain` flag drops from 8 to 7 rows (uk 7 to 5, en 1 to 2).

The rows the error report example fixed, P(complaint) before → after: `gen-uk-012` (a `502` for the whole
team) 0.61 → 0.34, `gen-uk-016` (the app crashes on one tab) 0.53 → 0.24 and `gen-uk-022` (a calendar bug
plus a wish) 0.52 → 0.26. Calm English reports that were already right moved further from the threshold, such
as `gen-en-004` (a `500` from the API) 0.49 → 0.20.

The 5 false positives left:

- `bitext-7ba1793968c9` "demanding compensations of my money" (0.58, `complaint_uncertain`): the refund
  question sentence did not move it below the threshold; its neighbour `bitext-b07d1c5688a4` stays at 0.15.
- `gen-en-006` "Possible data leak" (0.72 → 0.70): another customer's invoice in an email, called "a serious
  privacy issue"; the guidelines do not say whether naming the severity is dissatisfaction.
- `gen-uk-017` pages load slowly (0.65 → 0.51): moved toward the threshold but not past it.
- `gen-uk-019` "something is wrong with the site, it works strangely" (0.71 → 0.58), tagged `vague`.
- `gen-uk-021` webhooks arrive 10–15 minutes late, "not critical, but inconvenient" (0.69 → 0.61): the label
  reads "inconvenient" as neutral.

The other questions did not regress beyond run to run noise: category stays at 94.0% (94/100; en 45/50, uk
49/50) with the same confusion matrix, and priority is 82.0% (82/100; en 39/50, uk 43/50) against 83.0%
before, within one level 99.0% in both, with one more `normal` row answered `low`. The decisions at the
configured thresholds move by one row (`classified` 73 → 72, `category_other` 19, `low_confidence` 8 → 9).
Each call sends about 118 more input tokens (the Ukrainian examples tokenize long), so the cost per ticket
rises from $0.00004955 to $0.00005451.

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
| Complaint | 95.0% (95/100) | 96.0% (48/50) | 94.0% (47/50) |
| Priority | 82.0% (82/100) | 78.0% (39/50) | 86.0% (43/50) |
| Priority, within one level | 99.0% (99/100) | 98.0% (49/50) | 100.0% (50/50) |

### By hard-case tag

A row can carry several tags.

| Tag | Rows | Category | Complaint | Priority |
|---|---|---|---|---|
| `surzhyk` | 6 | 100.0% (6/6) | 100.0% (6/6) | 83.3% (5/6) |
| `typos` | 8 | 100.0% (8/8) | 87.5% (7/8) | 87.5% (7/8) |
| `angry` | 10 | 100.0% (10/10) | 100.0% (10/10) | 90.0% (9/10) |
| `vague` | 7 | 85.7% (6/7) | 85.7% (6/7) | 85.7% (6/7) |
| `mixed_topics` | 5 | 100.0% (5/5) | 100.0% (5/5) | 100.0% (5/5) |
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
| false | 77 | 5 |
| true | 0 | 18 |

#### Complaint en

| label \ answer | false | true |
|---|---|---|
| false | 37 | 2 |
| true | 0 | 11 |

#### Complaint uk

| label \ answer | false | true |
|---|---|---|
| false | 40 | 3 |
| true | 0 | 7 |

### Priority

#### Priority all

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 29 | 2 | 0 | 0 |
| 1 normal | 8 | 29 | 2 | 0 |
| 2 high | 1 | 2 | 14 | 1 |
| 3 critical | 0 | 0 | 2 | 10 |

#### Priority en

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 13 | 1 | 0 | 0 |
| 1 normal | 7 | 17 | 0 | 0 |
| 2 high | 1 | 2 | 5 | 0 |
| 3 critical | 0 | 0 | 0 | 4 |

#### Priority uk

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 16 | 1 | 0 | 0 |
| 1 normal | 1 | 12 | 2 | 0 |
| 2 high | 0 | 0 | 9 | 1 |
| 3 critical | 0 | 0 | 2 | 6 |

## Decisions at the configured thresholds

`classifier.Decide` on each answer; a failed call goes to `needs_human` as in the processor.

| Status | Reason | all | en | uk |
|---|---|---|---|---|
| `classified` | — | 72.0% (72/100) | 70.0% (35/50) | 74.0% (37/50) |
| `needs_human` | `category_other` | 19.0% (19/100) | 18.0% (9/50) | 20.0% (10/50) |
| `needs_human` | `low_confidence` | 9.0% (9/100) | 12.0% (6/50) | 6.0% (3/50) |
| flag | `complaint_uncertain` | 7.0% (7/100) | 4.0% (2/50) | 10.0% (5/50) |

## Latency

Latency of the call behind each answered row, as the classifier measured it; a cached row keeps
the latency of its original call.

| Language | Answered | p50 | p95 | max |
|---|---|---|---|---|
| all | 100 | 206 ms | 369 ms | 796 ms |
| en | 50 | 212 ms | 784 ms | 796 ms |
| uk | 50 | 202 ms | 288 ms | 390 ms |

## Cost

What the calls behind these answers cost, failed calls included; a cached row counts the cost
recorded when it was first classified.

| Total | Per ticket | Input tokens | Output tokens | Cost source |
|---|---|---|---|---|
| $0.005451 | $0.00005451 | 129780 | 8710 | reported 100 |

## Threshold sweep

Each line decides every row again with `classifier.Decide` at other minimum confidences, the
remaining thresholds as configured. `needs_human` is the share of all rows sent to a person, failed calls
included; the accuracy columns score only the auto-classified rows. Bold marks the configured value.

### CategoryMinConfidence (UrgencyMinConfidence 0.50)

| CategoryMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 22.0% (22/100) | 78 | 92.3% (72/78) | 82.1% (64/78) | 94.9% (74/78) |
| 0.05 | 22.0% (22/100) | 78 | 92.3% (72/78) | 82.1% (64/78) | 94.9% (74/78) |
| 0.10 | 22.0% (22/100) | 78 | 92.3% (72/78) | 82.1% (64/78) | 94.9% (74/78) |
| 0.15 | 22.0% (22/100) | 78 | 92.3% (72/78) | 82.1% (64/78) | 94.9% (74/78) |
| 0.20 | 22.0% (22/100) | 78 | 92.3% (72/78) | 82.1% (64/78) | 94.9% (74/78) |
| 0.25 | 22.0% (22/100) | 78 | 92.3% (72/78) | 82.1% (64/78) | 94.9% (74/78) |
| 0.30 | 22.0% (22/100) | 78 | 92.3% (72/78) | 82.1% (64/78) | 94.9% (74/78) |
| 0.35 | 22.0% (22/100) | 78 | 92.3% (72/78) | 82.1% (64/78) | 94.9% (74/78) |
| 0.40 | 22.0% (22/100) | 78 | 92.3% (72/78) | 82.1% (64/78) | 94.9% (74/78) |
| 0.45 | 25.0% (25/100) | 75 | 96.0% (72/75) | 82.7% (62/75) | 96.0% (72/75) |
| 0.50 | 27.0% (27/100) | 73 | 98.6% (72/73) | 82.2% (60/73) | 95.9% (70/73) |
| 0.55 | 27.0% (27/100) | 73 | 98.6% (72/73) | 82.2% (60/73) | 95.9% (70/73) |
| **0.60** | 28.0% (28/100) | 72 | 98.6% (71/72) | 81.9% (59/72) | 95.8% (69/72) |
| 0.65 | 29.0% (29/100) | 71 | 98.6% (70/71) | 81.7% (58/71) | 95.8% (68/71) |
| 0.70 | 31.0% (31/100) | 69 | 98.6% (68/69) | 82.6% (57/69) | 95.7% (66/69) |
| 0.75 | 33.0% (33/100) | 67 | 98.5% (66/67) | 83.6% (56/67) | 95.5% (64/67) |
| 0.80 | 34.0% (34/100) | 66 | 98.5% (65/66) | 84.8% (56/66) | 95.5% (63/66) |
| 0.85 | 34.0% (34/100) | 66 | 98.5% (65/66) | 84.8% (56/66) | 95.5% (63/66) |
| 0.90 | 35.0% (35/100) | 65 | 100.0% (65/65) | 84.6% (55/65) | 95.4% (62/65) |
| 0.95 | 41.0% (41/100) | 59 | 100.0% (59/59) | 83.1% (49/59) | 94.9% (56/59) |
| 1.00 | 56.0% (56/100) | 44 | 100.0% (44/44) | 86.4% (38/44) | 95.5% (42/44) |

### UrgencyMinConfidence (CategoryMinConfidence 0.60)

| UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 25.0% (25/100) | 75 | 98.7% (74/75) | 81.3% (61/75) | 94.7% (71/75) |
| 0.05 | 25.0% (25/100) | 75 | 98.7% (74/75) | 81.3% (61/75) | 94.7% (71/75) |
| 0.10 | 25.0% (25/100) | 75 | 98.7% (74/75) | 81.3% (61/75) | 94.7% (71/75) |
| 0.15 | 25.0% (25/100) | 75 | 98.7% (74/75) | 81.3% (61/75) | 94.7% (71/75) |
| 0.20 | 25.0% (25/100) | 75 | 98.7% (74/75) | 81.3% (61/75) | 94.7% (71/75) |
| 0.25 | 25.0% (25/100) | 75 | 98.7% (74/75) | 81.3% (61/75) | 94.7% (71/75) |
| 0.30 | 25.0% (25/100) | 75 | 98.7% (74/75) | 81.3% (61/75) | 94.7% (71/75) |
| 0.35 | 25.0% (25/100) | 75 | 98.7% (74/75) | 81.3% (61/75) | 94.7% (71/75) |
| 0.40 | 25.0% (25/100) | 75 | 98.7% (74/75) | 81.3% (61/75) | 94.7% (71/75) |
| 0.45 | 26.0% (26/100) | 74 | 98.6% (73/74) | 81.1% (60/74) | 94.6% (70/74) |
| **0.50** | 28.0% (28/100) | 72 | 98.6% (71/72) | 81.9% (59/72) | 95.8% (69/72) |
| 0.55 | 30.0% (30/100) | 70 | 98.6% (69/70) | 84.3% (59/70) | 95.7% (67/70) |
| 0.60 | 32.0% (32/100) | 68 | 98.5% (67/68) | 85.3% (58/68) | 95.6% (65/68) |
| 0.65 | 33.0% (33/100) | 67 | 98.5% (66/67) | 86.6% (58/67) | 95.5% (64/67) |
| 0.70 | 35.0% (35/100) | 65 | 98.5% (64/65) | 89.2% (58/65) | 95.4% (62/65) |
| 0.75 | 37.0% (37/100) | 63 | 98.4% (62/63) | 88.9% (56/63) | 95.2% (60/63) |
| 0.80 | 38.0% (38/100) | 62 | 98.4% (61/62) | 90.3% (56/62) | 95.2% (59/62) |
| 0.85 | 47.0% (47/100) | 53 | 100.0% (53/53) | 92.5% (49/53) | 94.3% (50/53) |
| 0.90 | 54.0% (54/100) | 46 | 100.0% (46/46) | 97.8% (45/46) | 93.5% (43/46) |
| 0.95 | 58.0% (58/100) | 42 | 100.0% (42/42) | 97.6% (41/42) | 92.9% (39/42) |
| 1.00 | 91.0% (91/100) | 9 | 100.0% (9/9) | 100.0% (9/9) | 88.9% (8/9) |

### Best trade-offs on the full grid

Both thresholds over the 0.05 grid; a pair is listed when no other pair sends as few or fewer rows to
`needs_human` with as high or higher category accuracy on the auto-classified rows. Pairs with the same
result are shown once, with the lowest thresholds.

| CategoryMinConfidence | UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|---|
| 0.00 | 0.00 | 19.0% (19/100) | 81 | 92.6% (75/81) | 81.5% (66/81) | 93.8% (76/81) |
| 0.45 | 0.00 | 22.0% (22/100) | 78 | 96.2% (75/78) | 82.1% (64/78) | 94.9% (74/78) |
| 0.50 | 0.00 | 24.0% (24/100) | 76 | 98.7% (75/76) | 81.6% (62/76) | 94.7% (72/76) |
| 0.90 | 0.00 | 32.0% (32/100) | 68 | 100.0% (68/68) | 83.8% (57/68) | 94.1% (64/68) |

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
