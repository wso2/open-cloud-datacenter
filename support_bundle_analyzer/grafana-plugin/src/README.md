# Harv-logs

Harv-logs is a Grafana app for root-cause analysis of Harvester support-bundle logs. It combines Loki queries, rule-based log matching, and AI-assisted investigation in one workflow.

## Features

- Select a time range for an incident investigation.
- Correlate recognized events across Harvester namespaces and optional node-level logs.
- Review events in a time-ordered timeline grouped by namespace.
- See event severity, category, pattern, timestamps, occurrence counts, and representative log lines.
- Generate a streaming AI root-cause report with suggested next checks.
- Ask follow-up questions about individual events in an AI chat.
- Configure the Loki datasource UID, namespace label, and optional node label.

## Requirements

- Grafana 12.3.0 or newer.
- A Loki datasource containing Harvester support-bundle logs.
- The `grafana-llm-app` installed and configured for AI completions.
- Loki labels matching the namespace and optional node label settings.

## Getting started

1. Open the **Configuration** page for Harv-logs.
2. Set the Loki datasource UID that contains the support-bundle logs.
3. Confirm the namespace label and, if needed, set the node label used by Loki.
4. Open the **RCA** page and select the incident time window.
5. Review the correlated timeline, generate the AI root-cause report, or ask about a specific event.

The correlation rules are packaged with the plugin in `rules/rca-rules.yaml`. The plugin uses the configured `grafana-llm-app` for report and event-chat responses.

## Log privacy policy

Event-chat prompts use the selected event's `LogLine`, or its `Sample` when no log line is available. Before the prompt is sent to the LLM provider, the default policy redacts values assigned to API keys, tokens, passwords, secrets, and authorization fields, bearer credentials, and credentials embedded in HTTP(S) URLs. The complete raw line is not sent unless the Grafana administrator explicitly sets `RCA_LLM_ALLOW_RAW_LOGS=true` in the plugin environment. Keep this setting unset or false unless the provider and incident-handling policy permit raw support-bundle data.
