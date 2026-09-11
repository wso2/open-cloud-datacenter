import React, { useState } from 'react';
import { Alert, Badge, Button, Card, Stack, Text } from '@grafana/ui';
import type { MatchedEvent, Timeline as TimelineData } from '../types/rca';
import type { ChatMessage } from '../api/backend';
import { LogChat } from './LogChat';

const SEVERITY_COLOR: Record<string, 'red' | 'orange' | 'blue'> = {
  high: 'red',
  medium: 'orange',
  low: 'blue',
};

function eventQuestion(event: MatchedEvent, additional: boolean) {
  return `${additional ? 'Investigate this additional selected event and explain how it may relate to the existing conversation.' : 'Explain what this selected log line means, what may have caused it, and what I should check next.'}

Event context:
- Time: ${event.firstSeen}${event.lastSeen !== event.firstSeen ? ` to ${event.lastSeen}` : ''}
- Namespace: ${event.namespace}
- Node: ${event.node ?? 'Not applicable'}
- Category: ${event.category}
- Severity: ${event.severity}
- Pattern: ${event.patternId}
- Count: ${event.count}
- Rule: ${event.ruleDescription ?? 'Not provided'}
- Log line: ${event.logLine ?? event.sample}

What should I check next?`;
}

function EventRow({ event, onAskAI }: { event: MatchedEvent; onAskAI: (event: MatchedEvent) => void }) {
  return (
    <>
      <Card>
        <Stack direction="row" justifyContent="space-between" alignItems="flex-start" wrap="wrap">
          <div style={{ minWidth: 0, flex: '1 1 20rem' }}>
            <Stack direction="column" gap={0.5}>
              <Stack direction="row" gap={1} alignItems="center">
                <Badge color={SEVERITY_COLOR[event.severity] ?? 'blue'} text={event.severity} />
                <Text weight="medium">Namespace: {event.namespace}</Text>
                {event.node && (
                  <Text color="secondary" variant="bodySmall">
                    {event.node}
                  </Text>
                )}
                <Text color="secondary" variant="bodySmall">
                  {event.category} · {event.patternId}
                </Text>
              </Stack>
              <pre
                style={{ margin: 0, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', color: 'var(--text-secondary)' }}
              >
                {event.sample}
              </pre>
            </Stack>
          </div>
          <div style={{ flexShrink: 0 }}>
            <Stack direction="column" alignItems="flex-end" gap={0.5}>
              <Text variant="bodySmall">
                {event.firstSeen}
                {event.lastSeen !== event.firstSeen ? ` → ${event.lastSeen}` : ''}
              </Text>
              <Text variant="bodySmall" color="secondary">
                ×{event.count}
              </Text>
              <Button size="sm" variant="secondary" icon="comment-alt" onClick={() => onAskAI(event)}>
                Ask AI
              </Button>
            </Stack>
          </div>
        </Stack>
      </Card>
    </>
  );
}

export function Timeline({ timeline }: { timeline: TimelineData }) {
  const [chatEvent, setChatEvent] = useState<MatchedEvent | null>(null);
  const [chatMessages, setChatMessages] = useState<ChatMessage[]>([]);
  const [chatOpen, setChatOpen] = useState(false);
  const openChat = (event: MatchedEvent) => {
    setChatMessages((current) => [...current, { role: 'user', content: eventQuestion(event, current.length > 0) }]);
    setChatEvent(event);
    setChatOpen(true);
  };
  const events = timeline.events ?? [];
  const warnings = timeline.warnings ?? [];
  const sorted = [...events].sort((a, b) => a.firstSeen.localeCompare(b.firstSeen));

  return (
    <Stack direction="column" gap={2}>
      {warnings.length > 0 && (
        <Alert title="Per-namespace query notes" severity="warning">
          <ul style={{ margin: 0, paddingLeft: '1.2em' }}>
            {warnings.map((w, i) => (
              <li key={i}>{w}</li>
            ))}
          </ul>
        </Alert>
      )}
      {sorted.length === 0 ? (
        <Text color="secondary">No pattern matches found in this window across the configured namespaces.</Text>
      ) : (
        sorted.map((event, i) => (
          <EventRow key={`${event.namespace}-${event.patternId}-${i}`} event={event} onAskAI={openChat} />
        ))
      )}
      {chatEvent && (
        <>
          {!chatOpen && (
            <Button icon="comment-alt" variant="primary" onClick={() => setChatOpen(true)}>
              Open AI chat
            </Button>
          )}
          {chatOpen && (
            <LogChat
              event={chatEvent}
              messages={chatMessages}
              onMessagesChange={setChatMessages}
              onDismiss={() => setChatOpen(false)}
            />
          )}
        </>
      )}
    </Stack>
  );
}
