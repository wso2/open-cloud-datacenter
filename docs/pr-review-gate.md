# The pre-PR review gate

A local, two-layer self-review you run **before opening a PR**. It front-runs the
external reviewer (CodeRabbit): it runs the same open-source analyzers CodeRabbit
runs and adds an LLM judgment pass, so most findings are fixed before the diff is
ever pushed. It is **not** a replacement for CodeRabbit — CodeRabbit still runs on
the PR — it just means there's little left for it to flag.

## Quick start

**Using Claude Code?** Just work as normal. This repo's `CLAUDE.md` tells the agent
to run this gate against your diff before pushing, and the `pr-review` skill gives
it the procedure — so it runs `make scan` (and, for a feature-sized change, the LLM
review) against your changes on its own. One-time: `make scan-tools` installs the
analyzers; `make hooks` adds a hard pre-push backstop.

**Reviewing by hand (no Claude needed):**

1. **One-time:** `make scan-tools` (install the analyzers via brew + go) and,
   recommended, `make hooks` (turn on the automatic pre-push scan).
2. **Before every PR:** `make scan` — runs the analyzers on your changed files;
   read what it flags and fix it.
3. **Push.** With `make hooks` on, the scan runs automatically and **blocks only on
   a detected secret** (everything else is advisory). Override once with
   `OCD_SKIP_PRESCAN=1 git push`.

**Terraform PRs** (any `.tf` change): `make scan` runs `terraform fmt -check`,
tflint, checkov, and trivy. If your Terraform work is on the `terraform` branch (or
a consumer repo), the gate must be present there too — see
[Beyond controlplane](#beyond-controlplane) below.

## The two layers

### Layer 1 — deterministic scanners (`make scan`)
`scripts/pr-scan.sh` figures out what changed (the diff against the merge-base with
`origin/controlplane`, falling back to `upstream/controlplane` then `origin/main`),
then runs the analyzer matching each changed file type. Every tool is optional: if
it's installed and relevant files changed it runs; otherwise it's skipped and named
under "gaps" so coverage holes are explicit. A tool that prints findings (or exits
nonzero) has flagged something to fix.

| Tool | Runs when | Catches |
|---|---|---|
| **golangci-lint** | `*.go` changed | Go lint/vet/staticcheck issues (new findings vs base) |
| **govulncheck** | `*.go` changed | Known vulnerabilities in Go deps actually reachable from your code |
| **gitleaks** | always | Committed secrets/credentials (this is the only hard block in the hook) |
| **osv-scanner** | `go.mod/go.sum`, `package.json`, lockfiles changed | Known-vulnerable dependency versions |
| **redocly lint** + **oasdiff** | `openapi.yaml` changed | Spec lint errors; **breaking API changes** vs the base spec |
| **eslint** + **tsc** | `cloud-ui/**` TS/JS changed | Lint errors and TypeScript type errors in the web app |
| **terraform fmt** + **tflint** + **checkov** + **trivy** | `*.tf` changed | Terraform formatting, lint, and IaC security/misconfiguration (see [Terraform PRs](#terraform-prs)) |
| **hadolint** | a `Dockerfile` changed | Dockerfile best-practice and correctness issues |
| **actionlint** (+ **zizmor**) | `.github/workflows/*` changed | GitHub Actions workflow errors (and Actions security) |
| **shellcheck** / **yamllint** / **markdownlint** | `*.sh` / `*.yaml` / `*.md` changed | Shell bugs, YAML errors, Markdown lint |
| **semgrep** | `*.go/ts/tsx/js/py` changed | SAST patterns across languages |

The script is macOS bash-3.2 safe and roots itself at the repo top level, so you can
run it from any subdirectory.

#### Terraform PRs

Terraform is first-class here because most PRs on this stack are Terraform. When the
diff touches any `*.tf` file, the scan runs four best-effort, init-free checks (each
only if the tool is installed; otherwise it is named under "gaps"):

| Check | What it does |
|---|---|
| `terraform fmt -check -recursive -diff` | Fails on unformatted HCL and prints the diff. No `terraform init` needed. |
| `tflint --recursive` | Terraform linter — provider-aware best-practice and correctness rules. |
| `checkov -d . --compact --quiet` | IaC security/compliance policy scan. |
| `trivy config --quiet .` | IaC misconfiguration scan (a second, complementary policy engine). |

It deliberately does **not** run `terraform validate`: that needs `terraform init`
and provider downloads, which are environment-specific and out of scope for a local
pre-PR diff check. Install all four with `make scan-tools` (or `brew install
terraform tflint checkov trivy`).

#### Beyond controlplane

This gate lives on the **controlplane** branch (the control-plane monorepo). The
shared Terraform module catalog lives on the **terraform** branch, and each
environment lives in a separate consumer repo — and that is where most Terraform
PRs are actually raised. The gate is plain files with no controlplane-specific
dependencies, so to cover those PRs too, copy these same files into the target
repo/branch and commit them there:

- `scripts/pr-scan.sh`
- `scripts/install-scan-tools.sh`
- `.githooks/pre-push`
- `.gitleaks.toml` (allowlists the gate's own `OCD_SKIP_PRESCAN` escape-hatch string
  so gitleaks doesn't flag it in help text/scripts/docs)
- the `scan`, `scan-tools`, and `hooks` targets from the `Makefile` (plus their
  `.PHONY` and `help` entries)

Then run `make hooks` once in that checkout. The Terraform checks above will run on
every `*.tf` diff there exactly as they do here.

### Layer 2 — LLM multi-lens review (the `pr-review` skill)
Scanners can't reason about intent, design, or cross-file contracts. The `pr-review`
skill (`.claude/skills/pr-review/`) adds that: one reviewer per CodeRabbit review
category (security, correctness, reliability, data-integrity/contracts, performance,
maintainability, tests, docs), then an **adversarial verification** pass that tries
to refute each finding and drops false positives, plus a real build/vet/test. It's
told to skip anything the scanners already cover.

## How to use it

- **One-time install (recommended first):** run `make scan-tools` to install the
  analyzers (best-effort via Homebrew + `go install`; it tolerates already-installed
  tools and prints an OK/MISSING list at the end). The scan never auto-installs — it
  only names what is missing — so do this once up front, and again whenever you start
  touching a new file type.
- **Every push, automatically (recommended):** run `make hooks` once. This sets
  `git config core.hooksPath .githooks`, enabling `.githooks/pre-push`. On every
  `git push` it runs the scanners as **advisory** output (prints findings) and
  **hard-blocks only on a detected secret**. Skip a single push (e.g. you already
  reviewed, or a confirmed false positive) with `OCD_SKIP_PRESCAN=1 git push`.
- **On demand:** `make scan` (or `bash scripts/pr-scan.sh [base-ref]`) anytime.
- **In Claude Code:** invoke the `pr-review` skill, which runs Layer 1 and then the
  Layer-2 engine via the Workflow tool:
  `Workflow({ scriptPath: ".claude/skills/pr-review/review.js", args: { base: "origin/controlplane", repo: "." } })`.

Fix what the gate flags, then open the PR — and note in the description that you
self-reviewed (scanners + N-lens review) so reviewers know the bar.

## How it relates to CodeRabbit

CodeRabbit is itself two layers: ~56 OSS analyzers in a sandbox, plus a
context-engineered LLM with a separate judge model that filters false positives.
This gate mirrors that locally — the same analyzers, the same multi-category LLM
review with adversarial verification — so it acts as a **front-runner**: it catches
most of what CodeRabbit would post, before the code is pushed. CodeRabbit still runs
on the PR as the backstop; the goal is simply that it finds little to add.

## Widen coverage

`make scan` names any analyzer it couldn't run because it isn't installed, and points
you at `make scan-tools`. The simplest path is to run that installer — it covers
everything below in one shot (best-effort, tolerating already-installed tools):

```bash
make scan-tools
```

To install by hand instead (or on a machine without Homebrew), the equivalent set is:

```bash
# Homebrew
brew install golangci-lint gitleaks osv-scanner actionlint shellcheck tflint \
  checkov hadolint yamllint markdownlint-cli semgrep trivy terraform

# Go-installed (resolved via $(go env GOPATH)/bin, which the script adds to PATH)
go install github.com/oasdiff/oasdiff@latest
go install golang.org/x/vuln/cmd/govulncheck@latest
```

Known CodeRabbit gaps you may want to cover by hand: i18n and license/SCA
compliance.
