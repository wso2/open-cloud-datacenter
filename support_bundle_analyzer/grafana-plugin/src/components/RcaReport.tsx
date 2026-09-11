import React, { useEffect, useRef, useState, useMemo } from 'react';
import { Alert, Button, Spinner, Stack } from '@grafana/ui';
import { renderMarkdown } from '@grafana/data';
import { streamReport } from '../api/backend';
import type { Timeline } from '../types/rca';

export function RcaReport({ timeline }: { timeline: Timeline }) {
  const [text, setText] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [hasRun, setHasRun] = useState(false);
  const abortRef = useRef<AbortController | null>(null);

  const run = () => {
    setText('');
    setError(null);
    setLoading(true);
    setHasRun(true); // Mark that a report has been requested

    const controller = new AbortController();
    abortRef.current = controller;

    streamReport(timeline, (delta) => setText((prev) => prev + delta), controller.signal)
      .catch((e) => setError(String(e?.message ?? e)))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    return () => abortRef.current?.abort();
  }, [timeline]);

  const htmlContent = useMemo(() => {
    return renderMarkdown(text);
  }, [text]);

  const downloadReport = () => {
    const blob = new Blob([text], { type: 'text/markdown;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = 'rca-report.md';
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
    URL.revokeObjectURL(url);
  };

  return (
    <Stack direction="column" gap={2}>
      <Stack direction="row" gap={1} alignItems="center">
        <Button size="sm" variant={hasRun ? 'secondary' : 'primary'} onClick={run} disabled={loading}>
          {hasRun ? 'Regenerate' : 'Generate'}
        </Button>
        {loading && <Spinner size={16} />}
      </Stack>
      {error && (
        <Alert title="RCA generation failed" severity="error">
          {error}
        </Alert>
      )}

      {hasRun && (
        <>
          <Stack direction="row" justifyContent="flex-end">
            <Button size="sm" variant="secondary" onClick={downloadReport} disabled={!text}>
              Download report
            </Button>
          </Stack>
          <div
            className="markdown-html"
            style={{ fontFamily: 'var(--font-family)', lineHeight: 1.5, padding: '0 1rem' }}
            dangerouslySetInnerHTML={{ __html: htmlContent }}
          />
        </>
      )}
    </Stack>
  );
}
