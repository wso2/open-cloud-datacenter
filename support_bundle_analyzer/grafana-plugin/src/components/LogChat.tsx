import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Drawer, Input, Spinner, Stack, Text } from '@grafana/ui';
import { renderMarkdown } from '@grafana/data';
import { streamChat, ChatMessage } from '../api/backend';
import type { MatchedEvent } from '../types/rca';

interface Props {
  event: MatchedEvent;
  messages: ChatMessage[];
  onMessagesChange: (messages: ChatMessage[]) => void;
  onDismiss: () => void;
}

export function LogChat({ event, messages, onMessagesChange, onDismiss }: Props) {
  const [draft, setDraft] = useState('');
  const [loading, setLoading] = useState(messages.length === 0);
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const messagesRef = useRef(messages);

  const ask = (history: ChatMessage[]) => {
    const controller = new AbortController();
    abortRef.current = controller;
    setLoading(true);
    setError(null);
    streamChat(
      event,
      history,
      (delta) => {
        const updateMessages = (current: ChatMessage[]) => {
          const last = current[current.length - 1];
          const next =
            last?.role === 'assistant'
              ? [...current.slice(0, -1), { ...last, content: last.content + delta }]
              : [...current, { role: 'assistant' as const, content: delta }];
          messagesRef.current = next;
          onMessagesChange(next);
        };
        updateMessages(messagesRef.current);
      },
      controller.signal
    )
      .catch((e) => {
        if (controller.signal.aborted || abortRef.current !== controller) {
          return;
        }
        setError(String(e?.message ?? e));
      })
      .finally(() => {
        if (abortRef.current === controller) {
          abortRef.current = null;
          setLoading(false);
        }
      });
  };

  useEffect(() => {
    messagesRef.current = messages;
  }, [messages]);

  useEffect(() => {
    abortRef.current?.abort();
    const lastMessage = messagesRef.current[messagesRef.current.length - 1];
    if (lastMessage?.role === 'user') {
      ask(messagesRef.current);
    }
    return () => abortRef.current?.abort();
  }, [event]);

  const submit = () => {
    const question = draft.trim();
    if (!question || loading) {
      return;
    }
    const nextMessages = [...messagesRef.current, { role: 'user' as const, content: question }];
    messagesRef.current = nextMessages;
    onMessagesChange(nextMessages);
    setDraft('');
    ask(nextMessages);
  };

  return (
    <Drawer title="Ask AI about selected logs" onClose={onDismiss} size="md">
      <div style={{ paddingLeft: '16px' }}>
          <Stack direction="column" gap={2}>
            {error && (
              <Alert title="AI chat failed" severity="error">
                {error}
              </Alert>
            )}
            <Stack direction="column" gap={1}>
              {messages.map((message, index) => (
                <Stack key={`${message.role}-${index}`} direction="column" gap={0.5}>
                  <Text weight="medium">{message.role === 'user' ? 'You' : 'AI'}</Text>
                  {message.role === 'assistant' ? (
                    <div style={{marginLeft: '8px'  }}>
                      <div dangerouslySetInnerHTML={{ __html: renderMarkdown(message.content) }} />
                    </div>
                  ) : (
                    <Text>{message.content}</Text>
                  )}
                </Stack>
              ))}
              {loading && <Spinner size={16} />}
            </Stack>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                submit();
              }}
            >
              <Stack direction="row" gap={1} alignItems="center">
                <div style={{ flexGrow: 1 }}>
                    <Input
                    aria-label="Ask a question about this log"
                    value={draft}
                    onChange={(e) => setDraft(e.currentTarget.value)}
                    placeholder="Ask a follow-up question"
                    disabled={loading}
                    />
                </div>
                <Button type="submit" icon="arrow-up" disabled={loading || !draft.trim()}>
                    Ask
                </Button>
             </Stack>
            </form>
          </Stack>
      </div>
    </Drawer>
  );
}
