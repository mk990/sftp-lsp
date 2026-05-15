BINARY  := sftp-lsp
OUTDIR  := build
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: all build install test clean lint

all: build

build:
	go build $(LDFLAGS) -o $(OUTDIR)/$(BINARY) ./cmd/sftp-lsp

install:
	go install $(LDFLAGS) ./cmd/sftp-lsp

test:
	go test ./...

clean:
	rm -rf $(OUTDIR)

lint:
	golangci-lint run ./...

tidy:
	go mod tidy
