# toolbox — Makefile
#
# Targets:
#   make build        build all binaries to ./bin/
#   make install      go install with version-stamping ldflags
#   make test         run unit tests
#   make test-race    run unit tests with the race detector
#   make fmt          gofmt -l -w on the tree
#   make vet          go vet ./...
#   make check        fmt + vet + test-race  (the recommended gate)
#   make doctor       run toolbox-bridge doctor against the current machine
#   make clean        remove ./bin
#
# Version stamping: `make install` injects git describe / commit hash /
# UTC build date into the version, commit, and buildDate vars in each
# binary's main package. `go install` from a module path skips the
# stamp — use the Makefile flow when you want the version info to be
# accurate.

# --- variables -------------------------------------------------------------

GO            ?= go
GOBIN         ?= $(shell $(GO) env GOPATH)/bin

VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT        ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Same ldflags target both binaries (main.version, main.commit,
# main.buildDate). If a binary doesn't declare one of these vars, the
# linker silently no-ops — no error.
LDFLAGS       := -X main.version=$(VERSION) \
                 -X main.commit=$(COMMIT) \
                 -X main.buildDate=$(BUILD_DATE)

BINARIES      := toolbox-bridge toolbox-mcp toolbox-pdf

.PHONY: all
all: check

# --- build / install -------------------------------------------------------

.PHONY: build
build:
	@mkdir -p bin
	@for bin in $(BINARIES); do \
		echo "building bin/$$bin"; \
		$(GO) build -ldflags "$(LDFLAGS)" -o bin/$$bin ./cmd/$$bin || exit 1; \
	done
	@echo "built $(BINARIES) → bin/  (version $(VERSION))"

.PHONY: install
install:
	@for bin in $(BINARIES); do \
		echo "installing $$bin to $(GOBIN)"; \
		$(GO) install -ldflags "$(LDFLAGS)" ./cmd/$$bin || exit 1; \
	done
	@echo "installed $(BINARIES) to $(GOBIN)  (version $(VERSION))"
	@echo "next: $(GOBIN)/toolbox-bridge init --write-profile --seed-schemas"

# --- quality gates ---------------------------------------------------------

.PHONY: test
test:
	$(GO) test ./...

.PHONY: test-race
test-race:
	$(GO) test -race ./...

.PHONY: fmt
fmt:
	gofmt -l -w .

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: check
check: fmt vet test-race
	@echo "check passed"

# --- convenience -----------------------------------------------------------

.PHONY: doctor
doctor:
	@if [ -x $(GOBIN)/toolbox-bridge ]; then \
		$(GOBIN)/toolbox-bridge doctor; \
	elif [ -x ./bin/toolbox-bridge ]; then \
		./bin/toolbox-bridge doctor; \
	else \
		echo "toolbox-bridge not built; run \`make install\` or \`make build\` first"; \
		exit 1; \
	fi

.PHONY: clean
clean:
	rm -rf bin
