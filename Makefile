.PHONY: build install clean clean-venv test generate test-integration check-formulas sync-formulas install-hooks

BINARY := af
BUILD_DIR := .

# Python toolchain for integration tests. The Python MCP server
# (py/issuestore/server.py) requires Python 3.12.x exactly — install.go's
# checkPython312 enforces this at runtime. The venv pins aiohttp/sqlalchemy
# from py/requirements.txt; no separate lockfile is needed because the
# requirements file itself uses == pins.
PYTHON ?= python3.12
VENV := .venv
VENV_MARKER := $(VENV)/.installed
PY_REQS := py/requirements.txt

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -X github.com/stempeck/agentfactory/internal/cmd.Version=$(VERSION) \
           -X github.com/stempeck/agentfactory/internal/cmd.Commit=$(COMMIT) \
           -X github.com/stempeck/agentfactory/internal/cmd.BuildTime=$(BUILD_TIME) \
           -X main.sourceRoot=$(CURDIR)

generate:
	@true

build: check-formulas
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/af

install: build install-hooks
	mkdir -p ~/.local/bin
	cp $(BUILD_DIR)/$(BINARY) ~/.local/bin/$(BINARY)

install-hooks:
	cp hooks/protect-agent-scaffold.sh .git/hooks/pre-commit

clean: clean-venv
	rm -f $(BUILD_DIR)/$(BINARY)

clean-venv:
	rm -rf $(VENV)

test:
	CGO_ENABLED=0 go test ./...

# The venv is rebuilt only when py/requirements.txt changes (marker file
# guards re-install on every test run). Fails fast with a clear message if
# python3.12 is missing — install.go enforces 3.12 at runtime, so anything
# else is wasted work.
$(VENV_MARKER): $(PY_REQS)
	@command -v $(PYTHON) >/dev/null 2>&1 || { echo "ERROR: $(PYTHON) not found on PATH (required for integration tests; the Python MCP server needs Python 3.12.x)"; exit 1; }
	@rm -rf $(VENV)
	$(PYTHON) -m venv $(VENV)
	$(VENV)/bin/pip install --quiet --require-hashes -r $(PY_REQS)
	@touch $(VENV_MARKER)

test-integration: $(VENV_MARKER)
	PATH="$(CURDIR)/$(VENV)/bin:$$PATH" CGO_ENABLED=0 go test -tags=integration -timeout=4m ./...

check-formulas:
	@fail=0; for f in internal/cmd/install_formulas/*.formula.toml; do \
		name=$$(basename "$$f"); \
		if ! diff -q "$$f" ".beads/formulas/$$name" > /dev/null 2>&1; then \
			echo "DRIFT: $$name"; fail=1; \
		fi; \
	done; \
	if [ "$$fail" = "0" ]; then echo "Formulas in sync"; else echo "ERROR: Formula drift detected between source and installed copies"; exit 1; fi

# Used by quickstart.sh install_af() — do not rename without updating quickstart.sh
sync-formulas:
	@rm -f .beads/formulas/*.formula.toml
	@cp internal/cmd/install_formulas/*.formula.toml .beads/formulas/
	@echo "Formulas synced: source → installed (orphans removed)"
