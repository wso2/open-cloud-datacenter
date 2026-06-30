---
name: pr-review
description: Pre-PR self-review modeled on how CodeRabbit actually works — a deterministic scanner layer (the same OSS analyzers CodeRabbit runs) plus an LLM multi-lens adversarial review across CodeRabbit's six review categories. Run on the branch/staged diff before every commit and PR, so the external reviewer (CodeRabbit) has little to add. Drops false positives via adversarial verification; runs the real build/test.
---

# pr-review — review your diff the way CodeRabbit will, first

Run this on the diff **before every commit + PR**. Goal: catch most of what CodeRabbit would post — *before* pushing — so it isn't publicly dismissing already-pushed code, and so the human reviewer sees a clean diff.

## How CodeRabbit actually works (what we're replicating)
CodeRabbit is **two layers**, not one LLM:
1. **~56 OSS analyzers** run in a sandbox (golangci-lint, Semgrep, Gitleaks, OSV-Scanner, ast-grep, ESLint, Checkov/Trivy/TFLint, Hadolint, oasdiff, actionlint, LanguageTool, …).
2. **A context-engineered LLM** that reads the diff + surrounding code + linked issues, then a **separate judge model** filters false positives before posting.

Its findings are tagged **severity** (Critical · Major · Minor · Trivial · Info) and triaged **Security → Correctness → Performance → Reliability → Best-practice → Style**. Its documented review taxonomy is **six categories** (below). It is high-recall but only ~middling precision (≈50%) and tends to be verbose/nitpicky — so our edge is **running the same scanners deterministically** (no guesswork) **and adversarially verifying the LLM findings** (kill the nits).

## Two layers, run in order

### Layer 1 — deterministic scanners (the cheap, certain half)
Run the same OSS tools CodeRabbit runs, locally, scoped to the diff:
```bash
make scan                 # from the repo root, or:
bash scripts/pr-scan.sh   # optional first arg: a base ref (default: merge-base with origin/controlplane)
```
It auto-selects tools by changed file type (Go → golangci-lint + govulncheck; secrets → gitleaks; deps → osv-scanner; `openapi.yaml` → redocly lint + **oasdiff breaking-change**; cloud-ui → eslint + tsc; `.tf` → tflint/checkov; Dockerfile → hadolint; workflows → actionlint; shell/yaml/md → shellcheck/yamllint/markdownlint; SAST → semgrep). Fix everything it reports — these are exactly the comments CodeRabbit's tool layer would post. It names any tool it couldn't run (not installed) so coverage gaps are explicit; widen coverage by installing them (see `docs/pr-review-gate.md`).

### Layer 2 — LLM multi-lens adversarial review (the judgment half)
The scanners can't reason about intent, design, or cross-file contracts. For that, run the engine via the Workflow tool:
```
Workflow({ scriptPath: ".claude/skills/pr-review/review.js",
           args: { base: "<base, e.g. origin/controlplane>", repo: "." } })
```
One reviewer per CodeRabbit category, each finding adversarially re-checked (try to *refute* it; drop if already-handled or uncertain), plus a real build/vet/test run. Returns `{ confirmed (by severity), build, droppedFalsePositives }`. **Don't have the LLM re-do what the scanners cover** (style, lint, known-vuln patterns) — it focuses on judgment.

## The rubric — CodeRabbit's six categories (+ tests, docs)
Each finding gets a **severity** (Critical/Major/Minor/Trivial/Info) and, where useful, an **effort** note (quick win / heavy lift).

1. **Security & Privacy** — injection (SQL/command), XSS, SSRF, path traversal, insecure deserialization, hardcoded secrets, weak crypto, broken authn/authz/access-control, missing input validation, PII/data exposure (OWASP Top 10). *(Layer-1: gitleaks/semgrep; Layer-2: authz logic, tenant/data isolation, reasoning about exploitability.)*
2. **Functional Correctness** — logic errors, wrong conditions, off-by-one, unhandled edge cases, null/None handling, **code that doesn't match its stated intent / the linked issue**.
3. **Stability & Reliability** — crashes, unhandled or **swallowed errors**, **resource leaks** (files/conns/goroutines/memory), **concurrency** (races, deadlocks, thread-safety, reentrancy), missing timeouts/retries, **version-skew / backward-compat across components**.
4. **Data Integrity & Integration** — data correctness, persistence/transaction bugs, schema mismatches, broken migrations, **API/schema contract breaks & backward-compat**. *(Layer-1: oasdiff for OpenAPI; Layer-2: semantic contract reasoning, cross-service effects the diff can't show.)*
5. **Performance & Scalability** — N+1 queries, missing caching, unoptimized/unbounded loops & responses, algorithmic complexity, hot-path allocations.
6. **Maintainability & Code Quality** — naming, structure/readability, **duplication/DRY**, complexity, **dead code / unused imports**, interface/API design — and **does a new method enforce the same preconditions/guards as its siblings?**
7. **Tests** — new branches/error paths/edge cases covered? a **regression test for each fixed bug** (the exact reported case)? are assertions *real* (not just "no error")? do they run + pass?
8. **Documentation & conventions** — stale comments/error-messages/helpers not updated to match the change; docs that now contradict the code; PR title/description quality; **plus every applicable rule from the repo's CLAUDE.md** (design patterns, layering, isolation, hard rules). Read CLAUDE.md first.

*(Style/formatting/typos are delegated to Layer-1 linters + LanguageTool-class tools — don't hand-nitpick them.)*

## Procedure
1. Scope the diff (base branch / merge-base). Note new/changed public surface (APIs, CLI, wire protocols, RBAC, DB schema, IaC).
2. **Layer 1:** run `make scan`; fix every real finding; note any uninstalled tool as a coverage gap.
3. **Layer 2:** run `review.js` for a non-trivial diff (inline a few lenses for a tiny one). Always keep the adversarial-verify step — it's what beats CodeRabbit's precision.
4. Triage confirmed findings **Security → Correctness → Performance → Reliability → Best-practice → Style**. Fix blockers + majors; decide minors/nits explicitly.
5. Re-run the affected scanner/lens after a substantive fix, then open the PR.
6. In the PR body, note "self-reviewed: scanners + N-lens review" so reviewers know the bar.

## Scale & gaps
Scale to the diff: a tiny change → `make scan` + a couple of lenses; a feature → the full set. Known CodeRabbit gaps to optionally cover yourself: i18n and license/SCA compliance. This is a **front-runner** for CodeRabbit, not a replacement — CodeRabbit still runs on the PR; see `docs/pr-review-gate.md` for how the two relate and how to enable the advisory pre-push hook (`make hooks`).
