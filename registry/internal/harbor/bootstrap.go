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
	"strings"
	"time"
)

const (
	// pageSize is the largest page Harbor's list endpoints accept.
	pageSize = 100

	// maxPages bounds every paged listing. A short page ends a listing, so a
	// server that kept answering with full ones would otherwise spin until the
	// context expired, holding the reconcile open. At pageSize this still covers
	// far more projects or repositories than a namespace realistically holds.
	maxPages = 1000
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

// Ping checks whether Harbor is up and accepting requests.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/v2.0/ping", nil)
	if err != nil {
		return fmt.Errorf("build ping request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err // pods are not accepting connections yet
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
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
	}
	return c.put(ctx, "/api/v2.0/configurations", body)
}

// schedule is the subset of Harbor's schedule object this client needs. Harbor
// uses the same shape for every scheduled system job.
type schedule struct {
	Schedule struct {
		Type string `json:"type"`
		Cron string `json:"cron"`
	} `json:"schedule"`
}

// ensureSchedule points one of Harbor's scheduled system jobs at cron, creating
// the schedule when absent and rewriting it only when it differs — the same
// read-compare-write shape as EnsureProjectQuota. Harbor's cron carries six
// fields, the first being seconds.
func (c *Client) ensureSchedule(ctx context.Context, path, cron string) error {
	var current schedule
	if err := c.get(ctx, path, &current, http.StatusOK); err != nil {
		return fmt.Errorf("get schedule %s: %w", path, err)
	}
	if current.Schedule.Type == "Schedule" && current.Schedule.Cron == cron {
		return nil // already correct
	}

	body := map[string]interface{}{
		"schedule": map[string]string{"type": "Schedule", "cron": cron},
	}
	// POST creates the schedule, PUT updates an existing one; Harbor rejects the
	// wrong verb, so pick by whether one is already configured.
	if current.Schedule.Type == "" || current.Schedule.Type == "None" {
		return c.do(ctx, "POST", path, body, nil, http.StatusCreated, http.StatusOK)
	}
	return c.put(ctx, path, body)
}

// EnsureGCSchedule sets Harbor's garbage-collection schedule to cron, creating
// it when absent and rewriting it only when it differs — the same
// read-compare-write shape as EnsureProjectQuota.
//
// GC is not an optimisation here, it is what makes the operator's capacity
// accounting sound. Deleting a project removes its manifests but leaves the
// blobs on disk, and those orphaned blobs belong to no project's quota, so they
// are invisible to both CommittedStorageBytes and UsedStorageBytes while still
// consuming the registry volume. Without a sweep that term grows without bound
// and nothing the operator measures can see it.
//
// delete_untagged is deliberately NOT set. It would additionally remove
// artifacts that have lost their tag, which breaks any deployment pinning an
// image by digest after a tag moves. It is also unnecessary for reclaiming a
// deleted Registry: deleting the project removes its manifests, so its blobs
// are already unreferenced and a plain sweep collects them.
func (c *Client) EnsureGCSchedule(ctx context.Context, cron string) error {
	return c.ensureSchedule(ctx, "/api/v2.0/system/gc/schedule", cron)
}

// EnsureScanAllSchedule sets Harbor's scan-all schedule to cron, so every
// artifact is periodically rescanned against updated vulnerability data. A scan
// records what was known at the time it ran, so without a repeating sweep a
// CVE published after an image is pushed never appears against it.
func (c *Client) EnsureScanAllSchedule(ctx context.Context, cron string) error {
	return c.ensureSchedule(ctx, "/api/v2.0/system/scanAll/schedule", cron)
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

// StorageTotals is the storage committed to and consumed by every project in a
// Harbor instance.
type StorageTotals struct {
	// Committed is the sum of every project's hard storage limit. Projects with
	// an unlimited quota (-1) are excluded, since they cannot be summed.
	Committed int64
	// Used is the sum of every project's actual consumption.
	Used int64
	// Unlimited counts projects whose quota is -1.
	Unlimited int
}

// ProjectStorageTotals sums the hard limits and usage of every project quota in
// Harbor. Harbor reports both on the quota object, so capacity planning needs
// no metrics pipeline.
func (c *Client) ProjectStorageTotals(ctx context.Context) (StorageTotals, error) {
	var totals StorageTotals

	for page := 1; page <= maxPages; page++ {
		var quotas []struct {
			Hard struct {
				Storage int64 `json:"storage"`
			} `json:"hard"`
			Used struct {
				Storage int64 `json:"storage"`
			} `json:"used"`
		}
		path := fmt.Sprintf("/api/v2.0/quotas?reference=project&page=%d&page_size=%d", page, pageSize)
		if err := c.get(ctx, path, &quotas, http.StatusOK); err != nil {
			return StorageTotals{}, fmt.Errorf("list project quotas page %d: %w", page, err)
		}
		for _, q := range quotas {
			if q.Hard.Storage < 0 {
				totals.Unlimited++
			} else {
				totals.Committed += q.Hard.Storage
			}
			if q.Used.Storage > 0 {
				totals.Used += q.Used.Storage
			}
		}
		if len(quotas) < pageSize {
			return totals, nil
		}
	}
	return StorageTotals{}, fmt.Errorf("list project quotas: exceeded %d pages", maxPages)
}

// robotFullName is how Harbor names a project-scoped robot: the account created
// for robotName inside projectName is listed and addressed only in this form.
func robotFullName(projectName, robotName string) string {
	return "robot$" + projectName + "+" + robotName
}

// EnsureProjectRobotAccount mints the project robot named robotName and returns
// its generated secret.
//
// Harbor discloses a robot's secret only in the create response, so an account
// whose secret was never stored can never be recovered — only replaced. That is
// precisely the state a failed credentials-Secret write leaves behind, so a
// conflict here means the previous attempt died mid-way: replace the orphan
// rather than failing forever against it.
func (c *Client) EnsureProjectRobotAccount(ctx context.Context, projectName, robotName string) (*RobotAccount, error) {
	robot, err := c.createProjectRobotAccount(ctx, projectName, robotName)
	if err == nil {
		return robot, nil
	}

	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusConflict {
		return nil, err
	}

	id, findErr := c.findProjectRobotID(ctx, projectName, robotName)
	if findErr != nil {
		return nil, findErr
	}
	if id == 0 {
		return nil, err // conflicts with an account this client cannot see
	}
	if delErr := c.DeleteRobot(ctx, id); delErr != nil {
		return nil, delErr
	}
	return c.createProjectRobotAccount(ctx, projectName, robotName)
}

// findProjectRobotID returns the ID of the project robot named robotName, or 0
// when Harbor holds no such account.
func (c *Client) findProjectRobotID(ctx context.Context, projectName, robotName string) (int64, error) {
	want := robotFullName(projectName, robotName)
	for page := 1; page <= maxPages; page++ {
		var batch []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		path := fmt.Sprintf("/api/v2.0/robots?page=%d&page_size=%d", page, pageSize)
		if err := c.get(ctx, path, &batch, http.StatusOK); err != nil {
			return 0, fmt.Errorf("list robots page %d: %w", page, err)
		}
		for _, r := range batch {
			if r.Name == want {
				return r.ID, nil
			}
		}
		if len(batch) < pageSize {
			return 0, nil
		}
	}
	return 0, fmt.Errorf("list robots: exceeded %d pages", maxPages)
}

// DeleteRobot removes a robot account. An absent account is success.
func (c *Client) DeleteRobot(ctx context.Context, id int64) error {
	return c.do(ctx, "DELETE", fmt.Sprintf("/api/v2.0/robots/%d", id), nil, nil,
		http.StatusOK, http.StatusNoContent, http.StatusNotFound)
}

// createProjectRobotAccount creates a project-scoped robot account with
// push/pull/delete. The robot can only operate within the named Harbor project
// (not system-wide).
//
// duration -1 makes the account non-expiring. The operator has no rotation
// pass, so a bounded lifetime would silently break every pipeline holding these
// credentials once it elapsed; the account is instead revoked by deleting the
// Registry that owns it.
func (c *Client) createProjectRobotAccount(ctx context.Context, projectName, robotName string) (*RobotAccount, error) {
	body := map[string]interface{}{
		"name":     robotName,
		"duration": -1,
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

// ListRepositories returns the repositories in a project, named as the delete
// endpoint expects them: Harbor reports the full "<project>/<repo>", and the
// project prefix is stripped here so callers never have to. Follows Harbor's
// pagination the same way ProjectStorageTotals does.
func (c *Client) ListRepositories(ctx context.Context, projectName string) ([]string, error) {
	var names []string
	for page := 1; page <= maxPages; page++ {
		var batch []struct {
			Name string `json:"name"`
		}
		path := fmt.Sprintf("/api/v2.0/projects/%s/repositories?page=%d&page_size=%d",
			url.PathEscape(projectName), page, pageSize)
		if err := c.get(ctx, path, &batch, http.StatusOK); err != nil {
			return nil, fmt.Errorf("list repositories in %s page %d: %w", projectName, page, err)
		}
		for _, repo := range batch {
			names = append(names, strings.TrimPrefix(repo.Name, projectName+"/"))
		}
		if len(batch) < pageSize {
			return names, nil
		}
	}
	return nil, fmt.Errorf("list repositories in %s: exceeded %d pages", projectName, maxPages)
}

// DeleteRepository removes one repository and everything in it. 404 is treated
// as success so the call is idempotent under retry.
//
// repoName is the name within the project, which may itself contain slashes
// ("team/app"). Harbor expects those percent-encoded rather than read as extra
// path segments, hence PathEscape.
func (c *Client) DeleteRepository(ctx context.Context, projectName, repoName string) error {
	path := fmt.Sprintf("/api/v2.0/projects/%s/repositories/%s",
		url.PathEscape(projectName), url.PathEscape(repoName))
	return c.do(ctx, "DELETE", path, nil, nil,
		http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound)
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
		// An empty body decodes to io.EOF. That is not a failure: Harbor answers
		// 200 with no content for "this thing is not configured yet" — its GC
		// schedule does exactly that before one is ever set. Leave out at its
		// zero value and let the caller read that as absent.
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	}
	return nil
}
