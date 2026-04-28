.PHONY: dev dev-consent build test cover lint lint-install clean

ENCRYPTION_KEY ?= $(shell openssl rand -base64 32)
VERSION ?= $(shell git describe --tags --always --dirty)
GOLANGCI_LINT_VERSION ?= v1.62.2
GOLANGCI_LINT := $(shell command -v golangci-lint 2>/dev/null || echo $(shell go env GOPATH)/bin/golangci-lint)

dev:
	DEV_MODE=true ENCRYPTION_KEY=$(ENCRYPTION_KEY) go run ./cmd/server

# Same as `dev` but starts MockEFBProvider with the simulated EFB v2026.1
# track-usage consent gate already active. Use this entrypoint when you
# want to walk through the consent-required flow end-to-end. See
# docs/local-consent-testing.md for the full runbook.
dev-consent:
	DEV_MODE=true DEV_MOCK_EFB_CONSENT=1 ENCRYPTION_KEY=$(ENCRYPTION_KEY) go run ./cmd/server

build:
	go build -ldflags="-X main.version=$(VERSION)" -o efb-connector ./cmd/server

test:
	go test ./...

cover:
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go tool cover -func=coverage.out | tail -20

lint:
	@test -x "$(GOLANGCI_LINT)" || (echo "golangci-lint not found; run 'make lint-install'" && exit 1)
	$(GOLANGCI_LINT) run ./...

lint-install:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

clean:
	rm -f efb-connector efb-connector.db coverage.out
