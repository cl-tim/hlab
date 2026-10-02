BINARY  := hlab
PKG     := ./cmd/hlab
MODULE  := github.com/cl-tim/homelab-cli

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(MODULE)/internal/buildinfo.Version=$(VERSION) \
	-X $(MODULE)/internal/buildinfo.Commit=$(COMMIT) \
	-X $(MODULE)/internal/buildinfo.Date=$(DATE)

# Platforms built by `make release`.
PLATFORMS := linux/amd64 linux/arm64 linux/arm darwin/arm64 darwin/amd64 windows/amd64

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build hlab for the host platform into bin/
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) $(PKG)

# Per-user by default, so installing needs no root. `sudo make install
# PREFIX=/usr/local` installs for every account instead.
PREFIX ?= $(HOME)/.local
BINDIR := $(DESTDIR)$(PREFIX)/bin

.PHONY: install
install: build ## Install hlab into $(PREFIX)/bin (default ~/.local/bin)
	install -d '$(BINDIR)'
	install -m 0755 bin/$(BINARY) '$(BINDIR)/$(BINARY)'
	@echo "Installed $(BINDIR)/$(BINARY)"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; \
		*) echo "$(BINDIR) is not on your PATH. Add to your shell profile:"; \
		   echo "  export PATH=\"$(BINDIR):\$$PATH\"";; esac

.PHONY: uninstall
uninstall: ## Remove hlab from $(PREFIX)/bin
	rm -f '$(BINDIR)/$(BINARY)'

.PHONY: test
test: ## Run the test suite
	go test ./...

.PHONY: race
race: ## Run the test suite under the race detector
	go test -race ./...

.PHONY: cover
cover: ## Run tests and open an HTML coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint (install: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest)
	golangci-lint run

.PHONY: fmt
fmt: ## Format all Go source
	go fmt ./...

.PHONY: tidy
tidy: ## Tidy and verify go.mod
	go mod tidy
	go mod verify

.PHONY: check
check: fmt vet test ## Format, vet and test

.PHONY: release
release: ## Cross-compile release binaries into dist/
	@rm -rf dist && mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "  building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' \
			-o dist/$(BINARY)-$$os-$$arch$$ext $(PKG) || exit 1; \
	done
	@echo "Release binaries in dist/"

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist coverage.out

.PHONY: help
help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
