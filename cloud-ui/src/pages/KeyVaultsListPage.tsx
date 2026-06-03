import {
  Button,
  Card,
  Menu,
  MenuItem,
  MenuList,
  MenuPopover,
  MenuTrigger,
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
  Key24Regular,
  MoreHorizontal20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import KeyVaultCreateDrawer, { type KeyVaultCreated } from '../components/KeyVaultCreateDrawer';
import StatusPill from '../components/StatusPill';
import { EmptyState, ErrorState, LoadingState } from '../components/list/PageStates';
import { useListPageStyles } from '../components/list/useListPageStyles';
import { useCan } from '../api/useCan';
import { PermissionTooltip } from '../components/PermissionTooltip';
import { listErrorMessage } from '../lib/apiError';
import { fmtDate } from '../lib/date';

interface KeyVault {
  id: string;
  name: string;
  status: string;
  soft_delete_days: number;
  mount_path?: string;
  created_at: string;
}

const usePageStyles = makeStyles({
  nameLink: {
    color: tokens.colorBrandForeground1,
    textDecoration: 'none',
    fontWeight: 600,
    '&:hover': { textDecoration: 'underline' },
  },
});

export default function KeyVaultsListPage() {
  const styles = useListPageStyles();
  const pageStyles = usePageStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const { can } = useCan(tenantId, ['keyvault/vaults/write'], projectId);
  const canWrite = can('keyvault/vaults/write');
  const navigate = useNavigate();
  const confirmDialog = useConfirmDialog();

  const [createOpen, setCreateOpen] = useState(false);

  const kvsQuery = useQuery({
    queryKey: ['keyvaults', tenantId, projectId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults',
        { params: { path: { tenant_id: tenantId!, project_id: projectId! } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as KeyVault[];
    },
    // Poll while any vault is PENDING / DELETING so the row flips without a manual refresh.
    refetchInterval: (query) => {
      const data = query.state.data as KeyVault[] | undefined;
      return data?.some((kv) => kv.status === 'PENDING' || kv.status === 'DELETING')
        ? 5000
        : false;
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE(
        '/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}',
        {
          params: {
            path: { tenant_id: tenantId!, project_id: projectId!, id },
          },
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Key vault deletion started</ToastTitle></Toast>, {
        intent: 'success',
      });
      queryClient.invalidateQueries({ queryKey: ['keyvaults', tenantId, projectId] });
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>, {
        intent: 'error',
      });
    },
  });

  const onDelete = async (kv: KeyVault) => {
    const ok = await confirmDialog({
      title: `Delete key vault "${kv.name}"?`,
      body: `Soft-delete will keep secrets recoverable for ${kv.soft_delete_days} days. Workloads that authenticate against this vault will lose access immediately.`,
      confirmLabel: 'Delete',
      destructive: true,
      typeToConfirm: kv.name,
    });
    if (!ok) return;
    deleteMutation.mutate(kv.id);
  };

  const onCreated = (kv: KeyVaultCreated) => {
    dispatchToast(
      <Toast>
        <ToastTitle>
          Key vault "{kv.name}" created — provisioning in background…
        </ToastTitle>
      </Toast>,
      { intent: 'success' },
    );
    navigate(`${kv.id}`);
  };

  const kvs = kvsQuery.data ?? [];
  const count = kvs.length;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <div className={styles.header}>
        <Title2>Key vaults</Title2>
        <Subtitle1 className={styles.subtitle}>
          Per-project secret stores. Each vault is isolated with its own access credentials.
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create key vaults">
          <Button
            appearance="primary"
            icon={<Add20Regular />}
            onClick={() => setCreateOpen(true)}
            disabledFocusable={!canWrite}
          >
            Create key vault
          </Button>
        </PermissionTooltip>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => kvsQuery.refetch()}
          disabled={kvsQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {kvsQuery.isLoading && <LoadingState label="Loading key vaults…" />}

      {kvsQuery.isError && !kvsQuery.isLoading && (
        <ErrorState
          message={listErrorMessage(kvsQuery.error, 'key vaults')}
        />
      )}

      {!kvsQuery.isLoading && !kvsQuery.isError && count === 0 && (
        <EmptyState
          icon={<Key24Regular />}
          title="No key vaults in this project yet"
          description="A key vault stores per-project secrets (DB passwords, API keys, certificates). Workloads read them at runtime using the vault's credentials."
          action={
            <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create key vaults">
              <Button
                appearance="primary"
                icon={<Add20Regular />}
                onClick={() => setCreateOpen(true)}
                disabledFocusable={!canWrite}
              >
                Create key vault
              </Button>
            </PermissionTooltip>
          }
        />
      )}

      {!kvsQuery.isLoading && !kvsQuery.isError && count > 0 && (
        <Card className={styles.tableCard}>
          <Table size="small" aria-label="Key vaults">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>Name</TableHeaderCell>
                <TableHeaderCell>Status</TableHeaderCell>
                <TableHeaderCell>Soft-delete</TableHeaderCell>
                <TableHeaderCell>Created</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {kvs.map((kv) => (
                <TableRow key={kv.id}>
                  <TableCell>
                    <Link to={`${kv.id}`} className={pageStyles.nameLink}>
                      {kv.name}
                    </Link>
                    <div className={styles.tableMutedCell}>{kv.id}</div>
                  </TableCell>
                  <TableCell><StatusPill status={kv.status} /></TableCell>
                  <TableCell className={styles.tableMutedCell}>
                    {kv.soft_delete_days} days
                  </TableCell>
                  <TableCell className={styles.tableMutedCell}>
                    {fmtDate(kv.created_at)}
                  </TableCell>
                  <TableCell>
                    <Menu>
                      <MenuTrigger disableButtonEnhancement>
                        <Button
                          appearance="subtle"
                          icon={<MoreHorizontal20Regular />}
                          aria-label="Actions"
                        />
                      </MenuTrigger>
                      <MenuPopover>
                        <MenuList>
                          <MenuItem onClick={() => navigate(`${kv.id}`)}>Open</MenuItem>
                          <MenuItem
                            onClick={() => onDelete(kv)}
                            disabled={
                              !canWrite ||
                              deleteMutation.isPending ||
                              kv.status === 'DELETING'
                            }
                          >
                            Delete
                          </MenuItem>
                        </MenuList>
                      </MenuPopover>
                    </Menu>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}

      <KeyVaultCreateDrawer
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={onCreated}
      />
    </div>
  );
}
