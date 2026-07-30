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

func TestCreateProjectRobotAccount(t *testing.T) {
	t.Run("newly created robot returns credentials", func(t *testing.T) {
		wantRobot := RobotAccount{Name: "robot$acme-project+ci-robot", Secret: "s3cr3t-token", ID: 42}
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v2.0/robots" {
				t.Errorf("CreateProjectRobotAccount hit unexpected path %q", r.URL.Path)
			}
			var gotBody map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			if gotBody["level"] != "project" {
				t.Errorf(`CreateProjectRobotAccount sent level=%v, want "project"`, gotBody["level"])
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(wantRobot)
		})
		robot, err := cli.CreateProjectRobotAccount(context.Background(), "acme-project", "ci-robot")
		if err != nil {
			t.Fatalf("CreateProjectRobotAccount() error = %v", err)
		}
		if robot.Secret != wantRobot.Secret || robot.ID != wantRobot.ID {
			t.Errorf("CreateProjectRobotAccount() = %+v, want %+v", robot, wantRobot)
		}
	})

	// Unlike CreateHarborProject, this call is not 409-tolerant: it uses post(),
	// which accepts only 201/200. A robot left behind by a Retain-policy delete
	// therefore blocks re-provisioning under the same name.
	t.Run("duplicate robot name errors, unlike project creation", func(t *testing.T) {
		cli, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
		})
		_, err := cli.CreateProjectRobotAccount(context.Background(), "acme-project", "ci-robot")
		if err == nil {
			t.Fatal("CreateProjectRobotAccount() with a 409 response returned nil error; " +
				"if 409-tolerance was added, update this test to expect success")
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
