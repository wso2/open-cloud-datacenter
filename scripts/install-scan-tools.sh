#!/usr/bin/env bash
# install-scan-tools — opt-in, best-effort installer for the analyzers that
# scripts/pr-scan.sh runs. The scan itself NEVER auto-installs anything; you run
# this once (or whenever you start touching a new file type) to widen coverage.
#
# Usage:  scripts/install-scan-tools.sh   (or: make scan-tools)
#
# It installs via Homebrew (if present) and `go install` (if Go is present),
# tolerating tools that are already installed and continuing past any single
# failure. At the end it prints a one-line OK/MISSING availability check for every
# tool so you can see what is ready. Portable to macOS bash 3.2 (no associative
# arrays, no mapfile).
set -u

# Brew formulae the scanners use. terraform is here for `terraform fmt -check`.
BREW_TOOLS="golangci-lint gitleaks osv-scanner actionlint shellcheck tflint checkov hadolint yamllint markdownlint-cli semgrep trivy terraform"

# go install targets (module@version).
GO_TOOLS="github.com/oasdiff/oasdiff@latest golang.org/x/vuln/cmd/govulncheck@latest"

# Every command we ultimately want on PATH (for the final availability check).
CHECK_CMDS="golangci-lint gitleaks osv-scanner actionlint shellcheck tflint checkov hadolint yamllint markdownlint semgrep trivy terraform oasdiff govulncheck"

echo "== install-scan-tools =="

# ---- Homebrew formulae ----
if command -v brew >/dev/null 2>&1; then
  echo
  echo "-- brew install (tolerating already-installed) --"
  for t in $BREW_TOOLS; do
    echo ">> brew install $t"
    brew install "$t" || echo "   (skipped: brew install $t failed — continuing)"
  done
else
  echo
  echo "Homebrew (brew) not found — skipping the brew tools. Install them manually with"
  echo "your package manager. The full list is:"
  echo "  $BREW_TOOLS"
fi

# ---- go install targets ----
if command -v go >/dev/null 2>&1; then
  echo
  echo "-- go install --"
  for g in $GO_TOOLS; do
    echo ">> go install $g"
    go install "$g" || echo "   (skipped: go install $g failed — continuing)"
  done
  # Make freshly go-installed tools discoverable for the availability check below.
  GOBIN_DIR="$(go env GOPATH 2>/dev/null)/bin"
  case ":$PATH:" in *":$GOBIN_DIR:"*) :;; *) PATH="$GOBIN_DIR:$PATH";; esac
else
  echo
  echo "Go (go) not found — skipping the go-installed tools (oasdiff, govulncheck)."
  echo "Install Go, then re-run, or run manually:"
  for g in $GO_TOOLS; do echo "  go install $g"; done
fi

# ---- availability check ----
echo
echo "-- availability (what pr-scan can use now) --"
missing=0
for c in $CHECK_CMDS; do
  if command -v "$c" >/dev/null 2>&1; then
    echo "  OK       $c"
  else
    echo "  MISSING  $c"
    missing=$((missing + 1))
  fi
done

echo
if [ "$missing" -eq 0 ]; then
  echo "All scan tools are available."
else
  echo "$missing tool(s) still MISSING above — re-run after installing them, or install by hand."
fi
exit 0
