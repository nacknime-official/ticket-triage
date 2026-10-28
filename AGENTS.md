# AGENTS.md

Rules for every coding agent working in this repository.

## Source of truth

- `README.md` is the approved PRD: behavior, API contracts, security controls, roadmap (§10).
  If code or a task disagrees with it, the README wins; raise the conflict instead of guessing.
- Each roadmap task is one reviewable change with its own tests.

## Commands

- `make check` (build, vet, `go test -race ./...`, golangci-lint) must pass before every PR.
- `make test-integration` runs tests behind the `integration` build tag (testcontainers: Postgres, Kafka).
- `make up` / `make down` start and stop the local stack (`compose.yaml`).

## Dependencies

- Go 1.27, standard library first: `net/http` routing patterns, `log/slog`, stdlib `uuid`
  (`uuid.NewV7()` for ids), `sync.WaitGroup.Go`, `testing/synctest`, `crypto/rand.Text`.
- Allowed third-party modules: `github.com/jackc/pgx/v5`, `github.com/pressly/goose/v3`,
  `github.com/twmb/franz-go` (incl. `kadm`, `kfake`), `github.com/testcontainers/testcontainers-go`
  (tests only), `golang.org/x/time/rate`.
- Anything else needs a stated reason in the PR that adds it.
- Use `encoding/json` (v1). Never mix `encoding/json` v1 and v2 in one package (lint denies v2).

## Architecture boundaries

- `ticket`, `classifier` and `triage` never import Kafka, Postgres or `net/http` (lint enforces this).
- Declare interfaces where they are used (e.g. `triage` declares its `Store`, `outbox` its `Publisher`),
  not next to the implementation.
- Wiring (constructing concrete types and passing them in) lives only in `cmd/*`.
- Each `internal/*/doc.go` states the package's responsibility and allowed imports; keep it true.
- Kafka payloads carry only ids and status fields, never ticket text.
- Every publish goes through the transactional outbox; no dual writes.

## Security

- Never log access tokens, stream tokens, the `Authorization` header, or ticket subject and body.
- Access logs record the route pattern (`r.Pattern`), never the raw URL (it can carry `?token=`).
- Store only `sha256(token)`; compare with `crypto/subtle.ConstantTimeCompare`.
- Unauthorized and not-found both return the same `404`, with no timing difference.
- Secrets live only in `.env` (git-ignored). Never commit keys, real customer data, or raw datasets.

## Testing

- Table tests with the stdlib `testing` package; run everything with `-race`.
- `testing/synctest` for pools, backoff, timeouts and rate limiters; no network inside a synctest bubble.
- `httptest` for handlers and the Jev adapter (golden JSON); `kfake` for Kafka; testcontainers for
  Postgres and end-to-end.
- Tests needing containers go under the `integration` build tag (`//go:build integration`).
- Live Jev calls never run in tests or CI; only `cmd/jevprobe` and `cmd/eval`, by hand, with a key.

## Review checklist

- Goroutine leaks: every goroutine has an owner and a way to stop.
- Ignored `ctx.Done()`: blocking loops, sends and waits honor cancellation.
- Data races: shared state is guarded; `-race` is clean.
- Swallowed errors: return or handle every error; wrap with `%w` and context.
- `defer` inside loops: resources released per iteration, not at function end.
- N+1 queries: no per-row query inside a loop over rows.
