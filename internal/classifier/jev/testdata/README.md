# Jev fixtures

These responses are built by hand from the documented examples (README §7, the architecture report §5.2),
not captured from a live call. Their shape is **[unverified]**. T12 (`cmd/jevprobe`) records live responses
and replaces or corrects them; the decode helpers in `wire.go` and `errors.go` are the code to adjust.

| File | What it is |
|---|---|
| `request.golden.json` | The request body the adapter sends for the test ticket (OpenRouter model naming) |
| `response_openrouter.json` | A success with `usage.cost` reported and the served snapshot id in `model` |
| `response_typesafe.json` | A direct-API success without `usage.cost`, so the cost is estimated |
