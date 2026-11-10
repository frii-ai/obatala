# Obatala build targets. Keep in sync with .github/workflows/ci.yml.

GO   ?= go
GOPATH_BIN := $(shell $(GO) env GOPATH)/bin
GL   := $(shell command -v golangci-lint 2>/dev/null || echo $(GOPATH_BIN)/golangci-lint)

.PHONY: all build vet fmt-check lint test tidy install

all: build vet fmt-check lint test

build:
	$(GO) build ./...

# Install the CLI as `obatala` on PATH (~/.local/bin is in PATH on
# typical Linux shells; override with INSTALL_DIR=...).
INSTALL_DIR ?= $(HOME)/.local/bin

install:
	$(GO) build -o $(INSTALL_DIR)/obatala ./cmd/obatala

vet:
	$(GO) vet ./...

fmt-check:
	@files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then \
		echo "gofmt needed on:"; echo "$$files"; exit 1; \
	fi

lint:
	$(GL) run

test:
	$(GO) test ./...

tidy:
	$(GO) mod tidy
