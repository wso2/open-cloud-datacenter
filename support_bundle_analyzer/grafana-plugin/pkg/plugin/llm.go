package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// LLMClient calls the grafana-llm-app plugin's resource API, which already
// has your Gemini API key configured as its provider. We don't touch the
// key at all — we just POST to the llm-app's chat-completions-style
// resource endpoint and relay the streamed tokens back to our own
// /rca/report/stream caller.
//
// grafana-llm-app exposes (as of the openai-compatible resource API):
//
//	POST /api/plugins/grafana-llm-app/resources/openai/v1/chat/completions
//
// with a standard OpenAI-shaped body (works for its Gemini backend too,
// since llm-app normalizes providers behind that same interface).
type LLMClient struct {
	grafanaBaseURL string
	httpClient     *http.Client
	model          string
	allowRawLogs   bool
	grafanaToken   string
}

var (
	logSecretPattern        = regexp.MustCompile(`(?i)(\b(?:api[_-]?key|token|password|passwd|secret|authorization)\b\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	logBearerPattern        = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	logURLCredentialPattern = regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^@\s]+@`)
)

func NewLLMClient(tokens ...string) *LLMClient {
	base := os.Getenv("GF_APP_URL")
	if base == "" {
		base = "http://localhost:3000"
	}
	model := os.Getenv("RCA_LLM_MODEL")
	if model == "" {
		model = "base"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 120 * time.Second
	var token string
	if len(tokens) > 0 {
		token = tokens[0]
	}
	token = grafanaTokenForURL(base, token)
	return &LLMClient{
		grafanaBaseURL: base,
		httpClient:     newGrafanaHTTPClient(transport),
		model:          model,
		grafanaToken:   token,
		// Raw support-bundle lines are excluded unless an administrator explicitly opts in.
		allowRawLogs: strings.EqualFold(strings.TrimSpace(os.Getenv("RCA_LLM_ALLOW_RAW_LOGS")), "true"),
	}
}

const systemPrompt = `You are an SRE assistant performing root cause analysis on Harvester HCI
clusters (Rancher + Harvester + Longhorn + Kubernetes). You are given a
pre-filtered, deduplicated timeline of pattern matches drawn from the
following namespaces, always investigated in this order because it reflects
how failures typically propagate:

  cattle-system -> cattle-fleet-system -> harvester-system -> longhorn-system -> kube-system

Each event has already been matched against a known failure-pattern taxonomy
(category, severity, id) and collapsed from raw logs, so treat the sample
text as representative, not exhaustive.

Produce your analysis in this structure:

## Root Cause Hypotheses
Ranked list, most likely first. For each: hypothesis, confidence (low/medium/high),
and the specific events (namespace + pattern id + timestamp) that support it.

## Propagation Timeline
A short narrative chain showing how the failure likely propagated across
namespaces/components, citing timestamps.

## Evidence
Bullet list of the key supporting log events, one line each.

## Recommended Remediation
Concrete next steps, ordered by priority.

Be precise about timestamps and don't speculate beyond what the timeline
supports. If the timeline doesn't clearly support a single root cause, say so
and list the top competing hypotheses instead of forcing one answer.`

func (c *LLMClient) StreamRCAReport(ctx context.Context, rules *Ruleset, timeline Timeline, onDelta func(string)) error {
	userPrompt := buildUserPrompt(rules, timeline, c.allowRawLogs)

	body := map[string]interface{}{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"stream": true,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/api/plugins/grafana-llm-app/resources/openai/v1/chat/completions", c.grafanaBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	applyGrafanaAuth(req, c.grafanaToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		if readErr != nil {
			return fmt.Errorf("llm-app returned %d (failed to read response: %w)", resp.StatusCode, readErr)
		}
		if message := strings.TrimSpace(string(responseBody)); message != "" {
			return fmt.Errorf("llm-app returned %d: %s", resp.StatusCode, message)
		}
		return fmt.Errorf("llm-app returned %d", resp.StatusCode)
	}

	// grafana-llm-app streams standard OpenAI-style SSE: lines prefixed
	// "data: {...}", terminated by "data: [DONE]".
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				onDelta(choice.Delta.Content)
			}
		}
	}
	return scanner.Err()
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (c *LLMClient) StreamEventChat(ctx context.Context, event MatchedEvent, messages []ChatMessage, onDelta func(string)) error {
	logLine := event.LogLine
	if logLine == "" {
		logLine = event.Sample
	}
	if !c.allowRawLogs {
		logLine = redactSupportBundleLog(logLine)
	}
	contextMessage := fmt.Sprintf("Selected matched log event:\n- time: %s to %s\n- namespace: %s\n- category: %s\n- severity: %s\n- pattern: %s\n- count: %d\n- rule description: %s\n- log line: %s",
		event.FirstSeen.Format(time.RFC3339), event.LastSeen.Format(time.RFC3339), event.Namespace,
		event.Category, event.Severity, event.PatternID, event.Count, event.RuleDescription, logLine)
	chatMessages := []map[string]string{{"role": "system", "content": systemChatPrompt}, {"role": "user", "content": contextMessage}}
	for _, message := range messages {
		if (message.Role == "user" || message.Role == "assistant") && strings.TrimSpace(message.Content) != "" {
			chatMessages = append(chatMessages, map[string]string{"role": message.Role, "content": message.Content})
		}
	}
	return c.streamMessages(ctx, chatMessages, onDelta)
}

func redactSupportBundleLog(logLine string) string {
	redacted := logSecretPattern.ReplaceAllString(logLine, `${1}[REDACTED]`)
	redacted = logBearerPattern.ReplaceAllString(redacted, "Bearer [REDACTED]")
	return logURLCredentialPattern.ReplaceAllString(redacted, `${1}[REDACTED]@`)
}

const systemChatPrompt = `You are an SRE assistant helping investigate one filtered Harvester HCI log event. Use the event context supplied in the conversation, including its timestamp, namespace, severity, category, pattern, rule description, count, and representative log line. Answer follow-up questions precisely, distinguish evidence from inference, and ask for more context when the selected line is insufficient.`

func (c *LLMClient) streamMessages(ctx context.Context, messages []map[string]string, onDelta func(string)) error {
	body := map[string]interface{}{"model": c.model, "messages": messages, "stream": true}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/plugins/grafana-llm-app/resources/openai/v1/chat/completions", c.grafanaBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	applyGrafanaAuth(req, c.grafanaToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return fmt.Errorf("llm-app returned %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk openAIStreamChunk
		if json.Unmarshal([]byte(data), &chunk) == nil {
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					onDelta(choice.Delta.Content)
				}
			}
		}
	}
	return scanner.Err()
}

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

func buildUserPrompt(rules *Ruleset, timeline Timeline, allowRawLogs bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Incident window: %s to %s\n\n", timeline.Window.From.Format(time.RFC3339), timeline.Window.To.Format(time.RFC3339))
	fmt.Fprintf(&b, "Timeline (%d matched events, chronological):\n", len(timeline.Events))
	for _, e := range timeline.Events {
		sample := e.Sample
		if !allowRawLogs {
			sample = redactSupportBundleLog(sample)
		}
		fmt.Fprintf(&b, "- [%s] pattern=%s category=%s severity=%s count=%d (first=%s last=%s)\n  sample: %s\n",
			e.Namespace, e.PatternID, e.Category, e.Severity, e.Count,
			e.FirstSeen.Format(time.RFC3339), e.LastSeen.Format(time.RFC3339), sample)
	}
	return b.String()
}
