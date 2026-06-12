import {
    Body1,
    Button,
    Card,
    MessageBar,
    MessageBarBody,
    MessageBarTitle,
    Spinner,
    Subtitle1,
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

import { ArrowLeft20Regular, Delete20Regular } from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { detailErrorMessage } from '../lib/apiError';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import StatusPill from '../components/StatusPill';

interface Bastion {
    id: string;
    name: string;
    status: string;
    tenant_id: string;
    vnet_id?: string;
    subnet_id?: string;
    provider_type: string;
    mgmt_ip?: string;
    internal_ip?: string;
    description?: string;
    message?: string;
    created_at: string;
}

const useStyles = makeStyles({
    root: {
      padding: tokens.spacingHorizontalXXL,
      maxWidth: '900px',
      display: 'flex',
      flexDirection: 'column',
      gap: tokens.spacingVerticalL,
    },
    header: {
      display: 'flex',
      flexDirection: 'column',
      gap: tokens.spacingVerticalS,
    },
    backButton: { alignSelf: 'flex-start' },
    cmdBar: {
      display: 'flex',
      gap: tokens.spacingHorizontalS,
      paddingTop: tokens.spacingVerticalS,
      paddingBottom: tokens.spacingVerticalS,
      ...shorthands.borderTop('1px', 'solid', tokens.colorNeutralStroke2),
      ...shorthands.borderBottom('1px', 'solid', tokens.colorNeutralStroke2),
    },
    detailsCard: { padding: tokens.spacingHorizontalXXL },
    grid: {
      display: 'grid',
      gridTemplateColumns: '180px 1fr',
      rowGap: tokens.spacingVerticalM,
      columnGap: tokens.spacingHorizontalL,
    },
    label: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
    value: { fontSize: tokens.fontSizeBase300 },
    mono: { fontFamily: tokens.fontFamilyMonospace },
    loading: {
      padding: tokens.spacingHorizontalXXL,
      display: 'flex',
      justifyContent: 'center',
    },
});

export default function BastionDetailPage() {
    const styles = useStyles();
    const api = useApi();
    const navigate = useNavigate();
    const queryClient = useQueryClient();
    const toasterId = useId('toaster');
    const { dispatchToast } = useToastController(toasterId);
    const { tenantId, bastionId } = useParams<{ tenantId: string; bastionId: string }>();
    const { projectId } = useActiveProject();
    const confirmDialog = useConfirmDialog();

    const bastionQuery = useQuery({
      queryKey: ['bastion', tenantId, projectId, bastionId],
      enabled: Boolean(tenantId) && Boolean(projectId) && Boolean(bastionId),
      queryFn: async () => {
        const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/bastions/{id}', {
          params: { path: { tenant_id: tenantId!, project_id: projectId!, id: bastionId! } },
        });
        if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
        return data as Bastion;
      },
      refetchInterval: (q) => {
        const d = q.state.data as Bastion | undefined;
        return d && (d.status === 'PENDING' || d.status === 'DELETING' || !d.mgmt_ip) ? 5_000 : false;
      },
    });

    const deleteMutation = useMutation({
      mutationFn: async () => {
        const { error } = await api.DELETE('/v1/tenants/{tenant_id}/projects/{project_id}/bastions/{id}', {
          params: { path: { tenant_id: tenantId!, project_id: projectId!, id: bastionId! } },
        });
        if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      },
      onSuccess: () => {
        dispatchToast(
          <Toast><ToastTitle>Delete requested — bastion will transition to DELETING</ToastTitle></Toast>,
          { intent: 'success' }
        );
        queryClient.invalidateQueries({ queryKey: ['bastions', tenantId, projectId] });
        // Navigate back to the list before the detail-page query refetches —
        // otherwise it 404s and renders "Failed to load bastion" in place.
        // Matches VmDetailPage.tsx delete-flow.
        navigate(`/tenants/${tenantId}/projects/${projectId}/bastions`);
      },
      onError: (e: Error) => {
        dispatchToast(
          <Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>,
          { intent: 'error' }
        );
      },
    });

    const onDelete = async () => {
      const ok = await confirmDialog({
        title: `Delete bastion "${bastionQuery.data?.name}"?`,
        body: 'The bastion VM will be terminated. Any active SSH tunnels through it will be dropped. This cannot be undone.',
        confirmLabel: 'Delete',
        destructive: true,
      });
      if (!ok) return;
      deleteMutation.mutate();
    };

    if (bastionQuery.isLoading) {
      return (
        <div className={styles.root}>
          <div className={styles.loading}>
            <Spinner label="Loading bastion…" />
          </div>
        </div>
      );
    }

    if (bastionQuery.isError || !bastionQuery.data) {
      return (
        <div className={styles.root}>
          <Button
            appearance="subtle"
            icon={<ArrowLeft20Regular />}
            onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/bastions`)}
            className={styles.backButton}
          >
            Back to bastions
          </Button>
          <MessageBar intent="error">
            <MessageBarBody>
              <MessageBarTitle>Failed to load bastion</MessageBarTitle>
              {detailErrorMessage(bastionQuery.error, 'bastion')}
            </MessageBarBody>
          </MessageBar>
        </div>
      );
    }

    const b = bastionQuery.data;
    const isDeleting = b.status === 'DELETING' || deleteMutation.isPending;

    return (
      <div className={styles.root}>
        <Toaster toasterId={toasterId} />

        <Button
          appearance="subtle"
          icon={<ArrowLeft20Regular />}
          onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/bastions`)}
          className={styles.backButton}
        >
          Back to bastions
        </Button>

        <div className={styles.header}>
          <Title2>{b.name}</Title2>
          <Subtitle1>
            <StatusPill status={b.status} />
          </Subtitle1>
        </div>

        <div className={styles.cmdBar}>
          <Button
            appearance="secondary"
            icon={<Delete20Regular />}
            onClick={onDelete}
            disabled={isDeleting}
          >
            {isDeleting ? 'Deleting…' : 'Delete'}
          </Button>
        </div>

        <Card className={styles.detailsCard}>
          <div className={styles.grid}>
            <span className={styles.label}>ID</span>
            <span className={`${styles.value} ${styles.mono}`}>{b.id}</span>

            <span className={styles.label}>SSH endpoint</span>
            <span className={`${styles.value} ${styles.mono}`}>{b.mgmt_ip ?? '—'}</span>

            <span className={styles.label}>Private IP</span>
            <span className={`${styles.value} ${styles.mono}`}>{b.internal_ip ?? '—'}</span>

            <span className={styles.label}>VNet</span>
            <span className={`${styles.value} ${styles.mono}`}>{b.vnet_id ?? '—'}</span>

            <span className={styles.label}>Subnet</span>
            <span className={`${styles.value} ${styles.mono}`}>{b.subnet_id ?? '—'}</span>

            <span className={styles.label}>Provider</span>
            <span className={styles.value}>{b.provider_type}</span>

            <span className={styles.label}>Created</span>
            <span className={styles.value}>{new Date(b.created_at).toLocaleString()}</span>

            {b.description && (
              <>
                <span className={styles.label}>Description</span>
                <span className={styles.value}>{b.description}</span>
              </>
            )}

            {b.message && (
              <>
                <span className={styles.label}>Message</span>
                <span className={styles.value}>{b.message}</span>
              </>
            )}
          </div>
        </Card>

        {b.mgmt_ip && (
          <Card className={styles.detailsCard}>
            <Body1>To connect from your workstation:</Body1>
            <pre className={`${styles.mono}`} style={{ marginTop: tokens.spacingVerticalS }}>
              {`ssh -i ${b.name}.pem -A ubuntu@${b.mgmt_ip}`}
            </pre>
          </Card>
        )}
      </div>
    );
}