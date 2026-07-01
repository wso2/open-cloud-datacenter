---
name: pr-review
description: Pre-PR self-review modeled on how CodeRabbit actually works — a deterministic scanner layer (the same OSS analyzers CodeRabbit runs) plus an LLM multi-lens adversarial review across CodeRabbit's six review categories. Run on the branch/staged diff before every commit and PR, so the external reviewer (CodeRabbit) has little to add. Drops false positives via adversarial verification; runs the real validate/lint.
---

# pr-review — review your diff the way CodeRabbit will, first

Run this on the diff **before every commit + PR**. Goal: catch most of what CodeRabbit would post — *before* pushing — so it isn't publicly dismissing already-pushed code, and so the human reviewer sees a clean diff. This is the Terraform module catalog, so the gate is **Terraform-first**.

## How CodeRabbit actually works (what we're replicating)
CodeRabbit is **two layers**, not one LLM:
1. **~56 OSS analyzers** run in a sandbox (Checkov/Trivy/TFLint for IaC, Gitleaks, Semgrep, ast-grep, actionlint, ShellCheck, yamllint, LanguageTool, …).
2. **A context-engineered LLM** that reads the diff + surrounding code + linked issues, then a **separate judge model** filters false positives before posting.

Its findings are tagged **severity** (Critical · Major · Minor · Trivial · Info) and triaged **Security → Correctness → Performance → Reliability → Best-practice → Style**. Its documented review taxonomy is **six categories** (below). It is high-recall but only ~middling precision (≈50%) and tends to be verbose/nitpicky — so our edge is **running the same scanners deterministically** (no guesswork) **and adversarially verifying the LLM findings** (kill the nits).

## Two layers, run in order

### Layer 1 — deterministic scanners (the cheap, certain half)
Run the same OSS tools CodeRabbit runs, locally, scoped to the diff:
```bash
make scan                 # from the repo root, or:
bash scripts/pr-scan.sh   # optional first arg: a base ref (default: merge-base with origin/terraform)
```
It auto-selects tools by changed file type (`.tf` → terraform fmt + tflint + checkov + trivy; secrets → gitleaks; Dockerfile → hadolint; workflows → actionlint; shell/yaml/md → shellcheck/yamllint/markdownlint; SAST → semgrep). Checkov picks up the repo's `.checkov.yaml` automatically. Fix everything it reports — these are exactly the comments CodeRabbit's tool layer would post. It names any tool it couldn't run (not installed) so coverage gaps are explicit; widen coverage by installing them (see `docs/pr-review-gate.md`).

### Layer 2 — LLM multi-lens adversarial review (the judgment half)
The scanners can't reason about intent, design, or cross-module contracts. For that, run the engine via the Workflow tool:
```text
Workflow({ scriptPath: ".claude/skills/pr-review/review.js",
           args: { base: "<base, e.g. origin/terraform>", repo: "." } })
```
One reviewer per CodeRabbit category, each finding adversarially re-checked (try to *refute* it; drop if already-handled or uncertain), plus a real fmt/validate/lint run. Returns `{ confirmed (by severity), build, droppedFalsePositives }`. **Don't have the LLM re-do what the scanners cover** (formatting, lint, known policy patterns) — it focuses on judgment.

## The rubric — CodeRabbit's six categories (+ tests, docs)
Each finding gets a **severity** (Critical/Major/Minor/Trivial/Info) and, where useful, an **effort** note (quick win / heavy lift).

1. **Security & Privacy** — insecure defaults, over-broad IAM/RBAC, unencrypted or publicly-exposed resources, secrets in variables/outputs, missing input validation, data exposure. *(Layer-1: checkov/trivy/gitleaks; Layer-2: defaults that are technically allowed but wrong for the module, reasoning about real exposure.)*
2. **Functional Correctness** — logic errors, wrong `count`/`for_each`/conditions, off-by-one, unhandled edge cases, null handling, **HCL or examples that don't match the stated intent / the variable description / the linked issue**.
3. **Stability & Reliability** — resource-lifecycle hazards (forced replacement, destroy-before-create, missing `depends_on`/`create_before_destroy`), **provider/version-constraint drift**, missing timeouts/retries, **backward-compat for module consumers**.
4. **Data Integrity & Integration** — **module input/output contract breaks & backward-compat** (renamed/removed/retyped variables or outputs, changed defaults that silently alter consumer behavior), state-move hazards, cross-module effects the diff can't show. *(Layer-1: none for HCL contracts; Layer-2 owns this.)*
5. **Performance & Scalability** — unnecessary resource churn, expensive data sources in hot paths, unbounded `for_each`/`count`, patterns that scale poorly across many tenants/clusters.
6. **Maintainability & Code Quality** — naming, structure/readability, **duplication/DRY across modules**, complexity, **dead code** — and **does a new module/resource follow the same conventions/guards as its siblings in the same family?**
7. **Tests** — new variables/branches/examples exercised? does each example **fmt-clean and `terraform validate`** (where providers are initialized)? are README/example snippets consistent with the actual variables and outputs?
8. **Documentation & conventions** — stale variable descriptions/READMEs not updated to match the change; docs that now contradict the HCL; PR title/description quality; **plus every applicable rule from the repo's CLAUDE.md** (catalog conventions, `.checkov.yaml` skips, ref-pinning style). Read CLAUDE.md first.

*(Formatting/style/typos are delegated to Layer-1 linters + LanguageTool-class tools — don't hand-nitpick them.)*

## Procedure
1. Scope the diff (base branch / merge-base). Note new/changed public surface (module variables, outputs, examples, new modules).
2. **Layer 1:** run `make scan`; fix every real finding; note any uninstalled tool as a coverage gap.
3. **Layer 2:** run `review.js` for a non-trivial diff (inline a few lenses for a tiny one). Always keep the adversarial-verify step — it's what beats CodeRabbit's precision.
4. Triage confirmed findings **Security → Correctness → Performance → Reliability → Best-practice → Style**. Fix blockers + majors; decide minors/nits explicitly.
5. Re-run the affected scanner/lens after a substantive fix, then open the PR.
6. In the PR body, note "self-reviewed: scanners + N-lens review" so reviewers know the bar.

## Scale & gaps
Scale to the diff: a one-variable tweak → `make scan` + a couple of lenses; a new module → the full set. Known CodeRabbit gaps to optionally cover yourself: i18n and license/SCA compliance. This is a **front-runner** for CodeRabbit, not a replacement — CodeRabbit still runs on the PR; see `docs/pr-review-gate.md` for how the two relate and how to enable the advisory pre-push hook (`make hooks`).
