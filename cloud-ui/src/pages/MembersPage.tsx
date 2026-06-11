import {
  Avatar,
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
  OverlayDrawer,
  DrawerBody,
  DrawerFooter,
  DrawerHeader,
  DrawerHeaderTitle,
  SearchBox,
  Spinner,
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
  Checkmark20Filled,
  Dismiss24Regular,
  MoreHorizontal20Regular,
  PersonAdd20Regular,
  ShieldPerson20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
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
  /** Two-line dropdown option (role picker + directory suggestions):
   *  primary text on top, secondary text underneath. */
  roleOption: {
    display: 'flex',
    flexDirection: 'column',
    gap: '2px',
  },
  roleOptionDesc: {
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
  },
  /** API error surfaced verbatim inside the invite dialog (422 copy is
   *  written for end users). */
  inviteError: {
    color: tokens.colorPaletteRedForeground1,
    fontSize: tokens.fontSizeBase200,
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

  // ── Member-picker Drawer (Azure "Select members" pattern) ──────────────
  picker: { width: '420px' },
  /** The selected-identity row inside the dialog: avatar + email/sub, with a
   *  "Select member" / "Change" button on the right. Reads like a chip. */
  identityRow: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalM,
    padding: tokens.spacingHorizontalS,
    border: `1px solid ${tokens.colorNeutralStroke1}`,
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: tokens.colorNeutralBackground1,
  },
  identityRowEmpty: {
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase300,
  },
  identityText: {
    display: 'flex',
    flexDirection: 'column',
    gap: '2px',
    flex: 1,
    minWidth: 0,
  },
  identityPrimary: {
    fontWeight: tokens.fontWeightSemibold,
    fontSize: tokens.fontSizeBase300,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  identitySecondary: {
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    fontFamily: tokens.fontFamilyMonospace,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  pickerBody: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalM,
    height: '100%',
  },
  pickerList: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXXS,
    overflowY: 'auto',
    // Fill the Drawer body; header (search) and footer stay fixed.
    flex: 1,
    minHeight: 0,
  },
  /** A single selectable directory user — a full-width radio-style button. */
  userRow: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalM,
    width: '100%',
    padding: tokens.spacingHorizontalS,
    border: '1px solid transparent',
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: 'transparent',
    cursor: 'pointer',
    textAlign: 'left',
    ':hover': { backgroundColor: tokens.colorNeutralBackground1Hover },
    ':focus-visible': {
      outline: `2px solid ${tokens.colorStrokeFocus2}`,
      outlineOffset: '-2px',
    },
  },
  userRowSelected: {
    backgroundColor: tokens.colorBrandBackground2,
    border: `1px solid ${tokens.colorBrandStroke1}`,
    ':hover': { backgroundColor: tokens.colorBrandBackground2Hover },
  },
  userRowText: {
    display: 'flex',
    flexDirection: 'column',
    gap: '2px',
    flex: 1,
    minWidth: 0,
  },
  userRowName: {
    fontWeight: tokens.fontWeightSemibold,
    fontSize: tokens.fontSizeBase300,
    color: tokens.colorNeutralForeground1,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  userRowEmail: {
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  userRowCheck: { color: tokens.colorBrandForeground1, flexShrink: 0 },
  pickerStatus: {
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    justifyContent: 'center',
    gap: tokens.spacingVerticalS,
    padding: tokens.spacingVerticalXXL,
    textAlign: 'center',
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
  },
  pickerError: {
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    gap: tokens.spacingVerticalS,
    padding: tokens.spacingVerticalL,
    textAlign: 'center',
    color: tokens.colorPaletteRedForeground1,
    fontSize: tokens.fontSizeBase200,
  },
  pickerCount: {
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    padding: `0 ${tokens.spacingHorizontalXS}`,
  },
  pickerFooter: {
    justifyContent: 'flex-end',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
  },
  pickerLoadMore: { alignSelf: 'center', marginTop: tokens.spacingVerticalXS },
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

/** Minimal user record from GET /v1/tenants/{id}/directory/users. */
interface DirectoryUser {
  sub: string;
  email: string;
  display_name?: string;
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

/** A scope-agnostic JSON fetch for the role-assignment / permissions:check calls.
 *  Resource-scope paths are dynamic per resource type, so they can't use the
 *  typed openapi-fetch client; cookie auth (credentials: include) carries the
 *  session regardless. Tenant/project keep using the typed client below. */
async function scopedJson(path: string, init?: RequestInit): Promise<unknown> {
  const res = await fetch(path, { credentials: 'include', ...init });
  if (res.status === 204) return null;
  const text = await res.text();
  if (!res.ok) {
    let msg = text;
    try {
      const j = JSON.parse(text);
      if (j && typeof j.error === 'string') msg = j.error;
    } catch {
      /* not JSON — use the raw text */
    }
    throw new Error(msg || res.statusText);
  }
  return text ? JSON.parse(text) : null;
}

interface MembersPageProps {
  /** When set, the panel manages access at RESOURCE scope using this path prefix
   *  (e.g. /v1/tenants/acme/projects/p1/virtual-machines/<uuid>). Resource detail
   *  pages pass it; the route-mounted tenant/project panel omits it. */
  resourceBase?: string;
  /** Human label for the resource (e.g. "web-server-01"), shown in the copy. */
  scopeLabel?: string;
}

export default function MembersPage({ resourceBase, scopeLabel }: MembersPageProps = {}) {
  const styles = useListPageStyles();
  const pageStyles = usePageStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId, projectId } = useParams<{ tenantId: string; projectId?: string }>();
  const isResource = Boolean(resourceBase);
  const isProject = !isResource && Boolean(projectId);
  // Distinct react-query key segment per scope (resource base is unique per resource).
  const scopeKey = resourceBase ?? projectId ?? 'tenant';
  const confirmDialog = useConfirmDialog();

  const [inviteOpen, setInviteOpen] = useState(false);
  // Identity to grant. When a directory is configured the picker resolves it to
  // a sub (`pickedSub`) plus the chip's email/alias. In directory-dark
  // deployments (probe 501) there is no directory to browse, so the field is a
  // plain dual-mode Input: an email (contains "@") or a raw OIDC sub.
  const [identity, setIdentity] = useState('');
  const [pickedSub, setPickedSub] = useState<string | null>(null);
  // Alias to send with a directory pick: the IdP display_name (falls back to
  // the email). Cleared in directory-dark mode, where no alias is sent.
  const [pickedAlias, setPickedAlias] = useState('');
  const [roleDefinition, setRoleDefinition] = useState('Contributor');

  // Member-picker Drawer (Azure "Select members"). Opens from inside the
  // dialog; rendered as a sibling so its overlay sits above the modal dialog
  // rather than fighting its focus trap.
  const [pickerOpen, setPickerOpen] = useState(false);
  // The row highlighted inside the Drawer, confirmed on "Select".
  const [draftUser, setDraftUser] = useState<DirectoryUser | null>(null);
  // Search box value, debounced into the browse query's filter.
  const [pickerSearch, setPickerSearch] = useState('');
  const [pickerFilter, setPickerFilter] = useState('');
  // How many pages are loaded; reset to 1 whenever the filter changes.
  const [pickerPages, setPickerPages] = useState(1);
  const PICKER_PAGE_SIZE = 20;
  useEffect(() => {
    const t = setTimeout(() => {
      setPickerFilter(pickerSearch.trim());
      setPickerPages(1);
    }, 300);
    return () => clearTimeout(t);
  }, [pickerSearch]);

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
    queryKey: ['perm-check', tenantId, scopeKey, ACTION_ROLE_WRITE],
    enabled: Boolean(tenantId),
    queryFn: async () => {
      if (resourceBase) {
        const data = await scopedJson(`${resourceBase}/permissions:check`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ actions: [ACTION_ROLE_WRITE] }),
        });
        return (data as { results?: { action: string; allowed: boolean }[] }).results ?? [];
      }
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

  // Feature-detect the IdP directory once per tenant when the dialog opens.
  // 501 is the stable "no directory provider configured" signal — only that
  // hides the type-ahead. A 502 (transient IdP failure) keeps it enabled.
  // Directory listing is tenant-level even when this dialog grants at
  // project/resource scope, so this always hits the tenant path.
  const directoryProbe = useQuery({
    queryKey: ['directory-probe', tenantId],
    enabled: inviteOpen && Boolean(tenantId),
    staleTime: Infinity,
    retry: false,
    queryFn: async () => {
      const { response } = await api.GET('/v1/tenants/{tenant_id}/directory/users', {
        params: { path: { tenant_id: tenantId! }, query: { limit: 1 } },
      });
      return response.status !== 501;
    },
  });
  const directoryDark = directoryProbe.data === false; // confirmed 501
  const directoryAvailable = directoryProbe.data === true;

  // Browse the tenant directory inside the picker Drawer. Pagination uses a
  // growing window from offset 0 (limit = pages * pageSize): a fresh fetch
  // returns the full prefix the list should show, so "Load more" can never
  // interleave or clobber, and react-query's per-key cache makes back-paging
  // free. The key carries tenantId + filter + the effective offset/limit, so a
  // stale page can never overwrite a newer filter's results.
  const pickerLimit = pickerPages * PICKER_PAGE_SIZE;
  const browseQuery = useQuery({
    queryKey: ['directory-users', tenantId, pickerFilter, 0, pickerLimit],
    enabled: pickerOpen && directoryAvailable && Boolean(tenantId),
    retry: false,
    // Keep the previous window visible while the next one loads ("Load more"
    // and filter changes don't flash the list empty).
    placeholderData: (prev) => prev,
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        '/v1/tenants/{tenant_id}/directory/users',
        {
          params: {
            path: { tenant_id: tenantId! },
            query: { filter: pickerFilter || undefined, limit: pickerLimit, offset: 0 },
          },
        },
      );
      if (response.status === 502) throw new Error('directory temporarily unavailable');
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return {
        users: (data?.users ?? []) as DirectoryUser[],
        total: data?.total_results ?? 0,
      };
    },
  });
  const browseUsers = browseQuery.data?.users ?? [];
  const browseTotal = browseQuery.data?.total ?? 0;
  const hasMore = browseUsers.length < browseTotal;

  const membersQuery = useQuery({
    queryKey: ['role-assignments', tenantId, scopeKey],
    enabled: Boolean(tenantId),
    queryFn: async () => {
      if (resourceBase) {
        const data = await scopedJson(`${resourceBase}/role-assignments`);
        return ((data as { role_assignments?: RoleAssignment[] }).role_assignments ?? []) as RoleAssignment[];
      }
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
      const id = identity.trim();
      const body: {
        user_sub?: string;
        user_email?: string;
        role_definition: string;
        display_alias?: string;
      } = { role_definition: roleDefinition };
      if (pickedSub) {
        // A directory pick already resolved the sub — send it directly and skip
        // server-side email re-resolution. The alias comes from the IdP
        // display_name (falling back to the email when display_name is empty).
        body.user_sub = pickedSub;
        if (pickedAlias) body.display_alias = pickedAlias;
      } else if (id.includes('@')) {
        // Directory-dark: raw email entry, no alias (server defaults it).
        body.user_email = id;
      } else {
        // Directory-dark: raw OIDC sub, no alias.
        body.user_sub = id;
      }

      if (resourceBase) {
        await scopedJson(`${resourceBase}/role-assignments`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        return;
      }
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
      queryClient.invalidateQueries({ queryKey: ['role-assignments', tenantId, scopeKey] });
      setInviteOpen(false);
      resetInviteForm();
    },
    // No error toast: failures (incl. the user-facing 422 copy for email
    // resolution) render verbatim inside the dialog, next to the form.
  });

  // Hoisted so `inviteMutation.onSuccess` (defined above) can call it. Clears
  // every field plus the picker's transient state back to the default.
  function resetInviteForm() {
    setIdentity('');
    setPickedSub(null);
    setPickedAlias('');
    setRoleDefinition('Contributor');
    setDraftUser(null);
    setPickerSearch('');
    setPickerFilter('');
    setPickerPages(1);
  }

  // Open the picker pre-seeded with the currently selected user (if any), so
  // re-opening "Change" keeps the highlight.
  const openPicker = () => {
    setDraftUser(pickedSub ? { sub: pickedSub, email: identity, display_name: pickedAlias } : null);
    setPickerSearch('');
    setPickerFilter('');
    setPickerPages(1);
    setPickerOpen(true);
  };

  // Confirm the Drawer selection: pin the sub, show the email in the dialog
  // chip, and carry the IdP display_name (or email fallback) as the alias.
  const confirmPicker = () => {
    if (!draftUser) return;
    setIdentity(draftUser.email);
    setPickedSub(draftUser.sub);
    setPickedAlias(draftUser.display_name?.trim() || draftUser.email);
    setPickerOpen(false);
  };

  const removeMutation = useMutation({
    mutationFn: async (principalId: string) => {
      if (resourceBase) {
        await scopedJson(`${resourceBase}/role-assignments/${principalId}`, { method: 'DELETE' });
        return;
      }
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
      queryClient.invalidateQueries({ queryKey: ['role-assignments', tenantId, scopeKey] });
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Remove failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const onRemove = async (m: RoleAssignment) => {
    const displayName = m.display_alias ?? m.principal_id;
    const scopeWord = isResource ? (scopeLabel ?? 'resource') : isProject ? 'project' : 'tenant';
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
        <Title2>
          {isResource
            ? `Access control · ${scopeLabel ?? 'resource'}`
            : isProject
            ? `Access control · project ${projectId}`
            : 'Access control'}
        </Title2>
        <Subtitle1 className={styles.subtitle}>
          {isResource ? (
            <>Roles granted on <strong>{scopeLabel ?? 'this resource'}</strong>.</>
          ) : isProject ? (
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
          title={isResource
            ? `No roles granted on ${scopeLabel ?? 'this resource'} yet`
            : `No members in ${(isProject ? projectId : tenantId) ?? (isProject ? 'this project' : 'this tenant')} yet`}
          description="Add a teammate by email (or by their OIDC subject) and grant them a role from the catalog. Roles range from Owner down to per-resource-type roles like Virtual Machine Contributor."
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
          You&apos;re the only one with a role on this {isResource ? 'resource' : isProject ? 'project' : 'tenant'}. Use the button above to add teammates.
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

      <Dialog
        open={inviteOpen}
        onOpenChange={(_, d) => {
          setInviteOpen(d.open);
          if (!d.open) {
            inviteMutation.reset();
            setPickerOpen(false);
            resetInviteForm();
          }
        }}
      >
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Add role assignment</DialogTitle>
            <DialogContent>
              <div className={pageStyles.dialogForm}>
                <Field
                  label="User"
                  required
                  hint={
                    pickedSub ? (
                      <>Selected from the directory · <code>{pickedSub}</code>.</>
                    ) : directoryDark ? (
                      <>Enter an email (looked up in the directory) or paste a raw OIDC `sub` — find it on the user&apos;s JWT.</>
                    ) : (
                      'Pick a teammate from the directory.'
                    )
                  }
                >
                  {directoryDark ? (
                    // Directory-dark (probe 501): no directory to browse, so the
                    // only path is a plain dual-mode Input (email or raw sub).
                    <Input
                      value={identity}
                      onChange={(_, d) => {
                        setIdentity(d.value);
                        setPickedSub(null);
                      }}
                      placeholder="01abc123-0000-0000-0000-user000000001"
                    />
                  ) : pickedSub ? (
                    // A directory member is selected — show it as a chip-like row.
                    <div className={pageStyles.identityRow}>
                      <Avatar name={identity} color="colorful" aria-hidden />
                      <div className={pageStyles.identityText}>
                        <span className={pageStyles.identityPrimary}>{identity}</span>
                        <span className={pageStyles.identitySecondary}>{pickedSub}</span>
                      </div>
                      <Button appearance="subtle" size="small" onClick={openPicker}>
                        Change
                      </Button>
                    </div>
                  ) : (
                    // Nothing chosen yet — the affordance to open the picker.
                    <div className={pageStyles.identityRow}>
                      <span className={pageStyles.identityRowEmpty}>No member selected</span>
                      <Button
                        appearance="primary"
                        size="small"
                        icon={<PersonAdd20Regular />}
                        style={{ marginLeft: 'auto' }}
                        onClick={openPicker}
                      >
                        Select member
                      </Button>
                    </div>
                  )}
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
                {inviteMutation.isError && (
                  <div className={pageStyles.inviteError} role="alert">
                    {(inviteMutation.error as Error).message}
                  </div>
                )}
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
                disabled={!identity.trim() || !roleDefinition || inviteMutation.isPending}
              >
                {inviteMutation.isPending ? 'Assigning…' : 'Assign role'}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>

      {/* Member picker — Azure "Select members" panel. Rendered as a sibling of
          the dialog (not nested inside it) so its overlay layers above the modal
          dialog cleanly. It only ever opens from the dialog's "Select member"
          button, which is itself behind the page's permission gate. */}
      <OverlayDrawer
        open={pickerOpen}
        onOpenChange={(_, d) => setPickerOpen(d.open)}
        position="end"
        className={pageStyles.picker}
        aria-label="Select a member from the directory"
      >
        <DrawerHeader>
          <DrawerHeaderTitle
            action={
              <Button
                appearance="subtle"
                icon={<Dismiss24Regular />}
                onClick={() => setPickerOpen(false)}
                aria-label="Close member picker"
              />
            }
          >
            Select member
          </DrawerHeaderTitle>
        </DrawerHeader>

        <DrawerBody className={pageStyles.pickerBody}>
          <SearchBox
            value={pickerSearch}
            onChange={(_, d) => setPickerSearch(d.value)}
            placeholder="Search by name or email"
            aria-label="Search the directory"
          />

          {/* First load (no placeholder data yet) */}
          {browseQuery.isLoading && (
            <div className={pageStyles.pickerStatus}>
              <Spinner size="small" label="Loading the directory…" />
            </div>
          )}

          {/* Non-fatal mid-browse failure (e.g. 502). The Drawer stays open and
              offers retry. */}
          {browseQuery.isError && !browseQuery.isLoading && (
            <div className={pageStyles.pickerError} role="alert">
              <span>Directory temporarily unavailable — try again.</span>
              <Button size="small" onClick={() => browseQuery.refetch()}>
                Try again
              </Button>
            </div>
          )}

          {/* Clean empty state */}
          {!browseQuery.isLoading && !browseQuery.isError && browseUsers.length === 0 && (
            <div className={pageStyles.pickerStatus}>
              {pickerFilter
                ? <span>No members match “{pickerFilter}”.</span>
                : <span>No members found in the directory.</span>}
            </div>
          )}

          {!browseQuery.isError && browseUsers.length > 0 && (
            <>
              <div className={pageStyles.pickerCount}>
                {browseUsers.length} of {browseTotal}
              </div>
              <div
                className={pageStyles.pickerList}
                role="radiogroup"
                aria-label="Directory members"
              >
                {browseUsers.map((u) => {
                  const selected = draftUser?.sub === u.sub;
                  return (
                    <button
                      key={u.sub}
                      type="button"
                      role="radio"
                      aria-checked={selected}
                      className={`${pageStyles.userRow} ${selected ? pageStyles.userRowSelected : ''}`}
                      onClick={() => setDraftUser(u)}
                      onDoubleClick={confirmPicker}
                    >
                      <Avatar name={u.display_name ?? u.email} color="colorful" aria-hidden />
                      <span className={pageStyles.userRowText}>
                        <span className={pageStyles.userRowName}>{u.display_name ?? u.email}</span>
                        <span className={pageStyles.userRowEmail}>{u.email}</span>
                      </span>
                      {selected && <Checkmark20Filled className={pageStyles.userRowCheck} />}
                    </button>
                  );
                })}
                {hasMore && (
                  <Button
                    className={pageStyles.pickerLoadMore}
                    appearance="subtle"
                    size="small"
                    disabled={browseQuery.isFetching}
                    onClick={() => setPickerPages((p) => p + 1)}
                  >
                    {browseQuery.isFetching ? 'Loading…' : 'Load more'}
                  </Button>
                )}
              </div>
            </>
          )}
        </DrawerBody>

        <DrawerFooter className={pageStyles.pickerFooter}>
          <Button appearance="secondary" onClick={() => setPickerOpen(false)}>
            Cancel
          </Button>
          <Button appearance="primary" disabled={!draftUser} onClick={confirmPicker}>
            Select
          </Button>
        </DrawerFooter>
      </OverlayDrawer>
    </div>
  );
}
