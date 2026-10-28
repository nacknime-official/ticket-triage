# Eval set

Labeled tickets for `cmd/eval` (T23). English and Ukrainian, about 100 rows.

> **Status: labels are unreviewed drafts.** An agent drafted every label in this directory. The human
> reviewer has not checked them yet. Until the review is done, treat accuracy numbers computed on this set as
> provisional. When the review is done, replace this note with the reviewer and the date.

| File | Rows | Source | License |
|---|---|---|---|
| `generated.jsonl` | 70 (50 `uk`, 20 `en`) | written for this project, `source: generated` | same as the repository |
| `bitext.jsonl` | 30 (`en`) | sampled from the Bitext dataset by `cmd/eval-import`, `source: bitext` | CDLA-Sharing-1.0, see below |
| `raw/` | — | downloaded dataset files, git-ignored | upstream license |

No real customer data is in this directory, and none is sent anywhere: Bitext is a synthetic dataset, and the
generated rows are invented.

## Row schema

One JSON object per line:

| Field | Type | Values |
|---|---|---|
| `id` | string | unique across all files: `gen-<lang>-NNN` or `bitext-<hash>` |
| `source` | string | `generated` or `bitext` |
| `lang` | string | `en` or `uk` |
| `subject` | string | 1–200 characters, as in `POST /tickets` |
| `body` | string | 1–5000 characters, as in `POST /tickets` |
| `expected_category` | string | `billing`, `technical`, `account`, `feature_request`, `other` |
| `expected_complaint` | bool | `true` if the customer expresses dissatisfaction |
| `expected_urgency` | int | `0` low, `1` normal, `2` high, `3` critical |
| `tags` | string array, optional | hard-case kinds: `surzhyk`, `typos`, `angry`, `vague`, `mixed_topics`, `translit`, `code_switching` |

Only `subject` and `body` may be sent to a classifier. The other fields are the answer key and metadata.
`internal/evalset` holds the schema, a reader, and a test that validates every committed row.

## Dataset license check

Checked on 2026-10-28. The import tool must work without an account or credentials, and committed rows need a
license that allows redistribution of derived data.

| Dataset | Source | License | Redistribution | Derivatives | Download needs an account | Real customer data | Decision |
|---|---|---|---|---|---|---|---|
| Bitext Customer Support LLM Chatbot Training Dataset | https://huggingface.co/datasets/bitext/Bitext-customer-support-llm-chatbot-training-dataset | CDLA-Sharing-1.0 | yes, under the same license with its text and attribution | yes; files with changes must say they were changed | no (public, not gated) | no, hybrid synthetic | **chosen** |
| Customer Support on Twitter (Kaggle) | https://www.kaggle.com/datasets/thoughtvector/customer-support-on-twitter | CC BY-NC-SA 4.0 | only non-commercial, share-alike | non-commercial, share-alike | yes, Kaggle account and API token | yes, real public tweets with user handles | rejected: credentials, real personal data, NC |
| Customer Support Tickets (Tobi-Bueck) | https://huggingface.co/datasets/Tobi-Bueck/customer-support-tickets | CC BY-NC 4.0 | only non-commercial, with attribution | non-commercial | no | no, synthetic | rejected: NC; closest fit otherwise (subject, body, queue, priority; `en`, `de`) |
| Classification of IT Support Tickets (tasksource) | https://huggingface.co/datasets/tasksource/it-support-tickets | CC BY-4.0 as declared | as declared | as declared | no | no, synthetic | rejected: looks like a re-upload of the CC BY-NC dataset above under a looser license, so provenance is unclear |

No public Ukrainian support-ticket dataset with a usable license turned up on Hugging Face, so every Ukrainian
row is generated.

### Bitext terms that apply here

CDLA-Sharing-1.0 allows using, changing and publishing the data. Publishing the data or a changed version
requires:

1. publishing it under CDLA-Sharing-1.0, unmodified, with the agreement text: `bitext.LICENSE.txt`;
2. a prominent notice that the files were changed: `bitext.NOTICE.md`;
3. keeping the attribution to the data provider, Bitext Innovations: `bitext.NOTICE.md`.

The agreement covers the data only. Results computed from it, such as eval reports, are not restricted.

## Regenerating `bitext.jsonl`

```sh
go run ./cmd/eval-import
```

The tool downloads the CSV at a pinned dataset revision into `testdata/eval/raw/`, checks its SHA-256,
samples rows deterministically, and rewrites `bitext.jsonl`. It does not touch the network when the raw file is
already present and its checksum matches. `go run ./cmd/eval-import -h` lists the flags (seed, sample size per
intent, paths).

The same seed and revision always produce the same rows: for each kept Bitext intent the tool orders the
candidate instructions by `sha256(seed + "\x00" + instruction)` and keeps the first N distinct ones, so the
selection does not depend on the Go version or on the order of the file.

Re-running the tool overwrites `bitext.jsonl`, including any labels a reviewer has changed by hand. Diff before
committing a re-run.

What the tool changes in each Bitext row:

- `body` is the Bitext `instruction` with the `{{...}}` placeholders replaced by fixed invented values
  (for example `{{Order Number}}` → `#48213`).
- `subject` is the start of the body, cut at a word boundary to at most 60 characters. Bitext rows are one
  sentence, so subject and body often match, as in a short chat-style submission.
- The labels are drafted from the Bitext intent by the table below, plus the complaint rule for offensive
  language.

| Bitext intent | `expected_category` | `expected_urgency` | Rows |
|---|---|---|---|
| `create_account` | `account` | 0 | 2 |
| `delete_account`, `edit_account` | `account` | 1 | 2 + 2 |
| `recover_password` | `account` | 1 | 3 |
| `check_payment_methods`, `check_refund_policy`, `check_cancellation_fee` | `billing` | 0 | 1 + 1 + 1 |
| `check_invoice`, `get_invoice`, `get_refund`, `track_refund` | `billing` | 1 | 1 + 2 + 2 + 1 |
| `payment_issue` | `billing` | 2 | 3 |
| `contact_customer_service`, `delivery_period`, `newsletter_subscription`, `review` | `other` | 0 | 1 + 1 + 1 + 1 |
| `contact_human_agent`, `track_order`, `cancel_order` | `other` | 1 | 1 + 1 + 1 |
| `complaint` | `other` | 1 | 2 |

`expected_complaint` is `true` for the `complaint` intent and for rows Bitext tagged `W` (offensive
language), `false` otherwise.

Intents left out because their category is ambiguous in this taxonomy: `switch_account` (plan change:
`billing` or `account`), `registration_problems` (sign-up bug: `technical` or `account`),
`change_shipping_address` and `set_up_shipping_address` (`account` profile or `other` logistics),
`change_order`, `place_order`, `delivery_options` (kept the set small; `other` is already covered).

Bitext has no technical or feature-request intents, so those categories come only from the generated rows.

## Labeling guidelines

The labels answer the three questions of README §3.3. Label what the text says, not what the customer might
mean beyond it.

### Category

The team that has to act to resolve the customer's main problem.

| Category | Use for |
|---|---|
| `billing` | payments, charges, invoices, refunds, prices, plans and subscriptions, payment methods, cancellation fees |
| `technical` | bugs, errors, crashes, outages, slowness, API and integration failures, security vulnerabilities and data leaks in the product |
| `account` | login, passwords, 2FA, access and invitations, profile data, account blocking, account deletion, a compromised account |
| `feature_request` | asking for functionality that does not exist |
| `other` | anything else: orders and delivery, contacting a person, feedback and thanks, partnerships, jobs, legal or data-protection requests, complaints about support itself, empty or meaningless text |

Edge-case rules:

- **Mixed topics.** A problem beats a question or a request. Among several problems, pick the one that hurts
  the customer most (money lost beats a bug on the side). With no problem at all, pick the main ask (the
  subject, or the first and longer part).
- **Money lost through a bug** (double charge after a page froze) is `billing`: the remedy is a refund.
- **Login failures.** A server error that hits everyone (`500` on the login page for the whole team) is
  `technical`. A problem tied to one user's credentials, email, 2FA or block is `account`.
- **Security.** A vulnerability or leak in the product (someone else's invoice visible) is `technical`. One
  customer's compromised account is `account`.
- **Vague text.** Pick the most likely team from the words that are there (`Billing` → `billing`, "the site
  works strangely" → `technical`). With no topic at all ("Алло", "the thing we talked about") use `other`.
- **Complaints about support or response time** with no underlying issue described are `other`.
- **Praise for a feature** is feedback, `other`, not `feature_request`.
- **Language mix** (surzhyk, translit, Ukrainian words in English text) does not change the category.

### Complaint

`true` when the customer expresses dissatisfaction: anger, blame, sarcasm, "again", a threat to leave or to
escalate, a demand for compensation, profanity, or an explicit claim or complaint. `false` for questions,
requests and neutral, factual problem reports, even when something is broken or money was lost.

Edge-case rules:

- A polite report of a double charge asking for a refund is `false`. "You charged me AGAIN, refund it now" is
  `true`.
- Profanity or insults aimed at the product or company are `true` (Bitext `W` rows).
- A formal request with a conditional legal step but no dissatisfaction ("otherwise I will have to contact the
  ombudsman") is `false`; the threat raises urgency instead.
- Asking how to file a complaint or claim (Bitext `complaint`) is `true`.

### Urgency

| Level | Meaning | Examples |
|---|---|---|
| `0` low | nothing broken and no time pressure | how-to and pricing questions, feature ideas, thanks, partnerships, newsletter |
| `1` normal | the customer needs something done or has a problem, but has a workaround or nothing is blocked yet | cancel an order, get an invoice, a refund for unused time, a bug with a workaround, slowness, reset a forgotten password |
| `2` high | the problem blocks the customer's work or money is already lost | payment fails, double charge, charged after cancelling, locked out with no way back, an API failure stopping their sync, lost unsaved work |
| `3` critical | outage, security incident, data loss, or a legal threat | the service is down for a whole team, a compromised account, a data leak, stored data gone, "our legal department will take action", a court or regulator complaint |

Edge-case rules:

- **Tone does not change urgency.** An angry feature request is still `0`.
- **Feature requests** are `0`, or `1` when the missing feature already costs the customer work or blocks a
  rollout (manual reconciliation every day, "security requires SSO before we roll you out").
- **Stated deadlines** ("needed today", "client meeting in an hour") move a `1` to `2`, never to `3`.
- **Partial failure** (half of API calls fail) is `2`. A whole team or all users unable to work is `3`.
- **Data loss.** Stored data gone is `3`. One session's unsaved work lost in a crash is `2`.
- **Legal threat** means a lawsuit, lawyers, a court, or a regulator or ombudsman complaint. A bank chargeback
  threat is not legal; it stays `2` when money was lost.
- **Vague text.** Take the impact literally from the words: "can't log in" blocks the user (`2`), "works
  strangely" does not (`1`), a bare "question about my bill" is `0`.
- **Password recovery.** Asking how to reset a forgotten password is `1` (there is a self-service path). The
  reset email never arriving, so the user cannot get in, is `2`.

## Hard-case tags

| Tag | Meaning |
|---|---|
| `surzhyk` | Ukrainian mixed with Russian words and forms ("шо робить", "ошибка платежа") |
| `typos` | spelling mistakes and missing punctuation |
| `angry` | caps, exclamation marks, sarcasm, threats |
| `vague` | too little text to know what is wrong |
| `mixed_topics` | two or more topics in one ticket |
| `translit` | Ukrainian written in Latin letters |
| `code_switching` | English text with Ukrainian words |

## Reviewing the labels

1. Read each row and change `expected_*` where you disagree. When a disagreement comes from a rule above,
   change the rule too, so later rows follow it.
2. Replace the status note at the top with the reviewer and the date.
3. Run `go test ./internal/evalset/` to check the files still match the schema.
