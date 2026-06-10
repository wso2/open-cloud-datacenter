---
name: address-reviews
description: Fetch and resolve review comments on an active PR — applies justified fixes, runs the checks appropriate to the changed files, commits, and pushes. Use when asked to address/resolve PR review feedback. Takes an optional PR number; defaults to the current branch's PR.
---

# Address PR review comments

## Parameters

- Optional PR number. If omitted, use the current branch's PR (`gh pr view --json number`).

## Steps

1. Fetch the feedback: `gh pr view <number> --comments` plus inline review comments via `gh api repos/{owner}/{repo}/pulls/<number>/comments`. Include both bot reviewers (e.g. coderabbitai) and humans.
2. For each comment:
   - Identify the file and line it targets.
   - Apply the fix if it aligns with the project standards in CLAUDE.md. Use the Edit tool — never in-place stream editors.
   - If a suggestion is rejected, prepare a brief justification to post in the reply.
3. Run the checks that match what changed (skip the rest):
   - Go files → `go build ./...` and `go test ./...` from the affected module (`dc-api/` or `dcctl/`)
   - `dc-api/openapi.yaml` → `npx @redocly/cli lint dc-api/openapi.yaml`, then regen consumers if shapes changed (`pnpm gen:api` in cloud-ui, `go generate ./internal/client/generated/...` in dcctl)
   - cloud-ui files → `pnpm lint && pnpm exec tsc --noEmit` from `cloud-ui/`
   - `.tf` files → `terraform fmt` and `terraform validate`
4. Commit with `git add <specific files>` (never `-a`). The commit message follows the repo convention — plain imperative subject, capitalized, no conventional-commits prefix, describing the actual change (e.g. `Fix nil check in subnet teardown`), NOT a generic "address review comments". Use the body to reference the review round if useful.
5. Push, then reply to the addressed comments (or post one summary comment) noting what was fixed and what was rejected with the justification.
