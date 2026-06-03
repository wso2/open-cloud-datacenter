import {
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
  MoreHorizontal20Regular,
  ShieldPerson20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useConfirmDialog } from '../components/useConfirmDialog';
import { EmptyState, ErrorState, LoadingState } from '../components/list/PageStates';
import { useListPageStyles } from '../components/list/useListPageStyles';
import { fmtDate } from '../lib/date';

/** Action that gates managing access at this scope (inviting/removing). */
const ACTION_ROLE_WRITE = 'authorization/roleAssignments/write';

const usePageStyles = makeStyles({
  rolePill: {
    display: 'inline-block',
    padding: `2px ${tokens.spacingHorizontalS}`,
    borderRadius: tokens.borderRadiusCircular,
    fontSize: tokens.fontSizeBase200,
    fontWeight: tokens.fontWeightMedium,
    whiteSpace: 'nowrap',
  },
  roleOwner: {
    backgroundColor: tokens.colorBrandBackground2,
    color: tokens.colorBrandForeground1,
  },
  roleContributor: {
    backgroundColor: tokens.colorPaletteGreenBackground1,
    color: tokens.colorPaletteGreenForeground2,
  },
  roleReader: {
    backgroundColor: tokens.colorNeutralBackground3,
    color: tokens.colorNeutralForeground2,
  },
  dialogForm: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  /** Two-line role option: display name on top, description underneath. */
  roleOption: {
    display: 'flex',
    flexDirection: 'column',
    gap: '2px',
  },
  roleOptionDesc: {
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
  },
  /** Two-line principal cell: alias on top, sub underneath in muted mono. */
  principalCell: {
    display: 'flex',
    flexDirection: 'column',
    gap: '2px',
  },
  principalAlias: {
    fontWeight: tokens.fontWeightSemibold,
    fontSize: tokens.fontSizeBase300,
  },
  principalSub: {
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    fontFamily: tokens.fontFamilyMonospace,
  },
});

interface RoleAssignment {
  id: string;
  principal_type: string;
  principal_id: string;
  scope_id: string;
  /** RBAC v2 role-definition key, e.g. "Contributor" or "VirtualMachineContributor". */
  role_definition: string;
  granted_at: string;
  granted_by: string;
  /** Admin-set mnemonic; shown instead of principal_id when present. */
  display_alias?: string;
}

interface RoleDefinition {
  key: string;
  display_name: string;
  description: string;
}

/** Colour a role pill by its broad shape: Owner, read-only Reader, or anything
 *  that can change things (Contributor + the per-resource-type roles). */
function RolePill({
  roleKey,
  label,
  classes,
}: {
  roleKey: string;
  label: string;
  classes: ReturnType<typeof usePageStyles>;
}) {
  const cls =
    roleKey === 'Owner'
      ? classes.roleOwner
      : roleKey === 'Reader'
      ? classes.roleReader
      : classes.roleContributor;
  return <span className={`${classes.rolePill} ${cls}`}>{label}</span>;
}

export default function MembersPage() {
  const styles = useListPageStyles();
  const pageStyles = usePageStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId, projectId } = useParams<{ tenantId: string; projectId?: string }>();
  const isProject = Boolean(projectId);
  const confirmDialog = useConfirmDialog();

  const [inviteOpen, setInviteOpen] = useState(false);
  const [userSub, setUserSub] = useState('');
  const [displayAlias, setDisplayAlias] = useState('');
  const [roleDefinition, setRoleDefinition] = useState('Contributor');

  // The assignable role catalog — drives the picker and resolves keys to display
  // names. The UI never invents role logic; dc-api owns the catalog.
  const catalogQuery = useQuery({
    queryKey: ['role-definitions'],
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/role-definitions');
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data?.role_definitions ?? []) as RoleDefinition[];
    },
  });
  const catalog = catalogQuery.data ?? [];
  const displayNameFor = (key: string) => catalog.find((r) => r.key === key)?.display_name ?? key;

  // Can the caller manage access here? Ask dc-api — it runs the matcher, we just
  // read the boolean. Never derive this from role names in the browser. At project
  // scope this MUST hit the project endpoint so a project-Owner who lacks tenant
  // write is still allowed to manage project access.
  const canManageQuery = useQuery({
    queryKey: ['perm-check', tenantId, projectId ?? 'tenant', ACTION_ROLE_WRITE],
    enabled: Boolean(tenantId),
    queryFn: async () => {
      if (projectId) {
        const { data, error } = await api.POST(
          '/v1/tenants/{tenant_id}/projects/{project_id}/permissions:check',
          {
            params: { path: { tenant_id: tenantId!, project_id: projectId } },
            body: { actions: [ACTION_ROLE_WRITE] },
          },
        );
        if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
        return data?.results ?? [];
      } else {
        const { data, error } = await api.POST('/v1/tenants/{tenant_id}/permissions:check', {
          params: { path: { tenant_id: tenantId! } },
          body: { actions: [ACTION_ROLE_WRITE] },
        });
        if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
        return data?.results ?? [];
      }
    },
  });
  const canManageAccess = (canManageQuery.data ?? []).some(
    (r) => r.action === ACTION_ROLE_WRITE && r.allowed,
  );

  const membersQuery = useQuery({
    queryKey: ['role-assignments', tenantId, projectId ?? 'tenant'],
    enabled: Boolean(tenantId),
    queryFn: async () => {
      if (projectId) {
        const { data, error } = await api.GET(
          '/v1/tenants/{tenant_id}/projects/{project_id}/role-assignments',
          { params: { path: { tenant_id: tenantId!, project_id: projectId } } },
        );
        if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
        return ((data as { role_assignments?: RoleAssignment[] }).role_assignments ?? []) as RoleAssignment[];
      } else {
        const { data, error } = await api.GET('/v1/tenants/{tenant_id}/role-assignments', {
          params: { path: { tenant_id: tenantId! } },
        });
        if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
        return ((data as { role_assignments?: RoleAssignment[] }).role_assignments ?? []) as RoleAssignment[];
      }
    },
  });

  const inviteMutation = useMutation({
    mutationFn: async () => {
      const body: { user_sub: string; role_definition: string; display_alias?: string } = {
        user_sub: userSub.trim(),
        role_definition: roleDefinition,
      };
      if (displayAlias.trim()) body.display_alias = displayAlias.trim();

      if (projectId) {
        const { error } = await api.POST(
          '/v1/tenants/{tenant_id}/projects/{project_id}/role-assignments',
          {
            params: { path: { tenant_id: tenantId!, project_id: projectId } },
            body: body as never,
          },
        );
        if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      } else {
        const { error } = await api.POST('/v1/tenants/{tenant_id}/role-assignments', {
          params: { path: { tenant_id: tenantId! } },
          body: body as never,
        });
        if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      }
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Member invited</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['role-assignments', tenantId, projectId ?? 'tenant'] });
      setInviteOpen(false);
      setUserSub('');
      setDisplayAlias('');
      setRoleDefinition('Contributor');
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Invite failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const removeMutation = useMutation({
    mutationFn: async (principalId: string) => {
      if (projectId) {
        const { error } = await api.DELETE(
          '/v1/tenants/{tenant_id}/projects/{project_id}/role-assignments/{principal_id}',
          {
            params: { path: { tenant_id: tenantId!, project_id: projectId, principal_id: principalId } },
          },
        );
        if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      } else {
        const { error } = await api.DELETE('/v1/tenants/{tenant_id}/role-assignments/{principal_id}', {
          params: { path: { tenant_id: tenantId!, principal_id: principalId } },
        });
        if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      }
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Member removed</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['role-assignments', tenantId, projectId ?? 'tenant'] });
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Remove failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const onRemove = async (m: RoleAssignment) => {
    const displayName = m.display_alias ?? m.principal_id;
    const scopeWord = isProject ? 'project' : 'tenant';
    const ok = await confirmDialog({
      title: `Remove "${displayName}"?`,
      body: `This will revoke ${displayNameFor(m.role_definition)} access to the ${scopeWord} immediately. The user will lose the ability to manage any resources here. This action can be reversed by adding them again.`,
      confirmLabel: 'Remove member',
      destructive: true,
    });
    if (!ok) return;
    removeMutation.mutate(m.principal_id);
  };

  const members = membersQuery.data ?? [];
  const count = members.length;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <div className={styles.header}>
        <Title2>{isProject ? `Access control · project ${projectId}` : 'Access control'}</Title2>
        <Subtitle1 className={styles.subtitle}>
          {isProject ? (
            <>Members of project <strong>{projectId}</strong> and their roles.</>
          ) : (
            <>Members of tenant <strong>{tenantId}</strong> and their roles.</>
          )}
          {!canManageAccess && canManageQuery.isSuccess && ' Read-only — you need permission to manage access here.'}
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <Button
          appearance="primary"
          icon={<Add20Regular />}
          onClick={() => setInviteOpen(true)}
          disabled={!canManageAccess}
        >
          Add role assignment
        </Button>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => membersQuery.refetch()}
          disabled={membersQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {membersQuery.isLoading && <LoadingState label="Loading members…" />}

      {membersQuery.isError && !membersQuery.isLoading && (
        <ErrorState
          message={`Failed to load members: ${(membersQuery.error as Error).message}`}
        />
      )}

      {!membersQuery.isLoading && !membersQuery.isError && count === 0 && (
        <EmptyState
          icon={<ShieldPerson20Regular />}
          title={`No members in ${(isProject ? projectId : tenantId) ?? (isProject ? 'this project' : 'this tenant')} yet`}
          description="Add a principal by their OIDC subject and grant them a role from the catalog. Roles range from Owner down to per-resource-type roles like Virtual Machine Contributor."
          action={
            canManageAccess ? (
              <Button
                appearance="primary"
                icon={<Add20Regular />}
                onClick={() => setInviteOpen(true)}
              >
                Add role assignment
              </Button>
            ) : undefined
          }
        />
      )}

      {!membersQuery.isLoading && !membersQuery.isError && count === 1 && canManageAccess && (
        <Subtitle1 style={{ color: tokens.colorNeutralForeground3, fontWeight: 400, fontSize: tokens.fontSizeBase200 }}>
          You&apos;re the only member of this {isProject ? 'project' : 'tenant'}. Use the button above to add teammates.
        </Subtitle1>
      )}

      {!membersQuery.isLoading && !membersQuery.isError && count > 0 && (
        <Card className={styles.tableCard}>
          <Table size="small" aria-label="Members">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>User</TableHeaderCell>
                <TableHeaderCell>Role</TableHeaderCell>
                <TableHeaderCell>Granted</TableHeaderCell>
                <TableHeaderCell>Granted by</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {members.map((m) => (
                <TableRow key={m.id}>
                  <TableCell>
                    {m.display_alias ? (
                      <div className={pageStyles.principalCell}>
                        <span className={pageStyles.principalAlias}>{m.display_alias}</span>
                        <span className={pageStyles.principalSub}>{m.principal_id}</span>
                      </div>
                    ) : (
                      <span className={styles.tableMonoCell}>{m.principal_id}</span>
                    )}
                  </TableCell>
                  <TableCell>
                    <RolePill
                      roleKey={m.role_definition}
                      label={displayNameFor(m.role_definition)}
                      classes={pageStyles}
                    />
                  </TableCell>
                  <TableCell className={styles.tableMutedCell}>{fmtDate(m.granted_at)}</TableCell>
                  <TableCell className={styles.tableMonoCell} style={{ color: tokens.colorNeutralForeground3 }}>
                    {m.granted_by}
                  </TableCell>
                  <TableCell>
                    {canManageAccess && (
                      <Menu>
                        <MenuTrigger disableButtonEnhancement>
                          <Button appearance="subtle" icon={<MoreHorizontal20Regular />} aria-label="Actions" />
                        </MenuTrigger>
                        <MenuPopover>
                          <MenuList>
                            <MenuItem onClick={() => onRemove(m)} disabled={removeMutation.isPending}>
                              Remove
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

      <Dialog open={inviteOpen} onOpenChange={(_, d) => setInviteOpen(d.open)}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Add role assignment</DialogTitle>
            <DialogContent>
              <div className={pageStyles.dialogForm}>
                <Field label="User subject (OIDC sub)" required hint="The user's OIDC `sub` claim — find it on their JWT.">
                  <Input
                    value={userSub}
                    onChange={(_, d) => setUserSub(d.value)}
                    placeholder="01abc123-0000-0000-0000-user000000001"
                  />
                </Field>
                <Field
                  label="Display name (optional)"
                  hint="Mnemonic shown in the members list. Not visible to the user, not synced to the IdP."
                >
                  <Input
                    value={displayAlias}
                    onChange={(_, d) => setDisplayAlias(d.value)}
                    placeholder="e.g. alice-wso2"
                  />
                </Field>
                <Field
                  label="Role"
                  required
                  hint={catalogQuery.isError ? 'Could not load the role catalog.' : undefined}
                  validationState={catalogQuery.isError ? 'error' : 'none'}
                >
                  <Dropdown
                    value={displayNameFor(roleDefinition)}
                    selectedOptions={[roleDefinition]}
                    onOptionSelect={(_, d) => setRoleDefinition(d.optionValue ?? 'Contributor')}
                    disabled={catalogQuery.isLoading}
                    placeholder={catalogQuery.isLoading ? 'Loading roles…' : 'Select a role'}
                  >
                    {catalog.map((r) => (
                      <Option key={r.key} value={r.key} text={r.display_name}>
                        <div className={pageStyles.roleOption}>
                          <span>{r.display_name}</span>
                          <span className={pageStyles.roleOptionDesc}>{r.description}</span>
                        </div>
                      </Option>
                    ))}
                  </Dropdown>
                </Field>
              </div>
            </DialogContent>
            <DialogActions>
              <DialogTrigger disableButtonEnhancement>
                <Button appearance="subtle" disabled={inviteMutation.isPending}>
                  Cancel
                </Button>
              </DialogTrigger>
              <Button
                appearance="primary"
                onClick={() => inviteMutation.mutate()}
                disabled={!userSub.trim() || !roleDefinition || inviteMutation.isPending}
              >
                {inviteMutation.isPending ? 'Assigning…' : 'Assign role'}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </div>
  );
}
