export type Severity = 'low' | 'medium' | 'high';

export interface TimeRange {
  from: string; // RFC3339
  to: string; // RFC3339
}

export interface MatchedEvent {
  patternId: string;
  namespace: string;
  node?: string;
  category: string;
  severity: Severity;
  ruleDescription?: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
  sample: string;
  logLine?: string;
}

export interface Timeline {
  window: TimeRange;
  events: MatchedEvent[];
  warnings?: string[];
}

export interface RcaRequest {
  window: TimeRange;
  namespaces?: string[]; // defaults to correlation_order from rca-rules.yaml
  lokiDatasourceUid: string;
  nodeLabel?: string;
}

export interface RcaAnalyzeResponse {
  timeline: Timeline;
  // report text arrives via a separate streaming call (see api/backend.ts streamReport)
}
