# ddgs-go — Makefile
# Build/test/release helpers for the Go port of deedy5/ddgs.
# NOTE: in the Minis PRoot sandbox the inherited TMPDIR points at a
# nonexistent Android path (/data/user/0/com.openminis.app/cache) and the go
# tool fails with "creating work dir: stat ...: no such file or directory".
# `?=` cannot fix that — make treats an environment variable as already
# defined — so probe the inherited value and fall back to /tmp. Override
# explicitly on the command line (make TMPDIR=/some/dir ...) when needed.
TMPDIR := $(shell [ -d "$(TMPDIR)" ] && echo "$(TMPDIR)" || echo /tmp)
export TMPDIR
export GOPROXY ?= https://goproxy.cn,direct
export GOTOOLCHAIN ?= auto

GO      ?= go
BIN_DIR ?= bin
PKGS    ?= ./...
# Release label: the checked-out tag when there is one, else the library
# version constant from version.go (9.16.0-go.1).
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || \
           sed -n 's/^const Version = "\(.*\)"$$/\1/p' version.go)
BINS     = ddgs ddgs-api ddgs-mcp
# Cross-compile matrix for `make release`: Linux ARM (64- and 32-bit) and
# macOS Apple Silicon only. POSIX only — the CLI's `api --detach` path uses
# setsid/signal, so Windows is not a target.
PLATFORMS = linux/arm64 linux/arm darwin/arm64

.PHONY: all build test vet fmt fmt-check lint race integration clean help release tidy

all: fmt-check vet test build

help:
	@echo "Targets:"
	@echo "  build        build the three binaries into $(BIN_DIR)/"
	@echo "  test         go test $(PKGS)"
	@echo "  race         go test -race (needs cgo; unavailable under PRoot)"
	@echo "  integration  go test -tags=integration (live network)"
	@echo "  vet          go vet $(PKGS)"
	@echo "  fmt          gofmt -w ."
	@echo "  fmt-check    fail if gofmt would change anything"
	@echo "  lint         fmt-check + vet"
	@echo "  tidy         go mod tidy"
	@echo "  release      cross-compile all platforms into dist/"
	@echo "  clean        remove $(BIN_DIR)/ dist/"

build:
	@mkdir -p $(BIN_DIR)
	@for b in $(BINS); do \
		echo "$(GO) build -o $(BIN_DIR)/$$b ./cmd/$$b"; \
		$(GO) build -o $(BIN_DIR)/$$b ./cmd/$$b || exit 1; \
	done

test:
	$(GO) test $(PKGS)

race:
	$(GO) test -race $(PKGS)

integration:
	$(GO) test -tags=integration $(PKGS)

vet:
	$(GO) vet $(PKGS)

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint: fmt-check vet

tidy:
	$(GO) mod tidy

release:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		for b in $(BINS); do \
			out=dist/$$b-$(VERSION)-$$os-$$arch; \
			echo "CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '-s -w' -o $$out ./cmd/$$b"; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '-s -w' -o $$out ./cmd/$$b || exit 1; \
		done; \
	done

clean:
	rm -rf $(BIN_DIR) dist
