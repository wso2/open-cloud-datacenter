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
  Key20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import SecretRevealBanner, { type Secret } from '../components/SecretRevealBanner';
import StatusPill from '../components/StatusPill';
import { fmtDate } from '../lib/date';
import MembersPage from './MembersPage';

interface Database {
  id: string;
  name: string;
  engine: string;
  engine_version?: string;
  instance_class: string;
  allocated_storage_gb: number;
  network_mode: string;
  status: string;
  message?: string;
  endpoint_address?: string;
  endpoint_port?: number;
  created_at: string;
  updated_at: string;
}

interface DatabaseCredentials {
  username: string;
  password: string;
  ca_cert?: string;
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

export default function DatabaseDetailPage() {
  const styles = useStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId, dbId } = useParams<{ tenantId: string; dbId: string }>();
  const { projectId } = useActiveProject();
  const confirmDialog = useConfirmDialog();

  const [tab, setTab] = useState<'overview' | 'access'>('overview');
  const [revealedSecrets, setRevealedSecrets] = useState<Secret[] | null>(null);
  // Tracks whether credentials were already retrieved (410 Gone response).
  const [credsAlreadyTaken, setCredsAlreadyTaken] = useState(false);

  const dbQuery = useQuery({
    queryKey: ['database', tenantId, projectId, dbId],
    enabled: Boolean(tenantId) && Boolean(projectId) && Boolean(dbId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/databases/{id}',
        { params: { path: { tenant_id: tenantId!, project_id: projectId!, id: dbId! } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as Database;
    },
    refetchInterval: (q) => {
      const d = q.state.data as Database | undefined;
      return d && (d.status === 'PENDING' || d.status === 'DELETING') ? 5_000 : false;
    },
  });

  const credsMutation = useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/databases/{id}/credentials',
        { params: { path: { tenant_id: tenantId!, project_id: projectId!, id: dbId! } } },
      );
      if (error) {
        if (response?.status === 410) {
          setCredsAlreadyTaken(true);
          throw new Error(
            'Credentials were already retrieved for this database and cannot be shown again.',
          );
        }
        if (response?.status === 409) {
          throw new Error('Database is not ready yet. Wait until status is Active.');
        }
        throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      }
      return data as DatabaseCredentials;
    },
    onSuccess: (c) => {
      const secrets: Secret[] = [
        {
          label: 'Username',
          value: c.username,
          filename: `${dbQuery.data?.name ?? 'database'}-username.txt`,
        },
        {
          label: 'Password (sensitive)',
          value: c.password,
          filename: `${dbQuery.data?.name ?? 'database'}-password.txt`,
        },
      ];
      if (c.ca_cert) {
        secrets.push({
          label: 'CA Certificate',
          value: c.ca_cert,
          filename: `${dbQuery.data?.name ?? 'database'}-ca.pem`,
          multiline: true,
        });
      }
      setRevealedSecrets(secrets);
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>{e.message}</ToastTitle></Toast>,
        { intent: 'warning' },
      );
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE(
        '/v1/tenants/{tenant_id}/projects/{project_id}/databases/{id}',
        { params: { path: { tenant_id: tenantId!, project_id: projectId!, id: dbId! } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(
        <Toast><ToastTitle>Delete requested — database transitioning to DELETING</ToastTitle></Toast>,
        { intent: 'success' },
      );
      queryClient.invalidateQueries({ queryKey: ['databases', tenantId, projectId] });
      navigate(`/tenants/${tenantId}/projects/${projectId}/databases`);
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
      title: `Delete database "${dbQuery.data?.name}"?`,
      body: 'The VM, data volumes, and credentials will be permanently destroyed. This cannot be undone.',
      confirmLabel: 'Delete',
      destructive: true,
      typeToConfirm: dbQuery.data?.name,
    });
    if (!ok) return;
    deleteMutation.mutate();
  };

  if (dbQuery.isLoading) {
    return (
      <div className={styles.root}>
        <Toaster toasterId={toasterId} />
        <div className={styles.loading}>
          <Spinner label="Loading database…" />
        </div>
      </div>
    );
  }

  if (dbQuery.isError || !dbQuery.data) {
    return (
      <div className={styles.root}>
        <Toaster toasterId={toasterId} />
        <Button
          appearance="subtle"
          icon={<ArrowLeft20Regular />}
          onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/databases`)}
          className={styles.backButton}
        >
          Back to databases
        </Button>
        <MessageBar intent="error">
          <MessageBarBody>
            <MessageBarTitle>Failed to load database</MessageBarTitle>
            {dbQuery.error instanceof Error ? dbQuery.error.message : 'Not found.'}
          </MessageBarBody>
        </MessageBar>
      </div>
    );
  }

  const db = dbQuery.data;
  const isDeleting = db.status === 'DELETING' || deleteMutation.isPending;
  const isActive = db.status === 'ACTIVE';

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <Button
        appearance="subtle"
        icon={<ArrowLeft20Regular />}
        onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/databases`)}
        className={styles.backButton}
      >
        Back to databases
      </Button>

      <div className={styles.header}>
        <Title2>{db.name}</Title2>
        <Subtitle1 style={{ color: tokens.colorNeutralForeground3 }}>
          <StatusPill status={db.status} />
          {db.message && (
            <span style={{ marginLeft: tokens.spacingHorizontalM }}>{db.message}</span>
          )}
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

      {db.status === 'PENDING' && (
        <MessageBar intent="info">
          <MessageBarBody>
            <MessageBarTitle>Provisioning</MessageBarTitle>
            The dbaas controller is creating the PostgreSQL VM. This typically takes 2–5 minutes.
            This page refreshes every 5 seconds.
          </MessageBarBody>
        </MessageBar>
      )}

      {db.status === 'FAILED' && (
        <MessageBar intent="error">
          <MessageBarBody>
            <MessageBarTitle>Provisioning failed</MessageBarTitle>
            {db.message ?? 'The dbaas controller reported an error.'} Delete and recreate.
          </MessageBarBody>
        </MessageBar>
      )}

      <TabList selectedValue={tab} onTabSelect={(_, d) => setTab(d.value as typeof tab)}>
        <Tab value="overview">Overview</Tab>
        <Tab value="access">Access control</Tab>
      </TabList>

      {tab === 'overview' && (
      <>
      <Card className={styles.card}>
        <div className={styles.cardHeader}>
          <Subtitle1>Details</Subtitle1>
        </div>
        <div className={styles.grid}>
          <div className={styles.label}>Database ID</div>
          <div className={`${styles.value} ${styles.mono}`}>{db.id}</div>
          <div className={styles.label}>Tenant / Project</div>
          <div className={styles.value}>{tenantId} / {projectId}</div>
          <div className={styles.label}>Engine</div>
          <div className={styles.value}>
            {db.engine}{db.engine_version ? ` ${db.engine_version}` : ''}
          </div>
          <div className={styles.label}>Instance class</div>
          <div className={styles.value}>{db.instance_class}</div>
          <div className={styles.label}>Storage</div>
          <div className={styles.value}>{db.allocated_storage_gb} GB</div>
          <div className={styles.label}>Network mode</div>
          <div className={styles.value}>{db.network_mode}</div>
          <div className={styles.label}>Endpoint</div>
          <div className={`${styles.value} ${styles.mono}`}>
            {db.endpoint_address && db.endpoint_port
              ? `${db.endpoint_address}:${db.endpoint_port}`
              : '—'}
          </div>
          <div className={styles.label}>Created</div>
          <div className={styles.value}>{fmtDate(db.created_at)}</div>
          <div className={styles.label}>Updated</div>
          <div className={styles.value}>{fmtDate(db.updated_at)}</div>
        </div>
      </Card>

      <Card className={styles.card}>
        <div className={styles.cardHeader}>
          <Subtitle1>Master credentials</Subtitle1>
          <Body1 style={{ color: tokens.colorNeutralForeground3 }}>
            Username and password for the database master user.{' '}
            <strong style={{ color: tokens.colorPaletteDarkOrangeForeground1, fontWeight: tokens.fontWeightBold }}>
              Shown once
            </strong>{' '}
            — they are not stored after retrieval. Use them to seed your application's secret store.
          </Body1>
        </div>

        {revealedSecrets && (
          <div style={{ marginBottom: tokens.spacingVerticalL }}>
            <SecretRevealBanner
              title={`Credentials for ${db.name}`}
              description="Save these values now — they will not be shown again."
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

        {credsAlreadyTaken && !revealedSecrets && (
          <MessageBar intent="warning" style={{ marginBottom: tokens.spacingVerticalM }}>
            <MessageBarBody>
              Credentials were already retrieved for this database and cannot be shown again.
              Delete and recreate the database if you need fresh credentials.
            </MessageBarBody>
          </MessageBar>
        )}

        <div className={styles.credsAction}>
          <Button
            appearance="primary"
            icon={<Key20Regular />}
            onClick={() => credsMutation.mutate()}
            disabled={!isActive || credsMutation.isPending || credsAlreadyTaken}
          >
            {credsMutation.isPending ? 'Retrieving…' : 'Retrieve credentials'}
          </Button>
          {!isActive && !credsAlreadyTaken && (
            <Body1 style={{ color: tokens.colorNeutralForeground3 }}>
              Available once the database is Active.
            </Body1>
          )}
        </div>
      </Card>
      </>
      )}

      {tab === 'access' && (
        <MembersPage
          resourceBase={`/v1/tenants/${tenantId}/projects/${projectId}/databases/${dbId}`}
          scopeLabel={db.name}
        />
      )}
    </div>
  );
}
