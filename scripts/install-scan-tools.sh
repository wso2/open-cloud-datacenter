#!/usr/bin/env bash
# install-scan-tools — opt-in, best-effort installer for the analyzers that
# scripts/pr-scan.sh runs on this Terraform module catalog. The scan itself NEVER
# auto-installs anything; you run this once (or whenever you start touching a new
# file type) to widen coverage.
#
# Usage:  scripts/install-scan-tools.sh   (or: make scan-tools)
#
# It installs via Homebrew (if present), tolerating tools that are already
# installed and continuing past any single failure. At the end it prints a
# one-line OK/MISSING availability check for every tool so you can see what is
# ready. Portable to macOS bash 3.2 (no associative arrays, no mapfile).
set -u

# Brew formulae the scanners use. terraform/tflint/checkov/trivy are the core of
# the Terraform gate; the rest cover the workflows, shell, yaml, and docs that
# ship alongside the modules.
BREW_TOOLS="terraform tflint checkov trivy gitleaks actionlint yamllint shellcheck markdownlint-cli semgrep"

# Every command we ultimately want on PATH (for the final availability check).
CHECK_CMDS="terraform tflint checkov trivy gitleaks actionlint yamllint shellcheck markdownlint semgrep"

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
