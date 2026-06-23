-- Migration 002: project-scoped Harbor projects and credentials
-- Supports multiple named registries per datacenter project.
-- Run AFTER migration 001 (initial schema already applied via schemaSQL).

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

CREATE INDEX IF NOT EXISTS idx_registry_projects_status
    ON registry_projects (status) WHERE status = 'PENDING';

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
