# Ticket Triage

Ticket Triage takes a support request over HTTP, classifies it in the background with **Jev** (a typed
"decision model" that answers with probabilities instead of text), and either routes it to a team or hands it
to a person when the model is not confident. The submitter watches the outcome arrive live over Server-Sent
Events (SSE).

Each ticket, in English or Ukrainian, gets three answers: whether it is a complaint, which team should handle
it (`billing`, `technical`, `account`, `feature_request` or `other`), and how urgent it is (`low`, `normal`,
`high`, `critical`). A confident answer makes the ticket `classified`; low confidence, the `other` category, a
classifier failure or a spent daily budget make it `needs_human`. A ticket is never lost, even when the
classifier is down.

It is also a learning project for Kafka (consumer groups, manual commit, retry topic, DLQ, group-less
fan-out, replay), the transactional outbox, Go concurrency, SSE, typed LLM classification with an eval set,
and hardening an anonymous public API.

The product requirements (behavior, API contracts, security controls, roadmap) are in
[`docs/prd.md`](docs/prd.md).

## How it works

1. `POST /tickets` stores the ticket as `pending` and an outbox row in one Postgres transaction, and answers
   `202` with the ticket id and a one-time access token.
2. The outbox relay in `cmd/api` publishes the row to the Kafka topic `tickets.submitted`.
3. `cmd/worker` consumes it in a consumer group, reads the ticket from Postgres, asks Jev the three questions
   in one call and applies the confidence thresholds.
4. The worker writes the result, an audit row and a status event (again through the outbox) in one
   transaction, then commits the Kafka offset.
5. Every API instance reads `tickets.events` without a consumer group and pushes the new status to the
   ticket's open SSE streams: "Your request was handed to the billing team, priority high." or "A person will
   contact you."

```
  Browser page (GET /) or any HTTP client
     │ POST /tickets, GET /tickets/{id}                       ▲ SSE: event: status
     ▼                                                        │
 ┌─────────────────────────────────── cmd/api ────────────────┴──────────────────┐
 │ httpapi (rate limits, caps, tokens) ─► ticket.Service.Submit                  │
 │   one tx: INSERT tickets (pending) + INSERT outbox (tickets.submitted)        │
 │ outbox relay: SELECT … FOR UPDATE SKIP LOCKED ─► produce ─► mark published    │
 │ events reader (no consumer group, from the end) ─► notify.Hub ─► SSE streams  │
 └───────┬──────────────────▲────────────────────────────────────────┬───────────┘
         │ produce          │ tickets.events                         │ SQL
         ▼                  │                                        ▼
 ┌───────┴──────────────────┴────── Kafka (KRaft) ─────┐   ┌─────── Postgres 16 ──────┐
 │ tickets.submitted        6 partitions, key=ticket id│   │ tickets                  │
 │ tickets.submitted.retry  tickets.submitted.dlq      │   │ classifications (audit)  │
 │ tickets.events                                      │   │ outbox                   │
 └───────┬─────────────────────────────────────────────┘   │ stream_tokens            │
         │ groups triage-workers, triage-workers-retry     │ shadow_classifications   │
         ▼                                                 └────────────▲─────────────┘
 ┌──────────────────────────────── cmd/worker ──────────────────────────┴────────┐
 │ poll a batch ─► bounded pool ─► triage.Processor.Process(ticket id)           │
 │   skip unless pending · budget check · Jev call (timeout, rate limit, retry)  │
 │   classifier.Decide ─► one tx: UPDATE … WHERE status = 'pending'              │
 │                        + INSERT classifications + INSERT outbox (events)      │
 │ whole batch done ─► commit offsets                                            │
 │ transient failure ─► tickets.submitted.retry · out of attempts or poison ─►   │
 │   needs_human + tickets.submitted.dlq (cmd/redrive re-publishes by hand)      │
 └───────────────────────────────────────┬───────────────────────────────────────┘
                                         │ HTTPS
                                         ▼
                Jev via OpenRouter (POST /api/v1/systemone) or the direct TypeSafe API
```

- Delivery is at-least-once with an idempotent consumer: the worker skips a ticket that is no longer `pending`,
  and the result is written with a conditional `UPDATE`, so a duplicate record costs at most one extra Jev
  call, never duplicated state.
- Kafka payloads carry only ids and status fields, never ticket text; the worker reads the text from Postgres.
- `ticket`, `classifier` and `triage` import neither Kafka, Postgres nor `net/http`, so another transport can
  reuse `triage.Processor`.
- `worker shadow` replays `tickets.submitted` in its own consumer group, re-classifies history with another
  model or other thresholds, and writes only to `shadow_classifications`, leaving tickets untouched.

## Run it locally

Needs Go 1.27 and Docker with Compose; `make smoke` also needs `curl` and `jq`.

```sh
make up                     # Postgres and Kafka in Docker, then create the topics
DATABASE_URL='postgres://triage:triage@localhost:5432/triage?sslmode=disable' \
  go run ./cmd/migrate up   # apply the database migrations; migrate has no default URL
go run ./cmd/api            # HTTP API on :8080, outbox relay, SSE fan-out
go run ./cmd/worker         # classifier worker, in another terminal
```

Open <http://localhost:8080/>, submit a request, and the page shows its status as it arrives over SSE.
`make down` stops the stack; the data stays in the Docker volumes until `docker compose down -v`.

Apart from `cmd/migrate`, which always needs `DATABASE_URL`, the defaults need no configuration: they point
at the Compose stack (Postgres on `localhost:5432`, Kafka on `localhost:29092`) and use the fake classifier,
a deterministic keyword classifier that needs no key and makes no network call. Configuration comes only from environment variables; [`.env.example`](.env.example) lists
every one with its default. The binaries do not read `.env` themselves, so load it into the shell first:

```sh
cp .env.example .env        # git-ignored; set CLASSIFIER=jev and OPENROUTER_API_KEY to use Jev
set -a; . ./.env; set +a
```

Other commands:

| Command | What it does |
|---|---|
| `make smoke` | `make up`, migrate, start `cmd/api` on `127.0.0.1:18080`, check the HTTP contract with `scripts/smoke.sh`, stop it with SIGTERM; the stack keeps running |
| `make check` | build, `go vet`, `go test -race ./...`, golangci-lint; must pass before every PR |
| `make test-integration` | tests behind the `integration` build tag, with Postgres and Kafka in testcontainers, including the end-to-end test |
| `docker compose --profile ui up -d` | Kafka UI on <http://localhost:8081> |
| `go run ./cmd/redrive -dry-run` | list the DLQ records; without `-dry-run` they go back to `tickets.submitted` |
| `go run ./cmd/worker shadow -dry-run` | replay ticket history in shadow mode (`-dry-run` uses the fake classifier and writes nothing) |
| `go run ./cmd/eval` | run the eval set through the fake classifier and write `docs/eval-fake.md` |

`cmd/jevprobe` and `cmd/eval -classifier jev` make live Jev calls and spend credits; they run only by hand,
with a key, never in tests or CI.

## API at a glance

All bodies are JSON; every error has the shape `{"error": {"code": "...", "message": "..."}}`.

| Endpoint | Auth | Success | Errors |
|---|---|---|---|
| `POST /tickets` | none | `202` `{id, status, access_token, created_at}` | `400`, `413`, `415`, `429`, `503` |
| `GET /tickets/{id}` | `Authorization: Bearer <access_token>` | `200` current state | `404` |
| `POST /tickets/{id}/stream-token` | `Authorization: Bearer <access_token>` | `201` `{stream_token, expires_in}` | `404`, `429` |
| `GET /tickets/{id}/events?token=<stream_token>` | single-use stream token | `200` `text/event-stream` | `404`, `429`, `503` |
| `GET /` | none | the built-in HTML page | — |
| `GET /healthz`, `GET /readyz` | none | `200`; `/readyz` answers `503` while Postgres is unreachable | — |

```sh
curl -s localhost:8080/tickets -H 'Content-Type: application/json' \
  -d '{"subject": "Charged twice", "body": "My card was charged twice for the May invoice."}'
# {"id":"019a…","status":"pending","access_token":"…","created_at":"…"}

curl -s localhost:8080/tickets/019a… -H 'Authorization: Bearer …'
# once a worker has classified it, for example:
# {"id":"019a…","status":"classified","status_reason":null,
#  "message":"Your request was handed to the billing team, priority high.",
#  "category":"billing","priority":"high","is_complaint":false,"created_at":"…",…}
```

The access token is returned only once. `EventSource` cannot send headers, so a page trades the access token
for a stream token that lives 60 seconds and opens one stream. The stream sends the current state first, then
every status change as `event: status` with the same JSON as `GET /tickets/{id}`, and a `: heartbeat` comment
every 15 seconds; it closes at a final status (`classified`, `needs_human`). The full contract is in
[PRD §3](docs/prd.md#3-functional-requirements).

## Eval results

The eval set has 100 labeled tickets, 50 in English and 50 in Ukrainian: 30 sampled from the Bitext customer
support dataset and 70 generated hard cases (surzhyk, typos, angry tone, vague requests, mixed topics). A live
run of `typesafe/jev-1.14` through OpenRouter on 2026-10-29, at the default thresholds:

| Question | all | en | uk |
|---|---|---|---|
| Category | 94% | 90% | 98% |
| Complaint | 93% | 96% | 90% |
| Priority, exact | 84% | 82% | 86% |
| Priority, within one level | 99% | 98% | 100% |

- 73% of tickets are classified automatically; 19% go to a person as `other` and 8% for low confidence.
  Category accuracy on the automatically classified tickets is 98.6%.
- Latency per call: p50 225 ms, p95 276 ms.
- Cost: about $0.00005 per ticket ($0.005 for the 100 calls), as reported by OpenRouter.
- The labels are agent drafts not yet reviewed by a person, so every accuracy number is provisional.

[`docs/eval.md`](docs/eval.md) has the confusion matrices, the per-tag results, the threshold sweep and how to
reproduce the run; [`docs/eval-fake.md`](docs/eval-fake.md) is the same report for the fake classifier.

## Why Kafka, and where it does not fit

Kafka is an event log, not a job queue, and most of this service is a job queue: one ticket, one
classification, retried until it succeeds or goes to a person. Kafka was chosen for what it teaches, with the
costs paid in the open:

- **No per-message ack.** Kafka commits an offset per partition, so retries, delays and a dead-letter queue are
  built by hand: a retry topic whose consumer pauses the partition until a record is due, and a DLQ topic with
  a re-drive tool. RabbitMQ (ack/nack, TTL delays, dead-letter exchanges) and River (per-job retries, snooze)
  have these built in.
- **No delayed messages or priorities.** A record is read in offset order as soon as it is written; a retry
  waits only because the retry consumer pauses its partition, and an urgent ticket cannot jump the queue.
- **One slow or poison record holds up its partition.** The offset cannot move past a record until it is
  done, and the worker commits a polled batch only once the whole batch is done, so one slow ticket holds up
  the rest of its batch and its partition. This service limits it: a transient failure moves the ticket to
  `tickets.submitted.retry` instead of blocking `tickets.submitted`, and a poison record, or one out of
  retries, goes to `tickets.submitted.dlq` at once, with the ticket in `needs_human`.
- **Parallelism is bound to partitions, planned ahead.** One partition is read by at most one consumer in a
  group, so the partition count caps how many workers can share the load and has to be chosen before the
  traffic arrives (`tickets.submitted` has 6). The worker adds parallelism inside a partition with a bounded
  pool per polled batch.
- **The database and the broker are separate systems.** Writing the ticket and then publishing can lose one of
  the two on a crash, so every publish goes through a transactional outbox and a relay: an extra component and
  some latency. River avoids it by inserting the job in the same Postgres transaction.
- **Operational weight.** A broker, KRaft, offsets and retention are a lot for one service with modest traffic.

Where Kafka earns its place even here:

- **Fan-out.** Every API instance reads `tickets.events` without a consumer group, so each one sees every status
  change and can serve the SSE streams it holds; analytics or notifications could read the same topic later.
- **Replay.** `worker shadow` re-reads `tickets.submitted` from the earliest offset in a new consumer group to
  re-classify history after a model or threshold change, which a job queue cannot do once its jobs are consumed.

River has neither fan-out nor replay. RabbitMQ does fan-out with exchanges but cannot replay consumed messages;
both would re-enqueue from Postgres instead.

The full comparison with River and RabbitMQ is in [PRD §6.1](docs/prd.md#61-kafka-vs-river-vs-rabbitmq); both
are planned as optional drivers that reuse `triage.Processor`.

## Security

The endpoint is anonymous and public, so these controls are part of the base scope
([PRD §4.1](docs/prd.md#41-security)):

- **No IDOR.** Ticket ids (UUIDv7) are not secrets; reading a ticket needs its access token from
  `crypto/rand.Text()`, sent as a Bearer header, never in a URL. Only `sha256(token)` is stored and compared in
  constant time. An unknown id, a malformed id, a missing token and a wrong token all get the same `404`, with
  a dummy compare so timing does not tell them apart.
- **SSE tokens.** The stream token in the query string is short-lived, single-use, bound to the ticket and
  consumed atomically in Postgres.
- **Bounded resources.** Server timeouts (no server-wide write timeout, which would cut SSE; non-streaming
  handlers set their own deadline), a request body limit (`413`), per-IP rate limits on submits and stream
  tokens (`429` with `Retry-After`), per-IP and per-instance SSE caps, and a cap on pending tickets (`503`).
- **Bounded spend.** A daily Jev budget in USD; once it is spent, tickets go to `needs_human` without a call.
- **No leaks in logs.** Access tokens, stream tokens, the `Authorization` header and ticket text are never
  logged, and the access log records the route pattern instead of the URL. Kafka carries no ticket text.
- **Headers.** The built-in page is same-origin and served with `Content-Security-Policy`,
  `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. `X-Forwarded-For` counts only from
  configured trusted proxies.
- pprof runs on a separate listener that must be a loopback address. Secrets live only in the git-ignored
  `.env`.

## Roadmap

Stages 0–5 are done: the ticket API on Postgres, the classifier with the Jev adapter, the Kafka pipeline with
the outbox, retry topic and DLQ, SSE notifications with the built-in page, and the eval set, eval report,
shadow replay and these docs (T01–T25). Next:

- Stage 6: reply drafts with RAG over a pgvector FAQ, the first AI-generated text for the user.
- Stage 7 (optional): River and RabbitMQ drivers and a comparison write-up.
- Stage 8: deploy and scaling (edge rate limiting, Kubernetes, autoscaling on consumer lag).
- Stage 9 (possible): user accounts instead of access tokens.

The task list is in [PRD §10](docs/prd.md#10-roadmap).

## Repository layout

| Path | Contents |
|---|---|
| `cmd/api`, `cmd/worker` | the two services |
| `cmd/migrate`, `cmd/kafka-setup`, `cmd/redrive` | migrations, topic setup, DLQ re-drive |
| `cmd/eval`, `cmd/eval-import`, `cmd/jevprobe` | eval report, eval set import, live Jev probe |
| `internal/ticket`, `internal/classifier`, `internal/triage` | domain: tickets and tokens, classification policy, the processor |
| `internal/classifier/jev` | the Jev adapter |
| `internal/httpapi`, `internal/notify` | HTTP handlers, middleware, the built-in page, the SSE hub |
| `internal/kafka`, `internal/outbox`, `internal/storage/postgres` | Kafka clients, outbox relay, repositories |
| `migrations/`, `testdata/eval/` | SQL migrations, the eval set and its labeling guidelines |
| `docs/` | the PRD and the eval reports |

[`AGENTS.md`](AGENTS.md) holds the rules for the coding agents that write this code.
