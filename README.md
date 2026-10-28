# Ticket Triage — Product Requirements

Status: draft PRD, 2026-10-28. This README holds the PRD until there is code; a later task turns it into a
regular project README and moves the PRD under `docs/`.

Facts tagged **[unverified]** come from documentation or third-party write-ups and have not been confirmed by
a live call or a local run. The adapter and the probe task (T12) are designed to absorb any differences.

---

## 1. Problem and goals

Support requests arrive as free text and someone has to read each one to decide which team owns it, how
urgent it is, and whether it is a complaint. Ticket Triage accepts a ticket over HTTP, classifies it
asynchronously with **Jev** (a typed "decision model" that returns probabilities instead of text), and either
routes it to a team or hands it to a person when the model is not confident. The submitter watches the result
arrive live over SSE.

### Product goals

- Accept a ticket in English or Ukrainian and answer `202 Accepted` immediately; classification is async.
- Classify each ticket on three questions: complaint or not, category, urgency.
- Route confidently classified tickets to a team; send everything uncertain or failed to `needs_human`.
  **A ticket is never lost**, even when the classifier is down.
- Tell the submitter the outcome as a plain status message, pushed over SSE.
- Behave like a public endpoint at work: no IDOR, bounded resources, bounded spend.

### Learning goals

This is also a learning and interview project. It should exercise, on purpose:

- Kafka: consumer groups, manual commit, rebalance handling, partition-bound parallelism, retry topics, DLQ,
  fan-out without a group, replay.
- The transactional outbox and why dual writes are wrong; at-least-once delivery with an idempotent consumer.
- Go concurrency: bounded worker pools, context cancellation, graceful drain, goroutine-leak checks,
  deterministic tests with `testing/synctest`.
- Streaming HTTP with SSE and in-process fan-out.
- Typed LLM classification: confidence thresholds, fail-open policy, an eval set, cost and latency accounting.
- Security hardening of an anonymous public API.
- Comparing transports (Kafka vs River vs RabbitMQ) on the same domain code.

All code is written by agents through the Firstmate workflow. The human decides product questions, provides
the API key and credits, reviews eval labels, and approves merges.

### Non-goals (for the base scope)

- User accounts or login (possible later stage, §11).
- AI-generated replies to the user before the RAG stage.
- A real chat widget, WebSocket, webhook, or email notification.
- A support-agent UI or a ticket listing endpoint (needs login; operators use Postgres directly until then).
- Cloud deploy, Kubernetes, WAF, autoscaling (later separate stage).
- Exactly-once processing or Kafka transactions: side effects live in Postgres and Jev, outside Kafka.
- Multi-tenancy.
- Sending real customer data to third parties or committing it.

---

## 2. Users and core flow

| User | What they do |
|---|---|
| Submitter (anonymous end user) | Submits a ticket from the built-in page or any HTTP client, watches its status |
| Support team (downstream) | Receives tickets routed by category and priority; `needs_human` tickets go to a person |
| Operator (the captain) | Runs the stack, sets thresholds and budget, runs the eval, reviews labels |

Core flow:

1. Submitter sends `POST /tickets {subject, body}`.
2. API stores the ticket as `pending` plus an outbox row in one transaction and returns `202` with the ticket
   id and a one-time **access token**.
3. A relay publishes the outbox row to Kafka; a worker classifies the ticket with Jev and applies thresholds.
4. Worker writes the result and a status event in one transaction (again through the outbox).
5. The submitter's page, subscribed over SSE, receives the status message:
   - classified: "Your request was handed to the **billing** team, priority **high**."
   - needs_human: "A person will contact you."

---

## 3. Functional requirements

### 3.1 API

All bodies are JSON. Errors use one shape: `{"error": {"code": "not_found", "message": "..."}}`.

| Endpoint | Auth | Success | Errors |
|---|---|---|---|
| `POST /tickets` | none | `202` | `400`, `413`, `415`, `429`, `503` |
| `GET /tickets/{id}` | `Authorization: Bearer <access_token>` | `200` | `404` |
| `POST /tickets/{id}/stream-token` | `Authorization: Bearer <access_token>` | `201` | `404`, `429` |
| `GET /tickets/{id}/events?token=<stream_token>` | single-use stream token | `200` `text/event-stream` | `404`, `429`, `503` |
| `GET /` | none | minimal HTML page (embedded) | — |
| `GET /healthz`, `GET /readyz` | none | `200` / `503` | — |

**`POST /tickets`**

- Request: `{"subject": string, "body": string}`, `Content-Type: application/json`. Subject 1–200 characters,
  body 1–5000 characters (proposed limits), request body capped by `http.MaxBytesReader` (`413` above it).
- Response `202`, `Location: /tickets/{id}`, `Cache-Control: no-store`:
  `{"id": "<uuidv7>", "status": "pending", "access_token": "<token>", "created_at": "..."}`.
- The access token is returned **only here, only once**. Only its SHA-256 hash is stored.
- `429` with `Retry-After` when the per-IP submit rate is exceeded; `503` with `Retry-After` when the number of
  `pending` tickets is above the cap.

**`GET /tickets/{id}`**: a plain API endpoint for the current state. There is no polling UX on top of it.

```json
{
  "id": "0192f6c1-...", "status": "classified", "status_reason": null,
  "message": "Your request was handed to the billing team, priority high.",
  "category": "billing", "priority": "high", "is_complaint": true,
  "created_at": "...", "updated_at": "...", "classified_at": "..."
}
```

Confidences and raw probabilities are internal and not returned.

**`404` instead of `403`.** An unknown id, a malformed id, a missing token, and a wrong token all return the
same `404` body. When the ticket does not exist the handler still runs a compare against a dummy hash, so the
cases are not distinguishable by timing either.

**`POST /tickets/{id}/stream-token`**: `EventSource` cannot send headers, so the page trades its Bearer token
for a short-lived, single-use stream token: `{"stream_token": "<token>", "expires_in": 60}`. Stored hashed,
bound to the ticket id.

**`GET /tickets/{id}/events?token=...`**: SSE stream, described in §3.4.

### 3.2 Ticket lifecycle

```
              submit                 confident result, category != other
   (none) ───────────► pending ──────────────────────────────────────► classified
                          │
                          │ low confidence / category=other / classifier failed
                          │ after retries / invalid answer / daily budget spent
                          └──────────────────────────────────────────► needs_human ──► resolved
                                                                                    (RAG stage)
```

| Status | Meaning |
|---|---|
| `pending` | Accepted, waiting for classification. No separate `queued` state: the outbox guarantees publication |
| `classified` | Category, complaint flag and urgency stored; confidence above thresholds |
| `needs_human` | Fail-open: a person takes it. `status_reason` says why |
| `resolved` | Reserved for when the user gets an answer (RAG stage or manual). Not used in the base scope |

`status_reason` values: `low_confidence`, `category_other`, `classifier_unavailable`, `invalid_answer`,
`budget_exhausted`. Transitions are validated in the domain; the worker may only move a ticket out of
`pending`. `attempts` and `last_error` are kept for diagnostics.

### 3.3 Classification

One Jev call per ticket carries all three questions. The `state` sent to Jev holds only `subject` and `body`.

| Question id | Jev type | Asks | Answer used |
|---|---|---|---|
| `is_complaint` | `noul` | Does the customer express dissatisfaction or complain? | `P(yes)` |
| `category` | `choice` | Which team should handle it? | option + `confidence` |
| `urgency` | `score` | How urgent is it for the customer? | weighted level 0..3 + `confidence` |

Category taxonomy (configurable): `billing` (payments, invoices, refunds, pricing, subscriptions), `technical`
(bugs, errors, outages, performance, API), `account` (login, password, access, profile, deletion),
`feature_request` (new functionality), `other` (none of the above).

Urgency levels: `0` low (no time pressure), `1` normal (has a workaround), `2` high (blocks work or money
already lost), `3` critical (outage, security incident, data loss, legal threat). The reported `priority` is
the score rounded to the nearest level.

Thresholds (one config struct; starting values, tuned later by the eval):

| Setting | Start value | Effect |
|---|---|---|
| `CategoryMinConfidence` | 0.60 | below → `needs_human` (`low_confidence`) |
| `UrgencyMinConfidence` | 0.50 | below → `needs_human` (`low_confidence`) |
| `ComplaintUncertainBand` | 0.35–0.65 | inside → flag `complaint_uncertain`, status unchanged |
| `RouteOtherToHuman` | true | `category == other` → `needs_human` (`category_other`) |

- **High urgency does not force `needs_human`.** Urgency only orders queues.
- The decision is a pure function `policy.Decide(result, thresholds) (status, reason)` with table tests.
- Tuning finer than 0.05 is pointless: probabilities vary by a few hundredths between runs **[unverified]**.
- The model version is pinned explicitly once thresholds are tuned.

### 3.4 Notification over SSE

- Flow on the page: `POST /tickets` → keep the access token in memory → `POST .../stream-token` → open
  `EventSource('/tickets/{id}/events?token=...')`.
- The handler consumes the stream token atomically, **subscribes to the hub first, then reads the current
  state from Postgres** and sends it as the first `status` event. This closes the race where the ticket is
  classified between submit and subscribe.
- Events: `event: status` with the same JSON as `GET /tickets/{id}`; a `: heartbeat` comment every 15 s.
- The stream closes after a final status (`classified`, `needs_human`), on client disconnect, or after a
  maximum stream duration (proposed 10 min).
- The stream token is single-use, so the browser's automatic reconnect would get `404`. The page handles
  `onerror` by closing the `EventSource`, minting a new stream token and reconnecting.
- Fan-out: every API instance reads `tickets.events` **without a consumer group**, from the end, and passes
  events to an in-memory hub keyed by ticket id.

---

## 4. Non-functional requirements

### 4.1 Security

The endpoint is anonymous and public, so the controls are part of the base scope, not deferred.

**Access to a ticket (IDOR).** Ticket ids are UUIDv7 and are not secrets (they are time-ordered and leak the
creation time). Possession of the id alone must never give access, which is why there is a separate token.

| Control | Requirement |
|---|---|
| Access token | `crypto/rand.Text()` (≥128 bits, base32), returned once on `POST`, `Cache-Control: no-store` |
| Storage | Only `sha256(token)` stored; compared with `crypto/subtle.ConstantTimeCompare` |
| Transport | `Authorization: Bearer` header, never in a URL |
| Unauthorized | `404`, identical to "not found", constant-time path for missing tickets |
| SSE | Short-lived (60 s) single-use stream token in the query string, bound to the ticket id, consumed atomically in Postgres (`UPDATE ... WHERE used_at IS NULL AND expires_at > now() RETURNING ticket_id`) |

**Resource limits** (values are proposed defaults, all configurable):

| Control | Requirement |
|---|---|
| Submit rate limit | Per client IP (e.g. 5/min, burst 3) via `golang.org/x/time/rate`; `429` + `Retry-After`. Stream-token minting is limited per IP too |
| Server timeouts | `http.Server` `ReadHeaderTimeout` 5 s, `ReadTimeout` 10 s, `IdleTimeout` 60 s. No server-wide `WriteTimeout` (it would kill SSE); non-streaming handlers set a write deadline via `http.ResponseController` |
| Body size | `http.MaxBytesReader` (e.g. 16 KiB) → `413` |
| SSE connections | Per-IP cap (e.g. 5) → `429`; global per-instance cap (e.g. 1000) → `503` |
| Backlog | Cap on `pending` tickets (e.g. 1000) → `503` + `Retry-After` |
| Spend | Daily Jev budget in USD (UTC day). Once spent, tickets go to `needs_human` (`budget_exhausted`) **without** a Jev call |

- Client IP is `RemoteAddr`. `X-Forwarded-For` is trusted only from configured proxies (relevant once a
  CDN/WAF sits in front).
- Rate limiters are in-memory per instance; global limits at the edge come with the deploy stage.
- **Never logged:** access tokens, stream tokens, the `Authorization` header, ticket subject and body.
  The access log records the route pattern (`r.Pattern`), never the raw URL, so `?token=` cannot leak.
- Kafka messages carry only ids and status fields, never ticket text.
- The HTML page is same-origin (no CORS in the base scope) and sends `Content-Security-Policy`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`.
- Secrets live only in `.env` (git-ignored). Live Jev calls never run in CI.
- `pprof` (incl. the `goroutineleak` profile) is served only on a separate localhost debug listener.

### 4.2 Reliability and delivery semantics

- **At-least-once** delivery; the Kafka offset is committed only after the database write.
- **Idempotent consumer:** a cheap `SELECT status` skips non-`pending` tickets before calling Jev; the write is
  `UPDATE tickets ... WHERE id = $1 AND status = 'pending'`, and if it updates 0 rows nothing else is written.
  A duplicate costs at most one extra Jev call, never duplicated state.
- **Transactional outbox** for every publish (submit and status events); no dual writes.
- **Retries in three levels:**
  1. in-process, for transient Jev errors (429, 529, 5xx, timeout, network): up to 3 attempts, exponential
     backoff with jitter, `Retry-After` honored if present;
  2. a retry topic with `attempt` and `not_before` headers; the retry consumer pauses the partition until due;
  3. a DLQ after max attempts or immediately for poison records (bad payload, missing ticket, permanent 4xx).
     The ticket goes to `needs_human` first, so the DLQ is for audit and manual re-drive, not rescue.
- **Auth errors (401/403) from Jev** are a configuration failure, not a ticket failure: the worker logs an
  error, does not commit, and stops consuming instead of dumping every ticket into `needs_human`.
- **Graceful shutdown:** stop polling, drain in-flight work with a timeout, commit, leave the group, close.
- **Jev per-call timeout** 3 s; client-side rate limit below the org limit plus a concurrency semaphore.

### 4.3 Observability

- `log/slog` JSON everywhere, request id on every HTTP log line.
- **One log line per Jev call** with `provider`, `model` (as served, from the response), `ticket_id`,
  `attempt`, `latency_ms`, `input_tokens`, `output_tokens`, `cost_usd`, `cost_source` (`reported` or
  `estimated`), `outcome`, `http_status`.
- The same data plus the raw response goes to the `classifications` audit table, so eval and debugging work
  without repeat calls.
- `/healthz` (process up) and `/readyz` (Postgres reachable).
- A metrics stack is not decided (open item).

### 4.4 Cost limits

- Expected cost is about $0.00002 per ticket: ~450 input tokens at $0.042 per 1M input tokens, output free
  **[unverified]**.
- `usage.cost` is reported by OpenRouter **[unverified]**; when missing, cost is estimated from
  `input_tokens × configured price` and marked `estimated`.
- The daily budget (§4.1) is the hard stop. It is checked before each call and may overshoot by the calls
  already in flight.

---

## 5. Architecture

Two binaries, `cmd/api` and `cmd/worker`, plus Postgres 16 and Kafka 4.x (KRaft, single node locally).

```
  Browser page / curl
     │ POST /tickets, GET /tickets/{id}              ▲ SSE: status events
     ▼                                               │
 ┌─────────────────────────────── cmd/api ───────────┴──────────────────────┐
 │ httpapi (limits, tokens) ─► ticket.Service.Submit                        │
 │   one tx: INSERT tickets(pending) + INSERT outbox(tickets.submitted)     │
 │ outbox relay: SELECT … FOR UPDATE SKIP LOCKED ─► ProduceSync             │
 │ events consumer (no group) ─► notify.Hub ─► SSE handlers                 │
 └──────┬─────────────────▲──────────────────────────────────┬──────────────┘
        │ produce         │ tickets.events                   │ SQL
        ▼                 │                                  ▼
 ┌──────┴─────────────────┴── Kafka (KRaft) ──────┐   ┌──────┴ Postgres 16 ─┐
 │ tickets.submitted        key=ticket_id, 6 part.│   │ tickets             │
 │ tickets.submitted.retry  tickets.submitted.dlq │   │ classifications     │
 │ tickets.events           key=ticket_id         │   │ outbox              │
 └──────────────┬─────────────────────────────────┘   │ stream_tokens       │
                │ group "triage-workers"              └──────────▲──────────┘
                ▼                                                │ SQL
 ┌──────────────┴──────────────── cmd/worker ────────────────────┴──────────┐
 │ PollRecords ─► bounded pool ─► triage.Processor.Process(ticketID)        │
 │   skip if not pending · budget check · Jev call (timeout, retry)         │
 │   policy.Decide ─► one tx: conditional UPDATE + INSERT classifications   │
 │                    + INSERT outbox(tickets.events)                       │
 │ all records done ─► CommitRecords ─► AllowRebalance                      │
 │ transient after retries ─► .retry   ·   poison / max attempts ─► .dlq    │
 └────────────────────────────────────┬─────────────────────────────────────┘
                                      │ HTTPS
                                      ▼
                    Jev via OpenRouter (POST /api/v1/systemone)
```

Key points:

- `triage.Processor` is **transport-agnostic**: it knows nothing about Kafka, so River and RabbitMQ drivers can
  reuse it.
- Kafka payloads are `{ticket_id, submitted_at}` (or status fields for events); the worker reads the ticket
  from Postgres.
- Topics are created explicitly by `cmd/kafka-setup` (auto-create disabled).
- Consumer pattern is batch-then-commit: poll N, process concurrently in a bounded pool, wait for all, commit.
  The price is head-of-line blocking inside a batch; acceptable at this scale.
- Domain packages (`ticket`, `classifier`, `triage`) do not import Kafka, Postgres or `net/http`; interfaces are
  declared where they are used; wiring lives only in `cmd/*`.

Data model (start):

| Table | Purpose |
|---|---|
| `tickets` | id (UUIDv7), subject, body, status, status_reason, category + confidence, complaint_p, urgency + confidence, access_token_hash, attempts, last_error, timestamps |
| `classifications` | Audit, one row per classifier call: provider, model, latency, tokens, cost, cost_source, outcome, raw response |
| `outbox` | topic, key, payload, headers, created_at, published_at; partial index on unpublished rows |
| `stream_tokens` | token hash, ticket id, expires_at, used_at |

Planned layout: `cmd/{api,worker,migrate,kafka-setup,jevprobe,eval}`, `internal/{ticket,classifier,classifier/jev,
triage,outbox,kafka,notify,storage/postgres,httpapi,config}`, `migrations/`, `testdata/eval/`.

---

## 6. Technology choices

| Choice | Why |
|---|---|
| **Go 1.27, stdlib first** | `net/http` routing patterns, `log/slog`, stdlib `uuid` (`uuid.NewV7()`), `sync.WaitGroup.Go`, `testing/synctest`, `crypto/rand.Text`. Verified locally on `go1.27.3`. Use `encoding/json/v2` (plus `encoding/json/jsontext` for streaming) everywhere, do not mix with v1 |
| **pgx v5** | Native Postgres driver and pool; encodes and scans stdlib `uuid.UUID` without adapters (verified with v5.11.0) |
| **goose v3** | SQL migrations embedded with `embed.FS`, run by `cmd/migrate` |
| **franz-go** (+ `kadm`, `kfake`) | Pure Go (no cgo), full control of manual commit and rebalance (`DisableAutoCommit`, `BlockRebalanceOnPoll`, `CommitRecords`), in-process fake broker for fast tests. Chosen over segmentio/kafka-go (still 0.x, less control) and confluent-kafka-go (cgo) |
| **Kafka 4.x, KRaft** | No ZooKeeper; single node locally (`apache/kafka` image, see [Local stack](#local-stack)) |
| **Jev via OpenRouter** | Typed answers with probabilities fit triage better than parsing free text; one OpenRouter key also serves the text LLM in the RAG stage. The captain creates the key and credits. The adapter also supports the direct TypeSafe API |
| **UUIDv7** for ids | Time-ordered (index locality), in the stdlib. ULID has the same properties but adds a dependency, and the id is not a secret either way |
| `golang.org/x/time/rate` | Client-side Jev rate limit and per-IP limits. Not stdlib, an accepted dependency |
| `testcontainers-go` | Postgres and Kafka in integration tests only |

This list is the allowed set of third-party dependencies for the base scope; the comparison stages add their
own (River, a RabbitMQ client), and anything else needs a reason in the task that adds it.

### 6.1 Kafka vs River vs RabbitMQ

Kafka is kept as the primary transport, chosen for learning value. Kafka is an event log, not a job queue, and
this service is mostly a job queue, so the trade-offs are explicit:

- **Dual write.** Writing the ticket and then publishing can lose one of the two on a crash. The outbox plus
  relay fixes it. RabbitMQ needs the same outbox. Only River avoids it, because the job is inserted in the same
  Postgres transaction (`InsertTx`).
- **Acks and retries.** Kafka commits an offset per partition, so per-message retry, delay and DLQ are built by
  hand (retry topic, partition pause, DLQ topic). RabbitMQ has per-message ack/nack, TTL delays and a native
  dead-letter exchange (DLX). River has per-job retries with backoff, snooze and discard.
- **Parallelism.** Kafka parallelism is bounded by partitions (worked around with a pool per batch); RabbitMQ
  uses prefetch × consumers; River uses `MaxWorkers` per queue.
- **Replay.** Not needed for the base flow. Re-classifying history after a model or threshold change is a real
  replay case here: a new consumer group reads `tickets.submitted` from the earliest offset (retention must
  cover the window) and writes shadow classifications without changing ticket status. RabbitMQ and River
  cannot replay consumed messages; they would re-enqueue from Postgres.
- **Fan-out.** Several independent readers of `tickets.events` (SSE now, analytics later) are natural in Kafka;
  RabbitMQ does it with exchanges (no replay); River does not do fan-out.
- **Where Kafka is the right tool:** many independent consumers of the same events, replay, high throughput,
  event history. **Where a job queue is better:** per-task ack, delays, priorities and retries with one
  database, as in the base flow of this service.

River and RabbitMQ are added later as optional drivers behind `QUEUE_DRIVER=kafka|river|rabbitmq`, reusing
`triage.Processor`, to compare code size, failure handling and operations.

---

## 7. Jev integration

- Endpoint: `POST https://openrouter.ai/api/v1/systemone` with a Bearer OpenRouter key; direct TypeSafe is
  `POST https://api.typesafe.ai/v1/systemone` with the same wire format **[unverified]**.
- Request `{model, state, questions}`; response `{model, answers: {<id>: ...}, usage: {input_tokens,
  output_tokens, cost?}}` **[unverified]**. Question ids are not shown to the model; meaning comes from
  `instructions` and `criteria`.
- Answer shapes: `noul` → `P(yes)` without a confidence; `choice` → option, probabilities, confidence;
  `score` → weighted level, probabilities, confidence **[unverified]**.
- Models: `typesafe/jev-1.14` (pinned), `~typesafe/jev-latest` (alias) **[unverified]**.
- Latency claimed 70–500 ms, mostly ~100 ms; extra questions barely add latency **[unverified]**.
- Context limit ~32k tokens for state plus one question in standard mode **[unverified]**; tickets are far below.
- The adapter maps HTTP failures to three errors the processor reacts to: `ErrTransient` (429, 529, 5xx,
  timeout, network), `ErrPermanent` (400, 422, missing or mistyped answers), `ErrAuth` (401, 403).
- `CLASSIFIER=fake|jev`. The fake is a deterministic keyword classifier for tests and dev without a key.

---

## 8. Data and evaluation

- **Eval set** (JSONL: `subject`, `body`, `lang`, `expected_category`, `expected_complaint`,
  `expected_urgency`) in English and Ukrainian, built from:
  - a public dataset of real support tickets, for example Kaggle "Customer Support on Twitter" or Bitext on
    Hugging Face. **The license must be checked first.** Raw dataset files are fetched by a script and
    git-ignored unless the license allows redistribution;
  - generated hard cases: surzhyk, typos, angry tone, vague requests, mixed topics.
- Agents draft labels; **the human reviews them**.
- No real customer data is sent to third parties or committed.
- `cmd/eval` reports per-question accuracy and confusion matrices, p50/p95 latency, total cost, and a threshold
  sweep (share of `needs_human` against accuracy on auto-classified tickets). It also measures Jev quality on
  Ukrainian, which is unknown. Output goes to `docs/eval.md`.
- End-to-end tests use the fake classifier, so realism matters only for the eval set.

---

## 9. Testing strategy

Every change passes `make check`: build, `go vet`, `go test -race ./...`, `golangci-lint` (incl. `errcheck`,
`staticcheck`, `contextcheck`, `bodyclose`, `noctx`, `sqlclosecheck`).

| Level | Tool | Covers |
|---|---|---|
| Unit | `testing`, table tests | Status transitions, `policy.Decide` at threshold edges, question building, Jev decoding, input validation, token hashing and compare |
| Concurrency | `testing/synctest` | Worker pool drain on cancel, backoff with fake clock, per-call timeout, rate limiter, hub unsubscribe. No network inside synctest |
| HTTP | `net/http/httptest` | Handlers with a fake service: status codes, 404-not-403, 413, 429 + `Retry-After`, 503 caps, stream-token single use, log redaction; Jev adapter against golden JSON and 401/422/429/529/timeout |
| Kafka | `franz-go/pkg/kfake` | Commit only after processing, rebalance, restart without loss, retry and DLQ routing, relay publish |
| Postgres | `testcontainers-go` | Repositories, transactions, conditional update race, outbox `SKIP LOCKED` with two relays, atomic stream-token consume |
| End-to-end | testcontainers Postgres + Kafka, build tag `integration` | submit → outbox → Kafka → worker (fake classifier) → `classified` + SSE event; separate CI job |
| Live | `cmd/jevprobe`, `cmd/eval` | Manual only, with a key; never in CI |

---

## 10. Roadmap

Each task is one reviewable change with its own tests and passes `make check`.

**Stage 0 — Foundation**
- T01 Skeleton and tooling: `go.mod`, layout, `Makefile`, lint config, `.env.example`, `AGENTS.md`, CI.
- T02 Local infra: `compose.yaml` with Postgres 16 (pgvector image) and Kafka KRaft, healthchecks.
- T03 Config, `slog` logging with redaction rules, signal handling and shutdown.

**Stage 1 — Ticket API on Postgres (no Kafka)**
- T04 Migrations: `tickets`, `classifications`, `stream_tokens`; `cmd/migrate`.
- T05 Domain: ticket, statuses and transitions, service, UUIDv7, validation, access token issue and verify.
- T06 Postgres repositories.
- T07 HTTP API: `POST /tickets`, `GET /tickets/{id}` with Bearer token, error model, 404-not-403.
- T08 **Security hardening**: server timeouts, body limit, per-IP submit rate limit, pending cap, trusted
  proxies, security headers, log-redaction tests.
- T09 `cmd/api` wiring, `/healthz`, `/readyz`, smoke script.

**Stage 2 — Classifier**
- T10 Classifier interface, thresholds and `Decide`, fake classifier.
- T11 Jev adapter (OpenRouter and TypeSafe), error mapping, cost, per-call logging.
- T12 `cmd/jevprobe`: live call, golden fixtures, closes the Jev open items. Needs the key and credits.
- T13 `triage.Processor`: skip, budget guard, timeout, in-process retry, conditional update and audit.

**Stage 3 — Kafka pipeline**
- T14 Kafka foundation and `cmd/kafka-setup`.
- T15 Transactional outbox and relay.
- T16 Classifier consumer: batch, bounded pool, manual commit, graceful drain, stop on `ErrAuth`.
- T17 Retry topic, DLQ, re-drive tool.
- T18 End-to-end integration test.

**Stage 4 — Notifying the user**
- T19 Status events through the outbox to `tickets.events`.
- T20 SSE: hub, group-less events consumer, stream tokens, per-IP and global connection caps.
- T21 Minimal built-in HTML page: submit form, status message via `EventSource`.

**Stage 5 — Eval and interview-ready**
- T22 Eval set: dataset license check and import script, generated hard cases, human-reviewed labels.
- T23 `cmd/eval`: accuracy, latency, cost, threshold sweep.
- T24 Replay: re-classify history with a new model or thresholds through a separate consumer group (shadow mode).
- T25 Docs: README from this PRD, eval results, Kafka rationale; PRD moves to `docs/`.

**Stage 6 — RAG reply drafts**
- T26 pgvector and FAQ ingest.
- T27 Reply suggestions (first AI-generated text for the user); introduces `resolved`.

**Stage 7 — Alternative transports (optional, for comparison)**
- T28 River driver (`InsertTx`, no outbox).
- T29 RabbitMQ driver (outbox kept, DLX instead of the DLQ topic).
- T30 Comparison write-up: code size, failure handling, operations.

**Stage 8 — Deploy and scaling (later, separate)**
- Cloudflare/WAF in front, edge rate limiting, trusted client IP.
- Kubernetes manifests.
- Autoscaling: HPA for the API; KEDA on Kafka consumer lag for workers, with worker replicas bounded by the
  partition count.

**Stage 9 — Login (possible later)**
- User accounts, ticket ownership instead of access tokens, operator listing and console.

Critical path to "a ticket is classified through Kafka": T01 → T02 → T04 → T06 → T13 → T16 (with T14, T15) → T18.
T22 does not depend on code and can start any time.

---

## 11. Open items

- **Jev details, unverified until a live call (T12):** error format on OpenRouter, `Retry-After` on 429/529,
  rate limits for a new account, `usage.cost` on the direct API, whether the Decisions API matches
  `/v1/systemone`, whether prepaid credits are required, served model snapshot ids.
- **Jev quality on Ukrainian** and on surzhyk: unknown until the eval.
- **Dataset license** for the public ticket dataset; decides whether any of it can be committed.
- **Limit values** (rate limits, caps, budget, token TTL, text lengths) are proposals to tune.
- **Metrics:** whether to add a metrics endpoint (e.g. Prometheus) beyond logs and the audit table.
- **Operator access before login:** the earlier plan had a public `GET /tickets` listing; it is dropped here
  because without login it would expose every ticket. Confirm, or add an operator-only variant.
- Webhook or email notification only if a "site with its own backend" surface appears.

## Local stack

`compose.yaml` runs the dependencies; the Go binaries run on the host.

| Service | Image | Host address | From containers |
|---|---|---|---|
| `postgres` | `pgvector/pgvector:0.8.7-pg16` | `localhost:5432` (user, password, db: `triage`) | `postgres:5432` |
| `kafka` | `apache/kafka:4.3.2`, single node, KRaft combined mode | `localhost:29092` | `kafka:9092` |
| `kafka-ui` (optional, profile `ui`) | `kafbat/kafka-ui:v1.5.0` | <http://localhost:8081> | — |

- `make up` starts Postgres and Kafka, waits for their healthchecks (`pg_isready`, `kafka-broker-api-versions.sh`),
  then runs `cmd/kafka-setup` to create the topics.
- `make smoke` runs `make up`, migrates, starts `cmd/api` on `127.0.0.1:18080`, checks the HTTP contract
  (`scripts/smoke.sh`, needs `curl` and `jq`) and stops the API with SIGTERM. The stack is left running.
- `docker compose --profile ui up -d` adds the Kafka UI. `make down` stops everything, UI included.
- Data lives in the named volumes `postgres-data` and `kafka-data`; `docker compose down -v` deletes it.
- Ports bind to `127.0.0.1` only. Topic auto-creation is off; topics come from `cmd/kafka-setup` (T14).
- List topics: `docker compose exec kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --list`.
