.PHONY: build build-webui install clean clean-venv test conformance generate test-integration check-formulas sync-formulas install-hooks check-skills sync-skills check-formula-skills check-regen

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

build: check-formulas check-skills check-formula-skills
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/af

# Phase 5: build the optional web console from its OWN module (web/go.mod). The source package is
# web/cmd/afweb (package main); the output/installed/launched binary is named `webui` — the
# container entrypoint guard and the Phase-5 ACs all key on that name. af-core never references
# this target or its output (cross-review H-3). Built standalone, NOT part of `build`, so af-core
# build/test stay independent of the web module.
build-webui:
	cd web && CGO_ENABLED=0 go build -o ../webui ./cmd/afweb

install: build install-hooks
	mkdir -p ~/.local/bin
	cp $(BUILD_DIR)/$(BINARY) ~/.local/bin/$(BINARY)
	@# Phase 5: install the web console best-effort — a missing/unbuilt `webui` must never fail the
	@# af install (run `make build-webui` first to produce it; the entrypoint guard launches it iff present).
	@[ -f webui ] && cp webui ~/.local/bin/webui || true

install-hooks:
	cp hooks/protect-agent-scaffold.sh .git/hooks/pre-commit

clean: clean-venv
	rm -f $(BUILD_DIR)/$(BINARY)

clean-venv:
	rm -rf $(VENV)

AF_TEST_TMPDIR := $(HOME)/.cache/af-test

test:
	@mkdir -p $(AF_TEST_TMPDIR)
	TMPDIR=$(AF_TEST_TMPDIR) GOTMPDIR=$(AF_TEST_TMPDIR) CGO_ENABLED=0 go test ./...

# Conformance-test the SHIPPED formula-editor engine
# (web/internal/web/static/scripts/toml-engine.js) against Python tomllib over the live
# store formulas. This is the EXACT command the toml-conformance CI job runs, so
# `make conformance` matches the CI lane byte-for-byte (local<->CI parity). Needs node +
# python3.12 (tomllib) on PATH; it is a plain Node script, no build step.
conformance:
	node web/conformance/test-engine.js .agentfactory/store/formulas

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
	@mkdir -p $(AF_TEST_TMPDIR)
	PATH="$(CURDIR)/$(VENV)/bin:$$PATH" TMPDIR=$(AF_TEST_TMPDIR) GOTMPDIR=$(AF_TEST_TMPDIR) CGO_ENABLED=0 go test -tags=integration -timeout=4m ./...
	@# Issue #425 Phase 5A: the web console is a SEPARATE Go module (web/go.mod), so the root
	@# `go test ./...` above never descends into it. Run its integration tier explicitly — the only
	@# `cd web` precedent is build-webui above. The `//go:build integration` bridge test lives here
	@# (web/internal/server/bridge_integration_test.go) and t.Skips when docker is absent, so CI
	@# (which runs `make test-integration` without a guaranteed docker host) stays green.
	cd web && TMPDIR=$(AF_TEST_TMPDIR) GOTMPDIR=$(AF_TEST_TMPDIR) CGO_ENABLED=0 go test -tags=integration -timeout=4m ./...

check-formulas:
	@fail=0; for f in internal/cmd/install_formulas/*.formula.toml; do \
		name=$$(basename "$$f"); \
		if ! diff -q "$$f" ".agentfactory/store/formulas/$$name" > /dev/null 2>&1; then \
			echo "DRIFT: $$name"; fail=1; \
		fi; \
	done; \
	if [ "$$fail" = "0" ]; then echo "Formulas in sync"; else echo "ERROR: Formula drift detected between source and installed copies"; exit 1; fi

check-skills:
	@fail=0; for d in internal/cmd/install_skills/*/; do \
		name=$$(basename "$$d"); \
		if ! diff -rq "$$d" ".claude/skills/$$name" > /dev/null 2>&1; then \
			echo "DRIFT: skill $$name differs"; fail=1; \
		fi; \
	done; \
	if [ "$$fail" = "0" ]; then echo "Skills in sync"; else echo "ERROR: Skill drift detected between source and installed copies"; exit 1; fi

check-formula-skills:
	@mkdir -p $(AF_TEST_TMPDIR)
	@TMPDIR=$(AF_TEST_TMPDIR) GOTMPDIR=$(AF_TEST_TMPDIR) go test -run TestEmbeddedFormulaSkillsAvailable ./internal/cmd/

# Regeneration parity check (af-13234830 Phase 3 / six_sigma_gaps Gap 5): proves the
# committed role templates in internal/templates/roles/ match what regeneration
# produces from the current formulas. A forgotten `agent-gen-all.sh` run after editing
# a formula would otherwise ship stale agent-identity prose — e.g. a branch literal
# fixed in the formula but never propagated into the rendered template — silently
# breaking agents on a non-main (e.g. master) repo.
#
# This is the honest alternative to un-skipping formula_template_drift_test.go (whose
# chicken-and-egg skip is real: a brand-new formula has no committed template until
# agent-gen runs in a live factory). It is intentionally NOT a `go test` and NOT part
# of `make test`: agent-gen-all.sh is non-hermetic — it needs `af` on PATH, runs
# `af down --all`, and regenerates agent artifacts — so it MUST run from the main repo
# checkout, never a git worktree. Depends on `install` to put a fresh `af` on PATH.
check-regen: install
	PATH="$(HOME)/.local/bin:$$PATH" bash agent-gen-all.sh --no-build
	git diff --exit-code internal/templates/roles/

sync-skills:
	@for d in internal/cmd/install_skills/*/; do \
		name=$$(basename "$$d"); \
		mkdir -p ".claude/skills/$$name"; \
		cp -r "$$d"* ".claude/skills/$$name/"; \
	done
	@echo "Skills synced: source -> installed"

# Used by quickstart.sh install_af() — do not rename without updating quickstart.sh
sync-formulas:
	@for f in internal/cmd/install_formulas/*.formula.toml; do \
		name=$$(basename "$$f"); \
		cp "$$f" ".agentfactory/store/formulas/$$name"; \
	done
	@echo "Formulas synced: source → installed"
