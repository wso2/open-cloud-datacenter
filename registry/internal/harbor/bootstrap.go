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

// RobotAccount is a Harbor robot account and its generated secret.
type RobotAccount struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
	ID     int64  `json:"id"`
}

// NewClient returns a Harbor client that verifies the server certificate.
func NewClient(baseURL, adminPassword string) *Client {
	return &Client{
		baseURL:  baseURL,
		username: "admin",
		password: adminPassword,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			},
		},
	}
}

// NewInsecureClient skips certificate verification — only for a dev/self-signed
// Harbor instance, gated behind config.HelmConfig.InsecureHarborTLS (default false).
func NewInsecureClient(baseURL, adminPassword string) *Client {
	c := NewClient(baseURL, adminPassword)
	c.http.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true},
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

// CreateHarborProject creates a Harbor project with an initial storage quota
// (bytes; -1 = unlimited). 409 (already exists) is treated as success; a
// project's quota is changed afterward via EnsureProjectQuota.
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

// ErrProjectNotFound lets callers distinguish a genuine 404 from any other
// Harbor/transport error via errors.Is.
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

// EnsureProjectQuota sets a project's storage quota to storageLimitBytes
// (-1 = unlimited), skipping the write if it's already at that value. Harbor
// rejects lowering a quota below current usage, surfaced here as an error.
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
		// Ambiguous — refuse rather than guess which quota is ours.
		return fmt.Errorf("expected exactly one quota for project %d, got %d", projectID, len(quotas))
	}
	if quotas[0].Hard.Storage == storageLimitBytes {
		return nil // already at the desired value
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
	return c.do(ctx, "DELETE", "/api/v2.0/projects/"+url.PathEscape(projectName), nil, nil,
		http.StatusOK, http.StatusNoContent, http.StatusNotFound)
}

// --- HTTP helpers ---

// get issues a GET request and decodes the response body into out.
func (c *Client) get(ctx context.Context, path string, out interface{}, acceptCodes ...int) error {
	return c.do(ctx, "GET", path, nil, out, acceptCodes...)
}

// post issues a POST request and decodes the response body into out.
func (c *Client) post(ctx context.Context, path string, body interface{}, out interface{}) error {
	return c.do(ctx, "POST", path, body, out, http.StatusCreated, http.StatusOK)
}

// put issues a PUT request, discarding the response body.
func (c *Client) put(ctx context.Context, path string, body interface{}) error {
	return c.do(ctx, "PUT", path, body, nil, http.StatusOK, http.StatusNoContent)
}

// StatusError carries Harbor's HTTP status so callers can match a specific
// code (e.g. 404) with errors.As.
type StatusError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

// Error implements the error interface.
func (e *StatusError) Error() string {
	return fmt.Sprintf("harbor %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// do performs an authenticated request, returning a StatusError when the
// response status is outside acceptCodes.
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
