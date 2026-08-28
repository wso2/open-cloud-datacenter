package harbor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient points a Client at a local httptest.Server instead of a real Harbor instance.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "test-admin-pass"), srv
}

func TestPing(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{"harbor up", http.StatusOK, false},
		{"pods scheduled but not ready", http.StatusServiceUnavailable, true},
		{"harbor core crashed", http.StatusInternalServerError, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2.0/ping" {
					t.Errorf("Ping hit unexpected path %q", r.URL.Path)
				}
				if r.Method != http.MethodGet {
					t.Errorf("Ping used method %q, want GET", r.Method)
				}
				w.WriteHeader(tt.statusCode)
			})
			err := cli.Ping(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("Ping() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfigure(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{"applied, 200", http.StatusOK, false},
		{"applied, 204 no content", http.StatusNoContent, false},
		{"rejected", http.StatusBadRequest, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody map[string]interface{}
			cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut {
					t.Errorf("Configure used method %q, want PUT", r.Method)
				}
				if r.URL.Path != "/api/v2.0/configurations" {
					t.Errorf("Configure hit unexpected path %q", r.URL.Path)
				}
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(tt.statusCode)
			})
			err := cli.Configure(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("Configure() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if gotBody["auth_mode"] != "db_auth" {
					t.Errorf("Configure sent auth_mode=%v, want db_auth", gotBody["auth_mode"])
				}
				if gotBody["self_registration"] != false {
					t.Errorf("Configure sent self_registration=%v, want false", gotBody["self_registration"])
				}
			}
		})
	}
}

func TestCreateHarborProject(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{"newly created", http.StatusCreated, false},
		{"200 is undocumented for this endpoint, so it is an error", http.StatusOK, true},
		{"already existed (409 conflict) — idempotent by design", http.StatusConflict, false},
		{"harbor rejected the request", http.StatusBadRequest, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody map[string]interface{}
			cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2.0/projects" {
					t.Errorf("CreateHarborProject hit unexpected path %q", r.URL.Path)
				}
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(tt.statusCode)
			})
			err := cli.CreateHarborProject(context.Background(), "acme-project", 5*1024*1024*1024)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CreateHarborProject() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if gotBody["project_name"] != "acme-project" {
					t.Errorf("CreateHarborProject sent project_name=%v, want acme-project", gotBody["project_name"])
				}
				if gotBody["storage_limit"] != float64(5*1024*1024*1024) {
					t.Errorf("CreateHarborProject sent storage_limit=%v, want %d", gotBody["storage_limit"], 5*1024*1024*1024)
				}
			}
		})
	}
}

func TestGetProject(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			wantPath := "/api/v2.0/projects/acme-project"
			if r.URL.Path != wantPath {
				t.Errorf("GetProject hit path %q, want %q", r.URL.Path, wantPath)
			}
			if r.Method != http.MethodGet {
				t.Errorf("GetProject used method %q, want GET", r.Method)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"project_id": 7})
		})
		proj, err := cli.GetProject(context.Background(), "acme-project")
		if err != nil {
			t.Fatalf("GetProject() error = %v", err)
		}
		if proj.ProjectID != 7 {
			t.Errorf("GetProject().ProjectID = %d, want 7", proj.ProjectID)
		}
	})

	t.Run("project name with special characters is path-escaped", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			wantPath := "/api/v2.0/projects/acme%2Fweird"
			if r.URL.EscapedPath() != wantPath {
				t.Errorf("GetProject hit path %q, want %q", r.URL.EscapedPath(), wantPath)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"project_id": 1})
		})
		if _, err := cli.GetProject(context.Background(), "acme/weird"); err != nil {
			t.Fatalf("GetProject() error = %v", err)
		}
	})

	t.Run("404 maps to ErrProjectNotFound", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		_, err := cli.GetProject(context.Background(), "ghost-project")
		if !errors.Is(err, ErrProjectNotFound) {
			t.Fatalf("GetProject() error = %v, want ErrProjectNotFound", err)
		}
	})

	t.Run("500 is a generic error, not ErrProjectNotFound", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		_, err := cli.GetProject(context.Background(), "acme-project")
		if err == nil {
			t.Fatal("GetProject() error = nil, want an error on 500")
		}
		if errors.Is(err, ErrProjectNotFound) {
			t.Fatal("GetProject() on a 500 incorrectly reported ErrProjectNotFound")
		}
	})
}

func TestEnsureProjectQuota(t *testing.T) {
	t.Run("finds the quota by project reference and updates it when it differs", func(t *testing.T) {
		var gotUpdatePath string
		var gotBody map[string]interface{}
		var putCalled bool
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/quotas":
				if got := r.URL.Query().Get("reference_id"); got != "7" {
					t.Errorf("quota list reference_id = %q, want 7", got)
				}
				if got := r.URL.Query().Get("reference"); got != "project" {
					t.Errorf("quota list reference = %q, want project", got)
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode([]map[string]interface{}{
					{"id": 99, "hard": map[string]interface{}{"storage": 1 * 1024 * 1024 * 1024}},
				})
			case r.Method == http.MethodPut:
				putCalled = true
				gotUpdatePath = r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(http.StatusOK)
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		})
		if err := cli.EnsureProjectQuota(context.Background(), 7, 5*1024*1024*1024); err != nil {
			t.Fatalf("EnsureProjectQuota() error = %v", err)
		}
		if !putCalled {
			t.Fatal("EnsureProjectQuota() did not PUT despite the quota differing from desired")
		}
		if gotUpdatePath != "/api/v2.0/quotas/99" {
			t.Errorf("PUT went to %q, want /api/v2.0/quotas/99", gotUpdatePath)
		}
		hard, ok := gotBody["hard"].(map[string]interface{})
		if !ok || hard["storage"] != float64(5*1024*1024*1024) {
			t.Errorf("PUT body hard.storage = %v, want %d", gotBody["hard"], 5*1024*1024*1024)
		}
	})

	t.Run("already at the desired value skips the write entirely", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				t.Fatal("EnsureProjectQuota() issued a PUT when the quota already matched")
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{
				{"id": 99, "hard": map[string]interface{}{"storage": 5 * 1024 * 1024 * 1024}},
			})
		})
		if err := cli.EnsureProjectQuota(context.Background(), 7, 5*1024*1024*1024); err != nil {
			t.Fatalf("EnsureProjectQuota() error = %v", err)
		}
	})

	t.Run("-1 (unlimited) round-trips as a valid target value", func(t *testing.T) {
		var putCalled bool
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode([]map[string]interface{}{
					{"id": 99, "hard": map[string]interface{}{"storage": 5 * 1024 * 1024 * 1024}},
				})
			case http.MethodPut:
				putCalled = true
				var body map[string]interface{}
				_ = json.NewDecoder(r.Body).Decode(&body)
				hard, _ := body["hard"].(map[string]interface{})
				if hard["storage"] != float64(-1) {
					t.Errorf("PUT body hard.storage = %v, want -1", hard["storage"])
				}
				w.WriteHeader(http.StatusOK)
			}
		})
		if err := cli.EnsureProjectQuota(context.Background(), 7, -1); err != nil {
			t.Fatalf("EnsureProjectQuota() error = %v", err)
		}
		if !putCalled {
			t.Fatal("EnsureProjectQuota() did not PUT when moving from a limited to an unlimited (-1) quota")
		}
	})

	t.Run("no quota found for the project errors instead of silently no-op'ing", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
		})
		if err := cli.EnsureProjectQuota(context.Background(), 7, 5*1024*1024*1024); err == nil {
			t.Fatal("EnsureProjectQuota() error = nil, want an error when no quota is found")
		}
	})

	t.Run("more than one quota returned errors instead of guessing which one is ours", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				t.Fatal("EnsureProjectQuota() issued a PUT despite an ambiguous quota list")
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{
				{"id": 99, "hard": map[string]interface{}{"storage": 1 * 1024 * 1024 * 1024}},
				{"id": 100, "hard": map[string]interface{}{"storage": 2 * 1024 * 1024 * 1024}},
			})
		})
		if err := cli.EnsureProjectQuota(context.Background(), 7, 5*1024*1024*1024); err == nil {
			t.Fatal("EnsureProjectQuota() error = nil, want an error when more than one quota is returned")
		}
	})
}

func TestEnsureProjectRobotAccount(t *testing.T) {
	t.Run("newly created robot returns non-expiring credentials", func(t *testing.T) {
		wantRobot := RobotAccount{Name: "robot$acme-project+ci-robot", Secret: "s3cr3t-token", ID: 42}
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v2.0/robots" {
				t.Errorf("EnsureProjectRobotAccount hit unexpected path %q", r.URL.Path)
			}
			var gotBody map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			if gotBody["level"] != "project" {
				t.Errorf(`EnsureProjectRobotAccount sent level=%v, want "project"`, gotBody["level"])
			}
			// Nothing rotates these credentials, so a bounded lifetime would
			// silently break every pipeline holding them once it elapsed.
			if gotBody["duration"] != float64(-1) {
				t.Errorf("EnsureProjectRobotAccount sent duration=%v, want -1", gotBody["duration"])
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(wantRobot)
		})
		robot, err := cli.EnsureProjectRobotAccount(context.Background(), "acme-project", "ci-robot")
		if err != nil {
			t.Fatalf("EnsureProjectRobotAccount() error = %v", err)
		}
		if robot.Secret != wantRobot.Secret || robot.ID != wantRobot.ID {
			t.Errorf("EnsureProjectRobotAccount() = %+v, want %+v", robot, wantRobot)
		}
	})

	// Harbor discloses a robot's secret only at creation, so an account left by a
	// half-finished provision is unusable and unrecoverable. Failing against it
	// forever would wedge the Registry, so the orphan is replaced instead.
	t.Run("conflict replaces the unrecoverable orphan", func(t *testing.T) {
		wantRobot := RobotAccount{Name: "robot$acme-project+ci-robot", Secret: "fresh-token", ID: 43}
		var posts, deletes int
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/robots":
				posts++
				if posts == 1 {
					w.WriteHeader(http.StatusConflict)
					return
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(wantRobot)
			case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/robots":
				_ = json.NewEncoder(w).Encode([]map[string]interface{}{
					{"id": 7, "name": "robot$other-project+ci-robot"},
					{"id": 42, "name": "robot$acme-project+ci-robot"},
				})
			case r.Method == http.MethodDelete && r.URL.Path == "/api/v2.0/robots/42":
				deletes++
				w.WriteHeader(http.StatusOK)
			default:
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			}
		})
		robot, err := cli.EnsureProjectRobotAccount(context.Background(), "acme-project", "ci-robot")
		if err != nil {
			t.Fatalf("EnsureProjectRobotAccount() error = %v", err)
		}
		if robot.Secret != wantRobot.Secret {
			t.Errorf("EnsureProjectRobotAccount() secret = %q, want the freshly minted %q",
				robot.Secret, wantRobot.Secret)
		}
		// Only the same-project account may be removed; matching on the bare
		// robot name would have deleted another project's robot.
		if deletes != 1 {
			t.Errorf("deleted %d robots, want exactly the one orphan", deletes)
		}
		if posts != 2 {
			t.Errorf("posted %d times, want a retry after the replacement", posts)
		}
	})

	t.Run("conflict with no matching account surfaces the error", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
				return
			}
			w.WriteHeader(http.StatusConflict)
		})
		_, err := cli.EnsureProjectRobotAccount(context.Background(), "acme-project", "ci-robot")
		if err == nil {
			t.Fatal("EnsureProjectRobotAccount() returned nil error when the conflicting " +
				"account was not visible; the conflict must not be silently swallowed")
		}
	})
}

func TestDeleteProject(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{"deleted, 200", http.StatusOK, false},
		{"deleted, 204 no content", http.StatusNoContent, false},
		{"already gone, 404 treated as success", http.StatusNotFound, false},
		{"project has repositories, 412 refused", http.StatusPreconditionFailed, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("DeleteProject used method %q, want DELETE", r.Method)
				}
				wantPath := "/api/v2.0/projects/acme-project"
				if r.URL.Path != wantPath {
					t.Errorf("DeleteProject hit path %q, want %q", r.URL.Path, wantPath)
				}
				w.WriteHeader(tt.statusCode)
			})
			err := cli.DeleteProject(context.Background(), "acme-project")
			if (err != nil) != tt.wantErr {
				t.Fatalf("DeleteProject() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}

	t.Run("project name with special characters is path-escaped", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			wantPath := "/api/v2.0/projects/acme%2Fweird"
			if r.URL.EscapedPath() != wantPath {
				t.Errorf("DeleteProject hit path %q, want %q", r.URL.EscapedPath(), wantPath)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := cli.DeleteProject(context.Background(), "acme/weird"); err != nil {
			t.Fatalf("DeleteProject() error = %v", err)
		}
	})
}

// Ping deliberately bypasses do() — Harbor's /ping endpoint needs no auth, so
// it's not the right call to prove auth headers are set. Configure goes
// through put() -> do(), which is where Basic Auth and Content-Type actually
// get attached.
func TestDo_SetsBasicAuthAndHeaders(t *testing.T) {
	cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok {
			t.Fatal("request had no Basic Auth credentials")
		}
		if user != "admin" {
			t.Errorf("BasicAuth username = %q, want %q", user, "admin")
		}
		if pass != "test-admin-pass" {
			t.Errorf("BasicAuth password = %q, want %q", pass, "test-admin-pass")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusOK)
	})
	if err := cli.Configure(context.Background()); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
}

func TestProjectStorageTotals(t *testing.T) {
	t.Run("sums hard and used across projects", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("reference"); got != "project" {
				t.Errorf("reference = %q, want project", got)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{
				{"hard": map[string]interface{}{"storage": 5}, "used": map[string]interface{}{"storage": 2}},
				{"hard": map[string]interface{}{"storage": 20}, "used": map[string]interface{}{"storage": 7}},
			})
		})
		got, err := cli.ProjectStorageTotals(context.Background())
		if err != nil {
			t.Fatalf("ProjectStorageTotals() error = %v", err)
		}
		if got.Committed != 25 {
			t.Errorf("Committed = %d, want 25", got.Committed)
		}
		if got.Used != 9 {
			t.Errorf("Used = %d, want 9", got.Used)
		}
		if got.Unlimited != 0 {
			t.Errorf("Unlimited = %d, want 0", got.Unlimited)
		}
	})

	// An unlimited quota commits an unbounded amount, so it must be counted
	// separately rather than folded into the sum as -1.
	t.Run("unlimited quotas are counted, not summed", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{
				{"hard": map[string]interface{}{"storage": 5}, "used": map[string]interface{}{"storage": 1}},
				{"hard": map[string]interface{}{"storage": -1}, "used": map[string]interface{}{"storage": 3}},
			})
		})
		got, err := cli.ProjectStorageTotals(context.Background())
		if err != nil {
			t.Fatalf("ProjectStorageTotals() error = %v", err)
		}
		if got.Committed != 5 {
			t.Errorf("Committed = %d, want 5 (the -1 must not be added)", got.Committed)
		}
		if got.Unlimited != 1 {
			t.Errorf("Unlimited = %d, want 1", got.Unlimited)
		}
		if got.Used != 4 {
			t.Errorf("Used = %d, want 4", got.Used)
		}
	})

	t.Run("no projects yields zero totals", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
		})
		got, err := cli.ProjectStorageTotals(context.Background())
		if err != nil {
			t.Fatalf("ProjectStorageTotals() error = %v", err)
		}
		if got.Committed != 0 || got.Used != 0 {
			t.Errorf("got %+v, want zero totals", got)
		}
	})
}

// GC keeps the operator's capacity accounting sound: blobs left by a deleted
// project belong to no quota, so they are invisible to ProjectStorageTotals
// while still filling the volume. The schedule must be re-asserted like every
// other desired state, and must not rewrite an already-correct one.
func TestEnsureGCSchedule(t *testing.T) {
	var gets, posts, puts int
	var sentCron string
	existing := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			gets++
			if existing == "" {
				_, _ = w.Write([]byte(`{"schedule":{"type":"None","cron":""}}`))
				return
			}
			_, _ = w.Write([]byte(`{"schedule":{"type":"Schedule","cron":"` + existing + `"}}`))
		case "POST", "PUT":
			var body struct {
				Schedule struct{ Cron string } `json:"schedule"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			sentCron = body.Schedule.Cron
			if r.Method == "POST" {
				posts++
			} else {
				puts++
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "pw")

	// No schedule yet -> create.
	if err := c.EnsureGCSchedule(context.Background(), "0 0 3 * * *"); err != nil {
		t.Fatalf("EnsureGCSchedule() error = %v", err)
	}
	if posts != 1 || puts != 0 {
		t.Errorf("absent schedule: posts=%d puts=%d, want 1/0", posts, puts)
	}
	if sentCron != "0 0 3 * * *" {
		t.Errorf("sent cron = %q", sentCron)
	}

	// Already correct -> no write at all.
	existing = "0 0 3 * * *"
	if err := c.EnsureGCSchedule(context.Background(), "0 0 3 * * *"); err != nil {
		t.Fatalf("EnsureGCSchedule() error = %v", err)
	}
	if posts != 1 || puts != 0 {
		t.Errorf("matching schedule rewrote it: posts=%d puts=%d, want 1/0", posts, puts)
	}

	// Drifted -> update in place.
	existing = "0 0 9 * * *"
	if err := c.EnsureGCSchedule(context.Background(), "0 0 3 * * *"); err != nil {
		t.Fatalf("EnsureGCSchedule() error = %v", err)
	}
	if puts != 1 {
		t.Errorf("drifted schedule: puts=%d, want 1", puts)
	}
}

// Harbor answers 200 with an EMPTY body for "not configured yet" — its GC
// schedule does exactly that before one is ever set, and an empty body decodes
// to io.EOF. This was found on a real cluster, not by the fake server above,
// which always returned JSON: a test that only feeds well-formed responses
// proves the parser works on the author's assumptions, not on the API.
func TestEnsureGCSchedule_TreatsEmptyBodyAsNoSchedule(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(http.StatusOK) // 200, no body at all
			return
		}
		posts++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := NewClient(srv.URL, "pw").EnsureGCSchedule(context.Background(), "0 0 3 * * *"); err != nil {
		t.Fatalf("EnsureGCSchedule() error = %v, want an empty body treated as no schedule", err)
	}
	if posts != 1 {
		t.Errorf("posts = %d, want 1 — an absent schedule must be created", posts)
	}
}
