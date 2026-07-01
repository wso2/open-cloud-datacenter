#!/usr/bin/env bash
# pr-scan — the deterministic scanner layer of the pre-PR review gate. Runs the
# same open-source analyzers CodeRabbit runs in its sandbox, locally, scoped to
# your diff, BEFORE you push — so its tool-based comments are pre-empted.
#
# Every tool is optional: installed + relevant files changed -> run; otherwise it
# is skipped and named under "gaps" so coverage holes are explicit. A tool
# printing findings (or exiting nonzero) means it FLAGGED something — read that
# section and fix it before opening the PR.
#
# Usage:  scripts/pr-scan.sh [base-ref]
#   base-ref  defaults to the first available of origin/controlplane,
#             upstream/controlplane, origin/main, else HEAD~1. The changed-file set
#             is the WORKING TREE (committed + staged + unstaged) diffed against the
#             merge-base of HEAD with that ref — so it reflects what you're about to
#             push/commit, not just committed history.
#
# Run it from anywhere in the repo — it roots itself at the repo top level.
# Portable to macOS bash 3.2 (no mapfile / no associative arrays).
set -u

# Root at the repo top level regardless of where we were invoked from.
REPO="$(git rev-parse --show-toplevel 2>/dev/null)" || { echo "pr-scan: not inside a git repo"; exit 2; }
cd "$REPO" || { echo "pr-scan: cannot cd to $REPO"; exit 2; }

# Make go-installed tools (oasdiff, govulncheck) discoverable. `go install` writes
# to $GOBIN when set, else to $GOPATH/bin — prepend both so either layout works.
if command -v go >/dev/null 2>&1; then
  GO_BIN_ENV="$(go env GOBIN 2>/dev/null)"
  [ -n "$GO_BIN_ENV" ] && case ":$PATH:" in *":$GO_BIN_ENV:"*) :;; *) PATH="$GO_BIN_ENV:$PATH";; esac
  GOBIN_DIR="$(go env GOPATH 2>/dev/null)/bin"
  case ":$PATH:" in *":$GOBIN_DIR:"*) :;; *) PATH="$GOBIN_DIR:$PATH";; esac
fi

BASE="${1:-}"
if [ -z "$BASE" ]; then
  for b in origin/controlplane upstream/controlplane origin/main; do
    git rev-parse --verify --quiet "$b" >/dev/null 2>&1 && { BASE="$b"; break; }
  done
  [ -z "$BASE" ] && BASE="HEAD~1"
fi
MB="$(git merge-base HEAD "$BASE" 2>/dev/null || echo "$BASE")"

FL="$(mktemp)"; OASB="$(mktemp)"; trap 'rm -f "$FL" "$OASB"' EXIT
# Scan the WORKING TREE vs the merge-base (committed + staged + unstaged), since the
# gate is meant to run before commit. Base versions are read with `git show $MB:...`.
git diff --name-only "$MB" > "$FL" 2>/dev/null
N=$(grep -c . "$FL" 2>/dev/null || echo 0)
echo "== pr-scan scanners ==  base=$BASE  merge-base=${MB:0:12}  changed-files=$N"
[ "$N" -eq 0 ] && { echo "nothing changed vs base — nothing to scan"; exit 0; }

have(){ command -v "$1" >/dev/null 2>&1; }
match(){ grep -qiE "$1" "$FL"; }
sec(){ echo; echo "### $1"; }
GAPS=""
gap(){ GAPS="$GAPS
  - $1  ($2)"; }

# ---- Go (multi-module: run in each module root that owns a changed .go file) ----
if match '\.go$'; then
  MODS="$(grep -iE '\.go$' "$FL" | while IFS= read -r f; do
            d="$(dirname "$f")"
            while [ "$d" != "." ] && [ "$d" != "/" ] && [ ! -f "$d/go.mod" ]; do d="$(dirname "$d")"; done
            [ -f "$d/go.mod" ] && echo "$d"
          done | sort -u)"
  if have golangci-lint; then
    echo "$MODS" | while IFS= read -r m; do [ -n "$m" ] || continue
      sec "golangci-lint ($m)"; ( cd "$m" && golangci-lint run --new-from-rev "$MB" ./... 2>&1 | head -80 ); done
  else gap "golangci-lint" "brew install golangci-lint"; fi
  if have govulncheck; then
    echo "$MODS" | while IFS= read -r m; do [ -n "$m" ] || continue
      sec "govulncheck ($m)"; ( cd "$m" && govulncheck ./... 2>&1 | head -40 ); done
  else gap "govulncheck" "go install golang.org/x/vuln/cmd/govulncheck@latest"; fi
fi

# ---- secrets (always; scoped to the new commits) ----
if have gitleaks; then sec "gitleaks (secrets)"; gitleaks git --no-banner --redact --log-opts="$MB..HEAD" 2>&1 | tail -30
else gap "gitleaks" "brew install gitleaks"; fi

# ---- dependency vulns ----
if match 'go\.(mod|sum)$|package\.json$|pnpm-lock|yarn\.lock$'; then
  if have osv-scanner; then sec "osv-scanner (deps)"; osv-scanner scan source -r . 2>&1 | tail -40
  else gap "osv-scanner" "brew install osv-scanner"; fi
fi

# ---- OpenAPI: lint + breaking-change vs base (every changed spec, not just one) ----
if match 'openapi\.ya?ml$'; then
  # Record coverage gaps once, at top level (a `grep | while` body is a subshell and
  # would lose GAPS mutations). The per-spec loop below only runs the tools.
  have npx     || gap "redocly lint" "needs npx/node"
  have oasdiff || gap "oasdiff" "go install github.com/oasdiff/oasdiff@latest"
  grep -iE 'openapi\.ya?ml$' "$FL" | while IFS= read -r SPEC; do [ -n "$SPEC" ] || continue
    if have npx; then sec "redocly lint ($SPEC)"; npx --yes @redocly/cli lint "$SPEC" 2>&1 | tail -20; fi
    if have oasdiff; then sec "oasdiff ($SPEC, breaking changes)"
      if git show "$MB:$SPEC" > "$OASB" 2>/dev/null; then oasdiff breaking "$OASB" "$SPEC" 2>&1 | head -40
      else echo "(no base version of $SPEC — treated as new)"; fi
    fi
  done
fi

# ---- cloud-ui TS (project-local eslint + tsc) ----
if match 'cloud-ui/.*\.(ts|tsx|js|jsx)$'; then
  if [ -d cloud-ui ] && have pnpm; then
    sec "eslint + tsc (cloud-ui)"; ( cd cloud-ui && pnpm exec eslint . 2>&1 | tail -50; pnpm exec tsc --noEmit 2>&1 | tail -20 )
  else gap "cloud-ui lint" "corepack enable && pnpm -C cloud-ui install"; fi
fi

# ---- Terraform (first-class: the team mostly sends TF PRs) ----
# All best-effort and init-free; we deliberately do NOT run `terraform validate`
# (it needs `terraform init`/providers, which is environment-specific).
if match '\.tf$'; then
  if have terraform; then sec "terraform fmt -check"; terraform fmt -check -recursive -diff 2>&1 | head -60
  else gap "terraform fmt" "brew install terraform"; fi
  if have tflint; then sec "tflint"; tflint --recursive 2>&1 | head -40; else gap "tflint" "brew install tflint"; fi
  if have checkov; then sec "checkov (IaC security)"; checkov -d . --compact --quiet 2>&1 | tail -40; else gap "checkov" "brew install checkov"; fi
  if have trivy; then sec "trivy config (IaC misconfig)"; trivy config --quiet . 2>&1 | tail -40; else gap "trivy" "brew install trivy"; fi
fi

# ---- Dockerfiles ----
if match '(^|/)Dockerfile'; then
  if have hadolint; then sec "hadolint"; grep -iE '(^|/)Dockerfile' "$FL" | while IFS= read -r f; do [ -f "$f" ] && { echo "-- $f"; hadolint "$f"; }; done 2>&1 | head -40
  else gap "hadolint" "brew install hadolint"; fi
fi

# ---- GitHub Actions ----
if match '\.github/workflows/.*\.ya?ml$'; then
  if have actionlint; then sec "actionlint"; actionlint 2>&1 | head -40; else gap "actionlint" "brew install actionlint"; fi
  if have zizmor; then sec "zizmor (Actions security)"; zizmor .github/workflows 2>&1 | tail -30; else gap "zizmor" "brew install zizmor"; fi
fi

# ---- shell / yaml / markdown ----
if match '\.sh$'; then
  if have shellcheck; then sec "shellcheck"; grep -iE '\.sh$' "$FL" | while IFS= read -r f; do [ -f "$f" ] && shellcheck "$f"; done 2>&1 | head -60
  else gap "shellcheck" "brew install shellcheck"; fi
fi
if match '\.ya?ml$'; then
  if have yamllint; then sec "yamllint"; grep -iE '\.ya?ml$' "$FL" | while IFS= read -r f; do [ -f "$f" ] && yamllint -f parsable "$f"; done 2>&1 | head -60
  else gap "yamllint" "brew install yamllint"; fi
fi
if match '\.md$'; then
  if have markdownlint; then sec "markdownlint"; grep -iE '\.md$' "$FL" | while IFS= read -r f; do [ -f "$f" ] && markdownlint "$f"; done 2>&1 | head -40
  else gap "markdownlint" "npm i -g markdownlint-cli"; fi
fi

# ---- SAST (multi-language) ----
if match '\.(go|ts|tsx|js|py)$'; then
  if have semgrep; then sec "semgrep (SAST)"; semgrep --config auto --error -q 2>&1 | tail -60
  else gap "semgrep" "brew install semgrep"; fi
fi

echo
echo "== summary =="
echo "Read each section above. Findings (or a nonzero tool exit) = something to address."
if [ -n "$GAPS" ]; then
  echo "not installed for files in this diff (install to widen coverage):$GAPS"
  echo
  echo "Install them all at once:  make scan-tools  (best-effort brew + go installer)"
else
  echo "all applicable scanners were available."
fi
