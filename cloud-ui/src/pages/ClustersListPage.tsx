import {
  Button,
  Card,
  MenuItem,
  Subtitle1,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Title2,
  Toast,
  ToastTitle,
  Toaster,
  makeStyles,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  Add20Regular,
  ArrowClockwise20Regular,
  CloudCube24Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import ClusterCreateDrawer, {
  type ClusterCreateResult,
} from '../components/ClusterCreateDrawer';
import { EmptyState, ErrorState, LoadingState } from '../components/list/PageStates';
import { RowActionsMenu } from '../components/list/RowActionsMenu';
import { useListPageStyles } from '../components/list/useListPageStyles';
import StatusPill from '../components/StatusPill';
import { useCan } from '../api/useCan';
import { PermissionTooltip } from '../components/PermissionTooltip';
import { listErrorMessage } from '../lib/apiError';
import { fmtDate } from '../lib/date';

const usePageStyles = makeStyles({
  notice: {
    backgroundColor: tokens.colorBrandBackground2,
    color: tokens.colorBrandForeground1,
    border: `1px solid ${tokens.colorBrandStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    padding: tokens.spacingHorizontalL,
    fontSize: tokens.fontSizeBase200,
  },
});

interface NodePool {
  name: string;
  role: 'system' | 'worker';
  size: string;
  count: number;
  status: string;
}

interface Cluster {
  id: string;
  name: string;
  status: string;
  message?: string;
  created_at: string;
  system_pool: NodePool;
  worker_pool_count: number;
  total_node_count: number;
}

/** Concise "3 × large" system pool summary for the list view. */
function systemPoolSummary(pool: NodePool | undefined): string {
  if (!pool) return '—';
  return `${pool.count} × ${pool.size}`;
}

/** "2 pools, 7 nodes" or "—" for worker pools. */
function workerSummary(workerPoolCount: number, totalNodeCount: number, sysCount: number): string {
  if (workerPoolCount === 0) return '—';
  const workerNodes = totalNodeCount - sysCount;
  return `${workerPoolCount} pool${workerPoolCount !== 1 ? 's' : ''}, ${workerNodes} node${workerNodes !== 1 ? 's' : ''}`;
}

export default function ClustersListPage() {
  const styles = useListPageStyles();
  const pageStyles = usePageStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const { can } = useCan(tenantId, ['compute/clusters/write'], projectId);
  const canWrite = can('compute/clusters/write');
  const confirmDialog = useConfirmDialog();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [created, setCreated] = useState<ClusterCreateResult | null>(null);

  const deleteMutation = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE(
        '/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId!, id } },
        },
      );
      if (error) {
        const msg =
          typeof error === 'string'
            ? error
            : typeof (error as { error?: unknown }).error === 'string'
              ? (error as { error: string }).error
              : JSON.stringify(error);
        throw new Error(msg);
      }
    },
    onSuccess: () => {
      dispatchToast(
        <Toast>
          <ToastTitle>Delete requested — cluster will transition to DELETING</ToastTitle>
        </Toast>,
        { intent: 'success' },
      );
      queryClient.invalidateQueries({ queryKey: ['clusters', tenantId, projectId] });
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>,
        { intent: 'error' },
      );
    },
  });

  const onDelete = async (c: Cluster) => {
    const ok = await confirmDialog({
      title: `Delete cluster "${c.name}"?`,
      body: 'All node pools and worker nodes will be deprovisioned. Kubeconfigs for this cluster will stop working immediately. This cannot be undone.',
      confirmLabel: 'Delete cluster',
      destructive: true,
      typeToConfirm: c.name,
    });
    if (!ok) return;
    deleteMutation.mutate(c.id);
  };

  const clustersQuery = useQuery({
    queryKey: ['clusters', tenantId, projectId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/clusters',
        { params: { path: { tenant_id: tenantId!, project_id: projectId! } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as Cluster[];
    },
    refetchInterval: (query) => {
      const data = query.state.data as Cluster[] | undefined;
      const transitioning = data?.some(
        (c) => c.status === 'PENDING' || c.status === 'DELETING',
      );
      return transitioning ? 5_000 : false;
    },
  });

  const clusters = clustersQuery.data ?? [];
  const count = clusters.length;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />
      <div className={styles.header}>
        <Title2>Clusters</Title2>
        <Subtitle1 className={styles.subtitle}>
          {clustersQuery.isLoading
            ? 'Loading…'
            : `${count} Kubernetes cluster${count === 1 ? '' : 's'} in this project`}
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create clusters">
          <Button
            appearance="primary"
            icon={<Add20Regular />}
            onClick={() => setDrawerOpen(true)}
            disabledFocusable={!canWrite}
          >
            Create cluster
          </Button>
        </PermissionTooltip>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => clustersQuery.refetch()}
          disabled={clustersQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {created && (
        <div className={pageStyles.notice}>
          Cluster <strong>{created.clusterName}</strong> is provisioning. Once it&apos;s Active,
          add worker pools and download the kubeconfig from the cluster&apos;s detail page.{' '}
          <Button
            appearance="transparent"
            size="small"
            onClick={() =>
              navigate(
                `/tenants/${tenantId}/projects/${projectId}/clusters/${created.clusterId}`,
              )
            }
          >
            View cluster
          </Button>{' '}
          <Button appearance="transparent" size="small" onClick={() => setCreated(null)}>
            Dismiss
          </Button>
        </div>
      )}

      <ClusterCreateDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        onCreated={(r) => setCreated(r)}
      />

      {clustersQuery.isLoading && <LoadingState label="Loading clusters…" />}

      {clustersQuery.isError && !clustersQuery.isLoading && (
        <ErrorState
          message={listErrorMessage(clustersQuery.error, 'clusters')}
        />
      )}

      {!clustersQuery.isLoading && !clustersQuery.isError && count === 0 && (
        <EmptyState
          icon={<CloudCube24Regular />}
          title={`No Kubernetes clusters in ${projectId ?? 'this project'} yet`}
          description="Provision a fully managed RKE2 cluster. Configure the system pool at creation time — worker pools can be added once the cluster is Active."
          action={
            <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create clusters">
              <Button
                appearance="primary"
                icon={<Add20Regular />}
                onClick={() => setDrawerOpen(true)}
                disabledFocusable={!canWrite}
              >
                Create cluster
              </Button>
            </PermissionTooltip>
          }
        />
      )}

      {!clustersQuery.isLoading && !clustersQuery.isError && count > 0 && (
        <Card className={styles.tableCard}>
          <Table size="small" aria-label="Clusters">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>Name</TableHeaderCell>
                <TableHeaderCell>Status</TableHeaderCell>
                <TableHeaderCell>System pool</TableHeaderCell>
                <TableHeaderCell>Workers</TableHeaderCell>
                <TableHeaderCell>Total nodes</TableHeaderCell>
                <TableHeaderCell>Created</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {clusters.map((c) => (
                <TableRow
                  key={c.id}
                  className={styles.rowClickable}
                  onClick={() =>
                    navigate(
                      `/tenants/${tenantId}/projects/${projectId}/clusters/${c.id}`,
                    )
                  }
                >
                  <TableCell>
                    <span className={styles.nameLink}>{c.name}</span>
                    <div className={styles.tableMutedCell}>{c.id}</div>
                  </TableCell>
                  <TableCell>
                    <StatusPill status={c.status} />
                  </TableCell>
                  <TableCell className={styles.tableMutedCell}>
                    {systemPoolSummary(c.system_pool)}
                  </TableCell>
                  <TableCell className={styles.tableMutedCell}>
                    {workerSummary(c.worker_pool_count, c.total_node_count, c.system_pool?.count ?? 0)}
                  </TableCell>
                  <TableCell className={styles.tableMutedCell}>
                    {c.total_node_count}
                  </TableCell>
                  <TableCell className={styles.tableMutedCell}>
                    {fmtDate(c.created_at)}
                  </TableCell>
                  <TableCell>
                    <RowActionsMenu>
                      <MenuItem
                        onClick={() => onDelete(c)}
                        disabled={
                          !canWrite || deleteMutation.isPending || c.status === 'DELETING'
                        }
                      >
                        Delete
                      </MenuItem>
                    </RowActionsMenu>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  );
}
