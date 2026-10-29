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
already present and its checksum matches. `go run ./cmd/eval-import -h` lists the flags (seed, URL and
checksum, paths). The kept intents and the number of rows per intent are the table below, in
`cmd/eval-import/main.go`.

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

Intent-based drafts are coarse. Rows a reviewer should look at first:

- `bitext-bed9e2b91309` "problem with opening a Business account": drafted `0` from `create_account`, but
  "problem" may mean something is broken (`1`).
- `bitext-4c97808eaa99` "how to report an issue with online payments?": drafted `2` from `payment_issue`,
  though it reads as a how-to question.
- `W` rows with mild profanity ("bloody withdrawal penalties"): drafted as complaints by the `W` rule.

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

`true` when the customer expresses dissatisfaction, `false` for questions, requests and neutral, factual
problem reports. Jev answers `is_complaint` against the two lists below, which it receives word for word as
the question's criteria: they are `complaintGuidelines` in `internal/classifier/jev/questions.go`, and
`TestComplaintGuidelinesMatchREADME` fails when the lists here and there differ, so change both together.

Complaint (`true`):

- The customer expresses dissatisfaction: anger, blame, sarcasm, "again", a threat to leave or to escalate, a
  demand for compensation, profanity, or an explicit claim or complaint.
- Profanity or insults aimed at the product or company are a complaint.
- Asking how to file a complaint or claim is a complaint.
- "You charged me AGAIN, refund it now" is a complaint.

Not a complaint (`false`):

- Questions, requests and neutral, factual problem reports are not complaints, even when something is broken
  or money was lost.
- A polite report of a double charge asking for a refund is not a complaint.
- A formal request with a conditional legal step but no dissatisfaction ("otherwise I will have to contact the
  ombudsman") is not a complaint.

Bitext rows: the `W` rows (offensive language) are complaints by the profanity rule, and the `complaint`
intent by the rule on filing a complaint. A conditional legal step raises urgency instead (see Urgency).

### Urgency

How soon the customer needs the request handled, from the impact the text states. Jev answers `urgency`
against the lists below, which it receives word for word: the "All levels" rules end the question's
instructions, and each level's list is that level's criterion. They are `urgencyGuidelines` in
`internal/classifier/jev/questions.go`, and `TestUrgencyGuidelinesMatchREADME` fails when the lists here and
there differ, so change both together.

All levels:

- Tone does not change urgency: anger, caps, profanity or a threat to leave do not raise the level, and an
  angry feature request is still low.
- Take the impact literally from the words, not what the customer might mean beyond them.

`0` low:

- Low: nothing is broken and there is no time pressure.
- How-to and pricing questions, feature ideas, thanks, partnerships and newsletter requests are low.
- A feature request is low, unless the missing feature already costs the customer work or blocks a rollout.
- A bare "question about my bill" is low.

`1` normal:

- Normal: the customer needs something done or has a problem, but has a workaround or nothing is blocked yet.
- Cancelling an order, getting an invoice, a refund for unused time, a bug with a workaround and slowness are
  normal.
- Asking how to reset a forgotten password is normal: there is a self-service path.
- A missing feature that already costs the customer work or blocks a rollout (manual reconciliation every
  day, "security requires SSO before we roll you out") is normal.
- Something that "works strangely" does not block the customer and is normal.

`2` high:

- High: the problem blocks the customer's work or money is already lost.
- A failing payment, a double charge, a charge after cancelling, being locked out with no way back and an API
  failure stopping the customer's sync are high.
- A stated deadline ("needed today", "client meeting in an hour") moves a normal request to high, never to
  critical.
- A partial failure (half of API calls fail) is high.
- One session's unsaved work lost in a crash is high.
- "Can't log in" blocks the user and is high, and so is a password reset email that never arrives, so the
  user cannot get in.
- A bank chargeback threat is not a legal threat; it stays high when money was lost.

`3` critical:

- Critical: an outage, a security incident, data loss, or a legal threat.
- The service down for a whole team or all users, a compromised account, a data leak and stored data gone
  are critical.
- A legal threat means a lawsuit, lawyers, a court, or a regulator or ombudsman complaint ("our legal
  department will take action"), and it is critical also when it is conditional ("if you do not refund by
  Friday, I will go to court").

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
