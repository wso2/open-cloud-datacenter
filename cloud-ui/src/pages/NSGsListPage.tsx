import {
  Body1,
  Button,
  Card,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  DialogTrigger,
  Field,
  Input,
  MenuItem,
  Subtitle1,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Textarea,
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
  ShieldKeyhole24Regular,
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
import { useCan } from '../api/useCan';
import { PermissionTooltip } from '../components/PermissionTooltip';
import { listErrorMessage } from '../lib/apiError';
import { fmtDate } from '../lib/date';

const usePageStyles = makeStyles({
  dialogForm: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
});

interface NSG {
  id: string;
  name: string;
  description?: string;
  rules: unknown[];
  attachments: unknown[];
  status: string;
  created_at: string;
}

export default function NSGsListPage() {
  const styles = useListPageStyles();
  const pageStyles = usePageStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const { can } = useCan(tenantId, ['network/nsgs/write'], projectId);
  const canWrite = can('network/nsgs/write');
  const confirmDialog = useConfirmDialog();

  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');

  const nsgsQuery = useQuery({
    queryKey: ['nsgs', tenantId, projectId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/security-groups', {
        params: { path: { tenant_id: tenantId!, project_id: projectId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as NSG[];
    },
    refetchInterval: (query) => {
      const data = query.state.data as NSG[] | undefined;
      return data?.some((n) => n.status === 'PENDING' || n.status === 'DELETING') ? 5_000 : false;
    },
  });

  const nameValid = /^[a-z][a-z0-9-]{0,61}[a-z0-9]$/.test(name);

  const createMutation = useMutation({
    mutationFn: async () => {
      const body: Record<string, unknown> = { name, rules: [] };
      if (description.trim()) body.description = description.trim();
      const { data, error } = await api.POST('/v1/tenants/{tenant_id}/projects/{project_id}/security-groups', {
        params: { path: { tenant_id: tenantId!, project_id: projectId! } },
        body: body as never,
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as { id: string };
    },
    onSuccess: (resp) => {
      dispatchToast(<Toast><ToastTitle>Security group created</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['nsgs', tenantId, projectId] });
      setCreateOpen(false);
      setName('');
      setDescription('');
      if (resp?.id) navigate(`/tenants/${tenantId}/projects/${projectId}/nsgs/${resp.id}`);
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Create failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE('/v1/tenants/{tenant_id}/projects/{project_id}/security-groups/{sg_id}', {
        params: { path: { tenant_id: tenantId!, project_id: projectId!, sg_id: id } },
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
        <Toast><ToastTitle>Delete requested</ToastTitle></Toast>,
        { intent: 'success' }
      );
      queryClient.invalidateQueries({ queryKey: ['nsgs', tenantId, projectId] });
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>,
        { intent: 'error' }
      );
    },
  });

  const onDelete = async (n: NSG) => {
    const ok = await confirmDialog({
      title: `Delete security group "${n.name}"?`,
      body: 'All subnet attachments will be removed and the inbound/outbound rules discarded. This cannot be undone.',
      confirmLabel: 'Delete',
      destructive: true,
    });
    if (!ok) return;
    deleteMutation.mutate(n.id);
  };

  const nsgs = nsgsQuery.data ?? [];
  const count = nsgs.length;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <div className={styles.header}>
        <Title2>Security groups</Title2>
        <Subtitle1 className={styles.subtitle}>
          {nsgsQuery.isLoading
            ? 'Loading…'
            : `${count} security group${count === 1 ? '' : 's'} in this tenant`}
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create security groups">
          <Button
            appearance="primary"
            icon={<Add20Regular />}
            onClick={() => setCreateOpen(true)}
            disabledFocusable={!canWrite}
          >
            Create security group
          </Button>
        </PermissionTooltip>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => nsgsQuery.refetch()}
          disabled={nsgsQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {nsgsQuery.isLoading && <LoadingState label="Loading security groups…" />}

      {nsgsQuery.isError && !nsgsQuery.isLoading && (
        <ErrorState
          message={listErrorMessage(nsgsQuery.error, 'security groups')}
        />
      )}

      {!nsgsQuery.isLoading && !nsgsQuery.isError && count === 0 && (
        <EmptyState
          icon={<ShieldKeyhole24Regular />}
          title={`No security groups in ${tenantId ?? 'this tenant'} yet`}
          description="A security group holds inbound and outbound firewall rules. Create one, define rules, then attach it to a subnet."
          action={
            <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create security groups">
              <Button
                appearance="primary"
                icon={<Add20Regular />}
                onClick={() => setCreateOpen(true)}
                disabledFocusable={!canWrite}
              >
                Create security group
              </Button>
            </PermissionTooltip>
          }
        />
      )}

      {!nsgsQuery.isLoading && !nsgsQuery.isError && count > 0 && (
        <Card className={styles.tableCard}>
          <Table size="small" aria-label="Security groups">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>Name</TableHeaderCell>
                <TableHeaderCell>Status</TableHeaderCell>
                <TableHeaderCell>Rules</TableHeaderCell>
                <TableHeaderCell>Attachments</TableHeaderCell>
                <TableHeaderCell>Created</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {nsgs.map((n) => (
                <TableRow
                  key={n.id}
                  className={styles.rowClickable}
                  onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/nsgs/${n.id}`)}
                >
                  <TableCell>
                    <span className={styles.nameLink}>{n.name}</span>
                    <div className={styles.tableMutedCell}>{n.id}</div>
                  </TableCell>
                  <TableCell><StatusPill status={n.status} /></TableCell>
                  <TableCell>{n.rules?.length ?? 0}</TableCell>
                  <TableCell>{n.attachments?.length ?? 0}</TableCell>
                  <TableCell className={styles.tableMutedCell}>{fmtDate(n.created_at)}</TableCell>
                  <TableCell>
                    <RowActionsMenu>
                      <MenuItem
                        onClick={() => onDelete(n)}
                        disabled={!canWrite || deleteMutation.isPending || n.status === 'DELETING'}
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

      <Dialog open={createOpen} onOpenChange={(_, d) => setCreateOpen(d.open)}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Create security group</DialogTitle>
            <DialogContent>
              <div className={pageStyles.dialogForm}>
                <Field
                  label="Name"
                  required
                  hint="Lowercase letters, numbers, hyphens. Unique within the tenant."
                  validationState={name && !nameValid ? 'error' : 'none'}
                  validationMessage={
                    name && !nameValid
                      ? 'Must start with a letter; only lowercase letters, numbers, hyphens.'
                      : undefined
                  }
                >
                  <Input
                    value={name}
                    onChange={(_, d) => setName(d.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))}
                    placeholder="e.g. web-sg"
                  />
                </Field>
                <Field label="Description" hint="Optional, max 256 chars.">
                  <Textarea
                    value={description}
                    onChange={(_, d) => setDescription(d.value)}
                    placeholder="Allow HTTPS in, deny everything else"
                    rows={2}
                  />
                </Field>
                <Body1 style={{ color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 }}>
                  Rules and subnet attachments are added from the security group&apos;s detail page.
                </Body1>
              </div>
            </DialogContent>
            <DialogActions>
              <DialogTrigger disableButtonEnhancement>
                <Button appearance="subtle" disabled={createMutation.isPending}>Cancel</Button>
              </DialogTrigger>
              <Button
                appearance="primary"
                onClick={() => createMutation.mutate()}
                disabled={!nameValid || createMutation.isPending}
              >
                {createMutation.isPending ? 'Creating…' : 'Create'}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </div>
  );
}
