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
  Globe24Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import { EmptyState, ErrorState, LoadingState } from '../components/list/PageStates';
import { RowActionsMenu } from '../components/list/RowActionsMenu';
import { useListPageStyles } from '../components/list/useListPageStyles';
import StatusPill from '../components/StatusPill';
import VnetCreateDrawer, { type VnetCreateResult } from '../components/VnetCreateDrawer';
import { useCan } from '../api/useCan';
import { PermissionTooltip } from '../components/PermissionTooltip';
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

interface VNet {
  id: string;
  name: string;
  region: string;
  address_space: string[];
  description?: string;
  status: string;
  created_at: string;
}

export default function VNetsListPage() {
  const styles = useListPageStyles();
  const pageStyles = usePageStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const { can } = useCan(tenantId, ['network/vnets/write'], projectId);
  const canWrite = can('network/vnets/write');
  const confirmDialog = useConfirmDialog();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [created, setCreated] = useState<VnetCreateResult | null>(null);

  const deleteMutation = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE('/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}', {
        params: { path: { tenant_id: tenantId!, project_id: projectId!, vnet_id: id } },
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
        <Toast><ToastTitle>Delete requested — VNet will transition to DELETING</ToastTitle></Toast>,
        { intent: 'success' }
      );
      queryClient.invalidateQueries({ queryKey: ['vnets', tenantId, projectId] });
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>,
        { intent: 'error' }
      );
    },
  });

  const onDelete = async (v: VNet) => {
    const ok = await confirmDialog({
      title: `Delete VNet "${v.name}"?`,
      body: 'All subnets, peerings, and associated route tables must be removed before this can complete. If any remain, the delete will be rejected.',
      confirmLabel: 'Delete',
      destructive: true,
      typeToConfirm: v.name,
    });
    if (!ok) return;
    deleteMutation.mutate(v.id);
  };

  const vnetsQuery = useQuery({
    queryKey: ['vnets', tenantId, projectId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/vnets', {
        params: { path: { tenant_id: tenantId!, project_id: projectId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as VNet[];
    },
    refetchInterval: (query) => {
      const data = query.state.data as VNet[] | undefined;
      const transitioning = data?.some((v) => v.status === 'PENDING' || v.status === 'DELETING');
      return transitioning ? 5_000 : false;
    },
  });

  const vnets = vnetsQuery.data ?? [];
  const count = vnets.length;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />
      <div className={styles.header}>
        <Title2>Virtual networks</Title2>
        <Subtitle1 className={styles.subtitle}>
          {vnetsQuery.isLoading
            ? 'Loading…'
            : `${count} VNet${count === 1 ? '' : 's'} in this tenant`}
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create VNets">
          <Button
            appearance="primary"
            icon={<Add20Regular />}
            onClick={() => setDrawerOpen(true)}
            disabledFocusable={!canWrite}
          >
            Create VNet
          </Button>
        </PermissionTooltip>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => vnetsQuery.refetch()}
          disabled={vnetsQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {created && (
        <div className={pageStyles.notice}>
          VNet <strong>{created.vnetName}</strong> is provisioning.{' '}
          <Button
            appearance="transparent"
            size="small"
            onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/vnets/${created.vnetId}`)}
          >
            View VNet
          </Button>{' '}
          <Button appearance="transparent" size="small" onClick={() => setCreated(null)}>
            Dismiss
          </Button>
        </div>
      )}

      <VnetCreateDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        onCreated={(r) => setCreated(r)}
      />

      {vnetsQuery.isLoading && <LoadingState label="Loading VNets…" />}

      {vnetsQuery.isError && !vnetsQuery.isLoading && (
        <ErrorState message={`Failed to load VNets: ${(vnetsQuery.error as Error).message}`} />
      )}

      {!vnetsQuery.isLoading && !vnetsQuery.isError && count === 0 && (
        <EmptyState
          icon={<Globe24Regular />}
          title={`No virtual networks in ${tenantId ?? 'this tenant'} yet`}
          description="A virtual network is the isolated L3 boundary for your tenant. Create one first — subnets, bastions, and VMs attach to it."
          action={
            <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create VNets">
              <Button
                appearance="primary"
                icon={<Add20Regular />}
                onClick={() => setDrawerOpen(true)}
                disabledFocusable={!canWrite}
              >
                Create VNet
              </Button>
            </PermissionTooltip>
          }
        />
      )}

      {!vnetsQuery.isLoading && !vnetsQuery.isError && count > 0 && (
        <Card className={styles.tableCard}>
          <Table size="small" aria-label="VNets">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>Name</TableHeaderCell>
                <TableHeaderCell>Status</TableHeaderCell>
                <TableHeaderCell>Address space</TableHeaderCell>
                <TableHeaderCell>Region</TableHeaderCell>
                <TableHeaderCell>Created</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {vnets.map((v) => (
                <TableRow
                  key={v.id}
                  className={styles.rowClickable}
                  onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/vnets/${v.id}`)}
                >
                  <TableCell>
                    <span className={styles.nameLink}>{v.name}</span>
                    <div className={styles.tableMutedCell}>{v.id}</div>
                  </TableCell>
                  <TableCell>
                    <StatusPill status={v.status} />
                  </TableCell>
                  <TableCell className={styles.tableMonoCell}>
                    {v.address_space.join(', ')}
                  </TableCell>
                  <TableCell>{v.region}</TableCell>
                  <TableCell className={styles.tableMutedCell}>{fmtDate(v.created_at)}</TableCell>
                  <TableCell>
                    <RowActionsMenu>
                      <MenuItem
                        onClick={() => onDelete(v)}
                        disabled={!canWrite || deleteMutation.isPending || v.status === 'DELETING'}
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
