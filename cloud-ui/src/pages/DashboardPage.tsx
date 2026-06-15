import {
  Body1,
  Button,
  Card,
  ProgressBar,
  Spinner,
  Subtitle1,
  Title2,
  makeStyles,
  mergeClasses,
  tokens,
} from '@fluentui/react-components';
import {
  Add20Regular,
  CloudArrowUp24Regular,
  Database24Regular,
  Key24Regular,
  Globe24Regular,
  Server24Regular,
  Apps24Regular,
} from '@fluentui/react-icons';
import { useQuery } from '@tanstack/react-query';
import { Fragment, type ReactNode } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { ACTIVITY_RESOURCE_ROUTES, useActivityQuery } from '../api/activity';
import {
  inventorySummary,
  regionDotVariant,
  useRegionsQuery,
  useZoneInventoryQuery,
  zoneSummary,
} from '../api/regions';
import { useAuth } from '../auth/useAuth';
import { useActiveProject } from '../hooks/useActiveProject';
import { fmtDate } from '../lib/date';

/**
 * Project dashboard: resource inventory with status breakdown, tenant
 * capacity, this project's quota, resources needing attention, and the
 * most recently created resources. Everything renders from the existing
 * list/cap-usage endpoints — each card loads independently so a slow
 * backend list never blanks the whole page.
 */

interface ResourceItem {
  id: string;
  name?: string;
  status?: string;
  created_at?: string;
}

interface ResourceKind {
  key: string;
  label: string;
  path: string; // route segment AND API path segment suffix
  icon: ReactNode;
}

const KINDS: ResourceKind[] = [
  { key: 'vms', label: 'Virtual machines', path: 'virtual-machines', icon: <Server24Regular /> },
  { key: 'clusters', label: 'Clusters', path: 'clusters', icon: <Apps24Regular /> },
  { key: 'vnets', label: 'Virtual networks', path: 'vnets', icon: <Globe24Regular /> },
  { key: 'keyvaults', label: 'Key vaults', path: 'keyvaults', icon: <Key24Regular /> },
  { key: 'databases', label: 'Databases', path: 'databases', icon: <Database24Regular /> },
  { key: 'bastions', label: 'Bastions', path: 'bastions', icon: <CloudArrowUp24Regular /> },
];

/** Route segment for a kind (differs from the API segment for VMs). */
function routeFor(kind: ResourceKind): string {
  return kind.path === 'virtual-machines' ? 'vms' : kind.path;
}

const FAILED = new Set(['FAILED', 'ERROR', 'DEGRADED']);
const TRANSITIONING = new Set(['PENDING', 'DELETING', 'PROVISIONING', 'UPDATING']);

const useStyles = makeStyles({
  root: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
    padding: tokens.spacingHorizontalXXL,
    maxWidth: '1400px',
  },
  headerRow: {
    display: 'flex',
    alignItems: 'flex-end',
    justifyContent: 'space-between',
    flexWrap: 'wrap',
    gap: tokens.spacingHorizontalM,
  },
  quickActions: { display: 'flex', gap: tokens.spacingHorizontalS, flexWrap: 'wrap' },
  inventoryGrid: {
    display: 'grid',
    gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))',
    gap: tokens.spacingHorizontalM,
  },
  invCard: {
    cursor: 'pointer',
    padding: tokens.spacingHorizontalL,
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalS,
  },
  invHead: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
    color: tokens.colorNeutralForeground3,
  },
  invCount: { fontSize: tokens.fontSizeHero800, fontWeight: 600, lineHeight: 1 },
  invStatus: {
    display: 'flex',
    gap: tokens.spacingHorizontalS,
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
    minHeight: '16px',
    flexWrap: 'wrap',
  },
  dotOk: { color: tokens.colorPaletteGreenForeground1 },
  dotWarn: { color: tokens.colorPaletteMarigoldForeground1 },
  dotBad: { color: tokens.colorPaletteRedForeground1 },
  dotUnknown: { color: tokens.colorNeutralForeground3 },
  twoCol: {
    display: 'grid',
    gridTemplateColumns: 'repeat(auto-fit, minmax(340px, 1fr))',
    gap: tokens.spacingHorizontalM,
    alignItems: 'start',
  },
  sectionCard: {
    padding: tokens.spacingHorizontalL,
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalM,
  },
  gaugeRow: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalXS },
  gaugeLabelRow: {
    display: 'flex',
    justifyContent: 'space-between',
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground2,
  },
  quotaGrid: {
    display: 'grid',
    gridTemplateColumns: '1fr 1fr',
    rowGap: tokens.spacingVerticalS,
    columnGap: tokens.spacingHorizontalL,
    fontSize: tokens.fontSizeBase300,
  },
  quotaLabel: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
  listRow: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    gap: tokens.spacingHorizontalM,
    paddingTop: tokens.spacingVerticalXS,
    paddingBottom: tokens.spacingVerticalXS,
    cursor: 'pointer',
  },
  listRowStatic: { cursor: 'default' },
  listName: { fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' },
  listMeta: {
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
    whiteSpace: 'nowrap',
    display: 'inline-flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
  },
  empty: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
  cardError: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
});

function useResourceList(kind: ResourceKind, tenantId?: string, projectId?: string) {
  const api = useApi();
  return useQuery({
    queryKey: ['dashboard', kind.key, tenantId, projectId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    staleTime: 15_000,
    queryFn: async (): Promise<ResourceItem[]> => {
      const { data, error } = await api.GET(
        `/v1/tenants/{tenant_id}/projects/{project_id}/${kind.path}` as '/v1/tenants/{tenant_id}/projects/{project_id}/virtual-machines',
        { params: { path: { tenant_id: tenantId!, project_id: projectId! } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as ResourceItem[];
    },
  });
}

function statusBuckets(items: ResourceItem[]) {
  let ok = 0;
  let warn = 0;
  let bad = 0;
  for (const it of items) {
    const s = (it.status ?? '').toUpperCase();
    if (FAILED.has(s)) bad += 1;
    else if (TRANSITIONING.has(s)) warn += 1;
    else ok += 1;
  }
  return { ok, warn, bad };
}

function Gauge({ label, used, total, unit }: { label: string; used: number; total: number; unit: string }) {
  const styles = useStyles();
  const ratio = total > 0 ? Math.min(used / total, 1) : 0;
  return (
    <div className={styles.gaugeRow}>
      <div className={styles.gaugeLabelRow}>
        <span>{label}</span>
        <span>
          {used} / {total} {unit}
        </span>
      </div>
      <ProgressBar value={ratio} color={ratio > 0.9 ? 'error' : ratio > 0.75 ? 'warning' : 'brand'} />
    </div>
  );
}

/**
 * Admin-only inventory sub-line for an `up` zone: fetches live capacity from the
 * zone's agent and renders a compact summary. Renders nothing while loading or
 * if the agent is momentarily unreachable, so it never clutters the card.
 */
function ZoneInventoryLine({ region, zone }: { region: string; zone: string }) {
  const styles = useStyles();
  const inv = useZoneInventoryQuery(region, zone, true);
  if (!inv.data) return null;
  return (
    <div className={mergeClasses(styles.listRow, styles.listRowStatic)}>
      <span className={styles.listMeta}>↳ {zone}</span>
      <span className={styles.listMeta}>{inventorySummary(inv.data)}</span>
    </div>
  );
}

export default function DashboardPage() {
  const styles = useStyles();
  const { user } = useAuth();
  const isAdmin = user?.isAdmin ?? false;
  const api = useApi();
  const navigate = useNavigate();
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();

  // One query per resource kind — unrolled so each hook call is explicit
  // (rules-of-hooks forbids calling hooks in a loop). Order matches KINDS.
  const listQueries = [
    useResourceList(KINDS[0], tenantId, projectId),
    useResourceList(KINDS[1], tenantId, projectId),
    useResourceList(KINDS[2], tenantId, projectId),
    useResourceList(KINDS[3], tenantId, projectId),
    useResourceList(KINDS[4], tenantId, projectId),
    useResourceList(KINDS[5], tenantId, projectId),
  ];

  const capQuery = useQuery({
    queryKey: ['cap-usage', tenantId],
    enabled: Boolean(tenantId),
    staleTime: 30_000,
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/cap-usage', {
        params: { path: { tenant_id: tenantId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data;
    },
  });

  const projectQuery = useQuery({
    queryKey: ['projects', tenantId],
    enabled: Boolean(tenantId),
    staleTime: 30_000,
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects', {
        params: { path: { tenant_id: tenantId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data ?? [];
    },
  });
  const project = (projectQuery.data ?? []).find((p) => p.id === projectId);

  const activityQuery = useActivityQuery(tenantId, projectId, 5, 0);
  const regionsQuery = useRegionsQuery();

  // Cross-kind derivation for the attention card.
  const tagged = KINDS.flatMap((kind, i) =>
    (listQueries[i].data ?? []).map((item) => ({ kind, item })),
  );
  const attention = tagged
    .filter(({ item }) => FAILED.has((item.status ?? '').toUpperCase()))
    .slice(0, 5);

  const cap = capQuery.data;

  return (
    <div className={styles.root}>
      <div className={styles.headerRow}>
        <div>
          <Title2 block>Dashboard</Title2>
          <Body1 block>
            Overview of project <b>{projectId}</b>
          </Body1>
        </div>
        <div className={styles.quickActions}>
          <Button appearance="primary" icon={<Add20Regular />} onClick={() => navigate('../vms')}>
            Create VM
          </Button>
          <Button icon={<Add20Regular />} onClick={() => navigate('../clusters')}>
            Create cluster
          </Button>
          <Button icon={<Add20Regular />} onClick={() => navigate('../vnets')}>
            Create network
          </Button>
        </div>
      </div>

      <div className={styles.inventoryGrid}>
        {KINDS.map((kind, i) => {
          const q = listQueries[i];
          const items = q.data ?? [];
          const { ok, warn, bad } = statusBuckets(items);
          return (
            <Card
              key={kind.key}
              className={styles.invCard}
              onClick={() => navigate(`../${routeFor(kind)}`)}
              aria-label={`${kind.label}: ${items.length}`}
            >
              <div className={styles.invHead}>
                {kind.icon}
                <span>{kind.label}</span>
              </div>
              {q.isLoading ? (
                <Spinner size="tiny" />
              ) : q.isError ? (
                <span className={styles.cardError}>unavailable</span>
              ) : (
                <>
                  <span className={styles.invCount}>{items.length}</span>
                  <div className={styles.invStatus}>
                    {items.length === 0 && <span>none yet</span>}
                    {ok > 0 && <span className={styles.dotOk}>● {ok} ready</span>}
                    {warn > 0 && <span className={styles.dotWarn}>● {warn} in progress</span>}
                    {bad > 0 && <span className={styles.dotBad}>● {bad} failed</span>}
                  </div>
                </>
              )}
            </Card>
          );
        })}
      </div>

      <div className={styles.twoCol}>
        <Card className={styles.sectionCard}>
          <Subtitle1>Tenant capacity</Subtitle1>
          {capQuery.isLoading && <Spinner size="tiny" />}
          {capQuery.isError && (
            <span className={styles.cardError}>
              Capacity is visible to tenant owners and administrators.
            </span>
          )}
          {cap && (
            <>
              <Gauge label="vCPU allocated" used={cap.allocated.cpu_cores} total={cap.cap.cpu_cores} unit="cores" />
              <Gauge label="Memory allocated" used={cap.allocated.memory_gb} total={cap.cap.memory_gb} unit="GiB" />
              <Gauge label="Storage allocated" used={cap.allocated.storage_gb} total={cap.cap.storage_gb} unit="GiB" />
              <span className={styles.empty}>
                Allocated across every project in tenant {tenantId}, against the tenant cap.
              </span>
            </>
          )}
        </Card>

        <Card className={styles.sectionCard}>
          <Subtitle1>Project quota</Subtitle1>
          {projectQuery.isLoading && <Spinner size="tiny" />}
          {project ? (
            <div className={styles.quotaGrid}>
              <span className={styles.quotaLabel}>vCPU</span>
              <span>{project.cpu_cores} cores</span>
              <span className={styles.quotaLabel}>Memory</span>
              <span>{project.memory_gb} GiB</span>
              <span className={styles.quotaLabel}>Storage</span>
              <span>{project.storage_gb} GiB</span>
              <span className={styles.quotaLabel}>Max VNets</span>
              <span>{project.max_vnets}</span>
              <span className={styles.quotaLabel}>Max clusters</span>
              <span>{project.max_clusters}</span>
              <span className={styles.quotaLabel}>Max public IPs</span>
              <span>{project.max_public_ips}</span>
            </div>
          ) : (
            !projectQuery.isLoading && <span className={styles.empty}>Project details unavailable.</span>
          )}
        </Card>
      </div>

      <div className={styles.twoCol}>
        <Card className={styles.sectionCard}>
          <Subtitle1>Needs attention</Subtitle1>
          {attention.length === 0 ? (
            <span className={styles.empty}>Nothing failing — all quiet.</span>
          ) : (
            attention.map(({ kind, item }) => (
              <div
                key={`${kind.key}-${item.id}`}
                className={styles.listRow}
                onClick={() => navigate(`../${routeFor(kind)}/${item.id}`)}
              >
                <span className={styles.listName}>
                  <span className={styles.dotBad}>● </span>
                  {item.name ?? item.id}
                </span>
                <span className={styles.listMeta}>
                  {kind.label} · {item.status}
                </span>
              </div>
            ))
          )}
        </Card>

        <Card className={styles.sectionCard}>
          <Subtitle1>Recent activity</Subtitle1>
          {activityQuery.isLoading && <Spinner size="tiny" />}
          {activityQuery.isError && <span className={styles.cardError}>unavailable</span>}
          {!activityQuery.isLoading &&
            !activityQuery.isError &&
            ((activityQuery.data?.items ?? []).length === 0 ? (
              <span className={styles.empty}>No activity yet.</span>
            ) : (
              (activityQuery.data?.items ?? []).map((ev) => {
                const route = ACTIVITY_RESOURCE_ROUTES[ev.resource_type];
                return (
                  <div
                    key={ev.id}
                    className={styles.listRow}
                    onClick={route && ev.resource_id ? () => navigate(`../${route}/${ev.resource_id}`) : undefined}
                  >
                    <span className={styles.listName}>
                      <span className={styles.empty}>{ev.action} </span>
                      {ev.resource_name}
                    </span>
                    <span className={styles.listMeta}>{fmtDate(ev.created_at)}</span>
                  </div>
                );
              })
            ))}
        </Card>

        <Card className={styles.sectionCard}>
          <Subtitle1>Regions</Subtitle1>
          {regionsQuery.isLoading && <Spinner size="tiny" />}
          {regionsQuery.isError && <span className={styles.cardError}>unavailable</span>}
          {!regionsQuery.isLoading &&
            !regionsQuery.isError &&
            ((regionsQuery.data?.items ?? []).length === 0 ? (
              <span className={styles.empty}>No regions registered.</span>
            ) : (
              (regionsQuery.data?.items ?? []).map((region) => {
                const dotClass = {
                  ok: styles.dotOk,
                  warn: styles.dotWarn,
                  bad: styles.dotBad,
                  unknown: styles.dotUnknown,
                }[regionDotVariant(region.status)];
                return (
                  <Fragment key={region.name}>
                    <div className={mergeClasses(styles.listRow, styles.listRowStatic)}>
                      <span className={styles.listName}>
                        <span className={dotClass}>● </span>
                        {region.display_name ?? region.name}
                      </span>
                      <span className={styles.listMeta}>{zoneSummary(region.zones)}</span>
                    </div>
                    {/* Admins see live per-zone capacity; up zones only (others have no agent). */}
                    {isAdmin &&
                      region.zones
                        .filter((z) => z.status === 'up')
                        .map((z) => (
                          <ZoneInventoryLine key={z.name} region={region.name} zone={z.name} />
                        ))}
                  </Fragment>
                );
              })
            ))}
        </Card>
      </div>
    </div>
  );
}
