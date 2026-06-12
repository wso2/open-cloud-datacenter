import {
  Body1,
  Button,
  Card,
  MessageBar,
  MessageBarBody,
  MessageBarTitle,
  Spinner,
  Subtitle1,
  Tab,
  TabList,
  Title2,
  Toast,
  ToastTitle,
  Toaster,
  makeStyles,
  shorthands,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  ArrowLeft20Regular,
  Delete20Regular,
  ArrowSync20Regular,
  Key20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import KeyVaultSecretsTab from '../components/KeyVaultSecretsTab';
import SecretRevealBanner, { type Secret } from '../components/SecretRevealBanner';
import StatusPill from '../components/StatusPill';
import { fmtDate } from '../lib/date';
import { detailErrorMessage } from '../lib/apiError';
import MembersPage from './MembersPage';

interface KeyVault {
  id: string;
  name: string;
  status: string;
  soft_delete_days: number;
  mount_path?: string;
  endpoint_address?: string;
  endpoint_port?: number;
  message?: string;
  created_at: string;
  updated_at: string;
}

interface Credentials {
  role_id: string;
  secret_id: string;
  mount_path: string;
  backend_address: string;
  backend_port: string;
}

const useStyles = makeStyles({
  root: {
    padding: tokens.spacingHorizontalXXL,
    maxWidth: '900px',
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  header: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalS },
  backButton: { alignSelf: 'flex-start' },
  cmdBar: {
    display: 'flex',
    gap: tokens.spacingHorizontalS,
    paddingTop: tokens.spacingVerticalS,
    paddingBottom: tokens.spacingVerticalS,
    ...shorthands.borderTop('1px', 'solid', tokens.colorNeutralStroke2),
    ...shorthands.borderBottom('1px', 'solid', tokens.colorNeutralStroke2),
  },
  card: { padding: tokens.spacingHorizontalXXL },
  cardHeader: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalS,
    paddingBottom: tokens.spacingVerticalM,
  },
  grid: {
    display: 'grid',
    gridTemplateColumns: '180px 1fr',
    rowGap: tokens.spacingVerticalM,
    columnGap: tokens.spacingHorizontalL,
  },
  label: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
  value: { fontSize: tokens.fontSizeBase300 },
  mono: {
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase200,
    wordBreak: 'break-all',
  },
  loading: {
    padding: tokens.spacingHorizontalXXL,
    display: 'flex',
    justifyContent: 'center',
  },
  credsAction: {
    display: 'flex',
    gap: tokens.spacingHorizontalM,
    alignItems: 'center',
  },
});

export default function KeyVaultDetailPage() {
  const styles = useStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId, kvId } = useParams<{ tenantId: string; kvId: string }>();
  const { projectId } = useActiveProject();
  const confirmDialog = useConfirmDialog();

  const [tab, setTab] = useState<'overview' | 'secrets' | 'access'>('overview');
  const [revealedSecrets, setRevealedSecrets] = useState<Secret[] | null>(null);
  // Track whether the GET /credentials returned 410 — sets a sticky banner
  // even if the user refreshes the page.
  const [credsAlreadyTakenAt, setCredsAlreadyTakenAt] = useState<string | null>(null);

  const kvQuery = useQuery({
    queryKey: ['keyvault', tenantId, projectId, kvId],
    enabled: Boolean(tenantId) && Boolean(projectId) && Boolean(kvId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId!, id: kvId! } },
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as KeyVault;
    },
    refetchInterval: (q) => {
      const d = q.state.data as KeyVault | undefined;
      return d && (d.status === 'PENDING' || d.status === 'DELETING') ? 5_000 : false;
    },
  });

  const credsMutation = useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}/credentials',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId!, id: kvId! } },
        },
      );
      if (error) {
        // Extract the dc-api timestamp if this was a 410 Gone.
        if (response?.status === 410) {
          const msg =
            typeof error === 'object' && error && 'message' in error
              ? String((error as { message: unknown }).message)
              : String(error);
          const m = msg.match(/retrieved at (\S+)/);
          if (m) setCredsAlreadyTakenAt(m[1]);
          throw new Error(
            'Credentials were already retrieved for this vault and cannot be shown again. Rotate via the operator if you need fresh ones.',
          );
        }
        throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      }
      return data as Credentials;
    },
    onSuccess: (c) => {
      setRevealedSecrets([
        {
          label: 'Role ID',
          value: c.role_id,
          filename: `${kvQuery.data?.name ?? 'keyvault'}-role-id.txt`,
        },
        {
          label: 'Secret ID (sensitive)',
          value: c.secret_id,
          filename: `${kvQuery.data?.name ?? 'keyvault'}-secret-id.txt`,
        },
        { label: 'Vault path', value: c.mount_path },
        {
          label: 'Vault endpoint',
          value: `${c.backend_address}:${c.backend_port}`,
        },
      ]);
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>{e.message}</ToastTitle></Toast>, {
        intent: 'warning',
      });
    },
  });

  const rotateMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST(
        '/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}/credentials/rotate',
        { params: { path: { tenant_id: tenantId!, project_id: projectId!, id: kvId! } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as Credentials;
    },
    onSuccess: (c) => {
      // Same reveal-banner shape as credsMutation onSuccess. We also clear
      // the "already taken" sticky banner — rotation is a fresh shown-once
      // event so the prior 410-state is irrelevant.
      setCredsAlreadyTakenAt(null);
      setRevealedSecrets([
        { label: 'Role ID', value: c.role_id,
          filename: `${kvQuery.data?.name ?? 'keyvault'}-role-id.txt` },
        { label: 'Secret ID (sensitive — NEW)', value: c.secret_id,
          filename: `${kvQuery.data?.name ?? 'keyvault'}-secret-id.txt` },
        { label: 'Vault path', value: c.mount_path },
        { label: 'Vault endpoint', value: `${c.backend_address}:${c.backend_port}` },
      ]);
      dispatchToast(
        <Toast><ToastTitle>Credentials rotated — old credentials are now invalid</ToastTitle></Toast>,
        { intent: 'success' },
      );
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Rotate failed: {e.message}</ToastTitle></Toast>, {
        intent: 'error',
      });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE(
        '/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId!, id: kvId! } },
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(
        <Toast><ToastTitle>Delete requested — vault transitioning to DELETING</ToastTitle></Toast>,
        { intent: 'success' },
      );
      queryClient.invalidateQueries({ queryKey: ['keyvaults', tenantId, projectId] });
      navigate(`/tenants/${tenantId}/projects/${projectId}/keyvaults`);
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>, {
        intent: 'error',
      });
    },
  });

  const onDelete = async () => {
    const ok = await confirmDialog({
      title: `Delete key vault "${kvQuery.data?.name}"?`,
      body: `Soft-delete keeps secrets recoverable for ${kvQuery.data?.soft_delete_days} days. Workloads using this vault's credentials will lose access immediately.`,
      confirmLabel: 'Delete',
      destructive: true,
      typeToConfirm: kvQuery.data?.name,
    });
    if (!ok) return;
    deleteMutation.mutate();
  };

  if (kvQuery.isLoading) {
    return (
      <div className={styles.root}>
        <Toaster toasterId={toasterId} />
        <div className={styles.loading}>
          <Spinner label="Loading key vault…" />
        </div>
      </div>
    );
  }

  if (kvQuery.isError || !kvQuery.data) {
    return (
      <div className={styles.root}>
        <Toaster toasterId={toasterId} />
        <Button
          appearance="subtle"
          icon={<ArrowLeft20Regular />}
          onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/keyvaults`)}
          className={styles.backButton}
        >
          Back to key vaults
        </Button>
        <MessageBar intent="error">
          <MessageBarBody>
            <MessageBarTitle>Failed to load key vault</MessageBarTitle>
            {detailErrorMessage(kvQuery.error, 'key vault')}
          </MessageBarBody>
        </MessageBar>
      </div>
    );
  }

  const kv = kvQuery.data;
  const isDeleting = kv.status === 'DELETING' || deleteMutation.isPending;
  const isActive = kv.status === 'ACTIVE';

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <Button
        appearance="subtle"
        icon={<ArrowLeft20Regular />}
        onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/keyvaults`)}
        className={styles.backButton}
      >
        Back to key vaults
      </Button>

      <div className={styles.header}>
        <Title2>{kv.name}</Title2>
        <Subtitle1 style={{ color: tokens.colorNeutralForeground3 }}>
          <StatusPill status={kv.status} />
          {kv.message && <span style={{ marginLeft: tokens.spacingHorizontalM }}>{kv.message}</span>}
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <Button
          appearance="subtle"
          icon={<Delete20Regular />}
          onClick={onDelete}
          disabled={isDeleting}
        >
          Delete
        </Button>
      </div>

      {kv.status === 'PENDING' && (
        <MessageBar intent="info">
          <MessageBarBody>
            <MessageBarTitle>Provisioning</MessageBarTitle>
            The first vault in a tenant takes ~2–3 minutes to set up; subsequent vaults
            are ready in seconds. This page refreshes every 5s.
          </MessageBarBody>
        </MessageBar>
      )}

      {kv.status === 'FAILED' && (
        <MessageBar intent="error">
          <MessageBarBody>
            <MessageBarTitle>Provisioning failed</MessageBarTitle>
            {kv.message ?? 'The KVI controller reported an error.'} Delete and recreate.
          </MessageBarBody>
        </MessageBar>
      )}

      <TabList selectedValue={tab} onTabSelect={(_, d) => setTab(d.value as typeof tab)}>
        <Tab value="overview">Overview</Tab>
        <Tab value="secrets" disabled={!isActive}>Secrets</Tab>
        <Tab value="access">Access control</Tab>
      </TabList>

      {tab === 'secrets' && <KeyVaultSecretsTab vaultId={kv.id} />}

      {tab === 'access' && (
        <MembersPage
          resourceBase={`/v1/tenants/${tenantId}/projects/${projectId}/keyvaults/${kvId}`}
          scopeLabel={kv.name}
        />
      )}

      {tab === 'overview' && (
      <>
      <Card className={styles.card}>
        <div className={styles.cardHeader}>
          <Subtitle1>Details</Subtitle1>
        </div>
        <div className={styles.grid}>
          <div className={styles.label}>Vault ID</div>
          <div className={`${styles.value} ${styles.mono}`}>{kv.id}</div>
          <div className={styles.label}>Tenant / Project</div>
          <div className={styles.value}>{tenantId} / {projectId}</div>
          <div className={styles.label}>Soft-delete window</div>
          <div className={styles.value}>{kv.soft_delete_days} days</div>
          <div className={styles.label}>Created</div>
          <div className={styles.value}>{fmtDate(kv.created_at)}</div>
        </div>
      </Card>

      <Card className={styles.card}>
        <div className={styles.cardHeader}>
          <Subtitle1>Workload credentials</Subtitle1>
          <Body1 style={{ color: tokens.colorNeutralForeground3 }}>
            Credentials scoped to this vault. Workloads use them to read and write secrets
            at runtime. <strong style={{ color: tokens.colorPaletteDarkOrangeForeground1, fontWeight: tokens.fontWeightBold }}>Shown once</strong> — they are not stored after retrieval.
          </Body1>
        </div>

        {revealedSecrets && (
          <div style={{ marginBottom: tokens.spacingVerticalL }}>
            <SecretRevealBanner
              title={`Credentials for ${kv.name}`}
              description="Save these values now — they will not be shown again. If you lose them, click Rotate to mint a fresh set."
              secrets={revealedSecrets}
              onDismiss={() => setRevealedSecrets(null)}
              onCopy={(label) =>
                dispatchToast(
                  <Toast><ToastTitle>Copied {label}</ToastTitle></Toast>,
                  { intent: 'success' },
                )
              }
            />
          </div>
        )}

        {credsAlreadyTakenAt && !revealedSecrets && (
          <MessageBar intent="warning" style={{ marginBottom: tokens.spacingVerticalM }}>
            <MessageBarBody>
              Credentials were already retrieved on {fmtDate(credsAlreadyTakenAt)}.
              Rotate via the operator if you need fresh ones.
            </MessageBarBody>
          </MessageBar>
        )}

        <div className={styles.credsAction}>
          <Button
            appearance="primary"
            icon={<Key20Regular />}
            onClick={() => credsMutation.mutate()}
            disabled={!isActive || credsMutation.isPending || Boolean(credsAlreadyTakenAt)}
          >
            {credsMutation.isPending ? 'Retrieving…' : 'Retrieve credentials'}
          </Button>
          <Button
            appearance="secondary"
            icon={<ArrowSync20Regular />}
            onClick={async () => {
              const ok = await confirmDialog({
                title: `Rotate credentials for "${kv.name}"?`,
                body: 'Invalidates the current credentials and mints a fresh set. Old credentials stop working IMMEDIATELY — any workload still using them will fail to authenticate. The new credentials are shown ONCE.',
                confirmLabel: 'Rotate',
                destructive: true,
              });
              if (!ok) return;
              rotateMutation.mutate();
            }}
            disabled={!isActive || rotateMutation.isPending}
          >
            {rotateMutation.isPending ? 'Rotating…' : 'Rotate credentials'}
          </Button>
          {!isActive && (
            <Body1 style={{ color: tokens.colorNeutralForeground3 }}>
              Available once the vault is Active.
            </Body1>
          )}
        </div>
      </Card>
      </>
      )}
    </div>
  );
}
