.PHONY: dev dev-consent build test cover lint lint-install clean egress-status rotate-egress

ENCRYPTION_KEY ?= $(shell openssl rand -base64 32)
VERSION ?= $(shell git describe --tags --always --dirty)
GOLANGCI_LINT_VERSION ?= v2.11.4
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

# Print currently-allocated Fly egress IPs and the live outbound IPv6 the
# machine actually uses. The two should match; if they diverge after a
# rotation, the machine hasn't picked up the new pair yet — restart it.
egress-status:
	@fly ips list --app efb-connector --json | jq -r '.[] | select(.Type=="egress_v6" or .Type=="egress_v4") | "\(.Type)\t\(.Address)\t\(.Region)"'
	@echo "live egress (from inside machine):"
	@fly ssh console --app efb-connector -C 'sh -c "wget -qO- https://api6.ipify.org"'

# When EFB rate-limits the current egress IP: allocate a new pair, drop
# the v4 (we only need v6 — outbound v4 falls back to Fly default NAT for
# free), restart the machine to pick up the new v6, then release the old
# v6. The "allocate before release" order matters — Fly's allocator
# returns the just-released IP back if you release first.
rotate-egress:
	@OLD_V6=$$(fly ips list --app efb-connector --json | jq -r '.[] | select(.Type=="egress_v6") | .Address'); \
	echo "old egress v6: $$OLD_V6"; \
	echo "allocating new pair..."; \
	fly ips allocate-egress --app efb-connector -r fra --yes; \
	echo "releasing all egress v4 (we use Fly default NAT for v4 — saves \$$3.60/mo per pair):"; \
	for v4 in $$(fly ips list --app efb-connector --json | jq -r '.[] | select(.Type=="egress_v4") | .Address'); do \
		fly ips release-egress $$v4 --app efb-connector; \
	done; \
	echo "restarting machine..."; \
	fly machine restart $$(fly machines list --app efb-connector --json | jq -r '.[0].id') --app efb-connector; \
	sleep 10; \
	echo "live egress after rotation:"; \
	fly ssh console --app efb-connector -C 'sh -c "wget -qO- https://api6.ipify.org"'; \
	[ -n "$$OLD_V6" ] && fly ips release-egress $$OLD_V6 --app efb-connector || true; \
	echo "done. send the new v6 above to Tim (kanu-efb.de) for whitelisting."
