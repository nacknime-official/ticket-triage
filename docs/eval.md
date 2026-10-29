# Eval report

Live run of `typesafe/jev-1.14` (served as `typesafe/jev-1.14-20261014`) through `openrouter` on 2026-10-29, over the eval set in `testdata/eval/`.

> **Labels are unreviewed drafts.** An agent drafted every label and no human has reviewed them yet
> (`testdata/eval/README.md`), so every accuracy number here is provisional.

## Urgency criteria: before and after

Written by hand; `cmd/eval` does not generate this section, so a re-run drops it. "Before" is the report of
the same eval set and model at commit `b4c631c`, when `urgency` sent only the instruction "How urgent is the
request in `subject` and `body` for the customer?" and one short line per level. "After" is this run
(`-refresh`, every row called again): the question now sends the "Urgency" labeling guidelines of
`testdata/eval/README.md`, the "All levels" rules after the instruction and each level's list as its
criterion. Labels, thresholds and the category and complaint questions did not change. The complaint
before and after of the previous change is in this file at commit `b4c631c`.

| Priority accuracy | before | after |
|---|---|---|
| all, exact | 67.0% (67/100) | 84.0% (84/100) |
| en, exact | 60.0% (30/50) | 82.0% (41/50) |
| uk, exact | 74.0% (37/50) | 86.0% (43/50) |
| all, within one level | 99.0% (99/100) | 99.0% (99/100) |
| en, within one level | 98.0% (49/50) | 98.0% (49/50) |
| uk, within one level | 100.0% (50/50) | 100.0% (50/50) |

Confusion, label \ answer, before → after:

| all | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 23 → 29 | 8 → 2 | 0 → 0 | 0 → 0 |
| 1 normal | 8 → 6 | 21 → 31 | 10 → 2 | 0 → 0 |
| 2 high | 1 → 1 | 2 → 2 | 12 → 14 | 3 → 1 |
| 3 critical | 0 → 0 | 0 → 0 | 1 → 2 | 11 → 10 |

| en | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 10 → 13 | 4 → 1 | 0 → 0 | 0 → 0 |
| 1 normal | 7 → 5 | 12 → 19 | 5 → 0 | 0 → 0 |
| 2 high | 1 → 1 | 2 → 2 | 4 → 5 | 1 → 0 |
| 3 critical | 0 → 0 | 0 → 0 | 0 → 0 | 4 → 4 |

| uk | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 13 → 16 | 4 → 1 | 0 → 0 | 0 → 0 |
| 1 normal | 1 → 1 | 9 → 12 | 5 → 2 | 0 → 0 |
| 2 high | 0 → 0 | 0 → 0 | 8 → 9 | 2 → 1 |
| 3 critical | 0 → 0 | 0 → 0 | 1 → 2 | 7 → 6 |

| Tag | Rows | Priority before | Priority after |
|---|---|---|---|
| `surzhyk` | 6 | 66.7% (4/6) | 83.3% (5/6) |
| `typos` | 8 | 75.0% (6/8) | 87.5% (7/8) |
| `angry` | 10 | 60.0% (6/10) | 90.0% (9/10) |
| `vague` | 7 | 85.7% (6/7) | 85.7% (6/7) |
| `mixed_topics` | 5 | 80.0% (4/5) | 100.0% (5/5) |
| `translit` | 1 | 100.0% (1/1) | 100.0% (1/1) |
| `code_switching` | 1 | 100.0% (1/1) | 100.0% (1/1) |

Answers above the label drop from 21 to 5 rows (normal rated high from 10 to 2, low rated normal from 8 to
2); answers below the label go from 12 to 11. The other questions did not regress: category stays at 94.0%
(94/100; en 45/50, uk 49/50) with the same confusion matrix, and complaint moves from 92.0% to 93.0% (en
48/50 both times, uk 44/50 to 45/50), inside the run to run noise. `low_confidence` drops from 11 to 8 rows,
so `classified` rises from 70 to 73. Each call sends about 423 more input tokens, so the cost per ticket rises
from $0.00003179 to $0.00004955.

The 16 rows still off:

- Normal rated low (6): Bitext how-to requests the intent table labels normal, `bitext-372f4d57b044`
  (account removal), `bitext-9ca165782526` (edit my account), `bitext-abf7092325c0` (key retrieval),
  `bitext-be841d0a84e3` (speak with a person), `bitext-c3d2103d3f37` (where to lodge a complaint), and
  `gen-uk-049` "I have a question, call me". The guidelines name none of these requests.
- Low rated normal (2): `bitext-bed9e2b91309` "problem with opening a Business account", which the Bitext
  notes above already flag for review, and `gen-uk-036`, a feature request that says one-by-one export
  "takes long", close to the "already costs the customer work" rule.
- Normal rated high (2): `gen-uk-005`, an invoice the accounting team needs "by the end of the week", which
  the stated-deadline rule makes high, so the reviewer should look at its label; `gen-uk-011`, wrong details
  on an invoice the accounting team cannot process.
- High rated lower (3): the Bitext `payment_issue` rows `bitext-4c97808eaa99`, `bitext-e3fc240fdc48` and
  `bitext-04652dda5305` ("how to report an issue with online payments"), which read as how-to questions.
- High rated critical (1): `gen-uk-029`, a stolen phone with the 2FA codes and no backup codes.
- Critical rated high (2): `gen-uk-024`, a `500` on login for "me and my colleagues", and `gen-uk-013`,
  "everything is down for the third time this week" in caps.

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
| Complaint | 93.0% (93/100) | 96.0% (48/50) | 90.0% (45/50) |
| Priority | 84.0% (84/100) | 82.0% (41/50) | 86.0% (43/50) |
| Priority, within one level | 99.0% (99/100) | 98.0% (49/50) | 100.0% (50/50) |

### By hard-case tag

A row can carry several tags.

| Tag | Rows | Category | Complaint | Priority |
|---|---|---|---|---|
| `surzhyk` | 6 | 100.0% (6/6) | 83.3% (5/6) | 83.3% (5/6) |
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
| false | 75 | 7 |
| true | 0 | 18 |

#### Complaint en

| label \ answer | false | true |
|---|---|---|
| false | 37 | 2 |
| true | 0 | 11 |

#### Complaint uk

| label \ answer | false | true |
|---|---|---|
| false | 38 | 5 |
| true | 0 | 7 |

### Priority

#### Priority all

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 29 | 2 | 0 | 0 |
| 1 normal | 6 | 31 | 2 | 0 |
| 2 high | 1 | 2 | 14 | 1 |
| 3 critical | 0 | 0 | 2 | 10 |

#### Priority en

| label \ answer | 0 low | 1 normal | 2 high | 3 critical |
|---|---|---|---|---|
| 0 low | 13 | 1 | 0 | 0 |
| 1 normal | 5 | 19 | 0 | 0 |
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
| `classified` | — | 73.0% (73/100) | 72.0% (36/50) | 74.0% (37/50) |
| `needs_human` | `category_other` | 19.0% (19/100) | 18.0% (9/50) | 20.0% (10/50) |
| `needs_human` | `low_confidence` | 8.0% (8/100) | 10.0% (5/50) | 6.0% (3/50) |
| flag | `complaint_uncertain` | 9.0% (9/100) | 4.0% (2/50) | 14.0% (7/50) |

## Latency

Latency of the call behind each answered row, as the classifier measured it; a cached row keeps
the latency of its original call.

| Language | Answered | p50 | p95 | max |
|---|---|---|---|---|
| all | 100 | 225 ms | 276 ms | 433 ms |
| en | 50 | 224 ms | 281 ms | 433 ms |
| uk | 50 | 225 ms | 276 ms | 290 ms |

## Cost

What the calls behind these answers cost, failed calls included; a cached row counts the cost
recorded when it was first classified.

| Total | Per ticket | Input tokens | Output tokens | Cost source |
|---|---|---|---|---|
| $0.004955 | $0.00004955 | 117980 | 8710 | reported 100 |

## Threshold sweep

Each line decides every row again with `classifier.Decide` at other minimum confidences, the
remaining thresholds as configured. `needs_human` is the share of all rows sent to a person, failed calls
included; the accuracy columns score only the auto-classified rows. Bold marks the configured value.

### CategoryMinConfidence (UrgencyMinConfidence 0.50)

| CategoryMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 21.0% (21/100) | 79 | 92.4% (73/79) | 84.8% (67/79) | 91.1% (72/79) |
| 0.05 | 21.0% (21/100) | 79 | 92.4% (73/79) | 84.8% (67/79) | 91.1% (72/79) |
| 0.10 | 21.0% (21/100) | 79 | 92.4% (73/79) | 84.8% (67/79) | 91.1% (72/79) |
| 0.15 | 21.0% (21/100) | 79 | 92.4% (73/79) | 84.8% (67/79) | 91.1% (72/79) |
| 0.20 | 21.0% (21/100) | 79 | 92.4% (73/79) | 84.8% (67/79) | 91.1% (72/79) |
| 0.25 | 21.0% (21/100) | 79 | 92.4% (73/79) | 84.8% (67/79) | 91.1% (72/79) |
| 0.30 | 21.0% (21/100) | 79 | 92.4% (73/79) | 84.8% (67/79) | 91.1% (72/79) |
| 0.35 | 21.0% (21/100) | 79 | 92.4% (73/79) | 84.8% (67/79) | 91.1% (72/79) |
| 0.40 | 21.0% (21/100) | 79 | 92.4% (73/79) | 84.8% (67/79) | 91.1% (72/79) |
| 0.45 | 22.0% (22/100) | 78 | 93.6% (73/78) | 84.6% (66/78) | 91.0% (71/78) |
| 0.50 | 23.0% (23/100) | 77 | 94.8% (73/77) | 84.4% (65/77) | 90.9% (70/77) |
| 0.55 | 25.0% (25/100) | 75 | 97.3% (73/75) | 84.0% (63/75) | 92.0% (69/75) |
| **0.60** | 27.0% (27/100) | 73 | 98.6% (72/73) | 83.6% (61/73) | 91.8% (67/73) |
| 0.65 | 27.0% (27/100) | 73 | 98.6% (72/73) | 83.6% (61/73) | 91.8% (67/73) |
| 0.70 | 29.0% (29/100) | 71 | 98.6% (70/71) | 83.1% (59/71) | 91.5% (65/71) |
| 0.75 | 31.0% (31/100) | 69 | 98.6% (68/69) | 85.5% (59/69) | 91.3% (63/69) |
| 0.80 | 33.0% (33/100) | 67 | 98.5% (66/67) | 86.6% (58/67) | 91.0% (61/67) |
| 0.85 | 33.0% (33/100) | 67 | 98.5% (66/67) | 86.6% (58/67) | 91.0% (61/67) |
| 0.90 | 36.0% (36/100) | 64 | 100.0% (64/64) | 85.9% (55/64) | 90.6% (58/64) |
| 0.95 | 39.0% (39/100) | 61 | 100.0% (61/61) | 85.2% (52/61) | 90.2% (55/61) |
| 1.00 | 56.0% (56/100) | 44 | 100.0% (44/44) | 86.4% (38/44) | 90.9% (40/44) |

### UrgencyMinConfidence (CategoryMinConfidence 0.60)

| UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|
| 0.00 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 92.0% (69/75) |
| 0.05 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 92.0% (69/75) |
| 0.10 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 92.0% (69/75) |
| 0.15 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 92.0% (69/75) |
| 0.20 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 92.0% (69/75) |
| 0.25 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 92.0% (69/75) |
| 0.30 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 92.0% (69/75) |
| 0.35 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 92.0% (69/75) |
| 0.40 | 26.0% (26/100) | 74 | 98.6% (73/74) | 82.4% (61/74) | 91.9% (68/74) |
| 0.45 | 26.0% (26/100) | 74 | 98.6% (73/74) | 82.4% (61/74) | 91.9% (68/74) |
| **0.50** | 27.0% (27/100) | 73 | 98.6% (72/73) | 83.6% (61/73) | 91.8% (67/73) |
| 0.55 | 29.0% (29/100) | 71 | 98.6% (70/71) | 83.1% (59/71) | 93.0% (66/71) |
| 0.60 | 34.0% (34/100) | 66 | 98.5% (65/66) | 87.9% (58/66) | 92.4% (61/66) |
| 0.65 | 34.0% (34/100) | 66 | 98.5% (65/66) | 87.9% (58/66) | 92.4% (61/66) |
| 0.70 | 36.0% (36/100) | 64 | 98.4% (63/64) | 89.1% (57/64) | 93.8% (60/64) |
| 0.75 | 36.0% (36/100) | 64 | 98.4% (63/64) | 89.1% (57/64) | 93.8% (60/64) |
| 0.80 | 38.0% (38/100) | 62 | 98.4% (61/62) | 88.7% (55/62) | 93.5% (58/62) |
| 0.85 | 50.0% (50/100) | 50 | 100.0% (50/50) | 96.0% (48/50) | 92.0% (46/50) |
| 0.90 | 55.0% (55/100) | 45 | 100.0% (45/45) | 97.8% (44/45) | 91.1% (41/45) |
| 0.95 | 57.0% (57/100) | 43 | 100.0% (43/43) | 97.7% (42/43) | 90.7% (39/43) |
| 1.00 | 88.0% (88/100) | 12 | 100.0% (12/12) | 100.0% (12/12) | 83.3% (10/12) |

### Best trade-offs on the full grid

Both thresholds over the 0.05 grid; a pair is listed when no other pair sends as few or fewer rows to
`needs_human` with as high or higher category accuracy on the auto-classified rows. Pairs with the same
result are shown once, with the lowest thresholds.

| CategoryMinConfidence | UrgencyMinConfidence | needs_human | Auto-classified | Category | Priority | Complaint |
|---|---|---|---|---|---|---|
| 0.00 | 0.00 | 19.0% (19/100) | 81 | 92.6% (75/81) | 84.0% (68/81) | 91.4% (74/81) |
| 0.45 | 0.00 | 20.0% (20/100) | 80 | 93.8% (75/80) | 83.8% (67/80) | 91.2% (73/80) |
| 0.50 | 0.00 | 21.0% (21/100) | 79 | 94.9% (75/79) | 83.5% (66/79) | 91.1% (72/79) |
| 0.55 | 0.00 | 23.0% (23/100) | 77 | 97.4% (75/77) | 83.1% (64/77) | 92.2% (71/77) |
| 0.60 | 0.00 | 25.0% (25/100) | 75 | 98.7% (74/75) | 82.7% (62/75) | 92.0% (69/75) |
| 0.90 | 0.00 | 34.0% (34/100) | 66 | 100.0% (66/66) | 84.8% (56/66) | 90.9% (60/66) |

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
