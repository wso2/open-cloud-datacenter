package harbor

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("harbor ping returned %d", resp.StatusCode)
	}
	return nil
}

// WaitReady polls /api/v2.0/ping until Harbor responds or ctx is cancelled.
func (c *Client) WaitReady(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for Harbor to be ready")
		case <-ticker.C:
			if err := c.Ping(ctx); err == nil {
				return nil
			}
		}
	}
}

// Configure sets Harbor system-level configuration.
func (c *Client) Configure(ctx context.Context) error {
	body := map[string]interface{}{
		"auth_mode":                       "db_auth",
		"project_creation_restriction":    "adminonly",
		"robot_token_duration":            365,
		"self_registration":               false,
		"read_only":                       false,
		"scan_all_policy": map[string]interface{}{
			"type": "scheduled",
			"parameter": map[string]string{
				"schedule": "0 2 * * *",
			},
		},
	}
	return c.put(ctx, "/api/v2.0/configurations", body)
}

// CreateProject creates the default "library" project.
// 409 Conflict is treated as success — Harbor auto-creates "library" on first startup.
func (c *Client) CreateProject(ctx context.Context) error {
	body := map[string]interface{}{
		"project_name": "library",
		"public":       false,
		"metadata": map[string]string{
			"auto_scan":    "true",
			"prevent_vul": "false",
		},
	}
	return c.do(ctx, "POST", "/api/v2.0/projects", body, nil,
		http.StatusCreated, http.StatusOK, http.StatusConflict)
}

// CreateRobotAccount creates the CI/CD robot account and returns its credentials.
func (c *Client) CreateRobotAccount(ctx context.Context) (*RobotAccount, error) {
	body := map[string]interface{}{
		"name":     "ci-default",
		"duration": 365,
		"level":    "system",
		"permissions": []map[string]interface{}{
			{
				"kind":      "project",
				"namespace": "library",
				"access": []map[string]string{
					{"resource": "repository", "action": "push"},
					{"resource": "repository", "action": "pull"},
					{"resource": "artifact", "action": "read"},
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

// CreateHarborProject creates a Harbor project with the given name.
// 409 Conflict is treated as success (already exists — Harbor auto-creates "library").
func (c *Client) CreateHarborProject(ctx context.Context, projectName string) error {
	body := map[string]interface{}{
		"project_name": projectName,
		"public":       false,
		"metadata": map[string]string{
			"auto_scan":    "true",
			"prevent_vul": "false",
		},
	}
	return c.do(ctx, "POST", "/api/v2.0/projects", body, nil,
		http.StatusCreated, http.StatusOK, http.StatusConflict)
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

// Bootstrap runs the full first-run setup: configure → create project → create robot.
// Returns the robot account credentials.
func (c *Client) Bootstrap(ctx context.Context) (*RobotAccount, error) {
	if err := c.Configure(ctx); err != nil {
		return nil, fmt.Errorf("configure harbor: %w", err)
	}
	if err := c.CreateProject(ctx); err != nil {
		return nil, fmt.Errorf("create library project: %w", err)
	}
	robot, err := c.CreateRobotAccount(ctx)
	if err != nil {
		return nil, fmt.Errorf("create robot account: %w", err)
	}
	return robot, nil
}

// --- HTTP helpers ---

func (c *Client) post(ctx context.Context, path string, body interface{}, out interface{}) error {
	return c.do(ctx, "POST", path, body, out, http.StatusCreated, http.StatusOK)
}

func (c *Client) put(ctx context.Context, path string, body interface{}) error {
	return c.do(ctx, "PUT", path, body, nil, http.StatusOK, http.StatusNoContent)
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
		return fmt.Errorf("harbor %s %s returned %d: %s", method, path, resp.StatusCode, respBody)
	}

	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
