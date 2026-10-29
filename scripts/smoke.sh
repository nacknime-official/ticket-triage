#!/usr/bin/env bash
# Smoke test of cmd/api against the local compose stack; `make smoke` starts
# the stack first. It migrates the database, builds and starts the API, checks
# the public contract (README §3.1, §4.1) over HTTP, then stops the API with
# SIGTERM and expects a clean shutdown. Needs curl and jq.
#
# The API listens on SMOKE_ADDR and SMOKE_DEBUG_ADDR (defaults below), so a
# dev instance on :8080 does not get in the way. DATABASE_URL and
# KAFKA_BROKERS default to the compose stack.
set -euo pipefail

cd "$(dirname "$0")/.."

for tool in curl jq docker go; do
	command -v "$tool" >/dev/null || { echo "smoke: $tool is required" >&2; exit 1; }
done

addr=${SMOKE_ADDR:-127.0.0.1:18080}
debug_addr=${SMOKE_DEBUG_ADDR:-127.0.0.1:16060}
base="http://$addr"
max_body=16384

export DATABASE_URL=${DATABASE_URL:-postgres://triage:triage@localhost:5432/triage?sslmode=disable}
export KAFKA_BROKERS=${KAFKA_BROKERS:-localhost:29092}

work=$(mktemp -d)
api_pid=
cleanup() {
	if [[ -n $api_pid ]] && kill -0 "$api_pid" 2>/dev/null; then
		kill -KILL "$api_pid" 2>/dev/null || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT

passed=0
pass() { echo "PASS $*"; passed=$((passed + 1)); }
fail() {
	echo "FAIL $*" >&2
	echo "--- api log ---" >&2
	cat "$work/api.log" >&2 || true
	exit 1
}

# request METHOD PATH [curl args...]: writes the body to $work/body and the
# headers to $work/headers, prints the status code.
request() {
	local method=$1 path=$2
	shift 2
	curl -sS -o "$work/body" -D "$work/headers" -w '%{http_code}' -X "$method" "$@" "$base$path"
}
header() { grep -i "^$1:" "$work/headers" | head -n1 | cut -d' ' -f2- | tr -d '\r'; }
expect() { # expect NAME WANT GOT
	[[ $3 == "$2" ]] || fail "$1: got $3, want $2: $(cat "$work/body")"
	pass "$1 ($3)"
}

echo "== migrate"
go run ./cmd/migrate up

echo "== build and start the api on $addr (debug $debug_addr)"
go build -o "$work/api" ./cmd/api
HTTP_ADDR=$addr DEBUG_ADDR=$debug_addr SUBMIT_RATE_PER_MIN=5 SUBMIT_BURST=3 MAX_BODY_BYTES=$max_body \
	SSE_HEARTBEAT=1s "$work/api" >"$work/api.log" 2>&1 &
api_pid=$!
for _ in $(seq 100); do
	curl -fsS -o /dev/null "$base/healthz" 2>/dev/null && break
	kill -0 "$api_pid" 2>/dev/null || fail "api exited during startup"
	sleep 0.1
done

echo "== checks"
expect "GET /healthz" 200 "$(request GET /healthz)"
expect "GET /readyz" 200 "$(request GET /readyz)"

expect "GET / (built-in page)" 200 "$(request GET /)"
expect "GET / Content-Type" "text/html; charset=utf-8" "$(header Content-Type)"
expect "GET / Referrer-Policy" no-referrer "$(header Referrer-Policy)"
[[ $(header Content-Security-Policy) == *"default-src 'self'"* ]] || fail "GET / CSP: $(header Content-Security-Policy)"
grep -q '<form id="ticket-form"' "$work/body" || fail "GET / does not serve the page: $(head -c 300 "$work/body")"
pass "GET / serves the submit form under the CSP"
for asset in /assets/app.js /assets/app.css; do
	expect "GET $asset" 200 "$(request GET "$asset")"
done

subject="Smoke test: card charged twice"
ticket_body="Order 4411, please refund the second payment."
code=$(request POST /tickets -H 'Content-Type: application/json' \
	--data "$(jq -cn --arg s "$subject" --arg b "$ticket_body" '{subject: $s, body: $b}')")
expect "POST /tickets" 202 "$code"
id=$(jq -r .id "$work/body")
token=$(jq -r .access_token "$work/body")
[[ $(jq -r .status "$work/body") == pending && -n $token && $token != null ]] || fail "submit body: $(cat "$work/body")"
expect "POST /tickets Location" "/tickets/$id" "$(header Location)"
expect "POST /tickets Cache-Control" no-store "$(header Cache-Control)"
expect "security header X-Content-Type-Options" nosniff "$(header X-Content-Type-Options)"

expect "GET /tickets/{id} with token" 200 "$(request GET "/tickets/$id" -H "Authorization: Bearer $token")"
expect "GET /tickets/{id} status" pending "$(jq -r .status "$work/body")"

expect "GET /tickets/{id} without token" 404 "$(request GET "/tickets/$id")"
no_token=$(cat "$work/body")
expect "GET /tickets/{id} with wrong token" 404 "$(request GET "/tickets/$id" -H 'Authorization: Bearer WRONGTOKEN')"
wrong_token=$(cat "$work/body")
unknown_id="${id:0:${#id}-4}0000"
expect "GET /tickets/{unknown} with token" 404 "$(request GET "/tickets/$unknown_id" -H "Authorization: Bearer $token")"
unknown=$(cat "$work/body")
[[ $no_token == "$wrong_token" && $wrong_token == "$unknown" ]] ||
	fail "404 bodies differ: '$no_token' / '$wrong_token' / '$unknown'"
pass "identical 404 bodies ($no_token)"

expect "POST /tickets/{id}/stream-token" 201 \
	"$(request POST "/tickets/$id/stream-token" -H "Authorization: Bearer $token")"
stream_token=$(jq -r .stream_token "$work/body")
[[ -n $stream_token && $stream_token != null && $(jq -r .expires_in "$work/body") == 60 ]] ||
	fail "stream-token body: $(cat "$work/body")"
expect "POST /tickets/{id}/stream-token without token" 404 "$(request POST "/tickets/$id/stream-token")"

# No worker runs, so the stream stays pending: it sends the state, then a
# heartbeat a second; curl exits 28 when --max-time cuts it.
curl -s -N --max-time 2.5 -D "$work/sse_headers" -o "$work/sse" \
	"$base/tickets/$id/events?token=$stream_token" || [[ $? == 28 ]] || fail "SSE request failed"
sse_status=$(head -n1 "$work/sse_headers" | cut -d' ' -f2)
expect "GET /tickets/{id}/events" 200 "$sse_status"
grep -qi '^Content-Type: text/event-stream' "$work/sse_headers" || fail "SSE Content-Type: $(cat "$work/sse_headers")"
grep -q '^data: .*"status":"pending"' "$work/sse" || fail "no pending status event: $(cat "$work/sse")"
grep -q '^: heartbeat$' "$work/sse" || fail "no heartbeat: $(cat "$work/sse")"
pass "SSE stream sends the pending state and heartbeats"
expect "GET /tickets/{id}/events with a used token" 404 \
	"$(request GET "/tickets/$id/events?token=$stream_token")"

head -c $((max_body + 1)) /dev/zero | tr '\0' x >"$work/oversized"
expect "POST /tickets oversized body" 413 \
	"$(request POST /tickets -H 'Content-Type: application/json' --data-binary "@$work/oversized")"

# The burst is 3: the submit and the oversized request above took two tokens.
limited=
for i in $(seq 10); do
	code=$(request POST /tickets -H 'Content-Type: application/json' --data '{"subject":"s","body":"b"}')
	if [[ $code == 429 ]]; then
		limited=$i
		break
	fi
	[[ $code == 202 ]] || fail "POST /tickets #$i: got $code before the rate limit"
done
[[ -n $limited ]] || fail "no 429 after 10 submits"
retry_after=$(header Retry-After)
[[ $retry_after =~ ^[0-9]+$ ]] || fail "429 without a numeric Retry-After: '$retry_after'"
pass "POST /tickets rate limited (429 on submit #$limited, Retry-After: $retry_after)"

expect "debug pprof goroutineleak" 200 "$(curl -sS -o "$work/body" -w '%{http_code}' \
	"http://$debug_addr/debug/pprof/goroutineleak?debug=1")"

published=
for _ in $(seq 50); do
	published=$(docker compose exec -T postgres psql -U triage -d triage -tAc \
		"SELECT published_at IS NOT NULL FROM outbox WHERE key = convert_to('$id', 'UTF8')")
	[[ $published == t ]] && break
	sleep 0.2
done
expect "outbox row published by the relay" t "$published"

for secret in "$token" "$stream_token" "$subject" "$ticket_body"; do
	! grep -qF "$secret" "$work/api.log" || fail "the api log contains a token or ticket text"
done
pass "api log free of tokens and ticket text"

echo "== graceful shutdown"
kill -TERM "$api_pid"
status=0
wait "$api_pid" || status=$?
api_pid=
expect "api exit status after SIGTERM" 0 "$status"
grep -q '"msg":"shutdown complete"' "$work/api.log" || fail "no shutdown complete log line"
pass "shutdown complete logged"

echo "smoke: all $passed checks passed"
