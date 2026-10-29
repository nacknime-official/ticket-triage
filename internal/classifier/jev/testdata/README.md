# Jev fixtures

`live/` holds HTTP exchanges recorded by `cmd/jevprobe` (T12) against OpenRouter on 2026-10-28, with the
eval rows `gen-en-001` and `gen-uk-001`; `ok-uk.json` was re-recorded on 2026-10-29 after the `is_complaint`
and `urgency` questions gained the labeling guidelines, so the other files carry an older request. Each file has the request body sent, the response status, the kept
response headers and the response body. Request headers are never recorded, and the account id in
OpenRouter error bodies is replaced by `redacted`. The adapter tests replay them with `httptest`; re-record
with `go run ./cmd/jevprobe` (see its doc comment), which spends a few live calls.

| File | What it is |
|---|---|
| `live/ok.json` | Success through the adapter, English ticket, `typesafe/jev-1.14` |
| `live/ok-uk.json` | Success through the adapter, Ukrainian ticket, with the current request: `TestRecordedRequestIsCurrent` fails when the adapter's request changes, until it is re-recorded |
| `live/decisions.json` | The same request sent to the Decisions API path (`/api/alpha/decisions`): same response shape |
| `live/one-level.json` | A score question with one level: accepted (docs say 2 to 10), score 0, confidence 1 |
| `live/bad-key.json` | Invalid key: 401 `{"error":{"code":401,"message":...}}` |
| `live/unknown-model.json` | `typesafe/jev-0.0`: 400 `Model ... does not exist` |
| `live/bad-criteria.json` | Choice criteria as an array: 400 with a validation message naming the field |
| `live/bad-json.json` | Truncated request body: 400 `JSON parsing failed` |
| `live/bad-key-typesafe.json` | Invalid key on the direct API: 401 `{"detail":{"error_type":...,"message":...}}` |

Built by hand, not recorded:

| File | What it is |
|---|---|
| `request.golden.json` | The request body the adapter sends for the test ticket (OpenRouter model naming) |
| `response_typesafe.json` | A direct-API success without `usage.cost`, so the cost is estimated. Its shape copies the OpenRouter one; the direct API's success format is **[unverified]** (no direct key) |
