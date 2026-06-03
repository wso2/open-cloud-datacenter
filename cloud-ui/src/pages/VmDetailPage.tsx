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
  Tooltip,
  Toaster,
  makeStyles,
  shorthands,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  ArrowClockwise20Regular,
  Copy20Regular,
  Delete20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import StatusPill from '../components/StatusPill';
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
  twoCol: {
    display: 'grid',
    gridTemplateColumns: '2fr 1fr',
    gap: tokens.spacingHorizontalL,
    alignItems: 'start',
  },
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
  rightStack: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalL },
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
  },
  copyBtn: {
    position: 'absolute',
    top: tokens.spacingVerticalXS,
    right: tokens.spacingHorizontalXS,
  },
  timeline: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalM },
  timelineItem: {
    display: 'grid',
    gridTemplateColumns: 'auto 1fr auto',
    gap: tokens.spacingHorizontalM,
    alignItems: 'start',
  },
  timelineDot: {
    width: '24px',
    height: '24px',
    borderRadius: tokens.borderRadiusCircular,
    backgroundColor: tokens.colorBrandBackground2,
    color: tokens.colorBrandForeground1,
    display: 'grid',
    placeItems: 'center',
    fontSize: tokens.fontSizeBase200,
  },
  timelineTitle: { fontWeight: tokens.fontWeightSemibold },
  timelineDesc: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
  timelineTime: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
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

interface VirtualMachine {
  id: string;
  name: string;
  size?: string;
  status: string;
  tenant_id: string;
  provider_type: string;
  ip_address?: string;
  message?: string;
  created_at: string;
}

function fmtDate(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export default function VmDetailPage() {
  const styles = useStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId, vmId } = useParams<{ tenantId: string; vmId: string }>();
  const { projectId } = useActiveProject();
  const confirmDialog = useConfirmDialog();
  const [tab, setTab] = useState<'overview' | 'activity' | 'configuration' | 'access'>('overview');

  const vmQuery = useQuery({
    queryKey: ['vm', tenantId, vmId],
    enabled: Boolean(tenantId) && Boolean(vmId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/virtual-machines/{id}', {
        params: { path: { tenant_id: tenantId!, project_id: projectId!, id: vmId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as VirtualMachine;
    },
    refetchInterval: (q) => {
      const d = q.state.data as VirtualMachine | undefined;
      return d?.status === 'PENDING' || d?.status === 'DELETING' ? 5_000 : false;
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE('/v1/tenants/{tenant_id}/projects/{project_id}/virtual-machines/{id}', {
        params: { path: { tenant_id: tenantId!, project_id: projectId!, id: vmId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Delete requested — VM will transition to DELETING</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['vms', tenantId, projectId] });
      navigate(`/tenants/${tenantId}/projects/${projectId}/vms`);
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const onDelete = async () => {
    const ok = await confirmDialog({
      title: `Delete VM "${vmQuery.data?.name}"?`,
      body: 'The VM will be stopped and all allocated resources released. This cannot be undone.',
      confirmLabel: 'Delete',
      destructive: true,
    });
    if (!ok) return;
    deleteMutation.mutate();
  };

  const onCopy = (text: string) => {
    void navigator.clipboard.writeText(text);
    dispatchToast(<Toast><ToastTitle>Copied to clipboard</ToastTitle></Toast>, { intent: 'success' });
  };

  if (!vmId) return <div className={styles.root}>No VM ID in URL</div>;

  if (vmQuery.isLoading) {
    return (
      <div className={styles.root}>
        <Card><div style={{ padding: tokens.spacingHorizontalXXL, textAlign: 'center' }}>
          <Spinner label="Loading VM…" />
        </div></Card>
      </div>
    );
  }

  if (vmQuery.isError) {
    const msg = (vmQuery.error as Error).message;
    return (
      <div className={styles.root}>
        <Card>
          <div className={styles.notFound}>
            <Subtitle1>VM not found</Subtitle1>
            <Body1 style={{ color: tokens.colorNeutralForeground3, marginTop: tokens.spacingVerticalS }}>
              {msg.includes('404') ? 'No VM with this ID exists in this tenant.' : msg}
            </Body1>
            <Button
              appearance="primary"
              style={{ marginTop: tokens.spacingVerticalL }}
              onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/vms`)}
            >
              Back to VMs
            </Button>
          </div>
        </Card>
      </div>
    );
  }

  const vm = vmQuery.data!;
  const sshCommand = vm.ip_address ? `ssh -i ${vm.name}.pem ubuntu@${vm.ip_address}` : null;

  const yamlSpec = `apiVersion: dc.wso2.com/v1
kind: VirtualMachine
metadata:
  name: ${vm.name}
  tenant: ${vm.tenant_id}
spec:
  size: ${vm.size ?? 'unknown'}
status:
  phase: ${vm.status}
  ip: ${vm.ip_address ?? '<pending>'}
  message: ${vm.message ?? ''}`;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <Breadcrumb>
        <BreadcrumbItem>
          <BreadcrumbButton onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/vms`)}>
            Virtual machines
          </BreadcrumbButton>
        </BreadcrumbItem>
        <BreadcrumbDivider />
        <BreadcrumbItem>
          <BreadcrumbButton current>{vm.name}</BreadcrumbButton>
        </BreadcrumbItem>
      </Breadcrumb>

      <div className={styles.header}>
        <div className={styles.titleRow}>
          <Title2>{vm.name}</Title2>
          <StatusPill status={vm.status} />
        </div>
        <div className={styles.subRow}>
          <span className={styles.mono}>{vm.id}</span>
          {' · created '}
          {fmtDate(vm.created_at)}
        </div>
      </div>

      <div className={styles.cmdBar}>
        <Tooltip content="Refetch VM" relationship="label">
          <Button
            appearance="subtle"
            icon={<ArrowClockwise20Regular />}
            onClick={() => vmQuery.refetch()}
            disabled={vmQuery.isFetching}
          >
            Refresh
          </Button>
        </Tooltip>
        <div className={styles.cmdSpacer} />
        <Button
          appearance="subtle"
          icon={<Delete20Regular />}
          onClick={onDelete}
          disabled={deleteMutation.isPending || vm.status === 'DELETING'}
          style={{ color: tokens.colorPaletteRedForeground1 }}
        >
          {vm.status === 'DELETING' ? 'Deleting…' : 'Delete'}
        </Button>
      </div>

      <TabList selectedValue={tab} onTabSelect={(_, d) => setTab(d.value as typeof tab)}>
        <Tab value="overview">Overview</Tab>
        <Tab value="activity">Activity</Tab>
        <Tab value="configuration">Configuration</Tab>
        <Tab value="access">Access control</Tab>
      </TabList>

      {tab === 'overview' && (
        <div className={styles.twoCol}>
          <Card className={styles.card}>
            <div className={styles.cardHeader}>General</div>
            <dl className={styles.kvList}>
              <dt className={styles.kvKey}>Resource ID</dt>
              <dd className={`${styles.kvValue} ${styles.mono}`}>{vm.id}</dd>
              <dt className={styles.kvKey}>Tenant</dt>
              <dd className={styles.kvValue}>{vm.tenant_id}</dd>
              <dt className={styles.kvKey}>Status</dt>
              <dd className={styles.kvValue}><StatusPill status={vm.status} /></dd>
              <dt className={styles.kvKey}>Size</dt>
              <dd className={styles.kvValue}>{vm.size ?? '—'}</dd>
              <dt className={styles.kvKey}>IP address</dt>
              <dd className={`${styles.kvValue} ${styles.mono}`}>{vm.ip_address ?? '— (pending)'}</dd>
              <dt className={styles.kvKey}>Created</dt>
              <dd className={styles.kvValue}>{fmtDate(vm.created_at)}</dd>
              {vm.message && (
                <>
                  <dt className={styles.kvKey}>Message</dt>
                  <dd className={styles.kvValue}>{vm.message}</dd>
                </>
              )}
            </dl>
          </Card>

          <div className={styles.rightStack}>
            <Card className={styles.card}>
              <div className={styles.cardHeader}>SSH connection</div>
              {sshCommand ? (
                <>
                  <Body2 style={{ color: tokens.colorNeutralForeground3, display: 'block', marginBottom: tokens.spacingVerticalS }}>
                    Connect using the private key downloaded at creation time.
                  </Body2>
                  <div className={styles.codeBlock}>
                    {sshCommand}
                    <Button
                      className={styles.copyBtn}
                      appearance="subtle"
                      size="small"
                      icon={<Copy20Regular />}
                      onClick={() => onCopy(sshCommand)}
                      aria-label="Copy"
                    />
                  </div>
                </>
              ) : (
                <div className={styles.noteCard}>
                  IP address not yet assigned. Once the VM reaches Active and the
                  qemu-guest-agent reports an IP, the SSH command appears here.
                </div>
              )}
            </Card>
          </div>
        </div>
      )}

      {tab === 'activity' && (
        <Card className={styles.card}>
          <div className={styles.cardHeader}>Audit timeline</div>
          <div className={styles.timeline}>
            <div className={styles.timelineItem}>
              <div className={styles.timelineDot}>•</div>
              <div>
                <div className={styles.timelineTitle}>Provisioning requested</div>
                <div className={styles.timelineDesc}>Size: {vm.size ?? 'unknown'}</div>
              </div>
              <div className={styles.timelineTime}>{fmtDate(vm.created_at)}</div>
            </div>
            <div className={styles.timelineItem}>
              <div className={styles.timelineDot}>•</div>
              <div>
                <div className={styles.timelineTitle}>Current status: {vm.status}</div>
                <div className={styles.timelineDesc}>{vm.message ?? '—'}</div>
              </div>
              <div className={styles.timelineTime}>now</div>
            </div>
          </div>
          <div className={styles.noteCard} style={{ marginTop: tokens.spacingVerticalL }}>
            Full audit timeline (every state transition, who triggered it, when)
            arrives once dc-api exposes <code>GET /v1/audit-events</code>. Tracked in
            FOLLOWUPS.md.
          </div>
        </Card>
      )}

      {tab === 'configuration' && (
        <Card className={styles.card}>
          <div className={styles.cardHeader}>Spec (read-only)</div>
          <div className={styles.codeBlock}>
            {yamlSpec}
            <Button
              className={styles.copyBtn}
              appearance="subtle"
              size="small"
              icon={<Copy20Regular />}
              onClick={() => onCopy(yamlSpec)}
              aria-label="Copy"
            />
          </div>
        </Card>
      )}

      {tab === 'access' && (
        <MembersPage
          resourceBase={`/v1/tenants/${tenantId}/projects/${projectId}/virtual-machines/${vmId}`}
          scopeLabel={vm.name}
        />
      )}
    </div>
  );
}
