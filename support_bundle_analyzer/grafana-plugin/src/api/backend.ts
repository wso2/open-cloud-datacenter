import { getBackendSrv } from '@grafana/runtime';
import type {
  RcaAnalyzeResponse,
  RcaRequest,
  Timeline,
  MatchedEvent,
} from '../types/rca';

const PLUGIN_ID = 'harv-logs-app';
const RESOURCE_BASE = `/api/plugins/${PLUGIN_ID}/resources`;


export async function analyze(req: RcaRequest): Promise<RcaAnalyzeResponse> {
  return getBackendSrv().post(`${RESOURCE_BASE}/rca/analyze`, req);
}

/**
 * Streams the RCA narrative for an already-built timeline.
 * The backend proxies to grafana-llm-app's streaming completion resource and
 * relays chunks back as newline-delimited JSON: {"delta": "..."} ... {"done": true}.
 *
 * We use fetch + ReadableStream directly here (rather than getBackendSrv)
 * because we need incremental chunks, not a single resolved promise.
 */
export async function streamReport(
  timeline: Timeline,
  onDelta: (text: string) => void,
  signal?: AbortSignal
): Promise<void> {
  const resp = await fetch(`${RESOURCE_BASE}/rca/report/stream`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ timeline }),
    signal,
  });

  if (!resp.ok || !resp.body) {
    throw new Error(`RCA report stream failed: ${resp.status} ${resp.statusText}`);
  }

  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let receivedDone = false;

  // eslint-disable-next-line no-constant-condition
  while (!receivedDone) {
    const { done, value } = await reader.read();
    if (done) {
      break;
    }
    buffer += decoder.decode(value, { stream: true });

    let newlineIdx: number;
    // eslint-disable-next-line no-cond-assign
    while ((newlineIdx = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, newlineIdx).trim();
      buffer = buffer.slice(newlineIdx + 1);
      if (!line) {
        continue;
      }
      let parsed: { delta?: string; error?: string; done?: boolean };
      try {
        parsed = JSON.parse(line);
      } catch (e) {
        // ignore partial/malformed lines; SSE-style framing can split mid-JSON
        // in rare cases — a production version should buffer more defensively.
        console.warn('failed to parse RCA stream chunk', line, e);
        continue;
      }
      if (parsed.error) {
        throw new Error(parsed.error);
      }
      if (parsed.done === true) {
        receivedDone = true;
      }
      if (parsed.delta) {
        onDelta(parsed.delta);
      }
    }
  }

  if (!receivedDone) {
    throw new Error('RCA report stream ended before completion');
  }
}

export interface ChatMessage {
  role: 'user' | 'assistant';
  content: string;
}

export async function streamChat(
  event: MatchedEvent,
  messages: ChatMessage[],
  onDelta: (text: string) => void,
  signal?: AbortSignal
): Promise<void> {
  const resp = await fetch(`${RESOURCE_BASE}/rca/chat/stream`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ event, messages }),
    signal,
  });
  if (!resp.ok || !resp.body) {
    throw new Error(`AI chat stream failed: ${resp.status} ${resp.statusText}`);
  }
  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let receivedDone = false;
  while (!receivedDone) {
    const { done, value } = await reader.read();
    if (done) {
      break;
    }
    buffer += decoder.decode(value, { stream: true });
    let newlineIdx: number;
    // eslint-disable-next-line no-cond-assign
    while ((newlineIdx = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, newlineIdx).trim();
      buffer = buffer.slice(newlineIdx + 1);
      if (!line) {
        continue;
      }
      const parsed: { delta?: string; error?: string; done?: boolean } = JSON.parse(line);
      if (parsed.error) {
        throw new Error(parsed.error);
      }
      if (parsed.done === true) {
        receivedDone = true;
      }
      if (parsed.delta) {
        onDelta(parsed.delta);
      }
    }
  }

  if (!receivedDone) {
    throw new Error('AI chat stream ended before completion');
  }
}
