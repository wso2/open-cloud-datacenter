import {
  Body1,
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
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  Add20Regular,
  ArrowClockwise20Regular,
  PersonShield24Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import BastionCreateDrawer, { type BastionCreateResult } from '../components/BastionCreateDrawer';
import { EmptyState, ErrorState, LoadingState } from '../components/list/PageStates';
import { RowActionsMenu } from '../components/list/RowActionsMenu';
import { useListPageStyles } from '../components/list/useListPageStyles';
import SecretRevealBanner, { type Secret } from '../components/SecretRevealBanner';
import StatusPill from '../components/StatusPill';
import { useCan } from '../api/useCan';
import { PermissionTooltip } from '../components/PermissionTooltip';
import { listErrorMessage } from '../lib/apiError';
import { fmtDate } from '../lib/date';

interface Bastion {
  id: string;
  name: string;
  status: string;
  vnet_id?: string;
  subnet_id?: string;
  mgmt_ip?: string;
  internal_ip?: string;
  description?: string;
  message?: string;
  created_at: string;
}

export default function BastionsListPage() {
  const styles = useListPageStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const { can } = useCan(tenantId, ['compute/bastions/write'], projectId);
  const canWrite = can('compute/bastions/write');
  const confirmDialog = useConfirmDialog();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [createdSecrets, setCreatedSecrets] = useState<{
    title: string;
    secrets: Secret[];
  } | null>(null);

  const deleteMutation = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE('/v1/tenants/{tenant_id}/projects/{project_id}/bastions/{id}', {
        params: { path: { tenant_id: tenantId!, project_id: projectId!, id } },
      });
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
        <Toast><ToastTitle>Delete requested — bastion will transition to DELETING</ToastTitle></Toast>,
        { intent: 'success' }
      );
      queryClient.invalidateQueries({ queryKey: ['bastions', tenantId, projectId] });
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>,
        { intent: 'error' }
      );
    },
  });

  const onDelete = async (b: Bastion) => {
    const ok = await confirmDialog({
      title: `Delete bastion "${b.name}"?`,
      body: 'The bastion VM will be terminated. Any active SSH tunnels through it will be dropped. This cannot be undone.',
      confirmLabel: 'Delete',
      destructive: true,
    });
    if (!ok) return;
    deleteMutation.mutate(b.id);
  };

  const onCreated = (r: BastionCreateResult) => {
    setCreatedSecrets({
      title: `Connection details for ${r.name}`,
      secrets: [
        {
          label: 'SSH private key',
          value: r.privateKey,
          filename: `${r.name}.pem`,
          multiline: true,
        },
        {
          label: 'Console password',
          value: r.consolePassword,
        },
      ],
    });
  };

  const bastionsQuery = useQuery({
    queryKey: ['bastions', tenantId, projectId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/bastions', {
        params: { path: { tenant_id: tenantId!, project_id: projectId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as Bastion[];
    },
    refetchInterval: (query) => {
      const data = query.state.data as Bastion[] | undefined;
      const hasTransitioning = data?.some(
        (b) => b.status === 'PENDING' || b.status === 'DELETING' || !b.mgmt_ip
      );
      return hasTransitioning ? 5_000 : false;
    },
  });

  const items = bastionsQuery.data ?? [];
  const count = items.length;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />
      <div className={styles.header}>
        <Title2>Bastions</Title2>
        <Subtitle1 className={styles.subtitle}>
          {bastionsQuery.isLoading
            ? 'Loading…'
            : `${count} bastion${count === 1 ? '' : 's'} in this tenant`}
        </Subtitle1>
        <Body1 className={styles.subtitle}>
          A bastion is a small SSH jump-host VM that gives you a reachable entry point into
          a VPC. SSH to the bastion, then ProxyJump to VMs on the private subnet.
        </Body1>
      </div>

      <div className={styles.cmdBar}>
        <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create bastions">
          <Button
            appearance="primary"
            icon={<Add20Regular />}
            onClick={() => setDrawerOpen(true)}
            disabledFocusable={!canWrite}
          >
            Create bastion
          </Button>
        </PermissionTooltip>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => bastionsQuery.refetch()}
          disabled={bastionsQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {createdSecrets && (
        <SecretRevealBanner
          title={createdSecrets.title}
          description="These are shown only once and cannot be recovered. Save them now."
          secrets={createdSecrets.secrets}
          onDismiss={() => setCreatedSecrets(null)}
          onCopy={() => {}}
        />
      )}

      <BastionCreateDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        onCreated={onCreated}
      />

      {bastionsQuery.isLoading && <LoadingState label="Loading bastions…" />}

      {bastionsQuery.isError && !bastionsQuery.isLoading && (
        <ErrorState
          message={listErrorMessage(bastionsQuery.error, 'bastions')}
        />
      )}

      {!bastionsQuery.isLoading && !bastionsQuery.isError && count === 0 && (
        <EmptyState
          icon={<PersonShield24Regular />}
          title={`No bastions in ${tenantId ?? 'this tenant'} yet`}
          description="A bastion is a small jump-host VM with a reachable IP. SSH to the bastion, then ProxyJump to VMs on the private subnet."
          action={
            <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create bastions">
              <Button
                appearance="primary"
                icon={<Add20Regular />}
                onClick={() => setDrawerOpen(true)}
                disabledFocusable={!canWrite}
              >
                Create bastion
              </Button>
            </PermissionTooltip>
          }
        />
      )}

      {!bastionsQuery.isLoading && !bastionsQuery.isError && count > 0 && (
        <Card className={styles.tableCard}>
          <Table size="small" aria-label="Bastions">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>Name</TableHeaderCell>
                <TableHeaderCell>Status</TableHeaderCell>
                <TableHeaderCell>SSH endpoint</TableHeaderCell>
                <TableHeaderCell>Private IP</TableHeaderCell>
                <TableHeaderCell>Created</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((b) => (
                <TableRow
                  key={b.id}
                  className={styles.rowClickable}
                  onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/bastions/${b.id}`)}>
                  <TableCell>
                    <span className={styles.nameLink}>{b.name}</span>
                    <div className={styles.tableMutedCell}>{b.id}</div>
                  </TableCell>
                  <TableCell>
                    <StatusPill status={b.status} />
                  </TableCell>
                  <TableCell className={styles.tableMonoCell}>{b.mgmt_ip ?? '—'}</TableCell>
                  <TableCell className={styles.tableMonoCell}>{b.internal_ip ?? '—'}</TableCell>
                  <TableCell className={styles.tableMutedCell}>{fmtDate(b.created_at)}</TableCell>
                  <TableCell>
                    <RowActionsMenu>
                      <MenuItem
                        onClick={() => onDelete(b)}
                        disabled={!canWrite || deleteMutation.isPending || b.status === 'DELETING'}
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
