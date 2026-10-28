# Keep GOLANGCI_LINT_VERSION in sync with .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION := v2.14.0
# go run builds the linter with the local toolchain, so it understands this
# module's Go version. Override to use an installed binary of the same version.
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: build vet test test-integration lint check up down smoke

build:
	go build ./...

vet:
	go vet ./...

test:
	go test -race ./...

test-integration:
	go test -race -tags=integration ./...

lint:
	$(GOLANGCI_LINT) run ./...

check: build vet test lint

# Topics are created once Kafka is healthy; kafka-setup is idempotent.
up:
	docker compose up -d --wait
	go run ./cmd/kafka-setup

# All profiles, so the optional kafka-ui container is removed too.
down:
	docker compose --profile "*" down

# Runs cmd/api against the local stack and checks the HTTP contract; the
# stack is left running.
smoke: up
	./scripts/smoke.sh
