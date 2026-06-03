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
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  Add20Regular,
  ArrowClockwise20Regular,
  Server24Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useConfirmDialog } from '../components/useConfirmDialog';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { EmptyState, ErrorState, LoadingState } from '../components/list/PageStates';
import { RowActionsMenu } from '../components/list/RowActionsMenu';
import { useListPageStyles } from '../components/list/useListPageStyles';
import SecretRevealBanner, { type Secret } from '../components/SecretRevealBanner';
import StatusPill from '../components/StatusPill';
import VmCreateDrawer, { type VmCreateResult } from '../components/VmCreateDrawer';
import { useCan } from '../api/useCan';
import { PermissionTooltip } from '../components/PermissionTooltip';
import { listErrorMessage } from '../lib/apiError';
import { fmtDate } from '../lib/date';

interface VirtualMachine {
  id: string;
  name: string;
  size?: string;
  status: string;
  ip_address?: string;
  message?: string;
  created_at: string;
}

export default function VmsListPage() {
  const styles = useListPageStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const { can } = useCan(tenantId, ['compute/virtualMachines/write'], projectId);
  const canWrite = can('compute/virtualMachines/write');
  const confirmDialog = useConfirmDialog();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [createdSecrets, setCreatedSecrets] = useState<{
    title: string;
    secrets: Secret[];
  } | null>(null);

  const deleteMutation = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE('/v1/tenants/{tenant_id}/projects/{project_id}/virtual-machines/{id}', {
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
        <Toast><ToastTitle>Delete requested — VM will transition to DELETING</ToastTitle></Toast>,
        { intent: 'success' }
      );
      queryClient.invalidateQueries({ queryKey: ['vms', tenantId, projectId] });
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>,
        { intent: 'error' }
      );
    },
  });

  const onDelete = async (vm: VirtualMachine) => {
    const ok = await confirmDialog({
      title: `Delete VM "${vm.name}"?`,
      body: 'The VM will be stopped and all allocated resources released. This cannot be undone.',
      confirmLabel: 'Delete',
      destructive: true,
    });
    if (!ok) return;
    deleteMutation.mutate(vm.id);
  };

  const onCreated = (r: VmCreateResult) => {
    setCreatedSecrets({
      title: `Connection details for ${r.vmName}`,
      secrets: [
        {
          label: 'SSH private key',
          value: r.privateKey,
          filename: `${r.vmName}.pem`,
          multiline: true,
        },
        {
          label: 'Console password',
          value: r.consolePassword,
        },
      ],
    });
  };

  const vmsQuery = useQuery({
    queryKey: ['vms', tenantId, projectId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/virtual-machines', {
        params: { path: { tenant_id: tenantId!, project_id: projectId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as VirtualMachine[];
    },
    refetchInterval: (query) => {
      const data = query.state.data as VirtualMachine[] | undefined;
      const hasTransitioning = data?.some(
        (v) => v.status === 'PENDING' || v.status === 'DELETING'
      );
      return hasTransitioning ? 5_000 : false;
    },
  });

  const vms = vmsQuery.data ?? [];
  const count = vms.length;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />
      <div className={styles.header}>
        <Title2>Virtual machines</Title2>
        <Subtitle1 className={styles.subtitle}>
          {vmsQuery.isLoading
            ? 'Loading…'
            : `${count} machine${count === 1 ? '' : 's'} in this tenant`}
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create VMs">
          <Button
            appearance="primary"
            icon={<Add20Regular />}
            onClick={() => setDrawerOpen(true)}
            disabledFocusable={!canWrite}
          >
            Create virtual machine
          </Button>
        </PermissionTooltip>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => vmsQuery.refetch()}
          disabled={vmsQuery.isFetching}
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

      <VmCreateDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        onCreated={onCreated}
      />

      {vmsQuery.isLoading && <LoadingState label="Loading virtual machines…" />}

      {vmsQuery.isError && !vmsQuery.isLoading && (
        <ErrorState message={listErrorMessage(vmsQuery.error, 'virtual machines')} />
      )}

      {!vmsQuery.isLoading && !vmsQuery.isError && count === 0 && (
        <EmptyState
          icon={<Server24Regular />}
          title={`No virtual machines in ${tenantId ?? 'this tenant'} yet`}
          description="Create a VM by picking a size, an image, and the subnet it should join."
          action={
            <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create VMs">
              <Button
                appearance="primary"
                icon={<Add20Regular />}
                onClick={() => setDrawerOpen(true)}
                disabledFocusable={!canWrite}
              >
                Create virtual machine
              </Button>
            </PermissionTooltip>
          }
        />
      )}

      {!vmsQuery.isLoading && !vmsQuery.isError && count > 0 && (
        <Card className={styles.tableCard}>
          <Table size="small" aria-label="Virtual machines">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>Name</TableHeaderCell>
                <TableHeaderCell>Status</TableHeaderCell>
                <TableHeaderCell>Size</TableHeaderCell>
                <TableHeaderCell>IP address</TableHeaderCell>
                <TableHeaderCell>Created</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {vms.map((vm) => (
                <TableRow
                  key={vm.id}
                  className={styles.rowClickable}
                  onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/vms/${vm.id}`)}
                >
                  <TableCell>
                    <span className={styles.nameLink}>{vm.name}</span>
                    <div className={styles.tableMutedCell}>{vm.id}</div>
                  </TableCell>
                  <TableCell>
                    <StatusPill status={vm.status} />
                  </TableCell>
                  <TableCell>{vm.size ?? '—'}</TableCell>
                  <TableCell className={styles.tableMonoCell}>{vm.ip_address ?? '—'}</TableCell>
                  <TableCell className={styles.tableMutedCell}>{fmtDate(vm.created_at)}</TableCell>
                  <TableCell>
                    <RowActionsMenu>
                      <MenuItem
                        onClick={() => onDelete(vm)}
                        disabled={!canWrite || deleteMutation.isPending || vm.status === 'DELETING'}
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
