.PHONY: dev build test lint clean

ENCRYPTION_KEY ?= $(shell openssl rand -base64 32)
VERSION ?= $(shell git describe --tags --always --dirty)

dev:
	DEV_MODE=true ENCRYPTION_KEY=$(ENCRYPTION_KEY) go run ./cmd/server

build:
	go build -ldflags="-X main.version=$(VERSION)" -o efb-connector ./cmd/server

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -f efb-connector gpx-uploader efb-connector.db
