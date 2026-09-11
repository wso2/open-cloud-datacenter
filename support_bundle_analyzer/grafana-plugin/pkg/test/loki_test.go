package main_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wso2/rca/pkg/plugin"
)

func TestFetchLogsBuildsLabelQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != `{k8s_namespace="cattle-\"system"}` {
			t.Errorf("unexpected LogQL query: %s", r.URL.Query().Get("query"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"result":[]}}`))
	}))
	defer server.Close()
	t.Setenv("GF_APP_URL", server.URL)

	client := plugin.NewLokiClient()
	_, err := client.FetchLogs(context.Background(), "loki", "k8s_namespace", `cattle-"system`, time.Unix(0, 0), time.Unix(1, 0))
	if err != nil {
		t.Fatalf("fetch logs: %v", err)
	}

	if _, err := client.FetchLogs(context.Background(), "loki", "namespace=", "cattle-system", time.Unix(0, 0), time.Unix(1, 0)); err == nil {
		t.Fatal("expected invalid label name to fail")
	}
}

func TestQueryLogRangePreservesStreamLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != `{namespace="cattle-system"}` {
			t.Errorf("unexpected LogQL query: %s", r.URL.Query().Get("query"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":[{"stream":{"namespace":"cattle-system","node":"worker-1"},"values":[["1700000000000000000","level=error failed"]]}]}}`))
	}))
	defer server.Close()
	t.Setenv("GF_APP_URL", server.URL)

	lines, err := plugin.NewLokiClient().FetchLogs(context.Background(), "loki", "namespace", "cattle-system", time.Unix(0, 0), time.Unix(1, 0))
	if err != nil {
		t.Fatalf("fetch logs: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one line, got %d", len(lines))
	}
	if lines[0].Labels["namespace"] != "cattle-system" || lines[0].Labels["node"] != "worker-1" {
		t.Fatalf("stream labels were not preserved: %#v", lines[0].Labels)
	}
	if !strings.Contains(lines[0].Line, "failed") {
		t.Fatalf("unexpected log line: %s", lines[0].Line)
	}
}
