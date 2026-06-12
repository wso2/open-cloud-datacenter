import {
  Body1,
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
  Delete20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { detailErrorMessage } from '../lib/apiError';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import PeeringsTab from '../components/PeeringsTab';
import StatusPill from '../components/StatusPill';
import SubnetsTab from '../components/SubnetsTab';

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
  noteCard: {
    padding: tokens.spacingHorizontalL,
    backgroundColor: tokens.colorNeutralBackground2,
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
    border: `1px dashed ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
  },
  notFound: { padding: tokens.spacingHorizontalXXXL, textAlign: 'center' },
});

interface VNet {
  id: string;
  tenant_id: string;
  name: string;
  region: string;
  address_space: string[];
  description?: string;
  status: string;
  provider_type: string;
  message?: string;
  created_at: string;
  updated_at: string;
}

function fmtDate(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export default function VNetDetailPage() {
  const styles = useStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId, vnetId } = useParams<{ tenantId: string; vnetId: string }>();
  const { projectId } = useActiveProject();
  const confirmDialog = useConfirmDialog();
  const [tab, setTab] = useState<'overview' | 'subnets' | 'peerings' | 'route-tables'>(
    'overview'
  );

  const vnetQuery = useQuery({
    queryKey: ['vnet', tenantId, vnetId],
    enabled: Boolean(tenantId) && Boolean(vnetId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}', {
        params: { path: { tenant_id: tenantId!, project_id: projectId!, vnet_id: vnetId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as VNet;
    },
    refetchInterval: (q) => {
      const d = q.state.data as VNet | undefined;
      return d?.status === 'PENDING' || d?.status === 'DELETING' ? 5_000 : false;
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE('/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}', {
        params: { path: { tenant_id: tenantId!, project_id: projectId!, vnet_id: vnetId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>VNet delete requested</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['vnets'] });
      navigate(`/tenants/${tenantId}/projects/${projectId}/vnets`);
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const onDelete = async () => {
    const ok = await confirmDialog({
      title: `Delete VNet "${vnetQuery.data?.name}"?`,
      body: 'All subnets, peerings, and associated route tables must be removed before this can complete. If any remain, the delete will be rejected.',
      confirmLabel: 'Delete',
      destructive: true,
      typeToConfirm: vnetQuery.data?.name,
    });
    if (!ok) return;
    deleteMutation.mutate();
  };

  if (!vnetId) return <div className={styles.root}>No VNet ID in URL</div>;

  if (vnetQuery.isLoading) {
    return (
      <div className={styles.root}>
        <Card>
          <div style={{ padding: tokens.spacingHorizontalXXL, textAlign: 'center' }}>
            <Spinner label="Loading VNet…" />
          </div>
        </Card>
      </div>
    );
  }

  if (vnetQuery.isError) {
    return (
      <div className={styles.root}>
        <Card>
          <div className={styles.notFound}>
            <Subtitle1>VNet not found</Subtitle1>
            <Body1
              style={{ color: tokens.colorNeutralForeground3, marginTop: tokens.spacingVerticalS }}
            >
              {detailErrorMessage(vnetQuery.error, 'VNet')}
            </Body1>
            <Button
              appearance="primary"
              style={{ marginTop: tokens.spacingVerticalL }}
              onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/vnets`)}
            >
              Back to VNets
            </Button>
          </div>
        </Card>
      </div>
    );
  }

  const v = vnetQuery.data!;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <Breadcrumb>
        <BreadcrumbItem>
          <BreadcrumbButton onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/vnets`)}>
            VNets
          </BreadcrumbButton>
        </BreadcrumbItem>
        <BreadcrumbDivider />
        <BreadcrumbItem>
          <BreadcrumbButton current>{v.name}</BreadcrumbButton>
        </BreadcrumbItem>
      </Breadcrumb>

      <div className={styles.header}>
        <div className={styles.titleRow}>
          <Title2>{v.name}</Title2>
          <StatusPill status={v.status} />
        </div>
        <div className={styles.subRow}>
          <span className={styles.mono}>{v.id}</span>
          {' · created '}
          {fmtDate(v.created_at)}
        </div>
      </div>

      <div className={styles.cmdBar}>
        <Tooltip content="Refetch VNet" relationship="label">
          <Button
            appearance="subtle"
            icon={<ArrowClockwise20Regular />}
            onClick={() => vnetQuery.refetch()}
            disabled={vnetQuery.isFetching}
          >
            Refresh
          </Button>
        </Tooltip>
        <div className={styles.cmdSpacer} />
        <Button
          appearance="subtle"
          icon={<Delete20Regular />}
          onClick={onDelete}
          disabled={deleteMutation.isPending || v.status === 'DELETING'}
          style={{ color: tokens.colorPaletteRedForeground1 }}
        >
          {v.status === 'DELETING' ? 'Deleting…' : 'Delete'}
        </Button>
      </div>

      <TabList selectedValue={tab} onTabSelect={(_, d) => setTab(d.value as typeof tab)}>
        <Tab value="overview">Overview</Tab>
        <Tab value="subnets">Subnets</Tab>
        <Tab value="peerings">Peerings</Tab>
        <Tab value="route-tables">Route tables</Tab>
      </TabList>

      {tab === 'overview' && (
        <Card className={styles.card}>
          <div className={styles.cardHeader}>General</div>
          <dl className={styles.kvList}>
            <dt className={styles.kvKey}>Resource ID</dt>
            <dd className={`${styles.kvValue} ${styles.mono}`}>{v.id}</dd>
            <dt className={styles.kvKey}>Tenant</dt>
            <dd className={styles.kvValue}>{v.tenant_id}</dd>
            <dt className={styles.kvKey}>Status</dt>
            <dd className={styles.kvValue}><StatusPill status={v.status} /></dd>
            <dt className={styles.kvKey}>Region</dt>
            <dd className={styles.kvValue}>{v.region}</dd>
            <dt className={styles.kvKey}>Address space</dt>
            <dd className={`${styles.kvValue} ${styles.mono}`}>
              {v.address_space.map((c, i) => (
                <span key={i} style={{ display: 'block' }}>{c}</span>
              ))}
            </dd>
            {v.description && (
              <>
                <dt className={styles.kvKey}>Description</dt>
                <dd className={styles.kvValue}>{v.description}</dd>
              </>
            )}
            <dt className={styles.kvKey}>Created</dt>
            <dd className={styles.kvValue}>{fmtDate(v.created_at)}</dd>
            {v.message && (
              <>
                <dt className={styles.kvKey}>Message</dt>
                <dd className={styles.kvValue}>{v.message}</dd>
              </>
            )}
          </dl>
        </Card>
      )}

      {tab === 'subnets' && (
        <Card className={styles.card}>
          <div className={styles.cardHeader}>Subnets</div>
          <SubnetsTab vnetId={v.id} vnetAddressSpace={v.address_space} />
        </Card>
      )}

      {tab === 'peerings' && (
        <Card className={styles.card}>
          <div className={styles.cardHeader}>Peerings</div>
          <PeeringsTab vnetId={v.id} tenantId={tenantId!} projectId={projectId!} />
        </Card>
      )}

      {tab === 'route-tables' && (
        <Card className={styles.card}>
          <div className={styles.cardHeader}>Route tables</div>
          <div className={styles.noteCard}>
            Route tables become available once the platform exposes a managed
            VPN gateway / bastion to route traffic through (tracked as F10 in
            FOLLOWUPS.md). Today, all subnets in a VNet share the default
            VPC-wide routing — there&apos;s nothing meaningful to route to yet.
          </div>
        </Card>
      )}
    </div>
  );
}
