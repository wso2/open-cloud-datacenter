package harbor

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is a Harbor REST API client for first-run bootstrap.
type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

type RobotAccount struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
	ID     int64  `json:"id"`
}

func NewClient(baseURL, adminPassword string) *Client {
	return &Client{
		baseURL:  baseURL,
		username: "admin",
		password: adminPassword,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				// In production use proper TLS; for dev/self-signed this is acceptable
				TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
			},
		},
	}
}

// NewInsecureClient is for dev environments with self-signed certs.
func NewInsecureClient(baseURL, adminPassword string) *Client {
	c := NewClient(baseURL, adminPassword)
	c.http.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	return c
}

// Ping checks whether Harbor is up and accepting requests.
func (c *Client) Ping(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/v2.0/ping", nil)
	resp, err := c.http.Do(req)
	if err != nil { //Pods are not started yet
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { //Pods are layed but not ready for perfomance
		return fmt.Errorf("harbor ping returned %d", resp.StatusCode)
	}
	return nil
}

// Configure sets Harbor system-level configuration.
func (c *Client) Configure(ctx context.Context) error {
	body := map[string]interface{}{
		"auth_mode":                    "db_auth",
		"project_creation_restriction": "adminonly",
		"robot_token_duration":         365,
		"self_registration":            false,
		"read_only":                    false,
		"scan_all_policy": map[string]interface{}{
			"type": "scheduled",
			"parameter": map[string]string{
				"schedule": "0 2 * * *", //make system wide scan at 2 AM
			},
		},
	}
	return c.put(ctx, "/api/v2.0/configurations", body)
}

// CreateHarborProject creates a Harbor project with the given name and an
// initial storage quota (bytes; -1 means unlimited). 409 Conflict is treated
// as success (already exists) — storage_limit only takes effect on the
// first, successful creation; an existing project's quota is changed
// separately via EnsureProjectQuota, which is safe to call on every
// reconcile regardless of whether the project is new or pre-existing.
// Harbor's real API documents 201 (created) and 409 (already exists) for
// this endpoint — not 200 — so 200 is deliberately not in the accepted list;
// treating an undocumented status as success would risk masking a
// misconfigured proxy/ingress in front of Harbor silently returning 200 with
// an unrelated body instead of actually proxying the request through.
func (c *Client) CreateHarborProject(ctx context.Context, projectName string, storageLimitBytes int64) error {
	body := map[string]interface{}{
		"project_name":  projectName,
		"public":        false,
		"storage_limit": storageLimitBytes,
		"metadata": map[string]string{
			"auto_scan":   "true",
			"prevent_vul": "false",
		},
	}
	return c.do(ctx, "POST", "/api/v2.0/projects", body, nil,
		http.StatusCreated, http.StatusConflict)
}

// Project is the subset of Harbor's project object this client needs.
type Project struct {
	ProjectID int64 `json:"project_id"`
}

// ErrProjectNotFound is returned by GetProject when Harbor responds 404 —
// callers can distinguish "genuinely doesn't exist" from any other transport
// or server error via errors.Is, rather than both looking like the same
// generic wrapped error string.
var ErrProjectNotFound = errors.New("harbor project not found")

// GetProject fetches a Harbor project by name.
func (c *Client) GetProject(ctx context.Context, projectName string) (*Project, error) {
	var p Project
	err := c.get(ctx, "/api/v2.0/projects/"+url.PathEscape(projectName), &p, http.StatusOK)
	if err != nil {
		var serr *StatusError
		if errors.As(err, &serr) && serr.StatusCode == http.StatusNotFound {
			return nil, ErrProjectNotFound
		}
		return nil, err
	}
	return &p, nil
}

// EnsureProjectQuota sets a project's storage quota to exactly
// storageLimitBytes (-1 for unlimited). Every Harbor project already has a
// quota object created automatically alongside it; this looks that quota up
// by the project's ID and updates its hard limit only if it isn't already
// set to the desired value — Harbor's own idempotency on an unchanged PUT
// isn't assumed, it's checked here, the same Cmp-before-write convergence
// idiom RegistryBackendReconciler.ensureStorageSize uses for PVC sizes. This
// is how a plan change on an existing RegistryInstance actually takes
// effect, and it's safe to call every reconcile since the steady-state case
// is now a read with no write. Harbor itself rejects lowering the quota
// below the project's current usage, which surfaces here as an error for
// the caller to report and retry.
func (c *Client) EnsureProjectQuota(ctx context.Context, projectID, storageLimitBytes int64) error {
	var quotas []struct {
		ID   int64 `json:"id"`
		Hard struct {
			Storage int64 `json:"storage"`
		} `json:"hard"`
	}
	path := fmt.Sprintf("/api/v2.0/quotas?reference=project&reference_id=%d", projectID)
	if err := c.get(ctx, path, &quotas, http.StatusOK); err != nil {
		return fmt.Errorf("list quota for project %d: %w", projectID, err)
	}
	if len(quotas) == 0 {
		return fmt.Errorf("no quota found for project %d", projectID)
	}
	if len(quotas) > 1 {
		// Harbor's own API contract says /quotas filters by reference_id, so
		// this shouldn't happen — but if it ever does, guessing which one is
		// "ours" and mutating it would risk silently repointing a different
		// project's quota. Fail loudly instead.
		return fmt.Errorf("expected exactly one quota for project %d, got %d", projectID, len(quotas))
	}
	if quotas[0].Hard.Storage == storageLimitBytes {
		return nil // already converged — no write needed
	}
	body := map[string]interface{}{
		"hard": map[string]int64{"storage": storageLimitBytes},
	}
	if err := c.put(ctx, fmt.Sprintf("/api/v2.0/quotas/%d", quotas[0].ID), body); err != nil {
		return fmt.Errorf("update quota %d: %w", quotas[0].ID, err)
	}
	return nil
}

// CreateProjectRobotAccount creates a project-scoped robot account with push/pull/delete.
// The robot can only operate within the named Harbor project (not system-wide).
func (c *Client) CreateProjectRobotAccount(ctx context.Context, projectName, robotName string) (*RobotAccount, error) {
	body := map[string]interface{}{
		"name":     robotName,
		"duration": 365,
		"level":    "project",
		"permissions": []map[string]interface{}{
			{
				"kind":      "project",
				"namespace": projectName,
				"access": []map[string]string{
					{"resource": "repository", "action": "push"},
					{"resource": "repository", "action": "pull"},
					{"resource": "repository", "action": "delete"},
					{"resource": "artifact", "action": "read"},
					{"resource": "artifact", "action": "delete"},
					{"resource": "tag", "action": "create"},
					{"resource": "tag", "action": "delete"},
					{"resource": "scan", "action": "create"},
				},
			},
		},
	}
	var robot RobotAccount
	if err := c.post(ctx, "/api/v2.0/robots", body, &robot); err != nil {
		return nil, err
	}
	return &robot, nil
}

// DeleteProject deletes a Harbor project by name. 404 (already gone) is treated
// as success. 412 Precondition Failed means the project still has repositories;
// Harbor refuses to delete a non-empty project, so we surface that as an error
// (the caller leaves cleanup to an admin rather than silently orphaning data).
func (c *Client) DeleteProject(ctx context.Context, projectName string) error {
	return c.do(ctx, "DELETE", "/api/v2.0/projects/"+projectName, nil, nil,
		http.StatusOK, http.StatusNoContent, http.StatusNotFound)
}

// --- HTTP helpers ---

func (c *Client) get(ctx context.Context, path string, out interface{}, acceptCodes ...int) error {
	return c.do(ctx, "GET", path, nil, out, acceptCodes...)
}

func (c *Client) post(ctx context.Context, path string, body interface{}, out interface{}) error {
	return c.do(ctx, "POST", path, body, out, http.StatusCreated, http.StatusOK)
}

func (c *Client) put(ctx context.Context, path string, body interface{}) error {
	return c.do(ctx, "PUT", path, body, nil, http.StatusOK, http.StatusNoContent)
}

// StatusError is returned by do() when Harbor responds with a status code
// outside the caller's accepted set, carrying the real StatusCode so callers
// like GetProject can distinguish specific codes (404) via errors.As instead
// of parsing the error string.
type StatusError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("harbor %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

func (c *Client) do(ctx context.Context, method, path string, body interface{}, out interface{}, acceptCodes ...int) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("harbor %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	ok := false
	for _, code := range acceptCodes {
		if resp.StatusCode == code {
			ok = true
			break
		}
	}
	if !ok {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &StatusError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
