BINARY  := agentchat
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test race vet fmt lint mock clean cross

all: test build

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/agentchat

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

lint: fmt vet

mock: build
	./bin/$(BINARY) mock-hub --listen 127.0.0.1:8787

cross:
	VERSION="$(VERSION)" ./scripts/build-all.sh

clean:
	rm -rf bin dist
