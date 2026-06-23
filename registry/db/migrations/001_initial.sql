-- Registry Auto-Deployment: Platform Database Schema
-- Run once on platform PostgreSQL (not Harbor's internal DB)

BEGIN;

-- Extension for uuid generation
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ─── Registry Deployments ────────────────────────────────────────────────────
-- Tracks the lifecycle of each per-tenant Harbor deployment.

CREATE TABLE IF NOT EXISTS registry_deployments (
    tenant_id       TEXT            PRIMARY KEY,
    namespace       TEXT            NOT NULL,
    status          TEXT            NOT NULL
                    CHECK (status IN ('PENDING','DEPLOYING','READY','FAILED','DELETING','DELETED')),
    registry_url    TEXT,
    helm_release    TEXT,
    plan            TEXT            NOT NULL DEFAULT 'professional',
    progress        JSONB           NOT NULL DEFAULT '{}',
    error_message   TEXT,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    ready_at        TIMESTAMPTZ,
    worker_lock     TEXT            -- hostname of worker processing this; prevents double-processing
);

CREATE INDEX IF NOT EXISTS idx_deployments_status
    ON registry_deployments (status)
    WHERE status IN ('PENDING', 'DEPLOYING');

-- Trigger: auto-update updated_at
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_deployments_updated_at
    BEFORE UPDATE ON registry_deployments
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();


-- ─── Registry Credentials ────────────────────────────────────────────────────
-- Stores encrypted Harbor credentials (robot token + admin password).
-- Never stored in plaintext. Encrypted with AES-256-GCM using the platform master key.

CREATE TABLE IF NOT EXISTS registry_credentials (
    tenant_id           TEXT        PRIMARY KEY
                        REFERENCES registry_deployments(tenant_id) ON DELETE CASCADE,

    -- CI/CD robot account
    robot_username      TEXT        NOT NULL,
    encrypted_token     BYTEA       NOT NULL,
    token_nonce         BYTEA       NOT NULL,   -- AES-GCM nonce, 12 bytes

    -- Admin account
    admin_username      TEXT        NOT NULL DEFAULT 'admin',
    encrypted_admin_pw  BYTEA       NOT NULL,
    admin_pw_nonce      BYTEA       NOT NULL,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at          TIMESTAMPTZ
);


-- ─── Audit Log ───────────────────────────────────────────────────────────────
-- Immutable log of all sensitive operations (append-only; no UPDATE/DELETE).

CREATE TABLE IF NOT EXISTS audit_log (
    id              BIGSERIAL       PRIMARY KEY,
    tenant_id       TEXT            NOT NULL,
    action          TEXT            NOT NULL,   -- REGISTRY_CREATE | REGISTRY_DELETE | GET_CREDENTIALS | ROTATE_CREDENTIALS
    actor_id        TEXT,
    actor_email     TEXT,
    source_ip       TEXT,
    result          TEXT            NOT NULL CHECK (result IN ('SUCCESS', 'FAILURE')),
    details         JSONB           NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_tenant   ON audit_log (tenant_id);
CREATE INDEX IF NOT EXISTS idx_audit_action   ON audit_log (action);
CREATE INDEX IF NOT EXISTS idx_audit_created  ON audit_log (created_at DESC);

-- Revoke DELETE on audit_log so no one can erase audit trail
REVOKE DELETE ON audit_log FROM PUBLIC;


-- ─── Seed: verify schema ─────────────────────────────────────────────────────
DO $$
BEGIN
    ASSERT (SELECT count(*) FROM information_schema.tables
            WHERE table_name IN ('registry_deployments','registry_credentials','audit_log')) = 3,
        'Schema verification failed';
    RAISE NOTICE 'Schema OK';
END
$$;

COMMIT;
