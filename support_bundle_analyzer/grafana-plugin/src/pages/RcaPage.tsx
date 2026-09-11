import React, { useState } from 'react';
import { PluginPage } from '@grafana/runtime';
import type { AppRootProps } from '@grafana/data';
import { Alert, Button, Spinner, Stack, Text } from '@grafana/ui';
import { IncidentPicker } from '../components/IncidentPicker';
import { Timeline } from '../components/Timeline';
import { RcaReport } from '../components/RcaReport';
import { analyze } from '../api/backend';
import type { Timeline as TimelineData, TimeRange } from '../types/rca';

type Step = 'pick' | 'analyzing' | 'review';

export function RcaPage({ meta }: AppRootProps) {
  const jsonData = meta.jsonData as { lokiDatasourceUid?: string; nodeLabel?: string } | undefined;
  const lokiDatasourceUid = jsonData?.lokiDatasourceUid ?? '';
  const nodeLabel = jsonData?.nodeLabel ?? '';

  const [step, setStep] = useState<Step>('pick');
  const [window, setWindow] = useState<TimeRange | null>(null);
  const [timeline, setTimeline] = useState<TimelineData | null>(null);
  const [error, setError] = useState<string | null>(null);

  const onConfirmWindow = async (range: TimeRange) => {
    setWindow(range);
    setStep('analyzing');
    setError(null);
    try {
      const resp = await analyze({ window: range, lokiDatasourceUid, nodeLabel });
      setTimeline(resp.timeline);
      setStep('review');
    } catch (e: any) {
      setError(String(e?.message ?? e));
      setStep('pick');
    }
  };

  if (!lokiDatasourceUid) {
    return (
      <PluginPage>
        <Alert title="Not configured" severity="warning">
          Set the Loki datasource UID on the Configuration page before running an RCA.
        </Alert>
      </PluginPage>
    );
  }

  return (
    <PluginPage>
      <Stack direction="column" gap={3}>
        {error && (
          <Alert title="Analysis failed" severity="error">
            {error}
          </Alert>
        )}

        {step === 'pick' && (
          <IncidentPicker lokiDatasourceUid={lokiDatasourceUid} onConfirm={onConfirmWindow} />
        )}

        {step === 'analyzing' && (
          <Stack direction="row" gap={1} alignItems="center">
            <Spinner size={16} />
            <Text>
              Walking cattle-system → cattle-fleet-system → harvester-system → longhorn-system → kube-system for{' '}
              {window?.from} → {window?.to}...
            </Text>
          </Stack>
        )}

        {step === 'review' && timeline && (
          <Stack direction="column" gap={3}>
            <Stack direction="row" justifyContent="space-between" alignItems="center">
              <Text variant="h4">
                Incident window: {timeline.window.from} → {timeline.window.to}
              </Text>
              <Button variant="secondary" onClick={() => setStep('pick')}>
                Change window
              </Button>
            </Stack>

            <Text variant="h5">Cross-namespace timeline</Text>
            <Timeline timeline={timeline} />

            <Text variant="h5">AI root-cause analysis</Text>
            <RcaReport timeline={timeline} />
          </Stack>
        )}
      </Stack>
    </PluginPage>
  );
}
