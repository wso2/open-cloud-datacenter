---
name: add-endpoint
description: End-to-end workflow for adding or changing a dc-api endpoint — spec-first design, handler, router wiring, tests, and regenerating every spec consumer (cloud-ui types, dcctl client) in a single PR. Use whenever a task adds a new API endpoint/resource or changes an existing endpoint's path, method, request/response shape, status code, or auth requirement.
---

# Add or change a dc-api endpoint

The OpenAPI spec is the contract; three consumers are generated from it. Skipping a step here is how spec drift and broken client builds happen, so run ALL applicable steps — in this order — and don't report done until each has actually run.

## Steps

1. **Design spec-first.** Delegate the request/response design to the `api-designer` agent. Add the result to `dc-api/openapi.yaml`, reusing existing schema/naming patterns (snake_case fields, `Tenant → Project → Resource` paths, flat `{"error": ...}` envelope, 202-async for long ops). Lint:
   ```bash
   npx @redocly/cli lint dc-api/openapi.yaml
   ```
2. **Implement the Go handler** (delegate to `backend-developer` for non-trivial logic). Handlers depend on interfaces only — repository for SQL, provider interfaces for backends.
3. **Wire it into `dc-api/internal/api/router.go`**, behind the auth middleware like every other `/v1/*` route. `go build ./...` must pass.
4. **Write tests** (delegate to `test-engineer`): unit tests beside the code; an integration test under `dc-api/test/integration/` if the endpoint touches live backends. Check whether the contract-test `tagRegex` in `dc-api/test/contract/contract_test.go` should cover the new operation (it should if the endpoint works with nopped backends).
5. **Regenerate cloud-ui types** — from `cloud-ui/`:
   ```bash
   pnpm gen:api && pnpm exec tsc --noEmit && pnpm lint
   ```
   If the UI will use the endpoint, write the calling code now while the spec is fresh.
6. **Regenerate the dcctl client** — `go generate ./internal/client/generated/...` from `dcctl/`, then update the hand-written wrappers in `dcctl/internal/client/` and add the noun-verb command if the CLI exposes it (delegate to `cli-developer`).
7. **Update docs** (delegate to `docs-writer`): endpoint reference, CLI help text, and any operator guide the change affects.
8. **One PR, all of it.** Spec, handler, tests, regenerated clients, and docs ship together — reviewers reject PRs that change a handler without the matching spec update.

## Verification before reporting

- `npx @redocly/cli lint` clean
- `go build ./...` + `go test ./...` in dc-api (paste the summary verbatim)
- `pnpm exec tsc --noEmit` clean in cloud-ui
- dcctl builds (`go build ./...` in dcctl)
