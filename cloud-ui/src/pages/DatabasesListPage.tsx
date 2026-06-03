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
  Database24Regular,
  MoreHorizontal20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import DatabaseCreateDrawer, { type DatabaseCreated } from '../components/DatabaseCreateDrawer';
import StatusPill from '../components/StatusPill';
import { EmptyState, ErrorState, LoadingState } from '../components/list/PageStates';
import { useListPageStyles } from '../components/list/useListPageStyles';
import { useCan } from '../api/useCan';
import { PermissionTooltip } from '../components/PermissionTooltip';
import { listErrorMessage } from '../lib/apiError';
import { fmtDate } from '../lib/date';

interface Database {
  id: string;
  name: string;
  engine: string;
  engine_version?: string;
  instance_class: string;
  allocated_storage_gb: number;
  network_mode: string;
  status: string;
  endpoint_address?: string;
  endpoint_port?: number;
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

export default function DatabasesListPage() {
  const styles = useListPageStyles();
  const pageStyles = usePageStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const { can } = useCan(tenantId, ['database/servers/write'], projectId);
  const canWrite = can('database/servers/write');
  const navigate = useNavigate();
  const confirmDialog = useConfirmDialog();

  const [createOpen, setCreateOpen] = useState(false);

  const dbsQuery = useQuery({
    queryKey: ['databases', tenantId, projectId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/databases',
        { params: { path: { tenant_id: tenantId!, project_id: projectId! } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as Database[];
    },
    // Poll while any database is provisioning or being torn down.
    refetchInterval: (query) => {
      const data = query.state.data as Database[] | undefined;
      return data?.some((db) => db.status === 'PENDING' || db.status === 'DELETING')
        ? 5000
        : false;
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE(
        '/v1/tenants/{tenant_id}/projects/{project_id}/databases/{id}',
        { params: { path: { tenant_id: tenantId!, project_id: projectId!, id } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(
        <Toast><ToastTitle>Database deletion started</ToastTitle></Toast>,
        { intent: 'success' },
      );
      queryClient.invalidateQueries({ queryKey: ['databases', tenantId, projectId] });
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>,
        { intent: 'error' },
      );
    },
  });

  const onDelete = async (db: Database) => {
    const ok = await confirmDialog({
      title: `Delete database "${db.name}"?`,
      body: 'The VM, data volumes, and credentials will be permanently destroyed. This cannot be undone.',
      confirmLabel: 'Delete',
      destructive: true,
      typeToConfirm: db.name,
    });
    if (!ok) return;
    deleteMutation.mutate(db.id);
  };

  const onCreated = (db: DatabaseCreated) => {
    dispatchToast(
      <Toast>
        <ToastTitle>Database "{db.name}" created — provisioning in background…</ToastTitle>
      </Toast>,
      { intent: 'success' },
    );
    navigate(`${db.id}`);
  };

  const dbs = dbsQuery.data ?? [];
  const count = dbs.length;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <div className={styles.header}>
        <Title2>Databases</Title2>
        <Subtitle1 className={styles.subtitle}>
          Managed PostgreSQL databases. Each database runs as a dedicated VM provisioned by the dbaas controller.
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create databases">
          <Button
            appearance="primary"
            icon={<Add20Regular />}
            onClick={() => setCreateOpen(true)}
            disabledFocusable={!canWrite}
          >
            Create database
          </Button>
        </PermissionTooltip>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => dbsQuery.refetch()}
          disabled={dbsQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {dbsQuery.isLoading && <LoadingState label="Loading databases…" />}

      {dbsQuery.isError && !dbsQuery.isLoading && (
        <ErrorState
          message={listErrorMessage(dbsQuery.error, 'databases')}
        />
      )}

      {!dbsQuery.isLoading && !dbsQuery.isError && count === 0 && (
        <EmptyState
          icon={<Database24Regular />}
          title="No databases in this project yet"
          description="A managed database gives your workloads a dedicated PostgreSQL instance. Credentials are provisioned by the controller and shown once on first retrieval."
          action={
            <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create databases">
              <Button
                appearance="primary"
                icon={<Add20Regular />}
                onClick={() => setCreateOpen(true)}
                disabledFocusable={!canWrite}
              >
                Create database
              </Button>
            </PermissionTooltip>
          }
        />
      )}

      {!dbsQuery.isLoading && !dbsQuery.isError && count > 0 && (
        <Card className={styles.tableCard}>
          <Table size="small" aria-label="Databases">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>Name</TableHeaderCell>
                <TableHeaderCell>Status</TableHeaderCell>
                <TableHeaderCell>Engine</TableHeaderCell>
                <TableHeaderCell>Instance class</TableHeaderCell>
                <TableHeaderCell>Storage</TableHeaderCell>
                <TableHeaderCell>Endpoint</TableHeaderCell>
                <TableHeaderCell>Created</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {dbs.map((db) => (
                <TableRow key={db.id}>
                  <TableCell>
                    <Link to={`${db.id}`} className={pageStyles.nameLink}>
                      {db.name}
                    </Link>
                    <div className={styles.tableMutedCell}>{db.id}</div>
                  </TableCell>
                  <TableCell><StatusPill status={db.status} /></TableCell>
                  <TableCell className={styles.tableMutedCell}>
                    {db.engine}{db.engine_version ? ` ${db.engine_version}` : ''}
                  </TableCell>
                  <TableCell className={styles.tableMutedCell}>{db.instance_class}</TableCell>
                  <TableCell className={styles.tableMutedCell}>{db.allocated_storage_gb} GB</TableCell>
                  <TableCell className={styles.tableMutedCell}>
                    {db.endpoint_address && db.endpoint_port
                      ? `${db.endpoint_address}:${db.endpoint_port}`
                      : '—'}
                  </TableCell>
                  <TableCell className={styles.tableMutedCell}>{fmtDate(db.created_at)}</TableCell>
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
                          <MenuItem onClick={() => navigate(`${db.id}`)}>Open</MenuItem>
                          <MenuItem
                            onClick={() => onDelete(db)}
                            disabled={!canWrite || deleteMutation.isPending || db.status === 'DELETING'}
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

      <DatabaseCreateDrawer
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={onCreated}
      />
    </div>
  );
}
