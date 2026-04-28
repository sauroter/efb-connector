.PHONY: dev dev-consent build test lint clean

ENCRYPTION_KEY ?= $(shell openssl rand -base64 32)
VERSION ?= $(shell git describe --tags --always --dirty)

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

lint:
	go vet ./...

clean:
	rm -f efb-connector gpx-uploader efb-connector.db
