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
  Dropdown,
  Field,
  Input,
  Menu,
  MenuItem,
  MenuList,
  MenuPopover,
  MenuTrigger,
  Option,
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
  Box24Regular,
  MoreHorizontal20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import { EmptyState, ErrorState, LoadingState } from '../components/list/PageStates';
import { useListPageStyles } from '../components/list/useListPageStyles';
import SecretRevealBanner, { type Secret } from '../components/SecretRevealBanner';
import { useCan } from '../api/useCan';
import { PermissionTooltip } from '../components/PermissionTooltip';
import { listErrorMessage } from '../lib/apiError';
import { fmtDate } from '../lib/date';

const usePageStyles = makeStyles({
  rolePill: {
    display: 'inline-block',
    padding: `2px ${tokens.spacingHorizontalS}`,
    borderRadius: tokens.borderRadiusCircular,
    fontSize: tokens.fontSizeBase200,
    fontWeight: tokens.fontWeightMedium,
    textTransform: 'capitalize',
    backgroundColor: tokens.colorNeutralBackground3,
    color: tokens.colorNeutralForeground2,
  },
  dialogForm: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
});

interface ServiceAccount {
  id: string;
  name: string;
  role: string;
  description?: string;
  created_at: string;
  last_used?: string | null;
}

interface CreateSAResponse extends ServiceAccount {
  token: string;
}

export default function ServiceAccountsPage() {
  const styles = useListPageStyles();
  const pageStyles = usePageStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const { can } = useCan(tenantId, ['authorization/serviceAccounts/write'], projectId);
  const canWrite = can('authorization/serviceAccounts/write');
  const confirmDialog = useConfirmDialog();

  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState('');
  const [role, setRole] = useState<'owner' | 'member' | 'viewer'>('member');
  const [description, setDescription] = useState('');
  const [createdSecrets, setCreatedSecrets] = useState<{
    title: string;
    secrets: Secret[];
  } | null>(null);

  const tenantsQuery = useQuery({
    queryKey: ['tenants'],
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants');
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data ?? [];
    },
  });
  const myRoles = tenantsQuery.data?.find((t) => t.id === tenantId)?.roles ?? [];
  const isOwner = myRoles.includes('owner');

  const sasQuery = useQuery({
    queryKey: ['service-accounts', tenantId, projectId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/service-accounts', {
        params: { path: { tenant_id: tenantId!, project_id: projectId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return ((data as { service_accounts?: ServiceAccount[] }).service_accounts ?? []);
    },
  });

  const nameValid = /^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$/.test(name);

  const createMutation = useMutation({
    mutationFn: async () => {
      const body: Record<string, unknown> = { name, role };
      if (description.trim()) body.description = description.trim();
      const { data, error } = await api.POST('/v1/tenants/{tenant_id}/projects/{project_id}/service-accounts', {
        params: { path: { tenant_id: tenantId!, project_id: projectId! } },
        body: body as never,
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as CreateSAResponse;
    },
    onSuccess: (resp) => {
      setCreatedSecrets({
        title: `Service account token for ${resp.name}`,
        secrets: [
          {
            label: 'Bearer token',
            value: resp.token,
            filename: `${resp.name}.token`,
            multiline: false,
          },
        ],
      });
      queryClient.invalidateQueries({ queryKey: ['service-accounts', tenantId, projectId] });
      setCreateOpen(false);
      setName('');
      setRole('member');
      setDescription('');
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Create failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (saId: string) => {
      const { error } = await api.DELETE('/v1/tenants/{tenant_id}/projects/{project_id}/service-accounts/{sa_id}', {
        params: { path: { tenant_id: tenantId!, project_id: projectId!, sa_id: saId } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Service account deleted</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['service-accounts', tenantId, projectId] });
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const onDelete = async (sa: ServiceAccount) => {
    const ok = await confirmDialog({
      title: `Delete service account "${sa.name}"?`,
      body: 'The service account token will stop working immediately. Any automation or scripts using it will lose access. This cannot be undone.',
      confirmLabel: 'Delete',
      destructive: true,
      typeToConfirm: sa.name,
    });
    if (!ok) return;
    deleteMutation.mutate(sa.id);
  };

  const sas = sasQuery.data ?? [];
  const count = sas.length;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <div className={styles.header}>
        <Title2>Service accounts</Title2>
        <Subtitle1 className={styles.subtitle}>
          Tokens for automation (CI, scripts, terraform-provider). Each token authenticates as
          its own principal in tenant <strong>{tenantId}</strong>.
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create service accounts">
          <Button
            appearance="primary"
            icon={<Add20Regular />}
            onClick={() => setCreateOpen(true)}
            disabledFocusable={!isOwner || !canWrite}
          >
            Create service account
          </Button>
        </PermissionTooltip>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => sasQuery.refetch()}
          disabled={sasQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {createdSecrets && (
        <SecretRevealBanner
          title={createdSecrets.title}
          description="Save this token now — it cannot be retrieved later. Delete and recreate the service account if you lose it."
          secrets={createdSecrets.secrets}
          onDismiss={() => setCreatedSecrets(null)}
          onCopy={() => {}}
        />
      )}

      {sasQuery.isLoading && <LoadingState label="Loading service accounts…" />}

      {sasQuery.isError && !sasQuery.isLoading && (
        <ErrorState
          message={listErrorMessage(sasQuery.error, 'service accounts')}
        />
      )}

      {!sasQuery.isLoading && !sasQuery.isError && count === 0 && (
        <EmptyState
          icon={<Box24Regular />}
          title={`No service accounts in ${tenantId ?? 'this tenant'} yet`}
          description="Service accounts are non-human principals for automation — one per CI pipeline, Terraform workspace, or script that calls dc-api."
          action={
            isOwner ? (
              <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create service accounts">
                <Button
                  appearance="primary"
                  icon={<Add20Regular />}
                  onClick={() => setCreateOpen(true)}
                  disabledFocusable={!canWrite}
                >
                  Create service account
                </Button>
              </PermissionTooltip>
            ) : undefined
          }
          hint={
            !isOwner
              ? 'Owner role required to create service accounts. Contact the tenant owner.'
              : undefined
          }
        />
      )}

      {!sasQuery.isLoading && !sasQuery.isError && count > 0 && (
        <Card className={styles.tableCard}>
          <Table size="small" aria-label="Service accounts">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>Name</TableHeaderCell>
                <TableHeaderCell>Role</TableHeaderCell>
                <TableHeaderCell>Description</TableHeaderCell>
                <TableHeaderCell>Created</TableHeaderCell>
                <TableHeaderCell>Last used</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {sas.map((sa) => (
                <TableRow key={sa.id}>
                  <TableCell>
                    <Body1 style={{ fontWeight: 600 }}>{sa.name}</Body1>
                    <div className={styles.tableMutedCell}>{sa.id}</div>
                  </TableCell>
                  <TableCell><span className={pageStyles.rolePill}>{sa.role}</span></TableCell>
                  <TableCell className={styles.tableMutedCell}>{sa.description ?? '—'}</TableCell>
                  <TableCell className={styles.tableMutedCell}>{fmtDate(sa.created_at)}</TableCell>
                  <TableCell className={styles.tableMutedCell}>
                    {sa.last_used ? fmtDate(sa.last_used) : 'Never'}
                  </TableCell>
                  <TableCell>
                    {isOwner && (
                      <Menu>
                        <MenuTrigger disableButtonEnhancement>
                          <Button appearance="subtle" icon={<MoreHorizontal20Regular />} aria-label="Actions" />
                        </MenuTrigger>
                        <MenuPopover>
                          <MenuList>
                            <MenuItem onClick={() => onDelete(sa)} disabled={!canWrite || deleteMutation.isPending}>
                              Delete
                            </MenuItem>
                          </MenuList>
                        </MenuPopover>
                      </Menu>
                    )}
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
            <DialogTitle>Create service account</DialogTitle>
            <DialogContent>
              <div className={pageStyles.dialogForm}>
                <Field
                  label="Name"
                  required
                  hint="Lowercase, alphanumeric + hyphens. Unique within the tenant."
                  validationState={name && !nameValid ? 'error' : 'none'}
                  validationMessage={
                    name && !nameValid ? 'Must match [a-z0-9][a-z0-9-]*[a-z0-9].' : undefined
                  }
                >
                  <Input
                    value={name}
                    onChange={(_, d) => setName(d.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))}
                    placeholder="e.g. ci-deploy"
                  />
                </Field>
                <Field label="Role" required>
                  <Dropdown
                    value={role}
                    selectedOptions={[role]}
                    onOptionSelect={(_, d) =>
                      setRole((d.optionValue as 'owner' | 'member' | 'viewer') ?? 'member')
                    }
                  >
                    <Option value="owner" text="Owner">Owner — full control</Option>
                    <Option value="member" text="Member">Member — create + manage resources</Option>
                    <Option value="viewer" text="Viewer">Viewer — read-only</Option>
                  </Dropdown>
                </Field>
                <Field label="Description" hint="Optional. What this account is used for.">
                  <Textarea
                    value={description}
                    onChange={(_, d) => setDescription(d.value)}
                    placeholder="GitHub Actions deploy pipeline"
                    rows={2}
                  />
                </Field>
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
