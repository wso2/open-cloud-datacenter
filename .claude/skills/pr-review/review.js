export const meta = {
  name: 'pr-review',
  description: 'LLM half of the pr-review gate: one reviewer per CodeRabbit review category (judgment, not lint), adversarial verification of each finding to drop false positives, a real validate/lint run, then synthesis. Pair with scripts/pr-scan.sh (the deterministic tool layer).',
  phases: [
    { title: 'Review', detail: 'one reviewer per category + a validate/lint run' },
    { title: 'Verify', detail: 'adversarially confirm or refute each candidate finding' },
  ],
}

// args.base = review base (PR base branch / merge-base). args.repo = repo path (default: repo root).
const base = (args && args.base) || 'origin/terraform'
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

const SCANNERS_NOTE = 'The deterministic scanner layer (terraform fmt, tflint, checkov, trivy, gitleaks, actionlint, shellcheck, yamllint, semgrep, etc.) already covers formatting, lint rules, IaC security/misconfig policy, secrets, and known-vuln patterns — do NOT re-report those. Focus only on what a linter cannot judge.'

const LENSES = [
  { key: 'security-privacy', focus: 'Insecure defaults, over-broad IAM/RBAC, exposed or unencrypted resources, secret handling in variables/outputs, public network exposure, missing input validation — the judgment a policy scanner misses (e.g. a default that is technically allowed but wrong for this module).' },
  { key: 'correctness', focus: 'Logic errors, wrong conditions/count/for_each, off-by-one, unhandled edge cases, null handling, and code (HCL or examples) that does NOT match its stated intent, the variable description, or the linked issue.' },
  { key: 'reliability', focus: 'Resource lifecycle hazards (forced replacement, destroy-before-create, missing depends_on), provider/version-constraint drift, create_before_destroy gaps, missing timeouts/retries, and backward-compat for module consumers (a renamed/removed variable or output is a breaking change).' },
  { key: 'data-integrity-contracts', focus: 'Module input/output contract breaks & backward-compat — renamed/removed/retyped variables or outputs, changed defaults that silently alter consumer behavior, state-move hazards, and cross-module effects the diff alone cannot show.' },
  { key: 'performance', focus: 'Unnecessary resource churn, expensive data sources in hot paths, unbounded for_each/count, and patterns that scale poorly across many tenants/clusters.' },
  { key: 'maintainability-design', focus: 'Naming, structure/readability, duplication/DRY across modules, complexity, dead code, variable/output design — and especially: does a NEW module or resource follow the SAME conventions/guards as its siblings in the same family?' },
  { key: 'tests', focus: 'Are new variables/branches/examples exercised? does each example terraform-validate and stay fmt-clean? are README/example snippets consistent with the actual variables and outputs?' },
  { key: 'docs-conventions', focus: 'Stale comments/variable-descriptions/READMEs not updated to match the change; docs now contradicting the HCL; PLUS every applicable rule from the repo CLAUDE.md and the module-catalog conventions (fmt+validate, the .checkov.yaml skips, ref-pinning style) — read CLAUDE.md in the repo first.' },
]

phase('Review')
const reviews = (await parallel(LENSES.map(l => () =>
  agent(
    `You are a strict senior Terraform/IaC reviewer (CodeRabbit-grade). Review ONLY the change in \`${diffRef}\` (read surrounding code in ${repo} for context) through ONE category: ${l.key}.\nFocus: ${l.focus}\n${SCANNERS_NOTE}\nReport concrete, line-anchored findings only — no vague advice, no praise. Severity: critical (ship-stopper), major (fix before merge), minor, trivial, info. Return an empty findings array if clean on this category.`,
    { label: `lens:${l.key}`, phase: 'Review', schema: FINDINGS, agentType: 'Explore', effort: 'high' }
  )
))).filter(Boolean)

const BUILD = { type: 'object', additionalProperties: false, required: ['pass', 'detail'], properties: { pass: { type: 'boolean' }, detail: { type: 'string' } } }
const build = await agent(
  `In ${repo}, check the Terraform touched by \`${diffRef}\`. Run terraform fmt -check -recursive -diff, and tflint --recursive if available. For any module/example whose providers are already initialized (.terraform present), run terraform validate; otherwise note it needs terraform init and skip it (do not run init — it is environment-specific). Report verbatim pass/fail per module/example.`,
  { label: 'build-test', phase: 'Review', schema: BUILD, agentType: 'Explore', effort: 'high' }
)

const candidates = reviews.flatMap(r => (r && r.findings) || [])
const rank = { critical: 0, major: 1, minor: 2, trivial: 3, info: 4 }

phase('Verify')
const VERDICT = { type: 'object', additionalProperties: false, required: ['real', 'reason'], properties: { real: { type: 'boolean' }, severity: { enum: SEV }, reason: { type: 'string' } } }
const verified = (await parallel(candidates.map(f => () =>
  agent(
    `Adversarially verify this review finding against the ACTUAL code in ${repo} (diff base ${base}). Try to REFUTE it: is it real, still valid, and NOT already handled elsewhere (a variable default, a validation block, an existing example, intentional design, or a .checkov.yaml skip)? Read the cited file and its neighbours.\nFinding: ${f.severity} | ${f.category} | ${f.file}:${f.line || '?'} — ${f.title}\n${f.detail}\nDefault real=false when uncertain — a confident wrong finding costs more than a missed nit. Return {real, severity (your re-assessed severity), reason}.`,
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
