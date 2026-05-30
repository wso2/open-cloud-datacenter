#!/usr/bin/env bash
# dev-up.sh — bring up the dc-api + cloud-ui local dev loop.
#
# The provisioning backends (Harvester, Rancher, KubeOVN, the OIDC IdP) are
# ALWAYS a real cluster — there is no laptop-scale hypervisor. The only thing
# that changes between modes is where the dc-api Postgres registry lives and
# how its config is sourced.
#
#   ./scripts/dev-up.sh local-stack   # RECOMMENDED daily driver.
#       Local dc-api + cloud-ui + a LOCAL throwaway Postgres (docker compose).
#       Backend config (OIDC / Harvester / Rancher / KubeOVN) is still sourced
#       live from the cluster, but the registry DB is local and isolated — so
#       schema migrations and write-heavy tests never touch the shared cluster
#       DB and there is no reconciler race with the in-cluster dc-api pod.
#
#   ./scripts/dev-up.sh local         # Local dc-api + cloud-ui, but the DB is
#       the CLUSTER's Postgres (port-forwarded). Use to REPRODUCE issues against
#       real data. NOTE: shares the DB with the in-cluster dc-api pod, so both
#       reconcile the same rows — scale the pod to 0 for exclusive write access.
#
#   ./scripts/dev-up.sh remote        # cloud-ui only → proxies to the deployed
#       dc-api ingress. UI-only smoke tests (login lands on the deployed UI).
#
#   ./scripts/dev-up.sh down          # stop the local dc-api / vite / port-forward.
#       Leaves the local docker Postgres running so its data persists; run
#       `docker compose down` to stop it, `down -v` to wipe it.
#
# Configuration — point at YOUR dev cluster. Copy scripts/dev-up.env.example →
# scripts/dev-up.env (gitignored) and set at least DCAPI_DEV_CONTEXT, or export
# the vars in your shell:
#   DCAPI_DEV_CONTEXT       kube context of your dev cluster   (required: local, local-stack)
#   DCAPI_DEV_NAMESPACE     dc-api's namespace                 (default: dc-system)
#   DCAPI_DEV_REMOTE_TARGET deployed dc-api ingress URL        (required: remote)
#   DC_PG_PORT              host port for the local dev DB     (default: 5433)
#
# State files (gitignored): /tmp/dc-dev/*

set -euo pipefail

REPO_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)

# Optional, gitignored local overrides (your cluster's context, namespace, …).
[[ -f "$REPO_ROOT/scripts/dev-up.env" ]] && source "$REPO_ROOT/scripts/dev-up.env"

STATE_DIR=/tmp/dc-dev
mkdir -p "$STATE_DIR"

PGPF_PID_FILE=$STATE_DIR/pgpf.pid
DCAPI_PID_FILE=$STATE_DIR/dc-api.pid
VITE_PID_FILE=$STATE_DIR/vite.pid
WATCHDOG_PID_FILE=$STATE_DIR/pgpf-watchdog.pid
PGPF_LOG=$STATE_DIR/pgpf.log
DCAPI_LOG=$STATE_DIR/dc-api.log
VITE_LOG=$STATE_DIR/vite.log

# Kube context + namespace the live backend config is sourced from.
CTX="${DCAPI_DEV_CONTEXT:-}"
NS="${DCAPI_DEV_NAMESPACE:-dc-system}"
# Deployed dc-api ingress used by `remote` mode.
REMOTE_API_TARGET="${DCAPI_DEV_REMOTE_TARGET:-}"
# Host port for the local throwaway Postgres (local-stack mode). 5433 by default
# so it doesn't collide with a system Postgres already bound to 5432.
LOCAL_DB_PORT="${DC_PG_PORT:-5433}"

require_ctx() {
  [[ -n "$CTX" ]] || {
    echo "error: DCAPI_DEV_CONTEXT is not set — point this at your dev cluster's kube context."
    echo "       Copy scripts/dev-up.env.example → scripts/dev-up.env and fill it in,"
    echo "       or: export DCAPI_DEV_CONTEXT=<context>  (see: kubectl config get-contexts)"
    exit 1
  }
}

stop_pid() {
  local f=$1; local name=$2
  if [[ -f $f ]]; then
    local pid=$(cat "$f")
    if kill -0 "$pid" 2>/dev/null; then
      echo "stopping $name (pid=$pid)"
      kill "$pid" 2>/dev/null || true
    fi
    rm -f "$f"
  fi
}

cmd_down() {
  stop_pid "$VITE_PID_FILE"   vite
  stop_pid "$DCAPI_PID_FILE"  dc-api
  # Kill the watchdog first so it doesn't relaunch the pf we're about to stop.
  stop_pid "$WATCHDOG_PID_FILE" pgpf-watchdog
  stop_pid "$PGPF_PID_FILE"   port-forward
  # The watchdog spawned children; sweep them.
  pkill -f 'port-forward.*15432' 2>/dev/null || true
  echo "done. local docker Postgres left running (docker compose down to stop it)."
  echo "logs in $STATE_DIR"
}

# ── shared helpers ───────────────────────────────────────────────────────────

build_dcapi() {
  echo "  building dc-api → /tmp/dc-api-local"
  ( cd "$REPO_ROOT/dc-api" && go build -o /tmp/dc-api-local ./cmd/dc-api/ )
}

# Source the live cluster Secret + ConfigMap into DCAPI_* env vars. (The sourced
# script also points DCAPI_DB_URL at the port-forwarded cluster DB and sets a
# debug log level; callers that want a different DB override DCAPI_DB_URL after.)
load_cluster_env() {
  source "$REPO_ROOT/docs/dev/dc-api-local-env.sh"
}

# Overrides shared by every local mode: listen on :8080 and rewrite the BFF
# OIDC redirect/cookie settings for localhost. Requires the cloud-ui BFF OIDC
# app to allow http://localhost:8080/v1/auth/callback as a redirect URI.
apply_localhost_overrides() {
  export DCAPI_LISTEN_ADDR=":8080"
  export DCAPI_BFF_REDIRECT_URL="http://localhost:8080/v1/auth/callback"
  export DCAPI_BFF_POST_LOGIN_REDIRECT="http://localhost:5173/"
  export DCAPI_BFF_POST_LOGOUT_REDIRECT="http://localhost:5173/login"
  export DCAPI_BFF_COOKIE_DOMAIN="localhost"
  export DCAPI_BFF_COOKIE_SECURE="false"
}

run_dcapi() {
  stop_pid "$DCAPI_PID_FILE" dc-api
  echo "  starting dc-api on :8080 (log → $DCAPI_LOG)"
  nohup /tmp/dc-api-local > "$DCAPI_LOG" 2>&1 &
  echo $! > "$DCAPI_PID_FILE"
  # health-check — kubeovn F15 bootstrap against Harvester can take 10-15s.
  echo -n "  waiting for dc-api health"
  for i in $(seq 1 30); do
    sleep 1
    if curl -sf http://localhost:8080/healthz > /dev/null 2>&1; then
      echo " — ready"
      return 0
    fi
    echo -n "."
  done
  echo ""
  echo "  dc-api never came up; see $DCAPI_LOG"
  tail -30 "$DCAPI_LOG"
  exit 1
}

start_vite() {
  stop_pid "$VITE_PID_FILE" vite
  echo "  starting cloud-ui (vite) → http://localhost:5173"
  ( cd "$REPO_ROOT/cloud-ui" && \
    VITE_API_PROXY_TARGET=http://localhost:8080 \
    nohup pnpm dev > "$VITE_LOG" 2>&1 & echo $! > "$VITE_PID_FILE" )
  for i in $(seq 1 15); do
    sleep 1
    if curl -sf http://localhost:5173/ > /dev/null 2>&1; then
      echo "  vite ready"
      break
    fi
  done
}

# Postgres port-forward (cluster DB) with an auto-restart watchdog.
# `kubectl port-forward` drops on long HTTP/2 streams (k8s issue #74551); the
# watchdog re-launches it so the dc-api pgx pool can reconnect.
start_pg_portforward() {
  require_ctx
  stop_pid "$WATCHDOG_PID_FILE" pgpf-watchdog
  stop_pid "$PGPF_PID_FILE" port-forward
  echo "  starting postgres port-forward → :15432 (with auto-restart)"
  (
    while true; do
      kubectl --context "$CTX" -n "$NS" port-forward svc/dc-postgres 15432:5432 \
        >> "$PGPF_LOG" 2>&1
      echo "[$(date +%H:%M:%S)] pf exited; restarting in 2s" >> "$PGPF_LOG"
      sleep 2
    done
  ) &
  echo $! > "$WATCHDOG_PID_FILE"
  for i in 1 2 3 4 5 6 7 8; do
    sleep 1
    nc -z localhost 15432 2>/dev/null && break
  done
  nc -z localhost 15432 2>/dev/null || { echo "  port-forward failed; see $PGPF_LOG"; exit 1; }
}

# Local throwaway Postgres via docker compose, on $LOCAL_DB_PORT.
ensure_local_postgres() {
  echo "  starting local postgres → :${LOCAL_DB_PORT} (docker compose)"
  # docker-compose pins container_name: dc-postgres. If a stale, NON-running
  # container with that name exists (e.g. from another compose project), remove
  # it so our compose can claim the name. A running one is left as-is.
  if docker ps -a --format '{{.Names}}' | grep -qx dc-postgres \
     && ! docker ps --format '{{.Names}}' | grep -qx dc-postgres; then
    docker rm dc-postgres >/dev/null 2>&1 || true
  fi
  ( cd "$REPO_ROOT" && DC_PG_PORT="$LOCAL_DB_PORT" docker compose up -d postgres )
  echo -n "  waiting for postgres"
  for i in $(seq 1 30); do
    if docker exec dc-postgres pg_isready -U dc_api >/dev/null 2>&1; then
      echo " — ready"
      return 0
    fi
    echo -n "."
    sleep 1
  done
  echo ""
  echo "  postgres did not become ready; see: docker compose logs postgres"
  exit 1
}

# ── modes ────────────────────────────────────────────────────────────────────

cmd_local_stack() {
  require_ctx
  echo "[1/3] local postgres (isolated dev DB)"
  ensure_local_postgres

  echo "[2/3] dc-api (local DB, real cluster backends)"
  build_dcapi
  load_cluster_env
  # Override the DB → local docker Postgres. Everything else (OIDC, Harvester,
  # Rancher, KubeOVN) stays as sourced from the cluster.
  export DCAPI_DB_URL="postgres://dc_api:dc_dev_password@localhost:${LOCAL_DB_PORT}/dc_api?sslmode=disable"
  apply_localhost_overrides
  run_dcapi

  echo "[3/3] cloud-ui"
  start_vite

  cat <<EOF

=== ready — local-stack (isolated local DB) ===
  cloud-ui   http://localhost:5173
  dc-api     http://localhost:8080   (BFF → /v1/auth/login, docs → /docs)
  postgres   localhost:${LOCAL_DB_PORT} → dc_api  (LOCAL docker volume, NOT the cluster)

  backends   Harvester / Rancher / KubeOVN / OIDC = the cluster ($CTX)

  First run? The local DB is empty. Seed a tenant + project:
    dcctl login
    dcctl admin tenant create acme --cpu 32 --memory 64 --storage 500
    dcctl tenant set acme && dcctl project create dev --cpu 8 --memory 16 --storage 100

  logs   $STATE_DIR/{dc-api,vite}.log
  stop   $REPO_ROOT/scripts/dev-up.sh down     (keeps the local DB)
         docker compose down                    (stops the DB, keeps its data)
         docker compose down -v                 (wipes the DB)

EOF
}

cmd_local() {
  echo "[1/3] postgres port-forward (CLUSTER DB — shared with the in-cluster pod)"
  start_pg_portforward

  echo "[2/3] dc-api (cluster DB via port-forward)"
  build_dcapi
  load_cluster_env   # sets DCAPI_DB_URL → localhost:15432 (the port-forward)
  apply_localhost_overrides
  run_dcapi

  echo "[3/3] cloud-ui"
  start_vite

  cat <<EOF

=== ready — local (CLUSTER DB via port-forward) ===
  cloud-ui   http://localhost:5173
  dc-api     http://localhost:8080   (BFF → /v1/auth/login, docs → /docs)
  postgres   localhost:15432 → dc_api  (the CLUSTER's DB — shared with the pod)

  ⚠ The in-cluster dc-api pod reconciles this same DB. For exclusive write
    testing, scale it down first:
      kubectl --context $CTX -n $NS scale deploy/dc-api --replicas=0
    (and back up with --replicas=1 when done).

  logs   $STATE_DIR/{pgpf,dc-api,vite}.log
  stop   $REPO_ROOT/scripts/dev-up.sh down

EOF
}

cmd_remote() {
  [[ -n "$REMOTE_API_TARGET" ]] || {
    echo "error: DCAPI_DEV_REMOTE_TARGET is not set — set it to your deployed dc-api ingress URL"
    echo "       (e.g. export DCAPI_DEV_REMOTE_TARGET=https://dcapi.example.com)"
    exit 1
  }
  echo "[remote] starting cloud-ui only → proxies to $REMOTE_API_TARGET"
  echo "         NOTE: BFF callback after login redirects to the deployed cloud-ui,"
  echo "         not localhost. For a full local login flow use 'local-stack'."
  stop_pid "$VITE_PID_FILE" vite
  ( cd "$REPO_ROOT/cloud-ui" && \
    VITE_API_PROXY_TARGET="$REMOTE_API_TARGET" \
    nohup pnpm dev > "$VITE_LOG" 2>&1 & echo $! > "$VITE_PID_FILE" )
  for i in $(seq 1 15); do
    sleep 1
    if curl -sf http://localhost:5173/ > /dev/null 2>&1; then
      echo "  vite ready → http://localhost:5173"
      break
    fi
  done
}

case "${1:-}" in
  local-stack) cmd_local_stack ;;
  local)       cmd_local       ;;
  remote)      cmd_remote      ;;
  down)        cmd_down        ;;
  *)
    echo "usage: $0 {local-stack|local|remote|down}"
    echo "  local-stack  local dc-api + cloud-ui + LOCAL docker Postgres (daily driver)"
    echo "  local        local dc-api + cloud-ui against the CLUSTER DB (issue repro)"
    echo "  remote       cloud-ui only, proxied to the deployed dc-api ingress"
    echo "  down         stop local dc-api / vite / port-forward"
    exit 1
    ;;
esac
