# The pre-PR review gate

A local, two-layer self-review you run **before opening a PR**. It front-runs the
external reviewer (CodeRabbit): it runs the same open-source analyzers CodeRabbit
runs and adds an LLM judgment pass, so most findings are fixed before the diff is
ever pushed. It is **not** a replacement for CodeRabbit — CodeRabbit still runs on
the PR — it just means there's little left for it to flag. This is the Terraform
module catalog, so the gate is **Terraform-first**.

## Quick start

**Using Claude Code?** Just work as normal. This branch's `CLAUDE.md` tells the agent
to run this gate against your diff before pushing, and the `pr-review` skill gives
it the procedure — so it runs `make scan` (and, for a feature-sized change, the LLM
review) against your changes on its own. One-time: `make scan-tools` installs the
analyzers; `make hooks` adds a hard pre-push backstop.

**Reviewing by hand (no Claude needed):**

1. **One-time:** `make scan-tools` (install the analyzers via Homebrew) and,
   recommended, `make hooks` (turn on the automatic pre-push scan).
2. **Before every PR:** `make scan` — runs the analyzers on your changed files;
   read what it flags and fix it.
3. **Push.** With `make hooks` on, the scan runs automatically and **blocks only on
   a detected secret** (everything else is advisory). Override once with
   `OCD_SKIP_PRESCAN=1 git push`.

## The two layers

### Layer 1 — deterministic scanners (`make scan`)
`scripts/pr-scan.sh` figures out what changed (the diff against the merge-base with
`origin/terraform`, falling back to `upstream/terraform` then `origin/main`), then
runs the analyzer matching each changed file type. Every tool is optional: if it's
installed and relevant files changed it runs; otherwise it's skipped and named under
"gaps" so coverage holes are explicit. A tool that prints findings (or exits
nonzero) has flagged something to fix.

| Tool | Runs when | Catches |
|---|---|---|
| **terraform fmt** + **tflint** + **checkov** + **trivy** | `*.tf` changed | Terraform formatting, lint, and IaC security/misconfiguration (see [Terraform PRs](#terraform-prs)) |
| **gitleaks** | always | Committed secrets/credentials (this is the only hard block in the hook) |
| **hadolint** | a `Dockerfile` changed | Dockerfile best-practice and correctness issues |
| **actionlint** (+ **zizmor**) | `.github/workflows/*` changed | GitHub Actions workflow errors (and Actions security) |
| **shellcheck** / **yamllint** / **markdownlint** | `*.sh` / `*.yaml` / `*.md` changed | Shell bugs, YAML errors, Markdown lint |
| **semgrep** | `*.go/ts/tsx/js/py` changed | SAST patterns across languages (for any helper scripts that ship alongside HCL) |

The script is macOS bash-3.2 safe and roots itself at the repo top level, so you can
run it from any subdirectory.

#### Terraform PRs

Terraform is first-class here because this branch *is* the Terraform module catalog.
When the diff touches any `*.tf` file, the scan runs four best-effort, init-free
checks (each only if the tool is installed; otherwise it is named under "gaps"):

| Check | What it does |
|---|---|
| `terraform fmt -check -recursive -diff` | Fails on unformatted HCL and prints the diff. No `terraform init` needed. |
| `tflint --recursive` | Terraform linter — provider-aware best-practice and correctness rules. |
| `checkov -d . --compact --quiet` | IaC security/compliance policy scan. Picks up the repo's `.checkov.yaml` automatically (so the catalog's documented skips, e.g. `CKV_TF_1` for example ref-tags, are honored). |
| `trivy config --quiet .` | IaC misconfiguration scan (a second, complementary policy engine). |

It deliberately does **not** run `terraform validate`: that needs `terraform init`
and provider downloads, which are environment-specific and out of scope for a local
pre-PR diff check. Install all four with `make scan-tools` (or `brew install
terraform tflint checkov trivy`).

#### Beyond this branch

This gate lives on the **terraform** branch (the shared Terraform module catalog).
The same checks are valuable in any other place that holds Terraform — most
notably the per-environment **consumer repos** that reference these modules, where
much of the day-to-day `*.tf` work actually happens. The gate is plain files with
no branch-specific dependencies, so to cover those PRs too, copy these same files
into the target repo and commit them there:

- `scripts/pr-scan.sh`
- `scripts/install-scan-tools.sh`
- `.githooks/pre-push`
- the `scan`, `scan-tools`, and `hooks` targets from the `Makefile` (plus their
  `.PHONY` and `help` entries)

Then run `make hooks` once in that checkout. The Terraform checks above will run on
every `*.tf` diff there exactly as they do here. (If the target repo's integration
branch isn't named `terraform`, adjust the base-detection list at the top of
`scripts/pr-scan.sh` and `.githooks/pre-push` to match.)

### Layer 2 — LLM multi-lens review (the `pr-review` skill)
Scanners can't reason about intent, design, or cross-module contracts. The `pr-review`
skill (`.claude/skills/pr-review/`) adds that: one reviewer per CodeRabbit review
category (security, correctness, reliability, data-integrity/contracts, performance,
maintainability, tests, docs), then an **adversarial verification** pass that tries
to refute each finding and drops false positives, plus a real fmt/validate/lint run.
It's told to skip anything the scanners already cover. For a module catalog its most
valuable lens is **contract/backward-compat**: a renamed or removed variable or
output is a breaking change for every consumer, and only the LLM lens reasons about
that.

## How to use it

- **One-time install (recommended first):** run `make scan-tools` to install the
  analyzers (best-effort via Homebrew; it tolerates already-installed tools and
  prints an OK/MISSING list at the end). The scan never auto-installs — it only
  names what is missing — so do this once up front, and again whenever you start
  touching a new file type.
- **Every push, automatically (recommended):** run `make hooks` once. This sets
  `git config core.hooksPath .githooks`, enabling `.githooks/pre-push`. On every
  `git push` it runs the scanners as **advisory** output (prints findings) and
  **hard-blocks only on a detected secret**. Skip a single push (e.g. you already
  reviewed, or a confirmed false positive) with `OCD_SKIP_PRESCAN=1 git push`.
- **On demand:** `make scan` (or `bash scripts/pr-scan.sh [base-ref]`) anytime.
- **In Claude Code:** invoke the `pr-review` skill, which runs Layer 1 and then the
  Layer-2 engine via the Workflow tool:
  `Workflow({ scriptPath: ".claude/skills/pr-review/review.js", args: { base: "origin/terraform", repo: "." } })`.

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
brew install terraform tflint checkov trivy gitleaks actionlint yamllint \
  shellcheck markdownlint-cli semgrep
```

Known CodeRabbit gaps you may want to cover by hand: i18n and license/SCA
compliance.
