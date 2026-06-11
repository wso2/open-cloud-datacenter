# cloud-ui

Web UI for the WSO2 Infrastructure Platform Control Plane (`dc-api`).

## Stack

- React 19 + TypeScript 5.6 + Vite 8
- Fluent UI v9 (`@fluentui/react-components`) themed with the WSO2 orange brand ramp
- TanStack Query for server state, React Router 7 for routing
- `oidc-client-ts` for Asgardeo PKCE auth (wired in Chunk 1)
- `openapi-typescript` + `openapi-fetch` for the typed API client (generated from `../dc-api/openapi.yaml` in Chunk 1)
- Vitest + React Testing Library for tests
- pnpm for package management

## Setup

```bash
pnpm install
pnpm dev      # http://localhost:5173
```

## Build & verify

```bash
pnpm build       # tsc -b && vite build
pnpm lint        # eslint
pnpm format      # prettier --write
pnpm test        # vitest run
```

## Layout

```
src/
  theme/      Fluent BrandVariants + light/dark themes
  test/       Vitest setup
  App.tsx     Root component
  main.tsx    Provider tree (Fluent + TanStack Query)
```

`src/api/generated/` is gitignored and produced by `openapi-typescript ../dc-api/openapi.yaml -o src/api/generated/types.ts` (added in Chunk 1).
