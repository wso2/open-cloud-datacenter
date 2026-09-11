# Harv Logs Grafana Plugin

This directory contains the custom `harv-logs-app` Grafana app. It correlates Harvester support-bundle logs from Loki into an incident timeline and can use `grafana-llm-app` to generate AI-assisted root-cause analysis.

## Requirements

- Node.js 22 or newer
- npm 11.16.0
- Go 1.26.5
- Mage
- Docker Engine with the Compose plugin for local Grafana testing

Install dependencies from this directory:

```bash
npm install
```

## Architecture

The plugin has two parts:

- **Frontend:** React and TypeScript components built with webpack. It provides the RCA timeline, incident picker, report, chat, and configuration views.
- **Backend:** A Go Grafana plugin executable named `gpx_rca`. It queries Loki, applies the RCA rules, builds the incident timeline, and proxies report and chat requests to the LLM app.

Plugin metadata is defined in `src/plugin.json`. It registers the RCA page at `/a/harv-logs-app` and the Configuration page at `/plugins/harv-logs-app`. Grafana requires version 12.3 or newer, and the root stack provisions the Loki datasource with the UID `loki`.

## Development Workflow

1. Run the checks changes:

```bash
npm run typecheck
npm run lint
npm run test:ci
go test ./...
```

2. Build and run the plugin fronend locally with these scripts:

```bash
npm run build 
```

3. The Mage build entry point is available for backend/plugin packaging:

```bash
mage -v
```

4. Start the plugin development Compose stack

```bash
npm run server
```

The `server` script starts the Compose setup in this directory. For an end-to-end RCA test, make sure Loki contains a loaded support bundle and that the Grafana plugin settings point to the correct Loki datasource UID.

The production build writes the complete plugin bundle to `dist/`. The root project mounts that directory into Grafana as the unsigned `harv-logs-app` app.


### Supplementary Command Reference

```bash
npm run dev     # Watch and rebuild frontend assets
npm run e2e     # Run Playwright browser tests
npm run sign    # Sign the plugin when signing credentials are available
```

## RCA Data Flow

1. The user selects an incident window in the RCA page.
2. The frontend calls the plugin backend through `src/api/backend.ts`.
3. The backend queries Loki through Grafana's datasource proxy.
4. Rules from `rules/rca-rules.yaml` match and correlate events across the
   configured namespaces and optional node logs.
5. The frontend renders the timeline and can stream an AI report or chat
   response through `grafana-llm-app`.

## Folder Structure

```text
grafana-plugin/
├── pkg/plugin/       Go backend for Loki, RCA rules, timelines, and AI calls
├── src/               React/TypeScript frontend and plugin metadata
│   ├── api/           Frontend calls to the backend
│   ├── components/    Incident picker, timeline, report, chat, and app UI
│   ├── pages/         RCA and configuration page entry points
│   ├── types/         Shared frontend data types
│   ├── utils/         Routing and frontend helpers
│   └── plugin.json    Grafana app metadata and page registration
├── rules/             YAML rules used to correlate log events
├── tests/             Playwright end-to-end tests
├── provisioning/      Grafana provisioning for local development
├── configs/            Loki and Promtail configuration for local development
├── dist/              Generated plugin bundle mounted into Grafana
├── webpack.config.ts  Frontend build configuration
├── package.json        Node scripts and dependencies
├── go.mod              Go module and backend dependencies
├── Magefile.go         Mage build entry point
└── docker-compose.yaml Local plugin development stack
```

Generated content such as `dist/`, `node_modules/`, `playwright-report/`, and `test-results/` should not be committed.

## Tests

> TODO: current version of this plugin does not contain any test cases needed to implement this in future

Backend unit tests are under `pkg/plugin/`. Frontend component tests are next to their components under `src/`, and browser tests are under `tests/`.

Use the focused commands while developing:

```bash
npm test -- --runInBand path/to/test.test.tsx
npm run test:ci
go test ./pkg/plugin/...
```
