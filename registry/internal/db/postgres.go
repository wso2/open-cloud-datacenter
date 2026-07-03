package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq"
	"go.uber.org/zap"

	dbschema "github.com/wso2/open-cloud-datacenter/crds/registry/db"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
)

// RegistryStatus represents the lifecycle state of a Harbor deployment.
type RegistryStatus string

const (
	StatusPending   RegistryStatus = "PENDING"
	StatusDeploying RegistryStatus = "DEPLOYING"
	StatusReady     RegistryStatus = "READY"
	StatusFailed    RegistryStatus = "FAILED"
	StatusDeleting  RegistryStatus = "DELETING"
	StatusDeleted   RegistryStatus = "DELETED"
)

type RegistryDeployment struct {
	TenantID     string
	Namespace    string
	Status       RegistryStatus
	RegistryURL  string
	HelmRelease  string
	Plan         string
	Progress     map[string]string
	ErrorMessage string
	HardDelete   bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ReadyAt      *time.Time
}

type RegistryCredentials struct {
	TenantID          string
	RobotUsername     string
	EncryptedToken    []byte
	TokenNonce        []byte
	AdminUsername     string
	EncryptedAdminPW  []byte
	AdminPWNonce      []byte
	CreatedAt         time.Time
	RotatedAt         *time.Time
}

// RegistryProject tracks one Harbor project inside a tenant's Harbor.
// Multiple RegistryProjects per (tenantID, projectID) are supported — each
// has a unique RegistryName (user-provided) that becomes the Harbor project name.
type RegistryProject struct {
	TenantID          string
	ProjectID         string
	RegistryName      string // user-provided name; unique within (tenantID, projectID)
	HarborProjectName string // = RegistryName (Harbor project name)
	Status            string // "PENDING" | "READY" | "FAILED"
	RobotID           int64
	ErrorMessage      string
	WorkerLock        string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// RegistryProjectCredentials holds encrypted robot credentials per registry.
type RegistryProjectCredentials struct {
	TenantID       string
	ProjectID      string
	RegistryName   string
	RobotUsername  string
	EncryptedToken []byte
	TokenNonce     []byte
	CreatedAt      time.Time
	RotatedAt      *time.Time
}

type AuditEntry struct {
	TenantID  string
	Action    string
	ActorID   string
	ActorEmail string
	SourceIP  string
	Result    string
	Details   map[string]interface{}
	CreatedAt time.Time
}

type Store struct {
	db     *sql.DB
	logger *zap.Logger
}

func New(cfg config.DBConfig, logger *zap.Logger) (*Store, error) {
	db, err := sql.Open("postgres", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	logger.Info("database connected")
	return &Store{db: db, logger: logger}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// expectedTables lists every table the operator depends on. verifySchema uses
// it as a startup guard so the pod fails fast with a clear error if a table is
// missing, instead of hitting "relation does not exist" mid-request later.
var expectedTables = []string{
	"registry_deployments",
	"registry_credentials",
	"audit_log",
	"registry_projects",
	"registry_project_credentials",
}

// Migrate applies every embedded migration (db/migrations/*.sql, in filename
// order) and then verifies all expected tables exist. Each migration is
// idempotent, so this is safe to run on every startup.
func (s *Store) Migrate() error {
	migrations, err := dbschema.LoadMigrations()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	for _, m := range migrations {
		if _, err := s.db.Exec(m.SQL); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.Name, err)
		}
		s.logger.Info("migration applied", zap.String("file", m.Name))
	}
	return s.verifySchema()
}

// verifySchema confirms every table in expectedTables is present after
// migrations have run.
func (s *Store) verifySchema() error {
	for _, t := range expectedTables {
		var exists bool
		err := s.db.QueryRow(
			`SELECT EXISTS (
			   SELECT 1 FROM information_schema.tables
			   WHERE table_schema = 'public' AND table_name = $1
			 )`, t).Scan(&exists)
		if err != nil {
			return fmt.Errorf("verify table %s: %w", t, err)
		}
		if !exists {
			return fmt.Errorf("schema verification failed: table %q missing after migrations", t)
		}
	}
	s.logger.Info("schema verified", zap.Int("tables", len(expectedTables)))
	return nil
}

// --- Registry Deployments ---

func (s *Store) CreateDeployment(ctx context.Context, d *RegistryDeployment) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO registry_deployments
		 (tenant_id, namespace, status, helm_release, plan, progress)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		d.TenantID, d.Namespace, d.Status, d.HelmRelease, d.Plan, progressJSON(d.Progress),
	)
	return err
}

func (s *Store) GetDeployment(ctx context.Context, tenantID string) (*RegistryDeployment, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT tenant_id, namespace, status, registry_url, helm_release, plan,
		        progress, error_message, hard_delete, created_at, updated_at, ready_at
		 FROM registry_deployments WHERE tenant_id = $1`, tenantID)
	return scanDeployment(row)
}

func (s *Store) UpdateDeploymentStatus(ctx context.Context, tenantID string, status RegistryStatus, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE registry_deployments
		 SET status = $2, error_message = $3, updated_at = now()
		 WHERE tenant_id = $1`,
		tenantID, status, errMsg,
	)
	return err
}

func (s *Store) SetDeploymentReady(ctx context.Context, tenantID, registryURL string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE registry_deployments
		 SET status = $2, registry_url = $3, ready_at = now(), updated_at = now()
		 WHERE tenant_id = $1`,
		tenantID, StatusReady, registryURL,
	)
	return err
}

func (s *Store) UpdateProgress(ctx context.Context, tenantID string, progress map[string]string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE registry_deployments
		 SET progress = $2, updated_at = now()
		 WHERE tenant_id = $1`,
		tenantID, progressJSON(progress),
	)
	return err
}

func (s *Store) DeleteDeployment(ctx context.Context, tenantID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM registry_deployments WHERE tenant_id = $1`, tenantID)
	return err
}

// --- Credentials ---

func (s *Store) SaveCredentials(ctx context.Context, c *RegistryCredentials) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO registry_credentials
		 (tenant_id, robot_username, encrypted_token, token_nonce,
		  admin_username, encrypted_admin_pw, admin_pw_nonce)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		   robot_username = $2, encrypted_token = $3, token_nonce = $4,
		   encrypted_admin_pw = $6, admin_pw_nonce = $7, rotated_at = now()`,
		c.TenantID, c.RobotUsername, c.EncryptedToken, c.TokenNonce,
		c.AdminUsername, c.EncryptedAdminPW, c.AdminPWNonce,
	)
	return err
}

func (s *Store) GetCredentials(ctx context.Context, tenantID string) (*RegistryCredentials, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT tenant_id, robot_username, encrypted_token, token_nonce,
		        admin_username, encrypted_admin_pw, admin_pw_nonce, created_at, rotated_at
		 FROM registry_credentials WHERE tenant_id = $1`, tenantID)

	c := &RegistryCredentials{}
	err := row.Scan(
		&c.TenantID, &c.RobotUsername, &c.EncryptedToken, &c.TokenNonce,
		&c.AdminUsername, &c.EncryptedAdminPW, &c.AdminPWNonce, &c.CreatedAt, &c.RotatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

func (s *Store) DeleteCredentials(ctx context.Context, tenantID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM registry_credentials WHERE tenant_id = $1`, tenantID)
	return err
}

// --- Registry Projects ---

func (s *Store) CreateProject(ctx context.Context, p *RegistryProject) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO registry_projects (tenant_id, project_id, registry_name, harbor_project_name, status)
		 VALUES ($1, $2, $3, $4, 'PENDING')`,
		p.TenantID, p.ProjectID, p.RegistryName, p.HarborProjectName,
	)
	return err
}

func (s *Store) GetProject(ctx context.Context, tenantID, projectID, registryName string) (*RegistryProject, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT tenant_id, project_id, registry_name, harbor_project_name, status, robot_id,
		        error_message, worker_lock, created_at, updated_at
		 FROM registry_projects WHERE tenant_id = $1 AND project_id = $2 AND registry_name = $3`,
		tenantID, projectID, registryName,
	)
	p := &RegistryProject{}
	var robotID sql.NullInt64
	var errMsg, workerLock sql.NullString
	err := row.Scan(&p.TenantID, &p.ProjectID, &p.RegistryName, &p.HarborProjectName, &p.Status,
		&robotID, &errMsg, &workerLock, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if robotID.Valid {
		p.RobotID = robotID.Int64
	}
	p.ErrorMessage = errMsg.String
	p.WorkerLock = workerLock.String
	return p, nil
}

// GetOldestPendingProject atomically claims the oldest PENDING project whose
// Harbor deployment is already READY. Uses FOR UPDATE SKIP LOCKED so multiple
// provisioner replicas can't pick up the same row.
func (s *Store) GetOldestPendingProject(ctx context.Context) (*RegistryProject, error) {
	hostname, _ := os.Hostname()
	row := s.db.QueryRowContext(ctx, `
		UPDATE registry_projects rp
		SET worker_lock = $1, updated_at = now()
		WHERE (rp.tenant_id, rp.project_id, rp.registry_name) = (
			SELECT rp2.tenant_id, rp2.project_id, rp2.registry_name
			FROM registry_projects rp2
			JOIN registry_deployments rd ON rd.tenant_id = rp2.tenant_id
			WHERE rp2.status = 'PENDING'
			  AND rp2.worker_lock IS NULL
			  AND rd.status = 'READY'
			ORDER BY rp2.created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING tenant_id, project_id, registry_name, harbor_project_name, status, robot_id,
		          error_message, worker_lock, created_at, updated_at
	`, hostname)
	p := &RegistryProject{}
	var robotID sql.NullInt64
	var errMsg, workerLock sql.NullString
	err := row.Scan(&p.TenantID, &p.ProjectID, &p.RegistryName, &p.HarborProjectName, &p.Status,
		&robotID, &errMsg, &workerLock, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if robotID.Valid {
		p.RobotID = robotID.Int64
	}
	return p, nil
}

func (s *Store) SetProjectReady(ctx context.Context, tenantID, projectID, registryName string, robotID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE registry_projects
		 SET status = 'READY', robot_id = $4, worker_lock = NULL, updated_at = now()
		 WHERE tenant_id = $1 AND project_id = $2 AND registry_name = $3`,
		tenantID, projectID, registryName, robotID,
	)
	return err
}

func (s *Store) SetProjectFailed(ctx context.Context, tenantID, projectID, registryName, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE registry_projects
		 SET status = 'FAILED', error_message = $4, worker_lock = NULL, updated_at = now()
		 WHERE tenant_id = $1 AND project_id = $2 AND registry_name = $3`,
		tenantID, projectID, registryName, errMsg,
	)
	return err
}

func (s *Store) DeleteProject(ctx context.Context, tenantID, projectID, registryName string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM registry_projects WHERE tenant_id = $1 AND project_id = $2 AND registry_name = $3`,
		tenantID, projectID, registryName,
	)
	return err
}

// --- Project Credentials ---

func (s *Store) SaveProjectCredentials(ctx context.Context, c *RegistryProjectCredentials) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO registry_project_credentials
		 (tenant_id, project_id, registry_name, robot_username, encrypted_token, token_nonce)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (tenant_id, project_id, registry_name) DO UPDATE SET
		   robot_username = $4, encrypted_token = $5, token_nonce = $6, rotated_at = now()`,
		c.TenantID, c.ProjectID, c.RegistryName, c.RobotUsername, c.EncryptedToken, c.TokenNonce,
	)
	return err
}

func (s *Store) GetProjectCredentials(ctx context.Context, tenantID, projectID, registryName string) (*RegistryProjectCredentials, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT tenant_id, project_id, registry_name, robot_username, encrypted_token, token_nonce, created_at, rotated_at
		 FROM registry_project_credentials WHERE tenant_id = $1 AND project_id = $2 AND registry_name = $3`,
		tenantID, projectID, registryName,
	)
	c := &RegistryProjectCredentials{}
	err := row.Scan(&c.TenantID, &c.ProjectID, &c.RegistryName, &c.RobotUsername,
		&c.EncryptedToken, &c.TokenNonce, &c.CreatedAt, &c.RotatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// --- Audit ---

func (s *Store) WriteAuditLog(ctx context.Context, entry *AuditEntry) error {
	details, _ := json.Marshal(entry.Details)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log
		 (tenant_id, action, actor_id, actor_email, source_ip, result, details)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		entry.TenantID, entry.Action, entry.ActorID, entry.ActorEmail,
		entry.SourceIP, entry.Result, details,
	)
	return err
}

// --- helpers ---

func scanDeployment(row *sql.Row) (*RegistryDeployment, error) {
	d := &RegistryDeployment{}
	var progressRaw []byte
	var registryURL, helmRelease, plan, errMsg sql.NullString
	var readyAt sql.NullTime

	err := row.Scan(
		&d.TenantID, &d.Namespace, &d.Status, &registryURL, &helmRelease, &plan,
		&progressRaw, &errMsg, &d.HardDelete, &d.CreatedAt, &d.UpdatedAt, &readyAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	d.RegistryURL = registryURL.String
	d.HelmRelease = helmRelease.String
	d.Plan = plan.String
	d.ErrorMessage = errMsg.String
	if readyAt.Valid {
		t := readyAt.Time
		d.ReadyAt = &t
	}
	if len(progressRaw) > 0 {
		json.Unmarshal(progressRaw, &d.Progress)
	}
	return d, nil
}

// Ping checks database connectivity (used by /readyz).
func (s *Store) Ping() error {
	return s.db.Ping()
}

// GetOldestPending atomically claims the oldest PENDING deployment.
// It sets worker_lock to the current hostname so other replicas skip this row.
func (s *Store) GetOldestPending(ctx context.Context) (*RegistryDeployment, error) {
	hostname, _ := os.Hostname()
	row := s.db.QueryRowContext(ctx, `
		UPDATE registry_deployments
		SET worker_lock = $1, status = 'DEPLOYING', updated_at = now()
		WHERE tenant_id = (
			SELECT tenant_id FROM registry_deployments
			WHERE status = 'PENDING' AND worker_lock IS NULL
			ORDER BY created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING tenant_id, namespace, status, registry_url, helm_release, plan,
		          progress, error_message, hard_delete, created_at, updated_at, ready_at
	`, hostname)
	return scanDeployment(row)
}

// UpdateForDelete atomically sets a deployment to DELETING and stores whether
// it is a hard delete (destroy all data) or soft delete (keep PVCs).
func (s *Store) UpdateForDelete(ctx context.Context, tenantID string, hardDelete bool) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE registry_deployments
		SET status = 'DELETING', hard_delete = $2, worker_lock = NULL, updated_at = now()
		WHERE tenant_id = $1`,
		tenantID, hardDelete,
	)
	return err
}

// GetOldestDeleting atomically claims the oldest DELETING deployment.
// Uses the same FOR UPDATE SKIP LOCKED pattern as GetOldestPending to prevent
// two worker replicas from deleting the same tenant simultaneously.
func (s *Store) GetOldestDeleting(ctx context.Context) (*RegistryDeployment, error) {
	hostname, _ := os.Hostname()
	row := s.db.QueryRowContext(ctx, `
		UPDATE registry_deployments
		SET worker_lock = $1, updated_at = now()
		WHERE tenant_id = (
			SELECT tenant_id FROM registry_deployments
			WHERE status = 'DELETING' AND worker_lock IS NULL
			ORDER BY updated_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING tenant_id, namespace, status, registry_url, helm_release, plan,
		          progress, error_message, hard_delete, created_at, updated_at, ready_at
	`, hostname)
	return scanDeployment(row)
}

// SetDeploymentDeleted marks a soft-deleted deployment as DELETED (keeps the row).
func (s *Store) SetDeploymentDeleted(ctx context.Context, tenantID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE registry_deployments
		SET status = 'DELETED', worker_lock = NULL, updated_at = now()
		WHERE tenant_id = $1`,
		tenantID,
	)
	return err
}

// HardDeleteTenant removes the registry deployment and all credentials permanently.
// Called only for hard deletes after PVCs and namespace are destroyed on Harvester.
func (s *Store) HardDeleteTenant(ctx context.Context, tenantID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM registry_deployments WHERE tenant_id = $1`, tenantID)
	return err
}

// ReleaseDeleteLock clears the worker_lock on a DELETING row without changing status.
// Used when a delete operation fails so another worker replica can retry.
func (s *Store) ReleaseDeleteLock(ctx context.Context, tenantID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE registry_deployments
		SET worker_lock = NULL, updated_at = now()
		WHERE tenant_id = $1 AND status = 'DELETING'`,
		tenantID,
	)
	return err
}

func progressJSON(m map[string]string) []byte {
	if m == nil {
		m = map[string]string{}
	}
	b, _ := json.Marshal(m)
	return b
}
