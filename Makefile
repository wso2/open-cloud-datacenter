.PHONY: help scan scan-tools hooks

# Default target — print help
help:
	@echo "Usage:"
	@echo "  make scan        Run the pre-PR scanner gate on your diff (OSS analyzers)"
	@echo "  make scan-tools  Install the analyzers make scan uses (brew)"
	@echo "  make hooks       Enable the advisory pre-push scanner hook (one-time)"

# ── Pre-PR review gate ──────────────────────────────────────────────────────
# Run the deterministic scanner layer (the OSS analyzers CodeRabbit runs) on your
# diff before opening a PR; `make hooks` wires it as an advisory pre-push hook.
scan:
	bash scripts/pr-scan.sh

# Opt-in, best-effort installer for the analyzers `make scan` runs (brew).
# The scan never auto-installs; run this once to widen coverage.
scan-tools:
	bash scripts/install-scan-tools.sh

hooks:
	git config core.hooksPath .githooks
	@echo "pre-push gate active — scripts/pr-scan.sh runs on push (skip once: OCD_SKIP_PRESCAN=1 git push)"
