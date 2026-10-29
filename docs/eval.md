# Eval report

Live run of `typesafe/jev-1.14` (served as `typesafe/jev-1.14-20261014`) through `openrouter` on 2026-10-29, over the eval set in `testdata/eval/`.

> **Labels are unreviewed drafts.** An agent drafted every label and no human has reviewed them yet
> (`testdata/eval/README.md`), so every accuracy number here is provisional.

## Complaint example and label fix: before and after

Written by hand; `cmd/eval` does not generate this section, so a re-run drops it. "Before" is a live run of
the same eval set and model on the same day at commit `87939f8`, with the complaint guidelines as they were
then (`-cache` empty, every row called). "After" is this run, every row called again (the questions hash
changed, so the cache missed). Two changes sit between them:

- The "Complaint" labeling guidelines of `testdata/eval/README.md`, which `is_complaint` sends word for word,
  gained one "Not a complaint" sentence: a calm, factual report of an error, a crash or something not
  working is not a complaint in any language, even when it stops work, with a Ukrainian example and the same
  report with dissatisfaction as the contrast.
- `bitext-7ba1793968c9` "i need assistance demanding compensations of my money" (Bitext `get_refund`) is now
  labeled a complaint: the guidelines count a demand for compensation as a complaint, and the intent-based
  draft had labeled it `false`.

The category and urgency questions, the thresholds and every other label did not change. The urgency before
and after of the previous change is in this file at commit `87939f8`; that run had complaint at 93.0% and
this day's "before" run 92.0% with the same questions and labels, run to run noise.

The middle column scores the "before" answers against the new label, so it separates the two changes: the
label fix accounts for one row, the guidelines example for four.

| Complaint accuracy | before | before, new label | after |
|---|---|---|---|
| all | 92.0% (92/100) | 93.0% (93/100) | 97.0% (97/100) |
| en | 96.0% (48/50) | 98.0% (49/50) | 98.0% (49/50) |
| uk | 88.0% (44/50) | 88.0% (44/50) | 96.0% (48/50) |

Confusion, label \ answer, before → after (the "before" counts with the old label of
`bitext-7ba1793968c9`, which moves one row from `false` to `true`):

| all | false | true |
|---|---|---|
| false | 74 → 78 | 8 → 3 |
| true | 0 → 0 | 18 → 19 |

| en | false | true |
|---|---|---|
| false | 37 → 37 | 2 → 1 |
| true | 0 → 0 | 11 → 12 |

| uk | false | true |
|---|---|---|
| false | 37 → 41 | 6 → 2 |
| true | 0 → 0 | 7 → 7 |

Complaints answered `false` stay at 0, so the new sentence did not make Jev miss a complaint. By tag, complaint
accuracy rises for `surzhyk` (5/6 to 6/6), `typos` (7/8 to 8/8) and `mixed_topics` (4/5 to 5/5); `vague`
stays at 6/7. The `complaint_uncertain` flag drops from 8 to 6
rows (uk 7 to 5).

The rows the example fixed, P(complaint) before → after: `gen-uk-012` (a `502` for the whole team) 0.61 →
0.34, `gen-uk-016` (the app crashes on one tab) 0.53 → 0.23, `gen-uk-017` (pages load slowly) 0.65 → 0.48 and
`gen-uk-022` (a calendar bug plus a wish) 0.52 → 0.26. Calm English reports that were already right moved
further from the threshold, such as `gen-en-004` (a `500` from the API) 0.49 → 0.21.

The 3 false positives left:

- `gen-en-006` "Possible data leak" (0.72 → 0.71): another customer's invoice in an email, called "a serious
  privacy issue"; the guidelines do not say whether naming the severity is dissatisfaction.
- `gen-uk-019` "something is wrong with the site, it works strangely" (0.71 → 0.58), tagged `vague`.
- `gen-uk-021` webhooks arrive 10–15 minutes late, "not critical, but inconvenient" (0.69 → 0.59): the label
  reads "inconvenient" as neutral.

The other questions did not regress: on every row the category and the priority answer are the same as before,
so category stays at 94.0% (94/100; en 45/50, uk 49/50) and priority at 83.0% (83/100; en 40/50, uk 43/50),
within one level 99.0%, with the same confusion matrices, and the decisions at the configured thresholds are
unchanged (`classified` 73, `category_other` 19, `low_confidence` 8). Priority is 84.0% in the report at commit
`87939f8` and 83.0% in both runs of this day, run to run noise. Each call sends about 102 more input tokens
(the Ukrainian examples tokenize long), so the cost per ticket rises from $0.00004955 to $0.00005384.

## Run

| Setting | Value |
|---|---|
| Classifier | `jev` (provider `openrouter`, model `typesafe/jev-1.14`) |
| Date | 2026-10-29 |
| Served model | `typesafe/jev-1.14-20261014` |
| Eval set | `bitext.jsonl` (30 rows, sha256 `ffb913f4d65f`), `generated.jsonl` (70 rows, sha256 `420bcf4501ea`) |
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
| Complaint | 97.0% (97/100) | 98.0% (49/50) | 96.0% (48/50) |
| Priority | 83.0% (83/100) | 80.0% (40/50) | 86.0% (43/50) |
| Priority, within one level | 99.0% (99/100) | 98.0% (49/50) | 100.0% (50/50) |

### By hard-case tag

A row can carry several tags.

| Tag | Rows | Category | Complaint | Priority |
|---|---|---|---|---|
| `surzhyk` | 6 | 100.0% (6/6) | 100.0% (6/6) | 83.3% (5/6) |
| `typos` | 8 | 100.0% (8/8) | 100.0% (8/8) | 87.5% (7/8) |
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
| false | 78 | 3 |
| true | 0 | 19 |

#### Complaint en

| label \ answer | false | true |
|---|---|---|
| false | 37 | 1 |
| true | 0 | 12 |

#### Complaint uk

| label \ answer | false | true |
|---|---|---|
| false | 41 | 2 |
| true | 0 | 7 |

### Priority

#### Priority all

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 29 | 2 | 0 | 0 |
| 1 normal | 7 | 30 | 2 | 0 |
| 2 high | 1 | 2 | 14 | 1 |
| 3 critical | 0 | 0 | 2 | 10 |

#### Priority en

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 13 | 1 | 0 | 0 |
| 1 normal | 6 | 18 | 0 | 0 |
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
| `classified` | — | 73.0% (73/100) | 70.0% (35/50) | 76.0% (38/50) |
| `needs_human` | `category_other` | 19.0% (19/100) | 18.0% (9/50) | 20.0% (10/50) |
| `needs_human` | `low_confidence` | 8.0% (8/100) | 12.0% (6/50) | 4.0% (2/50) |
| flag | `complaint_uncertain` | 6.0% (6/100) | 2.0% (1/50) | 10.0% (5/50) |

## Latency

Latency of the call behind each answered row, as the classifier measured it; a cached row keeps
the latency of its original call.

| Language | Answered | p50 | p95 | max |
|---|---|---|---|---|
| all | 100 | 222 ms | 341 ms | 1319 ms |
| en | 50 | 226 ms | 843 ms | 1319 ms |
| uk | 50 | 218 ms | 304 ms | 341 ms |

## Cost

What the calls behind these answers cost, failed calls included; a cached row counts the cost
recorded when it was first classified.

| Total | Per ticket | Input tokens | Output tokens | Cost source |
|---|---|---|---|---|
| $0.005384 | $0.00005384 | 128180 | 8710 | reported 100 |

## Threshold sweep

Each line decides every row again with `classifier.Decide` at other minimum confidences, the
remaining thresholds as configured. `needs_human` is the share of all rows sent to a person, failed calls
included; the accuracy columns score only the auto-classified rows. Bold marks the configured value.

### CategoryMinConfidence (UrgencyMinConfidence 0.50)

| CategoryMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 21.0% (21/100) | 79 | 92.4% (73/79) | 83.5% (66/79) | 96.2% (76/79) |
| 0.05 | 21.0% (21/100) | 79 | 92.4% (73/79) | 83.5% (66/79) | 96.2% (76/79) |
| 0.10 | 21.0% (21/100) | 79 | 92.4% (73/79) | 83.5% (66/79) | 96.2% (76/79) |
| 0.15 | 21.0% (21/100) | 79 | 92.4% (73/79) | 83.5% (66/79) | 96.2% (76/79) |
| 0.20 | 21.0% (21/100) | 79 | 92.4% (73/79) | 83.5% (66/79) | 96.2% (76/79) |
| 0.25 | 21.0% (21/100) | 79 | 92.4% (73/79) | 83.5% (66/79) | 96.2% (76/79) |
| 0.30 | 21.0% (21/100) | 79 | 92.4% (73/79) | 83.5% (66/79) | 96.2% (76/79) |
| 0.35 | 21.0% (21/100) | 79 | 92.4% (73/79) | 83.5% (66/79) | 96.2% (76/79) |
| 0.40 | 22.0% (22/100) | 78 | 93.6% (73/78) | 83.3% (65/78) | 96.2% (75/78) |
| 0.45 | 22.0% (22/100) | 78 | 93.6% (73/78) | 83.3% (65/78) | 96.2% (75/78) |
| 0.50 | 22.0% (22/100) | 78 | 93.6% (73/78) | 83.3% (65/78) | 96.2% (75/78) |
| 0.55 | 26.0% (26/100) | 74 | 98.6% (73/74) | 83.8% (62/74) | 97.3% (72/74) |
| **0.60** | 27.0% (27/100) | 73 | 98.6% (72/73) | 83.6% (61/73) | 97.3% (71/73) |
| 0.65 | 28.0% (28/100) | 72 | 98.6% (71/72) | 83.3% (60/72) | 97.2% (70/72) |
| 0.70 | 28.0% (28/100) | 72 | 98.6% (71/72) | 83.3% (60/72) | 97.2% (70/72) |
| 0.75 | 31.0% (31/100) | 69 | 98.6% (68/69) | 84.1% (58/69) | 97.1% (67/69) |
| 0.80 | 33.0% (33/100) | 67 | 98.5% (66/67) | 86.6% (58/67) | 97.0% (65/67) |
| 0.85 | 33.0% (33/100) | 67 | 98.5% (66/67) | 86.6% (58/67) | 97.0% (65/67) |
| 0.90 | 34.0% (34/100) | 66 | 100.0% (66/66) | 86.4% (57/66) | 97.0% (64/66) |
| 0.95 | 40.0% (40/100) | 60 | 100.0% (60/60) | 85.0% (51/60) | 96.7% (58/60) |
| 1.00 | 54.0% (54/100) | 46 | 100.0% (46/46) | 87.0% (40/46) | 97.8% (45/46) |

### UrgencyMinConfidence (CategoryMinConfidence 0.60)

| UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 97.3% (73/75) |
| 0.05 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 97.3% (73/75) |
| 0.10 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 97.3% (73/75) |
| 0.15 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 97.3% (73/75) |
| 0.20 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 97.3% (73/75) |
| 0.25 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 97.3% (73/75) |
| 0.30 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 97.3% (73/75) |
| 0.35 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 97.3% (73/75) |
| 0.40 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 97.3% (73/75) |
| 0.45 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 97.3% (73/75) |
| **0.50** | 27.0% (27/100) | 73 | 98.6% (72/73) | 83.6% (61/73) | 97.3% (71/73) |
| 0.55 | 29.0% (29/100) | 71 | 98.6% (70/71) | 83.1% (59/71) | 97.2% (69/71) |
| 0.60 | 32.0% (32/100) | 68 | 98.5% (67/68) | 85.3% (58/68) | 97.1% (66/68) |
| 0.65 | 33.0% (33/100) | 67 | 98.5% (66/67) | 86.6% (58/67) | 97.0% (65/67) |
| 0.70 | 35.0% (35/100) | 65 | 98.5% (64/65) | 89.2% (58/65) | 96.9% (63/65) |
| 0.75 | 36.0% (36/100) | 64 | 98.4% (63/64) | 89.1% (57/64) | 96.9% (62/64) |
| 0.80 | 38.0% (38/100) | 62 | 98.4% (61/62) | 88.7% (55/62) | 96.8% (60/62) |
| 0.85 | 46.0% (46/100) | 54 | 98.1% (53/54) | 94.4% (51/54) | 96.3% (52/54) |
| 0.90 | 54.0% (54/100) | 46 | 100.0% (46/46) | 97.8% (45/46) | 95.7% (44/46) |
| 0.95 | 59.0% (59/100) | 41 | 100.0% (41/41) | 97.6% (40/41) | 95.1% (39/41) |
| 1.00 | 90.0% (90/100) | 10 | 100.0% (10/10) | 100.0% (10/10) | 90.0% (9/10) |

### Best trade-offs on the full grid

Both thresholds over the 0.05 grid; a pair is listed when no other pair sends as few or fewer rows to
`needs_human` with as high or higher category accuracy on the auto-classified rows. Pairs with the same
result are shown once, with the lowest thresholds.

| CategoryMinConfidence | UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|---|
| 0.00 | 0.00 | 19.0% (19/100) | 81 | 92.6% (75/81) | 82.7% (67/81) | 96.3% (78/81) |
| 0.40 | 0.00 | 20.0% (20/100) | 80 | 93.8% (75/80) | 82.5% (66/80) | 96.2% (77/80) |
| 0.55 | 0.00 | 24.0% (24/100) | 76 | 98.7% (75/76) | 82.9% (63/76) | 97.4% (74/76) |
| 0.90 | 0.00 | 32.0% (32/100) | 68 | 100.0% (68/68) | 85.3% (58/68) | 97.1% (66/68) |

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
