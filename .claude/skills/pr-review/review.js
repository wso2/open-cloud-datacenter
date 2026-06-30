export const meta = {
  name: 'pr-review',
  description: 'LLM half of the pr-review gate: one reviewer per CodeRabbit review category (judgment, not lint), adversarial verification of each finding to drop false positives, a real build/vet/test, then synthesis. Pair with scripts/pr-scan.sh (the deterministic tool layer).',
  phases: [
    { title: 'Review', detail: 'one reviewer per category + a build/vet/test run' },
    { title: 'Verify', detail: 'adversarially confirm or refute each candidate finding' },
  ],
}

// args.base = review base (PR base branch / merge-base). args.repo = repo path (default: repo root).
const base = (args && args.base) || 'HEAD~1'
const repo = (args && args.repo) || '.'
// Working tree vs base (committed + staged + unstaged) — the gate runs before commit,
// so uncommitted changes must be in scope.
const diffRef = `git -C ${repo} diff ${base}`

const SEV = ['critical', 'major', 'minor', 'trivial', 'info']
const FINDINGS = {
  type: 'object', additionalProperties: false, required: ['findings'],
  properties: {
    findings: {
      type: 'array',
      items: {
        type: 'object', additionalProperties: false,
        required: ['file', 'severity', 'category', 'title', 'detail'],
        properties: {
          file: { type: 'string' }, line: { type: 'string' },
          severity: { enum: SEV }, category: { type: 'string' },
          title: { type: 'string' }, detail: { type: 'string' }, fix: { type: 'string' },
        },
      },
    },
  },
}

const SCANNERS_NOTE = 'The deterministic scanner layer (golangci-lint, govulncheck, gitleaks, osv-scanner, semgrep, eslint, oasdiff, etc.) already covers style, formatting, lint rules, secrets, known-vuln patterns, and OpenAPI breaking changes — do NOT re-report those. Focus only on what a linter cannot judge.'

const LENSES = [
  { key: 'security-privacy', focus: 'Authn/authz & access-control logic, secret handling, injection/SSRF/path-traversal reachability, tenant/data isolation, PII exposure, exploitability reasoning (OWASP Top 10) — the judgment a SAST tool misses.' },
  { key: 'correctness', focus: 'Logic errors, wrong conditions/boolean logic, off-by-one, unhandled edge cases, null/None handling, and code that does NOT match its stated intent or the linked issue.' },
  { key: 'reliability', focus: 'Crashes, unhandled or swallowed errors, resource leaks (files/connections/goroutines/memory), concurrency (races, deadlocks, thread-safety, reentrancy), missing timeouts/retries, and version-skew / backward-compat across components (new client vs old server).' },
  { key: 'data-integrity-contracts', focus: 'Data correctness, persistence/transaction bugs, schema mismatches, broken migrations, and API/schema contract breaks & backward-compat — including cross-service effects the diff alone cannot show. (oasdiff checks OpenAPI syntactically; you cover semantics + wire-compat across modules.)' },
  { key: 'performance', focus: 'N+1 queries, missing caching, unoptimized or unbounded loops/responses, algorithmic complexity, hot-path allocations.' },
  { key: 'maintainability-design', focus: 'Naming, structure/readability, duplication/DRY, complexity, dead code, interface/API design — and especially: does a NEW method enforce the SAME preconditions/guards as its sibling methods?' },
  { key: 'tests', focus: 'Are new branches/error paths/edge cases covered? a regression test for EACH fixed bug (the exact reported case)? are assertions real (not just no-error)?' },
  { key: 'docs-conventions', focus: 'Stale comments/error-messages/helpers not updated to match the change; docs now contradicting the code; PLUS every applicable rule from the repo CLAUDE.md (design patterns, layering, isolation, hard rules, scrub gate) — read CLAUDE.md in the repo first.' },
]

phase('Review')
const reviews = (await parallel(LENSES.map(l => () =>
  agent(
    `You are a strict senior code reviewer (CodeRabbit-grade). Review ONLY the change in \`${diffRef}\` (read surrounding code in ${repo} for context) through ONE category: ${l.key}.\nFocus: ${l.focus}\n${SCANNERS_NOTE}\nReport concrete, line-anchored findings only — no vague advice, no praise. Severity: critical (ship-stopper), major (fix before merge), minor, trivial, info. Return an empty findings array if clean on this category.`,
    { label: `lens:${l.key}`, phase: 'Review', schema: FINDINGS, agentType: 'Explore', effort: 'high' }
  )
))).filter(Boolean)

const BUILD = { type: 'object', additionalProperties: false, required: ['pass', 'detail'], properties: { pass: { type: 'boolean' }, detail: { type: 'string' } } }
const build = await agent(
  `In ${repo}, build + vet + test the code touched by \`${diffRef}\`. Go: go build ./..., go vet ./..., and go test (-race where concurrency is involved) on the affected packages; do each touched module separately. Run any linter relevant to the changed files. Report verbatim pass/fail per package/module.`,
  { label: 'build-test', phase: 'Review', schema: BUILD, agentType: 'Explore', effort: 'high' }
)

const candidates = reviews.flatMap(r => (r && r.findings) || [])
const rank = { critical: 0, major: 1, minor: 2, trivial: 3, info: 4 }

phase('Verify')
const VERDICT = { type: 'object', additionalProperties: false, required: ['real', 'reason'], properties: { real: { type: 'boolean' }, severity: { enum: SEV }, reason: { type: 'string' } } }
const verified = (await parallel(candidates.map(f => () =>
  agent(
    `Adversarially verify this review finding against the ACTUAL code in ${repo} (diff base ${base}). Try to REFUTE it: is it real, still valid, and NOT already handled elsewhere (a guard upstream, a caller-side check, an existing test, intentional design)? Read the cited file and its neighbours.\nFinding: ${f.severity} | ${f.category} | ${f.file}:${f.line || '?'} — ${f.title}\n${f.detail}\nDefault real=false when uncertain — a confident wrong finding costs more than a missed nit. Return {real, severity (your re-assessed severity), reason}.`,
    { label: `verify:${f.file}`, phase: 'Verify', schema: VERDICT, agentType: 'Explore', effort: 'high' }
  ).then(v => (v ? { ...f, verdict: v } : null))
))).filter(Boolean)

const confirmed = verified
  .filter(f => f.verdict.real)
  .map(f => ({ ...f, severity: f.verdict.severity || f.severity }))
  .sort((a, b) => rank[a.severity] - rank[b.severity])

return {
  confirmed,
  build,
  droppedFalsePositives: verified.filter(f => !f.verdict.real).map(f => ({ title: f.title, file: f.file, why: f.verdict.reason })),
}
