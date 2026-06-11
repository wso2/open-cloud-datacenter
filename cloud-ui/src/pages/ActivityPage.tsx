import {
  Button,
  Card,
  Subtitle1,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Title2,
  makeStyles,
  tokens,
} from '@fluentui/react-components';
import { ArrowClockwise20Regular, History24Regular } from '@fluentui/react-icons';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { ACTIVITY_RESOURCE_ROUTES, useActivityQuery, type ActivityEvent } from '../api/activity';
import { useActiveProject } from '../hooks/useActiveProject';
import { EmptyState, ErrorState, LoadingState } from '../components/list/PageStates';
import { useListPageStyles } from '../components/list/useListPageStyles';
import { listErrorMessage } from '../lib/apiError';
import { fmtDate } from '../lib/date';

const PAGE_SIZE = 20;

const useStyles = makeStyles({
  actionCreate: {
    color: tokens.colorPaletteGreenForeground1,
    fontWeight: tokens.fontWeightSemibold,
  },
  actionDelete: {
    color: tokens.colorPaletteRedForeground1,
    fontWeight: tokens.fontWeightSemibold,
  },
  actionStatus: {
    color: tokens.colorPaletteDarkOrangeForeground1,
    fontWeight: tokens.fontWeightSemibold,
  },
  actionNeutral: { fontWeight: tokens.fontWeightSemibold },
  resourceLink: {
    color: tokens.colorBrandForeground1,
    fontWeight: tokens.fontWeightSemibold,
    cursor: 'pointer',
    ':hover': { textDecorationLine: 'underline' },
  },
  pagination: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
  },
  paginationInfo: {
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
    marginLeft: tokens.spacingHorizontalS,
  },
});

/**
 * Colored action label. A top-level component (not an inline render
 * callback) so the styles hook runs at component scope — StatusPill is
 * deliberately NOT reused here: its variants map ResourceStatus values,
 * not activity actions.
 */
function ActionText({ action }: { action: string }) {
  const styles = useStyles();
  const cls =
    action === 'CREATE'
      ? styles.actionCreate
      : action === 'DELETE'
        ? styles.actionDelete
        : action === 'STATUS_CHANGE'
          ? styles.actionStatus
          : styles.actionNeutral;
  return <span className={cls}>{action}</span>;
}

export default function ActivityPage() {
  const styles = useListPageStyles();
  const local = useStyles();
  const navigate = useNavigate();
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const [offset, setOffset] = useState(0);

  const activityQuery = useActivityQuery(tenantId, projectId, PAGE_SIZE, offset);

  const items: ActivityEvent[] = activityQuery.data?.items ?? [];
  const total = activityQuery.data?.total ?? 0;
  const showingFrom = total === 0 ? 0 : offset + 1;
  const showingTo = Math.min(offset + items.length, total);

  return (
    <div className={styles.root}>
      <div className={styles.header}>
        <Title2>Activity</Title2>
        <Subtitle1 className={styles.subtitle}>
          {activityQuery.isLoading
            ? 'Loading…'
            : `${total} event${total === 1 ? '' : 's'} in this project`}
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => activityQuery.refetch()}
          disabled={activityQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {activityQuery.isLoading && <LoadingState label="Loading activity…" />}

      {activityQuery.isError && !activityQuery.isLoading && (
        <ErrorState message={listErrorMessage(activityQuery.error, 'activity')} />
      )}

      {!activityQuery.isLoading && !activityQuery.isError && items.length === 0 && (
        <EmptyState
          icon={<History24Regular />}
          title="No activity yet"
          description="Events appear here as resources in this project are created, change status, or are deleted."
        />
      )}

      {!activityQuery.isLoading && !activityQuery.isError && items.length > 0 && (
        <>
          <Card className={styles.tableCard}>
            <Table size="small" aria-label="Project activity">
              <TableHeader>
                <TableRow>
                  <TableHeaderCell>Time</TableHeaderCell>
                  <TableHeaderCell>Action</TableHeaderCell>
                  <TableHeaderCell>Resource</TableHeaderCell>
                  <TableHeaderCell>Actor</TableHeaderCell>
                  <TableHeaderCell>Status change</TableHeaderCell>
                  <TableHeaderCell>Message</TableHeaderCell>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((ev) => {
                  const route = ACTIVITY_RESOURCE_ROUTES[ev.resource_type];
                  return (
                    <TableRow key={ev.id}>
                      <TableCell className={styles.tableMutedCell}>
                        {fmtDate(ev.created_at)}
                      </TableCell>
                      <TableCell>
                        <ActionText action={ev.action} />
                      </TableCell>
                      <TableCell>
                        {route && ev.resource_id ? (
                          <span
                            className={local.resourceLink}
                            onClick={() => navigate(`../${route}/${ev.resource_id}`)}
                          >
                            {ev.resource_name}
                          </span>
                        ) : (
                          <>
                            <span>{ev.resource_name}</span>
                            <div className={styles.tableMutedCell}>{ev.resource_type}</div>
                          </>
                        )}
                      </TableCell>
                      <TableCell className={styles.tableMutedCell}>{ev.actor_id}</TableCell>
                      <TableCell>
                        {ev.from_status && ev.to_status
                          ? `${ev.from_status} → ${ev.to_status}`
                          : '—'}
                      </TableCell>
                      <TableCell className={styles.tableMutedCell}>{ev.message ?? '—'}</TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </Card>

          <div className={local.pagination}>
            <Button
              size="small"
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
            >
              Previous
            </Button>
            <Button
              size="small"
              disabled={offset + PAGE_SIZE >= total}
              onClick={() => setOffset(offset + PAGE_SIZE)}
            >
              Next
            </Button>
            <span className={local.paginationInfo}>
              Showing {showingFrom}–{showingTo} of {total}
            </span>
          </div>
        </>
      )}
    </div>
  );
}
