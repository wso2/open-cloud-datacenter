package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LokiClient talks to Loki through Grafana's own datasource proxy
// (/api/datasources/proxy/uid/<uid>/loki/...) so it inherits Grafana's
// existing auth/TLS/routing to Loki rather than needing its own Loki
// credentials. It forwards the caller's auth headers (cookie/Authorization)
// from the original resource request.
type LokiClient struct {
	grafanaBaseURL string
	httpClient     *http.Client
	grafanaToken   string
}

func NewLokiClient(tokens ...string) *LokiClient {
	base := os.Getenv("GF_APP_URL")
	if base == "" {
		base = "http://localhost:3000"
	}
	var token string
	if len(tokens) > 0 {
		token = tokens[0]
	}
	token = grafanaTokenForURL(base, token)
	return &LokiClient{
		grafanaBaseURL: base,
		httpClient:     newGrafanaHTTPClient(&http.Transport{Proxy: http.ProxyFromEnvironment}),
		grafanaToken:   token,
	}
}

type LogLine struct {
	Timestamp time.Time
	Line      string
	Labels    map[string]string
}

// FetchLogs pulls raw log lines for one namespace within [from,to]. Callers
// (timeline.go) are expected to match/collapse these against the ruleset
// rather than forwarding raw lines further downstream, to keep volume and
// LLM token usage bounded.
func (c *LokiClient) FetchLogs(ctx context.Context, datasourceUID, namespaceLabel, namespace string, from, to time.Time) ([]LogLine, error) {
	query, err := buildLabelQuery(namespaceLabel, namespace)
	if err != nil {
		return nil, err
	}
	return c.queryLogRange(ctx, datasourceUID, query, from, to)
}

func (c *LokiClient) FetchNodeLogs(ctx context.Context, datasourceUID, nodeLabel string, from, to time.Time) ([]LogLine, error) {
	if nodeLabel == "" {
		return nil, nil
	}
	if !labelNamePattern.MatchString(nodeLabel) {
		return nil, fmt.Errorf("invalid node label %q", nodeLabel)
	}
	query := fmt.Sprintf(`{%s=~".+"}`, nodeLabel)
	return c.queryLogRange(ctx, datasourceUID, query, from, to)
}

var labelNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func buildLabelQuery(label, value string) (string, error) {
	if !labelNamePattern.MatchString(label) {
		return "", fmt.Errorf("invalid namespace label %q", label)
	}
	return fmt.Sprintf("{%s=%s}", label, strconv.Quote(value)), nil
}

// queryLogRange calls Loki's /loki/api/v1/query_range in "streams" mode
// (log query, not metric query) and returns raw lines.
func (c *LokiClient) queryLogRange(ctx context.Context, datasourceUID, query string, from, to time.Time) ([]LogLine, error) {
	var lines []LogLine
	seen := make(map[string]struct{})
	pageStart := from
	for {
		page, err := c.queryLogPage(ctx, datasourceUID, query, pageStart, to)
		if err != nil {
			return nil, err
		}
		newEntries := 0
		for _, line := range page {
			cursor := logLineCursor(line)
			if _, ok := seen[cursor]; ok {
				continue
			}
			seen[cursor] = struct{}{}
			lines = append(lines, line)
			newEntries++
		}
		if len(page) < 5000 {
			break
		}

		last := page[len(page)-1].Timestamp
		if newEntries == 0 {
			return nil, fmt.Errorf("loki query_range (logs) truncated at %s: unable to advance pagination without a usable entry cursor", last.UTC().Format(time.RFC3339Nano))
		}
		if last.Before(pageStart) || !last.Before(to) {
			return nil, fmt.Errorf("loki query_range (logs) truncated at %s: unable to advance pagination", last.UTC().Format(time.RFC3339Nano))
		}
		pageStart = last
	}

	sort.SliceStable(lines, func(i, j int) bool {
		return lines[i].Timestamp.Before(lines[j].Timestamp)
	})
	return lines, nil
}

func logLineCursor(line LogLine) string {
	labels := make([]string, 0, len(line.Labels))
	for key, value := range line.Labels {
		labels = append(labels, key+"="+value)
	}
	sort.Strings(labels)
	return strconv.FormatInt(line.Timestamp.UnixNano(), 10) + "\x00" + strings.Join(labels, "\x00") + "\x00" + line.Line
}

func (c *LokiClient) queryLogPage(ctx context.Context, datasourceUID, query string, from, to time.Time) ([]LogLine, error) {
	u := fmt.Sprintf(
		"%s/api/datasources/proxy/uid/%s/loki/api/v1/query_range?%s",
		c.grafanaBaseURL, url.PathEscape(datasourceUID),
		url.Values{
			"query":     {query},
			"start":     {strconv.FormatInt(from.UnixNano(), 10)},
			"end":       {strconv.FormatInt(to.UnixNano(), 10)},
			"limit":     {"5000"},
			"direction": {"forward"},
		}.Encode(),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	applyGrafanaAuth(req, c.grafanaToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki query_range (logs) returned %d", resp.StatusCode)
	}

	var parsed lokiStreamsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	var lines []LogLine
	for _, stream := range parsed.Data.Result {
		for _, entry := range stream.Values {
			if len(entry) != 2 {
				continue
			}
			nanos, err := strconv.ParseInt(entry[0], 10, 64)
			if err != nil {
				continue
			}
			lines = append(lines, LogLine{
				Timestamp: time.Unix(0, nanos),
				Line:      entry[1],
				Labels:    stream.Stream,
			})
		}
	}
	sort.SliceStable(lines, func(i, j int) bool {
		return lines[i].Timestamp.Before(lines[j].Timestamp)
	})
	return lines, nil
}

type lokiStreamsResponse struct {
	Data struct {
		Result []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"result"`
	} `json:"data"`
}
