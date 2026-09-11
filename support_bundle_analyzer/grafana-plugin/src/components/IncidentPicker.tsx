import React, { useState } from 'react';
import { Button, Stack, TimeRangePicker } from '@grafana/ui';
import { dateTime, TimeRange as GrafanaTimeRange } from '@grafana/data';
import type {TimeRange } from '../types/rca';

interface Props {
  lokiDatasourceUid: string;
  onConfirm: (range: TimeRange) => void;
}

export function IncidentPicker({ lokiDatasourceUid, onConfirm }: Props) {
  const [range, setRange] = useState<GrafanaTimeRange>(() => {
    const to = dateTime();
    const from = dateTime().subtract(6, 'h');
    return { from, to, raw: { from: 'now-6h', to: 'now' } };
  });

  return (
    <Stack direction="column" gap={2}>
      <Stack direction="row" gap={2} alignItems="center" justifyContent="flex-end">
        <TimeRangePicker
          value={range}
          onChange={setRange}
          onChangeTimeZone={() => {}}
          onMoveBackward={() => {}}
          onMoveForward={() => {}}
          onZoom={() => {}}
        />
        <Button
          onClick={() => onConfirm({ from: range.from.toISOString(), to: range.to.toISOString() })}
        >
          Use selected range
        </Button>
      </Stack>

    
    </Stack>
  );
}
