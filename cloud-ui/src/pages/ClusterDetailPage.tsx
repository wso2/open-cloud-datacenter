import {
  Body1,
  Body2,
  Breadcrumb,
  BreadcrumbButton,
  BreadcrumbDivider,
  BreadcrumbItem,
  Button,
  Card,
  Spinner,
  Subtitle1,
  Tab,
  TabList,
  Title2,
  Toast,
  ToastTitle,
  Toaster,
  Tooltip,
  makeStyles,
  shorthands,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  ArrowClockwise20Regular,
  ArrowDownload20Regular,
  Copy20Regular,
  Delete20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import NodePoolsTab from '../components/NodePoolsTab';
import StatusPill from '../components/StatusPill';
import { fmtDate } from '../lib/date';
import { detailErrorMessage } from '../lib/apiError';
import MembersPage from './MembersPage';

const useStyles = makeStyles({
  root: {
    padding: tokens.spacingHorizontalXXL,
    maxWidth: '1400px',
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  header: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalS },
  titleRow: { display: 'flex', alignItems: 'center', gap: tokens.spacingHorizontalM },
  subRow: { color: tokens.colorNeutralForeground3 },
  cmdBar: {
    display: 'flex',
    gap: tokens.spacingHorizontalS,
    paddingTop: tokens.spacingVerticalS,
    paddingBottom: tokens.spacingVerticalS,
    ...shorthands.borderTop('1px', 'solid', tokens.colorNeutralStroke2),
    ...shorthands.borderBottom('1px', 'solid', tokens.colorNeutralStroke2),
  },
  cmdSpacer: { flex: 1 },
  card: { padding: tokens.spacingHorizontalXXL },
  cardHeader: {
    fontWeight: tokens.fontWeightSemibold,
    marginBottom: tokens.spacingVerticalM,
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  kvList: {
    display: 'grid',
    gridTemplateColumns: '180px 1fr',
    rowGap: tokens.spacingVerticalS,
    columnGap: tokens.spacingHorizontalL,
    margin: 0,
  },
  kvKey: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
  kvValue: { fontSize: tokens.fontSizeBase200 },
  mono: { fontFamily: tokens.fontFamilyMonospace, fontSize: tokens.fontSizeBase200 },
  codeBlock: {
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase200,
    backgroundColor: tokens.colorNeutralBackground2,
    border: `1px solid ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    padding: tokens.spacingHorizontalM,
    whiteSpace: 'pre-wrap',
    wordBreak: 'break-all',
    position: 'relative',
    maxHeight: '500px',
    overflowY: 'auto',
  },
  copyBtn: {
    position: 'absolute',
    top: tokens.spacingVerticalXS,
    right: tokens.spacingHorizontalXS,
  },
  noteCard: {
    padding: tokens.spacingHorizontalL,
    backgroundColor: tokens.colorNeutralBackground2,
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
    border: `1px dashed ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
  },
  notFound: { padding: tokens.spacingHorizontalXXXL, textAlign: 'center' },
  poolSummary: {
    display: 'flex',
    gap: tokens.spacingHorizontalL,
    flexWrap: 'wrap',
  },
  poolSummaryItem: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXS,
  },
  poolSummaryLabel: {
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    textTransform: 'uppercase',
    letterSpacing: '0.05em',
  },
  poolSummaryValue: {
    fontWeight: tokens.fontWeightSemibold,
    fontSize: tokens.fontSizeBase300,
  },
});

interface NodePool {
  name: string;
  role: 'system' | 'worker';
  size: string;
  count: number;
  disk_gb?: number;
  status: string;
}

interface Cluster {
  id: string;
  name: string;
  status: string;
  tenant_id: string;
  provider_type: string;
  message?: string;
  created_at: string;
  system_pool: NodePool;
  worker_pool_count: number;
  total_node_count: number;
}

export default function ClusterDetailPage() {
  const styles = useStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId, clusterId } = useParams<{ tenantId: string; clusterId: string }>();
  const { projectId } = useActiveProject();
  const confirmDialog = useConfirmDialog();
  const [tab, setTab] = useState<'overview' | 'node-pools' | 'kubeconfig' | 'activity' | 'access'>(
    'overview',
  );

  const clusterQuery = useQuery({
    queryKey: ['cluster', tenantId, projectId, clusterId],
    enabled: Boolean(tenantId) && Boolean(projectId) && Boolean(clusterId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}',
        { params: { path: { tenant_id: tenantId!, project_id: projectId!, id: clusterId! } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as Cluster;
    },
    refetchInterval: (q) => {
      const d = q.state.data as Cluster | undefined;
      return d?.status === 'PENDING' || d?.status === 'DELETING' ? 5_000 : false;
    },
  });

  const kubeconfigQuery = useQuery({
    queryKey: ['cluster-kubeconfig', tenantId, projectId, clusterId],
    enabled:
      Boolean(tenantId) &&
      Boolean(projectId) &&
      Boolean(clusterId) &&
      tab === 'kubeconfig' &&
      clusterQuery.data?.status === 'ACTIVE',
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/kubeconfig',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId!, id: clusterId! } },
          parseAs: 'text',
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as string;
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE(
        '/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}',
        { params: { path: { tenant_id: tenantId!, project_id: projectId!, id: clusterId! } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Delete requested</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['clusters', tenantId, projectId] });
      navigate(`/tenants/${tenantId}/projects/${projectId}/clusters`);
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>,
        { intent: 'error' },
      );
    },
  });

  const onDelete = async () => {
    const ok = await confirmDialog({
      title: `Delete cluster "${clusterQuery.data?.name}"?`,
      body: 'All node pools and worker nodes will be deprovisioned. Kubeconfigs for this cluster will stop working immediately. This cannot be undone.',
      confirmLabel: 'Delete cluster',
      destructive: true,
      typeToConfirm: clusterQuery.data?.name,
    });
    if (!ok) return;
    deleteMutation.mutate();
  };

  const onCopy = (text: string) => {
    void navigator.clipboard.writeText(text);
    dispatchToast(
      <Toast><ToastTitle>Copied to clipboard</ToastTitle></Toast>,
      { intent: 'success' },
    );
  };

  const onDownloadKubeconfig = () => {
    if (!kubeconfigQuery.data) return;
    const blob = new Blob([kubeconfigQuery.data], { type: 'text/yaml' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${clusterQuery.data?.name ?? 'cluster'}.kubeconfig`;
    a.click();
    URL.revokeObjectURL(url);
  };

  if (!clusterId) return <div className={styles.root}>No cluster ID in URL</div>;

  if (clusterQuery.isLoading) {
    return (
      <div className={styles.root}>
        <Card>
          <div style={{ padding: tokens.spacingHorizontalXXL, textAlign: 'center' }}>
            <Spinner label="Loading cluster…" />
          </div>
        </Card>
      </div>
    );
  }

  if (clusterQuery.isError) {
    return (
      <div className={styles.root}>
        <Card>
          <div className={styles.notFound}>
            <Subtitle1>Cluster not found</Subtitle1>
            <Body1
              style={{ color: tokens.colorNeutralForeground3, marginTop: tokens.spacingVerticalS }}
            >
              {detailErrorMessage(clusterQuery.error, 'cluster')}
            </Body1>
            <Button
              appearance="primary"
              style={{ marginTop: tokens.spacingVerticalL }}
              onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/clusters`)}
            >
              Back to clusters
            </Button>
          </div>
        </Card>
      </div>
    );
  }

  const cluster = clusterQuery.data!;
  const sysPool = cluster.system_pool;
  const sysPoolSummary = sysPool
    ? `${sysPool.count} × ${sysPool.size}`
    : '—';

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <Breadcrumb>
        <BreadcrumbItem>
          <BreadcrumbButton
            onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/clusters`)}
          >
            Clusters
          </BreadcrumbButton>
        </BreadcrumbItem>
        <BreadcrumbDivider />
        <BreadcrumbItem>
          <BreadcrumbButton current>{cluster.name}</BreadcrumbButton>
        </BreadcrumbItem>
      </Breadcrumb>

      <div className={styles.header}>
        <div className={styles.titleRow}>
          <Title2>{cluster.name}</Title2>
          <StatusPill status={cluster.status} />
        </div>
        <div className={styles.subRow}>
          <span className={styles.mono}>{cluster.id}</span>
          {' · created '}
          {fmtDate(cluster.created_at)}
        </div>
      </div>

      <div className={styles.cmdBar}>
        <Tooltip content="Refetch cluster" relationship="label">
          <Button
            appearance="subtle"
            icon={<ArrowClockwise20Regular />}
            onClick={() => clusterQuery.refetch()}
            disabled={clusterQuery.isFetching}
          >
            Refresh
          </Button>
        </Tooltip>
        <div className={styles.cmdSpacer} />
        <Button
          appearance="subtle"
          icon={<Delete20Regular />}
          onClick={onDelete}
          disabled={deleteMutation.isPending || cluster.status === 'DELETING'}
          style={{ color: tokens.colorPaletteRedForeground1 }}
        >
          {cluster.status === 'DELETING' ? 'Deleting…' : 'Delete'}
        </Button>
      </div>

      <TabList
        selectedValue={tab}
        onTabSelect={(_, d) => setTab(d.value as typeof tab)}
      >
        <Tab value="overview">Overview</Tab>
        <Tab value="node-pools">Node pools</Tab>
        <Tab value="kubeconfig">Kubeconfig</Tab>
        <Tab value="activity">Activity</Tab>
        <Tab value="access">Access control</Tab>
      </TabList>

      {tab === 'overview' && (
        <Card className={styles.card}>
          <div className={styles.cardHeader}>General</div>
          <dl className={styles.kvList}>
            <dt className={styles.kvKey}>Resource ID</dt>
            <dd className={`${styles.kvValue} ${styles.mono}`}>{cluster.id}</dd>
            <dt className={styles.kvKey}>Tenant</dt>
            <dd className={styles.kvValue}>{cluster.tenant_id}</dd>
            <dt className={styles.kvKey}>Status</dt>
            <dd className={styles.kvValue}>
              <StatusPill status={cluster.status} />
            </dd>
            <dt className={styles.kvKey}>System pool</dt>
            <dd className={styles.kvValue}>{sysPoolSummary}</dd>
            <dt className={styles.kvKey}>Worker pools</dt>
            <dd className={styles.kvValue}>{cluster.worker_pool_count}</dd>
            <dt className={styles.kvKey}>Total nodes</dt>
            <dd className={styles.kvValue}>{cluster.total_node_count}</dd>
            <dt className={styles.kvKey}>Created</dt>
            <dd className={styles.kvValue}>{fmtDate(cluster.created_at)}</dd>
            {cluster.message && (
              <>
                <dt className={styles.kvKey}>Message</dt>
                <dd className={styles.kvValue}>{cluster.message}</dd>
              </>
            )}
          </dl>
          {sysPool && (
            <>
              <div
                className={styles.cardHeader}
                style={{ marginTop: tokens.spacingVerticalXL }}
              >
                System pool
              </div>
              <div className={styles.poolSummary}>
                <div className={styles.poolSummaryItem}>
                  <span className={styles.poolSummaryLabel}>Size</span>
                  <span className={styles.poolSummaryValue}>{sysPool.size}</span>
                </div>
                <div className={styles.poolSummaryItem}>
                  <span className={styles.poolSummaryLabel}>Nodes</span>
                  <span className={styles.poolSummaryValue}>{sysPool.count}</span>
                </div>
                {sysPool.disk_gb && (
                  <div className={styles.poolSummaryItem}>
                    <span className={styles.poolSummaryLabel}>Disk</span>
                    <span className={styles.poolSummaryValue}>{sysPool.disk_gb} GB</span>
                  </div>
                )}
                <div className={styles.poolSummaryItem}>
                  <span className={styles.poolSummaryLabel}>Pool status</span>
                  <span className={styles.poolSummaryValue}>{sysPool.status}</span>
                </div>
              </div>
            </>
          )}
        </Card>
      )}

      {tab === 'node-pools' && (
        <NodePoolsTab clusterId={clusterId} clusterStatus={cluster.status} />
      )}

      {tab === 'kubeconfig' && (
        <Card className={styles.card}>
          <div className={styles.cardHeader}>
            <span>Kubeconfig</span>
            {kubeconfigQuery.data && (
              <div style={{ display: 'flex', gap: tokens.spacingHorizontalS }}>
                <Button
                  appearance="primary"
                  icon={<ArrowDownload20Regular />}
                  onClick={onDownloadKubeconfig}
                >
                  Download
                </Button>
                <Button
                  appearance="subtle"
                  icon={<Copy20Regular />}
                  onClick={() => onCopy(kubeconfigQuery.data!)}
                >
                  Copy
                </Button>
              </div>
            )}
          </div>
          {cluster.status !== 'ACTIVE' ? (
            <div className={styles.noteCard}>
              Kubeconfig is downloadable once the cluster is Active. Currently {cluster.status}.
            </div>
          ) : kubeconfigQuery.isLoading ? (
            <div
              style={{ display: 'flex', justifyContent: 'center', padding: tokens.spacingHorizontalXXL }}
            >
              <Spinner label="Fetching kubeconfig…" />
            </div>
          ) : kubeconfigQuery.isError ? (
            <div
              className={styles.noteCard}
              style={{ color: tokens.colorPaletteRedForeground1 }}
            >
              Failed to fetch kubeconfig: {(kubeconfigQuery.error as Error).message}
            </div>
          ) : (
            <>
              <Body2
                style={{
                  color: tokens.colorNeutralForeground3,
                  display: 'block',
                  marginBottom: tokens.spacingVerticalS,
                }}
              >
                Use this kubeconfig with kubectl to access the cluster.
              </Body2>
              <div className={styles.codeBlock}>{kubeconfigQuery.data}</div>
            </>
          )}
        </Card>
      )}

      {tab === 'activity' && (
        <Card className={styles.card}>
          <div className={styles.cardHeader}>Audit timeline</div>
          <Body1>
            Provisioning requested at <strong>{fmtDate(cluster.created_at)}</strong> · current
            status <StatusPill status={cluster.status} />
          </Body1>
          {cluster.message && (
            <Body2
              style={{
                color: tokens.colorNeutralForeground3,
                display: 'block',
                marginTop: tokens.spacingVerticalS,
              }}
            >
              {cluster.message}
            </Body2>
          )}
          <div className={styles.noteCard} style={{ marginTop: tokens.spacingVerticalL }}>
            Full audit timeline arrives once dc-api exposes <code>GET /v1/audit-events</code>.
          </div>
        </Card>
      )}

      {tab === 'access' && (
        <MembersPage
          resourceBase={`/v1/tenants/${tenantId}/projects/${projectId}/clusters/${clusterId}`}
          scopeLabel={cluster.name}
        />
      )}
    </div>
  );
}
