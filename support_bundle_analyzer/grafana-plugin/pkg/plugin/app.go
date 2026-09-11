package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/backend/resource/httpadapter"
)

type App struct {
	backend.CallResourceHandler

	settings AppSettings
	rules    atomic.Pointer[Ruleset]
	loki     *LokiClient
	llm      *LLMClient
}

type AppSettings struct {
	LokiDatasourceUID string `json:"lokiDatasourceUid"`
	NamespaceLabel    string `json:"namespaceLabel"`
	NodeLabel         string `json:"nodeLabel"`
}

// NewApp is called by app.Manage in main.go whenever Grafana needs a new
// instance (e.g. on settings change).
func NewApp(_ context.Context, settings backend.AppInstanceSettings) (instancemgmt.Instance, error) {
	var jsonData AppSettings
	if len(settings.JSONData) > 0 {
		if err := json.Unmarshal(settings.JSONData, &jsonData); err != nil {
			return nil, err
		}
	}

	rules, err := LoadRuleset(rulesFilePath())
	if err != nil {
		log.DefaultLogger.Error("failed to load rca-rules.yaml", "error", err)
		return nil, fmt.Errorf("load RCA ruleset: %w", err)
	}

	a := &App{
		settings: jsonData,
		loki:     NewLokiClient(settings.DecryptedSecureJSONData["grafanaToken"]),
		llm:      NewLLMClient(settings.DecryptedSecureJSONData["grafanaToken"]),
	}
	a.rules.Store(rules)

	mux := http.NewServeMux()
	a.registerRoutes(mux)
	a.CallResourceHandler = httpadapter.New(mux)

	return a, nil
}

func (a *App) Dispose() {}

func (a *App) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/rca/analyze", a.handleAnalyze)
	mux.HandleFunc("/rca/report/stream", a.handleStreamReport)
	mux.HandleFunc("/rca/chat/stream", a.handleStreamChat)
	mux.HandleFunc("/rca/reload-rules", a.handleReloadRules)
}
