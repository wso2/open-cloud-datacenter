import { makeStyles, shorthands, tokens } from '@fluentui/react-components';
import {
  Apps20Regular,
  Box20Regular,
  CloudCube20Regular,
  Database20Regular,
  Globe20Regular,
  History20Regular,
  Image20Regular,
  Key20Regular,
  LockClosed20Regular,
  NetworkCheck20Regular,
  PersonShield20Regular,
  Server20Regular,
  ShieldKeyhole20Regular,
  ShieldPerson20Regular,
} from '@fluentui/react-icons';
import { NavLink, useParams } from 'react-router-dom';
import type { ReactNode } from 'react';
import { useActiveProject } from '../hooks/useActiveProject';
import { useCan } from '../api/useCan';

const useStyles = makeStyles({
  nav: {
    width: '224px',
    flexShrink: 0,
    backgroundColor: tokens.colorNeutralBackground1,
    ...shorthands.borderRight('1px', 'solid', tokens.colorNeutralStroke2),
    padding: tokens.spacingHorizontalS,
    overflowY: 'auto',
  },
  scopeDivider: {
    marginTop: tokens.spacingVerticalL,
    marginBottom: tokens.spacingVerticalS,
    marginLeft: tokens.spacingHorizontalS,
    marginRight: tokens.spacingHorizontalS,
    borderTop: `1px solid ${tokens.colorNeutralStroke2}`,
  },
  scopeBanner: {
    padding: `${tokens.spacingVerticalS} ${tokens.spacingHorizontalS} 0`,
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    fontStyle: 'italic',
  },
  group: { padding: `${tokens.spacingVerticalM} ${tokens.spacingHorizontalS} ${tokens.spacingVerticalXS}` },
  groupLabel: {
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    fontWeight: 600,
  },
  link: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
    padding: `${tokens.spacingVerticalS} ${tokens.spacingHorizontalS}`,
    borderRadius: tokens.borderRadiusMedium,
    color: tokens.colorNeutralForeground2,
    textDecoration: 'none',
    fontSize: tokens.fontSizeBase300,
    transition: 'background-color 0.1s',
  },
  linkHover: { backgroundColor: tokens.colorNeutralBackground1Hover },
  linkActive: {
    backgroundColor: tokens.colorBrandBackground2,
    color: tokens.colorBrandForeground1,
    fontWeight: 600,
  },
});

interface NavItem {
  label: string;
  to: string;
  icon: ReactNode;
  /**
   * Read action gating this item. When set, the item only appears if the caller
   * holds it (checked via permissions:check). Items without an action — overview
   * pages and tenant-shared catalog reads that are membership-gated server-side —
   * always show. Keep these in sync with the GET-route gates in dc-api/router.go.
   */
  action?: string;
}

// Items scoped to the project (under /projects/:projectId/)
const projectGroups: { label: string; items: NavItem[] }[] = [
  {
    label: 'Overview',
    items: [
      { label: 'Dashboard', to: 'dashboard', icon: <Apps20Regular /> },
      { label: 'Activity', to: 'activity', icon: <History20Regular /> },
    ],
  },
  {
    label: 'Compute',
    items: [
      { label: 'Virtual machines', to: 'vms', icon: <Server20Regular />, action: 'compute/virtualMachines/read' },
      { label: 'Bastions', to: 'bastions', icon: <PersonShield20Regular />, action: 'compute/bastions/read' },
      { label: 'Clusters', to: 'clusters', icon: <CloudCube20Regular />, action: 'compute/clusters/read' },
    ],
  },
  {
    label: 'Networking',
    items: [
      { label: 'Virtual networks', to: 'vnets', icon: <NetworkCheck20Regular />, action: 'network/vnets/read' },
      { label: 'Security groups', to: 'nsgs', icon: <ShieldKeyhole20Regular />, action: 'network/nsgs/read' },
      { label: 'Private DNS', to: 'dns', icon: <Globe20Regular />, action: 'network/dnsZones/read' },
    ],
  },
  {
    label: 'Security',
    items: [
      { label: 'Key vaults', to: 'keyvaults', icon: <LockClosed20Regular />, action: 'keyvault/vaults/read' },
      { label: 'Service accounts', to: 'service-accounts', icon: <Box20Regular />, action: 'authorization/serviceAccounts/read' },
    ],
  },
  {
    label: 'Managed services',
    items: [
      { label: 'Databases', to: 'databases', icon: <Database20Regular />, action: 'database/servers/read' },
    ],
  },
  {
    label: 'Project',
    items: [
      { label: 'Access control', to: 'access', icon: <ShieldPerson20Regular />, action: 'authorization/roleAssignments/read' },
    ],
  },
];

// Items scoped to the tenant (not under /projects/)
const tenantGroups: { label: string; items: NavItem[] }[] = [
  {
    label: 'Catalog',
    items: [
      { label: 'Images', to: 'images', icon: <Image20Regular /> },
      { label: 'SSH keys', to: 'keys', icon: <Key20Regular /> },
    ],
  },
  {
    label: 'Tenant',
    items: [
      { label: 'Access control', to: 'iam', icon: <ShieldPerson20Regular />, action: 'authorization/roleAssignments/read' },
    ],
  },
];

// The distinct read actions each scope's nav items gate on. Project items are
// checked at PROJECT scope and tenant items at tenant scope, so a project Owner
// who holds only a narrow tenant role still sees the full project nav.
const actionsOf = (groups: { items: NavItem[] }[]) => [
  ...new Set(
    groups
      .flatMap((g) => g.items)
      .map((i) => i.action)
      .filter((a): a is string => Boolean(a)),
  ),
];
const PROJECT_ACTIONS = actionsOf(projectGroups);
const TENANT_ACTIONS = actionsOf(tenantGroups);

export default function SideNav() {
  const styles = useStyles();
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  // Project nav gated at PROJECT scope (honours project grants AND inherited
  // tenant grants); tenant nav at tenant scope. Two batched permissions:check
  // calls, one per scope.
  const projectScope = useCan(tenantId, PROJECT_ACTIONS, projectId);
  const tenantScope = useCan(tenantId, TENANT_ACTIONS);

  const tenantBase = tenantId ? `/tenants/${tenantId}` : '';
  const projectBase = tenantId && projectId ? `/tenants/${tenantId}/projects/${projectId}` : null;

  // Show overview/catalog items (no action) always; gate the rest on read access.
  // While the check is in flight, show everything so a full-access user sees no
  // flash — items the caller can't read drop out once the result arrives.
  const renderGroups = (
    groups: { label: string; items: NavItem[] }[],
    base: string,
    scope: { can: (a: string) => boolean; isLoading: boolean },
  ) =>
    groups.map((g) => {
      const items = g.items.filter(
        (item) => scope.isLoading || !item.action || scope.can(item.action),
      );
      if (items.length === 0) return null;
      return (
        <div key={g.label}>
          <div className={styles.group}>
            <span className={styles.groupLabel}>{g.label}</span>
          </div>
          {items.map((item) => (
            <NavLink
              key={item.to}
              to={`${base}/${item.to}`}
              className={({ isActive }) =>
                `${styles.link} ${isActive ? styles.linkActive : styles.linkHover}`
              }
            >
              {item.icon}
              <span>{item.label}</span>
            </NavLink>
          ))}
        </div>
      );
    });

  return (
    <aside className={styles.nav}>
      {/* Project-scoped items — only render when inside a project route */}
      {projectBase && renderGroups(projectGroups, projectBase, projectScope)}

      {/* Tenant-scoped items — always show when inside a tenant.
          When inside a project, show a divider + scope banner so users
          know clicking these leaves the project context. */}
      {tenantBase && projectBase && <div className={styles.scopeDivider} />}
      {tenantBase && projectBase && (
        <div className={styles.scopeBanner}>Tenant scope — leaves project context</div>
      )}
      {tenantBase && renderGroups(tenantGroups, tenantBase, tenantScope)}
    </aside>
  );
}
