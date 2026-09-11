import React from 'react';
import { render, screen } from '@testing-library/react';
import { Timeline } from './Timeline';

describe('Timeline', () => {
  test('separates matched events by namespace', () => {
    render(
      <Timeline
        timeline={{
          window: { from: '2026-08-21T10:00:00Z', to: '2026-08-21T11:00:00Z' },
          events: [
            {
              namespace: 'cattle-system',
              patternId: 'webhook-failure',
              category: 'connectivity',
              severity: 'high',
              count: 2,
              firstSeen: '2026-08-21T10:01:00Z',
              lastSeen: '2026-08-21T10:02:00Z',
              sample: 'webhook connection refused',
            },
            {
              namespace: 'longhorn-system',
              patternId: 'volume-failure',
              category: 'storage',
              severity: 'medium',
              count: 1,
              firstSeen: '2026-08-21T10:03:00Z',
              lastSeen: '2026-08-21T10:03:00Z',
              sample: 'volume detached',
            },
          ],
        }}
      />
    );

    expect(screen.getByText('Namespace: cattle-system')).toBeInTheDocument();
    expect(screen.getByText('Namespace: longhorn-system')).toBeInTheDocument();
  });
});