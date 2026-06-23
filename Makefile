.PHONY: help bootstrap check up down db-reset build-api build-cli build run dev gen-agent-rbac

# Default target — print help
help:
	@echo "Usage:"
	@echo "  make bootstrap   First-time setup: check prerequisites, start postgres, build everything"
	@echo "  make check       Just verify prerequisites (no side effects)"
	@echo "  make up          Start PostgreSQL (docker compose)"
	@echo "  make down        Stop PostgreSQL"
	@echo "  make db-reset    Wipe and restart the database (fresh schema on next dc-api run)"
	@echo "  make build-api   Compile dc-api binary"
	@echo "  make build-cli   Compile dcctl binary to ~/bin/dcctl"
	@echo "  make build       Build both"
	@echo "  make run         Source .env and run dc-api (postgres must be up)"
	@echo "  make dev         up + build-api + run (one command for local dev)"

# ── Bootstrap ─────────────────────────────────────────────────────────────────
bootstrap:
	@./scripts/bootstrap.sh

check:
	@./scripts/bootstrap.sh check

# ── Docker compose ────────────────────────────────────────────────────────────
up:
	docker compose up -d
	@echo "Waiting for PostgreSQL..."
	@until docker exec dc-postgres pg_isready -U dc_api >/dev/null 2>&1; do sleep 1; done
	@echo "PostgreSQL is ready at localhost:5432"

down:
	docker compose down

db-reset:
	docker compose down -v
	docker compose up -d
	@until docker exec dc-postgres pg_isready -U dc_api >/dev/null 2>&1; do sleep 1; done
	@echo "Fresh database ready — schema will be applied on next dc-api startup"

# ── Build ─────────────────────────────────────────────────────────────────────
build-api:
	cd dc-api && go build -o dc-api ./cmd/dc-api/
	@echo "Built: dc-api/dc-api"

build-cli:
	cd dcctl && go build -o ~/bin/dcctl .
	@echo "Built: ~/bin/dcctl"

build: build-api build-cli

# ── Code generation ───────────────────────────────────────────────────────────
# Regenerate the dc-agent RBAC manifest from the capability registry
# (dc-api/internal/providers/clusteraccess/capabilities.go). Run this after
# editing any AgentCapability.AgentVerbs; the drift-check unit test fails CI if
# the committed file is stale.
gen-agent-rbac:
	cd dc-api && go run ./cmd/gen-agent-rbac -out ../flux/platform/dc-agent/base/rbac.yaml

# ── Run ───────────────────────────────────────────────────────────────────────
run:
	@if [ ! -f .env ]; then \
		echo "Error: .env not found. Copy .env.example → .env and fill in your values."; \
		exit 1; \
	fi
	@echo "Starting DC-API on :8080 ..."
	@cd dc-api && bash -c 'set -a; source ../.env; set +a; ./dc-api'

dev: up build-api run
