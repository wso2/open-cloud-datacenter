import {
  Button,
  Menu,
  MenuDivider,
  MenuItem,
  MenuList,
  MenuPopover,
  MenuTrigger,
  Spinner,
  makeStyles,
  tokens,
} from '@fluentui/react-components';
import {
  Add16Regular,
  ArrowExit16Regular,
  Checkmark16Filled,
  ChevronDown16Regular,
  Cube20Regular,
  Dismiss16Regular,
} from '@fluentui/react-icons';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useAuth } from '../auth/useAuth';
import RegisterProjectDialog from '../components/RegisterProjectDialog';

const useStyles = makeStyles({
  triggerGroup: {
    display: 'flex',
    alignItems: 'center',
    gap: '2px',
  },
  trigger: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
  },
  label: {
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
  },
  name: { fontWeight: 600 },
  menuRow: {
    display: 'flex',
    flexDirection: 'column',
    gap: '2px',
    flex: 1,
    minWidth: 0,
  },
  menuRowName: {
    fontWeight: tokens.fontWeightSemibold,
    fontSize: tokens.fontSizeBase300,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  menuRowId: {
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    fontFamily: tokens.fontFamilyMonospace,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  checkIcon: {
    color: tokens.colorBrandForeground1,
    flexShrink: 0,
    width: '16px',
    height: '16px',
  },
  checkPlaceholder: {
    width: '16px',
    flexShrink: 0,
  },
  menuItem: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
    paddingTop: tokens.spacingVerticalS,
    paddingBottom: tokens.spacingVerticalS,
    width: '240px',
  },
  createRow: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
    color: tokens.colorBrandForeground1,
    fontWeight: tokens.fontWeightSemibold,
  },
  emptyMsg: {
    padding: `${tokens.spacingVerticalM} ${tokens.spacingHorizontalL}`,
    maxWidth: '240px',
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
    lineHeight: tokens.lineHeightBase300,
  },
});

interface Project {
  id: string;
  name: string;
  description?: string;
}

export default function ProjectSwitcher() {
  const styles = useStyles();
  const api = useApi();
  const navigate = useNavigate();
  const { tenantId, projectId } = useParams<{ tenantId: string; projectId?: string }>();
  const { user } = useAuth();
  const isAdmin = user?.isAdmin ?? false;
  const [dialogOpen, setDialogOpen] = useState(false);

  // Check if user is owner of this tenant
  const tenantsQuery = useQuery({
    queryKey: ['tenants'],
    enabled: Boolean(tenantId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants');
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data ?? [];
    },
  });
  const myRoles = tenantsQuery.data?.find((t) => t.id === tenantId)?.roles ?? [];
  const isOwner = myRoles.includes('owner');
  const canCreate = isAdmin || isOwner;

  const projectsQuery = useQuery({
    queryKey: ['projects', tenantId],
    enabled: Boolean(tenantId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects', {
        params: { path: { tenant_id: tenantId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as Project[];
    },
  });

  // Only render when we're inside a tenant
  if (!tenantId) return null;

  const projects = projectsQuery.data ?? [];
  const activeProject = projects.find((p) => p.id === projectId);

  const onSelect = (id: string) => {
    if (id !== projectId) navigate(`/tenants/${tenantId}/projects/${id}/dashboard`);
  };

  const onClearProject = () => {
    navigate(`/tenants/${tenantId}`);
  };

  if (projectsQuery.isLoading) {
    return (
      <Button appearance="subtle" icon={<Cube20Regular />} disabled>
        <div className={styles.trigger}>
          <span className={styles.label}>Project</span>
          <Spinner size="tiny" />
        </div>
      </Button>
    );
  }

  // Show the picker whenever there's at least one project to select — selecting a
  // project is navigation, available to any member, NOT an owner/admin action.
  // (Creating a project is still gated by canCreate, inside the menu.) Without
  // this, a non-owner in a single-project tenant got a disabled label and could
  // never enter the project, so the resource nav never appeared.
  const showMenu = projects.length >= 1 || canCreate;

  if (!showMenu) {
    const sole = projects[0];
    if (!sole) return null;
    return (
      <Button appearance="subtle" icon={<Cube20Regular />} disabled>
        <div className={styles.trigger}>
          <span className={styles.label}>Project</span>
          <span className={styles.name}>{sole.name ?? sole.id}</span>
        </div>
      </Button>
    );
  }

  const triggerLabel =
    activeProject?.name ??
    activeProject?.id ??
    (projectId ? projectId : 'Select project');

  return (
    <>
      <div className={styles.triggerGroup}>
        <Menu>
          <MenuTrigger disableButtonEnhancement>
            <Button appearance="subtle" icon={<Cube20Regular />}>
              <div className={styles.trigger}>
                <span className={styles.label}>Project</span>
                <span className={styles.name}>{triggerLabel}</span>
                <ChevronDown16Regular />
              </div>
            </Button>
          </MenuTrigger>

          <MenuPopover>
          <MenuList>
            {projectsQuery.isError && (
              <MenuItem disabled>Failed to load projects</MenuItem>
            )}

            {!projectsQuery.isError && projects.length === 0 && (
              <div className={styles.emptyMsg}>
                {canCreate
                  ? 'No projects yet. Create the first one below.'
                  : 'No projects yet. Ask an owner to create one.'}
              </div>
            )}

            {projects.map((p) => {
              const isActive = p.id === projectId;
              const displayName = p.name ?? p.id;
              const showId = p.name && p.name !== p.id;

              return (
                <MenuItem key={p.id} onClick={() => onSelect(p.id)}>
                  <div className={styles.menuItem}>
                    {isActive ? (
                      <Checkmark16Filled className={styles.checkIcon} />
                    ) : (
                      <span className={styles.checkPlaceholder} />
                    )}
                    <div className={styles.menuRow}>
                      <span className={styles.menuRowName}>{displayName}</span>
                      {showId && (
                        <span className={styles.menuRowId}>{p.id}</span>
                      )}
                    </div>
                  </div>
                </MenuItem>
              );
            })}

            {projectId && (
              <>
                <MenuDivider />
                <MenuItem onClick={onClearProject} icon={<ArrowExit16Regular />}>
                  Back to tenant overview
                </MenuItem>
              </>
            )}

            {canCreate && (
              <>
                {projects.length > 0 && <MenuDivider />}
                <MenuItem onClick={() => setDialogOpen(true)}>
                  <div className={styles.createRow}>
                    <Add16Regular />
                    Create project
                  </div>
                </MenuItem>
              </>
            )}
          </MenuList>
        </MenuPopover>
        </Menu>

        {projectId && (
          <Button
            appearance="subtle"
            size="small"
            icon={<Dismiss16Regular />}
            onClick={onClearProject}
            aria-label="Back to tenant overview"
            title="Back to tenant overview"
          />
        )}
      </div>

      <RegisterProjectDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        tenantId={tenantId}
      />
    </>
  );
}
