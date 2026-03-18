.PHONY: dev build test lint clean

ENCRYPTION_KEY ?= $(shell openssl rand -base64 32)

dev:
	DEV_MODE=true ENCRYPTION_KEY=$(ENCRYPTION_KEY) go run ./cmd/server

build:
	go build -o efb-connector ./cmd/server

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -f efb-connector gpx-uploader efb-connector.db
