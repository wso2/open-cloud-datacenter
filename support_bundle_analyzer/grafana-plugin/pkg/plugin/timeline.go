package plugin

import (
	"context"
	"fmt"
	"time"
)

type MatchedEvent struct {
	PatternID       string    `json:"patternId"`
	Namespace       string    `json:"namespace"`
	Node            string    `json:"node,omitempty"`
	Category        string    `json:"category"`
	Severity        Severity  `json:"severity"`
	RuleDescription string    `json:"ruleDescription"`
	Count           int       `json:"count"`
	FirstSeen       time.Time `json:"firstSeen"`
	LastSeen        time.Time `json:"lastSeen"`
	Sample          string    `json:"sample"`
	LogLine         string    `json:"logLine"`
}

type TimeWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type Timeline struct {
	Window   TimeWindow     `json:"window"`
	Events   []MatchedEvent `json:"events"`
	Warnings []string       `json:"warnings"`
}

// BuildTimeline walks `namespaces` in order (expected to be
// rules.CorrelationOrder), fetches logs for each from Loki within
// [from,to], matches them against that namespace's configured patterns,
// collapses repeats of the same pattern into one summarized event, and
// returns everything merged into a single chronological Timeline.
//
// This is the step that keeps LLM input bounded: instead of forwarding
// thousands of raw lines, we forward one row per (namespace, pattern) with
// a count and a representative sample.
func BuildTimeline(
	ctx context.Context,
	loki *LokiClient,
	rules *Ruleset,
	datasourceUID, namespaceLabel, nodeLabel string,
	namespaces []string,
	from, to time.Time,
) (Timeline, error) {
	timeline := Timeline{
		Window:   TimeWindow{From: from, To: to},
		Events:   make([]MatchedEvent, 0),
		Warnings: make([]string, 0),
	}

	for _, ns := range namespaces {
		lines, err := loki.FetchLogs(ctx, datasourceUID, namespaceLabel, ns, from, to)
		if err != nil {
			timeline.Warnings = append(timeline.Warnings, fmt.Sprintf("%s: query failed: %v", ns, err))
			continue
		}
		if len(lines) == 0 {
			timeline.Warnings = append(timeline.Warnings, fmt.Sprintf("%s: query succeeded but returned 0 log lines for this window", ns))
			continue
		}

		collapsed := map[string]*MatchedEvent{}
		matchedCount := 0
		for _, line := range lines {
			for _, pattern := range rules.MatchLines(ns, line.Line) {
				matchedCount++
				ev, ok := collapsed[pattern.ID]
				if !ok {
					ev = &MatchedEvent{
						PatternID:       pattern.ID,
						Namespace:       ns,
						Category:        pattern.Category,
						Severity:        pattern.Severity,
						RuleDescription: pattern.Description,
						FirstSeen:       line.Timestamp,
						LastSeen:        line.Timestamp,
						Sample:          truncate(line.Line, 300),
						LogLine:         line.Line,
					}
					collapsed[pattern.ID] = ev
				}
				ev.Count++
				if line.Timestamp.Before(ev.FirstSeen) {
					ev.FirstSeen = line.Timestamp
				}
				if line.Timestamp.After(ev.LastSeen) {
					ev.LastSeen = line.Timestamp
				}
			}
		}
		if matchedCount == 0 {
			timeline.Warnings = append(timeline.Warnings, fmt.Sprintf("%s: fetched %d log lines but none matched any configured pattern", ns, len(lines)))
		}

		for _, ev := range collapsed {
			timeline.Events = append(timeline.Events, *ev)
		}
	}

	if nodeLabel != "" {
		nodeLines, err := loki.FetchNodeLogs(ctx, datasourceUID, nodeLabel, from, to)
		if err != nil {
			timeline.Warnings = append(timeline.Warnings, fmt.Sprintf("node: query failed: %v", err))
		} else if len(nodeLines) > 0 {
			collapsed := map[string]*MatchedEvent{}
			matchedCount := 0
			for _, line := range nodeLines {
				for _, pattern := range rules.MatchNodeLines(line.Line) {
					matchedCount++
					nodeName := line.Labels[nodeLabel]
					if nodeName == "" {
						nodeName = "unknown"
					}
					key := pattern.ID + "\x00" + nodeName
					ev, ok := collapsed[key]
					if !ok {
						ev = &MatchedEvent{
							PatternID:       pattern.ID,
							Namespace:       "node",
							Node:            nodeName,
							Category:        pattern.Category,
							Severity:        pattern.Severity,
							RuleDescription: pattern.Description,
							FirstSeen:       line.Timestamp,
							LastSeen:        line.Timestamp,
							Sample:          truncate(line.Line, 300),
							LogLine:         line.Line,
						}
						collapsed[key] = ev
					}
					ev.Count++
					if line.Timestamp.Before(ev.FirstSeen) {
						ev.FirstSeen = line.Timestamp
					}
					if line.Timestamp.After(ev.LastSeen) {
						ev.LastSeen = line.Timestamp
					}
				}
			}
			if matchedCount == 0 {
				timeline.Warnings = append(timeline.Warnings, fmt.Sprintf("node: fetched %d log lines but none matched any configured node pattern", len(nodeLines)))
			}
			for _, ev := range collapsed {
				timeline.Events = append(timeline.Events, *ev)
			}
		} else {
			timeline.Warnings = append(timeline.Warnings, "node: query succeeded but returned 0 log lines for this window")
		}
	}

	sortEventsByFirstSeen(timeline.Events)
	return timeline, nil
}

func sortEventsByFirstSeen(events []MatchedEvent) {
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j].FirstSeen.Before(events[j-1].FirstSeen); j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
