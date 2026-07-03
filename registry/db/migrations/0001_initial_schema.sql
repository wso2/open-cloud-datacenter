-- ============================================================================
-- Registry Auto-Deployment: Platform Database Schema — migration 0001
-- ----------------------------------------------------------------------------
-- Runs once on the platform PostgreSQL (NOT Harbor's internal DB).
--
-- This file is EMBEDDED into the operator binary via //go:embed (see db/embed.go)
-- and applied on startup by Store.Migrate() in internal/db/postgres.go. It is
-- idempotent (CREATE ... IF NOT EXISTS + defensive ALTERs), so re-running is safe.
--
-- To add a schema change later, drop a new file next to this one
-- (e.g. 0002_add_something.sql); files are applied in filename order.
-- ============================================================================

BEGIN;

-- Extension for uuid / crypto helpers
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ─── Registry Deployments ────────────────────────────────────────────────────
-- Tracks the lifecycle of each per-tenant Harbor deployment.
CREATE TABLE IF NOT EXISTS registry_deployments (
    tenant_id     TEXT PRIMARY KEY,
    namespace     TEXT NOT NULL,
    status        TEXT NOT NULL CHECK (status IN ('PENDING','DEPLOYING','READY','FAILED','DELETING','DELETED')),
    registry_url  TEXT,
    helm_release  TEXT,
    plan          TEXT,
    progress      JSONB DEFAULT '{}',
    error_message TEXT,
    hard_delete   BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    ready_at      TIMESTAMPTZ,
    worker_lock   TEXT              -- hostname of worker processing this; prevents double-processing
);

-- Safe to run on existing clusters: adds hard_delete if it doesn't exist yet.
ALTER TABLE registry_deployments ADD COLUMN IF NOT EXISTS hard_delete BOOLEAN NOT NULL DEFAULT false;

-- Index speeds up the worker's polling queries.
CREATE INDEX IF NOT EXISTS idx_deployments_status
    ON registry_deployments (status)
    WHERE status IN ('PENDING', 'DEPLOYING', 'DELETING');

-- ─── Registry Credentials ────────────────────────────────────────────────────
-- Stores encrypted Harbor credentials (robot token + admin password).
-- Never stored in plaintext. Encrypted with AES-256-GCM using the platform master key.
CREATE TABLE IF NOT EXISTS registry_credentials (
    tenant_id          TEXT PRIMARY KEY REFERENCES registry_deployments(tenant_id) ON DELETE CASCADE,
    robot_username     TEXT NOT NULL,
    encrypted_token    BYTEA NOT NULL,
    token_nonce        BYTEA NOT NULL,   -- AES-GCM nonce, 12 bytes
    admin_username     TEXT NOT NULL DEFAULT 'admin',
    encrypted_admin_pw BYTEA NOT NULL,
    admin_pw_nonce     BYTEA NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at         TIMESTAMPTZ
);

-- ─── Audit Log ───────────────────────────────────────────────────────────────
-- Immutable log of all sensitive operations (append-only; no UPDATE/DELETE).
CREATE TABLE IF NOT EXISTS audit_log (
    id          BIGSERIAL PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    action      TEXT NOT NULL,   -- REGISTRY_CREATE | REGISTRY_DELETE | GET_CREDENTIALS | ROTATE_CREDENTIALS
    actor_id    TEXT,
    actor_email TEXT,
    source_ip   TEXT,
    result      TEXT NOT NULL,   -- SUCCESS | FAILURE
    details     JSONB DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_log_tenant_id ON audit_log(tenant_id);

-- ─── Registry Projects ───────────────────────────────────────────────────────
-- Project-scoped Harbor projects: multiple named registries per datacenter project.
CREATE TABLE IF NOT EXISTS registry_projects (
    tenant_id           TEXT NOT NULL REFERENCES registry_deployments(tenant_id) ON DELETE CASCADE,
    project_id          TEXT NOT NULL,
    registry_name       TEXT NOT NULL DEFAULT '',
    harbor_project_name TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'PENDING'
                            CHECK (status IN ('PENDING','READY','FAILED')),
    robot_id            BIGINT,
    error_message       TEXT,
    worker_lock         TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, project_id, registry_name)
);

-- Safe to run on existing clusters that were created without registry_name.
-- Adds the column and rebuilds the PK to include registry_name.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name   = 'registry_projects'
          AND column_name  = 'registry_name'
    ) THEN
        ALTER TABLE registry_projects ADD COLUMN registry_name TEXT NOT NULL DEFAULT '';
        ALTER TABLE registry_projects DROP CONSTRAINT IF EXISTS registry_projects_pkey;
        ALTER TABLE registry_projects ADD PRIMARY KEY (tenant_id, project_id, registry_name);
    END IF;
END
$$;

CREATE INDEX IF NOT EXISTS idx_registry_projects_status
    ON registry_projects (status) WHERE status = 'PENDING';

-- ─── Registry Project Credentials ────────────────────────────────────────────
-- Encrypted robot credentials, scoped per named registry.
CREATE TABLE IF NOT EXISTS registry_project_credentials (
    tenant_id          TEXT NOT NULL,
    project_id         TEXT NOT NULL,
    registry_name      TEXT NOT NULL DEFAULT '',
    robot_username     TEXT NOT NULL,
    encrypted_token    BYTEA NOT NULL,
    token_nonce        BYTEA NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at         TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, project_id, registry_name),
    FOREIGN KEY (tenant_id, project_id, registry_name)
        REFERENCES registry_projects(tenant_id, project_id, registry_name) ON DELETE CASCADE
);

COMMIT;
