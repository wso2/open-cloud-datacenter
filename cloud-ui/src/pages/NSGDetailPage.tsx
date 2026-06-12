import {
  Body1,
  Body2,
  Breadcrumb,
  BreadcrumbButton,
  BreadcrumbDivider,
  BreadcrumbItem,
  Button,
  Card,
  Dropdown,
  Field,
  Input,
  Option,
  Spinner,
  Subtitle1,
  Tab,
  TabList,
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
  Tooltip,
  makeStyles,
  shorthands,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  Add20Regular,
  ArrowClockwise20Regular,
  Delete20Regular,
} from '@fluentui/react-icons';
import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { detailErrorMessage } from '../lib/apiError';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from '../components/useConfirmDialog';
import StatusPill from '../components/StatusPill';

type Direction = 'inbound' | 'outbound';
type Protocol = 'tcp' | 'udp' | 'icmp' | '*';
type Action = 'allow' | 'deny';

interface NSGRule {
  name: string;
  direction: Direction;
  priority: number;
  protocol: Protocol;
  source_address_prefix: string;
  source_port_range: string;
  destination_address_prefix: string;
  destination_port_range: string;
  action: Action;
}

interface NSGAttachment {
  id: string;
  sg_id: string;
  target_type: 'subnet' | 'nic';
  target_id: string;
  created_at: string;
}

interface NSG {
  id: string;
  tenant_id: string;
  name: string;
  description?: string;
  rules: NSGRule[];
  attachments: NSGAttachment[];
  status: string;
  created_at: string;
  updated_at: string;
}

interface VNet {
  id: string;
  name: string;
  status: string;
}

interface Subnet {
  id: string;
  vnet_id: string;
  name: string;
  cidr: string;
  status: string;
}

const useStyles = makeStyles({
  root: {
    padding: tokens.spacingHorizontalXXL,
    maxWidth: '1400px',
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  header: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalS },
  titleRow: { display: 'flex', alignItems: 'center', gap: tokens.spacingHorizontalM },
  subRow: { color: tokens.colorNeutralForeground3 },
  cmdBar: {
    display: 'flex',
    gap: tokens.spacingHorizontalS,
    paddingTop: tokens.spacingVerticalS,
    paddingBottom: tokens.spacingVerticalS,
    ...shorthands.borderTop('1px', 'solid', tokens.colorNeutralStroke2),
    ...shorthands.borderBottom('1px', 'solid', tokens.colorNeutralStroke2),
  },
  cmdSpacer: { flex: 1 },
  card: { padding: tokens.spacingHorizontalXXL },
  cardHeader: {
    fontWeight: tokens.fontWeightSemibold,
    marginBottom: tokens.spacingVerticalM,
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  mono: { fontFamily: tokens.fontFamilyMonospace, fontSize: tokens.fontSizeBase200 },
  ruleEditor: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalM },
  ruleRow: {
    display: 'grid',
    gridTemplateColumns: '1.5fr 80px 1fr 1.5fr 1fr 1.5fr 1fr 100px auto',
    gap: tokens.spacingHorizontalXS,
    alignItems: 'flex-end',
  },
  ruleHeader: {
    fontSize: tokens.fontSizeBase100,
    fontWeight: tokens.fontWeightSemibold,
    color: tokens.colorNeutralForeground3,
    textTransform: 'uppercase',
  },
  actionPillAllow: {
    display: 'inline-block',
    padding: `2px ${tokens.spacingHorizontalS}`,
    borderRadius: tokens.borderRadiusCircular,
    fontSize: tokens.fontSizeBase200,
    fontWeight: tokens.fontWeightMedium,
    backgroundColor: tokens.colorPaletteGreenBackground1,
    color: tokens.colorPaletteGreenForeground2,
  },
  actionPillDeny: {
    display: 'inline-block',
    padding: `2px ${tokens.spacingHorizontalS}`,
    borderRadius: tokens.borderRadiusCircular,
    fontSize: tokens.fontSizeBase200,
    fontWeight: tokens.fontWeightMedium,
    backgroundColor: tokens.colorPaletteRedBackground1,
    color: tokens.colorPaletteRedForeground1,
  },
  emptyBox: {
    padding: tokens.spacingHorizontalL,
    color: tokens.colorNeutralForeground3,
    border: `1px dashed ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    fontSize: tokens.fontSizeBase200,
  },
  errorMsg: {
    color: tokens.colorPaletteRedForeground1,
    fontSize: tokens.fontSizeBase200,
  },
  notFound: { padding: tokens.spacingHorizontalXXXL, textAlign: 'center' },
  attachRow: { display: 'flex', gap: tokens.spacingHorizontalS, alignItems: 'flex-end' },
});

function fmtDate(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export default function NSGDetailPage() {
  const styles = useStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId, nsgId } = useParams<{ tenantId: string; nsgId: string }>();
  const { projectId } = useActiveProject();
  const confirmDialog = useConfirmDialog();
  const [tab, setTab] = useState<'inbound' | 'outbound' | 'attachments'>('inbound');

  const nsgQuery = useQuery({
    queryKey: ['nsg', tenantId, projectId, nsgId],
    enabled: Boolean(tenantId) && Boolean(projectId) && Boolean(nsgId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/security-groups/{sg_id}', {
        params: { path: { tenant_id: tenantId!, project_id: projectId!, sg_id: nsgId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as NSG;
    },
  });

  const onDelete = async () => {
    const ok = await confirmDialog({
      title: `Delete security group "${nsgQuery.data?.name}"?`,
      body: 'All subnet attachments will be removed and the inbound/outbound rules discarded. This cannot be undone.',
      confirmLabel: 'Delete',
      destructive: true,
    });
    if (!ok) return;
    const { error } = await api.DELETE('/v1/tenants/{tenant_id}/projects/{project_id}/security-groups/{sg_id}', {
      params: { path: { tenant_id: tenantId!, project_id: projectId!, sg_id: nsgId! } },
    });
    if (error) {
      dispatchToast(<Toast><ToastTitle>Delete failed: {String(error)}</ToastTitle></Toast>, { intent: 'error' });
    } else {
      dispatchToast(<Toast><ToastTitle>Deleted</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['nsgs', tenantId, projectId] });
      navigate(`/tenants/${tenantId}/projects/${projectId}/nsgs`);
    }
  };

  if (!nsgId) return <div className={styles.root}>No NSG ID in URL</div>;
  if (nsgQuery.isLoading) {
    return (
      <div className={styles.root}>
        <Card><div style={{ padding: tokens.spacingHorizontalXXL, textAlign: 'center' }}>
          <Spinner label="Loading security group…" />
        </div></Card>
      </div>
    );
  }
  if (nsgQuery.isError) {
    return (
      <div className={styles.root}>
        <Card>
          <div className={styles.notFound}>
            <Subtitle1>Security group not found</Subtitle1>
            <Body1 style={{ color: tokens.colorNeutralForeground3, marginTop: tokens.spacingVerticalS }}>
              {detailErrorMessage(nsgQuery.error, 'security group')}
            </Body1>
            <Button appearance="primary" style={{ marginTop: tokens.spacingVerticalL }} onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/nsgs`)}>
              Back to security groups
            </Button>
          </div>
        </Card>
      </div>
    );
  }

  const nsg = nsgQuery.data!;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <Breadcrumb>
        <BreadcrumbItem>
          <BreadcrumbButton onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/nsgs`)}>
            Security groups
          </BreadcrumbButton>
        </BreadcrumbItem>
        <BreadcrumbDivider />
        <BreadcrumbItem><BreadcrumbButton current>{nsg.name}</BreadcrumbButton></BreadcrumbItem>
      </Breadcrumb>

      <div className={styles.header}>
        <div className={styles.titleRow}>
          <Title2>{nsg.name}</Title2>
          <StatusPill status={nsg.status} />
        </div>
        <div className={styles.subRow}>
          <span className={styles.mono}>{nsg.id}</span>
          {' · '}
          {nsg.description ?? 'no description'}
          {' · created '}
          {fmtDate(nsg.created_at)}
        </div>
      </div>

      <div className={styles.cmdBar}>
        <Tooltip content="Refetch security group" relationship="label">
          <Button appearance="subtle" icon={<ArrowClockwise20Regular />} onClick={() => nsgQuery.refetch()}>
            Refresh
          </Button>
        </Tooltip>
        <div className={styles.cmdSpacer} />
        <Button appearance="subtle" icon={<Delete20Regular />} onClick={onDelete} style={{ color: tokens.colorPaletteRedForeground1 }}>
          Delete
        </Button>
      </div>

      <TabList selectedValue={tab} onTabSelect={(_, d) => setTab(d.value as typeof tab)}>
        <Tab value="inbound">Inbound rules</Tab>
        <Tab value="outbound">Outbound rules</Tab>
        <Tab value="attachments">Attachments</Tab>
      </TabList>

      {(tab === 'inbound' || tab === 'outbound') && (
        <RulesTab nsg={nsg} direction={tab as Direction} tenantId={tenantId!} projectId={projectId!} />
      )}

      {tab === 'attachments' && <AttachmentsTab nsg={nsg} tenantId={tenantId!} projectId={projectId!} />}
    </div>
  );
}

// ─── Rules editor (per-direction view of the full rule list) ──────────────────

function RulesTab({ nsg, direction, tenantId, projectId }: { nsg: NSG; direction: Direction; tenantId: string; projectId: string }) {
  const styles = useStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster-rules');
  const { dispatchToast } = useToastController(toasterId);

  // Edit state holds the FULL rule set (PUT replaces all). Filter only for
  // display. The rules-source-of-truth is the server; useEffect would risk
  // reverting in-flight edits, so we use a key-style "reset on nsg.id change"
  // pattern by reading the latest rules via state initialiser when the
  // NSG identity changes.
  const [draftHolder, setDraftHolder] = useState<{ id: string; rules: NSGRule[] }>({
    id: nsg.id,
    rules: nsg.rules ?? [],
  });
  if (draftHolder.id !== nsg.id) {
    setDraftHolder({ id: nsg.id, rules: nsg.rules ?? [] });
  }
  const draft = draftHolder.rules;
  const setDraft = (next: NSGRule[] | ((prev: NSGRule[]) => NSGRule[])) =>
    setDraftHolder((h) => ({
      id: h.id,
      rules: typeof next === 'function' ? (next as (p: NSGRule[]) => NSGRule[])(h.rules) : next,
    }));

  const visible = draft
    .map((r, i) => ({ rule: r, idx: i }))
    .filter(({ rule }) => rule.direction === direction)
    .sort((a, b) => a.rule.priority - b.rule.priority);

  const updateRule = (idx: number, patch: Partial<NSGRule>) => {
    setDraft((rs) => rs.map((r, i) => (i === idx ? { ...r, ...patch } : r)));
  };

  const addRule = () => {
    const usedPriorities = new Set(
      draft.filter((r) => r.direction === direction).map((r) => r.priority)
    );
    let p = 100;
    while (usedPriorities.has(p) && p <= 4096) p += 10;
    setDraft((rs) => [
      ...rs,
      {
        name: '',
        direction,
        priority: p,
        protocol: 'tcp',
        source_address_prefix: '*',
        source_port_range: '*',
        destination_address_prefix: '*',
        destination_port_range: '*',
        action: 'allow',
      },
    ]);
  };

  const removeRule = (idx: number) => setDraft((rs) => rs.filter((_, i) => i !== idx));

  // Validation per rule
  const errors: (string | null)[] = visible.map(({ rule }) => {
    if (!/^[a-z][a-z0-9-]{0,61}[a-z0-9]$/.test(rule.name))
      return 'Name must be lowercase letters, numbers, hyphens.';
    if (rule.priority < 100 || rule.priority > 4096)
      return 'Priority must be between 100 and 4096.';
    return null;
  });
  // Priority must be unique per direction
  const seenPriorities = new Set<number>();
  visible.forEach(({ rule }, i) => {
    if (seenPriorities.has(rule.priority)) errors[i] = 'Priority must be unique within direction.';
    seenPriorities.add(rule.priority);
  });
  const allValid = errors.every((e) => e === null);

  const saveMutation = useMutation({
    mutationFn: async () => {
      const { error } = await api.PUT('/v1/tenants/{tenant_id}/projects/{project_id}/security-groups/{sg_id}/rules', {
        params: { path: { tenant_id: tenantId, project_id: projectId, sg_id: nsg.id } },
        body: { rules: draft } as never,
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Rules saved</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['nsg', nsg.id] });
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Save failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  return (
    <Card className={styles.card}>
      <Toaster toasterId={toasterId} />
      <div className={styles.cardHeader}>
        <span>{direction === 'inbound' ? 'Inbound rules' : 'Outbound rules'}</span>
        <div style={{ display: 'flex', gap: tokens.spacingHorizontalS }}>
          <Button appearance="subtle" icon={<Add20Regular />} onClick={addRule}>
            Add rule
          </Button>
          <Button
            appearance="primary"
            onClick={() => saveMutation.mutate()}
            disabled={!allValid || saveMutation.isPending}
          >
            {saveMutation.isPending ? 'Saving…' : 'Save'}
          </Button>
        </div>
      </div>

      {visible.length === 0 ? (
        <div className={styles.emptyBox}>
          No {direction} rules yet. Default behaviour applies — typically allow all egress,
          deny all ingress that&apos;s not explicitly allowed elsewhere.
        </div>
      ) : (
        <div className={styles.ruleEditor}>
          <div className={styles.ruleRow}>
            <span className={styles.ruleHeader}>Name</span>
            <span className={styles.ruleHeader}>Priority</span>
            <span className={styles.ruleHeader}>Protocol</span>
            <span className={styles.ruleHeader}>Source</span>
            <span className={styles.ruleHeader}>Src port</span>
            <span className={styles.ruleHeader}>Destination</span>
            <span className={styles.ruleHeader}>Dst port</span>
            <span className={styles.ruleHeader}>Action</span>
            <span></span>
          </div>
          {visible.map(({ rule, idx }, vIdx) => (
            <div key={idx}>
              <div className={styles.ruleRow}>
                <Input
                  size="small"
                  value={rule.name}
                  onChange={(_, d) =>
                    updateRule(idx, { name: d.value.toLowerCase().replace(/[^a-z0-9-]/g, '') })
                  }
                />
                <Input
                  size="small"
                  type="number"
                  value={String(rule.priority)}
                  onChange={(_, d) => updateRule(idx, { priority: Number(d.value) || 100 })}
                />
                <Dropdown
                  size="small"
                  value={rule.protocol}
                  selectedOptions={[rule.protocol]}
                  onOptionSelect={(_, d) => updateRule(idx, { protocol: (d.optionValue as Protocol) ?? 'tcp' })}
                >
                  <Option value="tcp" text="tcp">tcp</Option>
                  <Option value="udp" text="udp">udp</Option>
                  <Option value="icmp" text="icmp">icmp</Option>
                  <Option value="*" text="any">any (*)</Option>
                </Dropdown>
                <Input
                  size="small"
                  value={rule.source_address_prefix}
                  onChange={(_, d) => updateRule(idx, { source_address_prefix: d.value })}
                  placeholder="* / CIDR / VnetLocal"
                />
                <Input
                  size="small"
                  value={rule.source_port_range}
                  onChange={(_, d) => updateRule(idx, { source_port_range: d.value })}
                  placeholder="* / 443 / 1024-65535"
                />
                <Input
                  size="small"
                  value={rule.destination_address_prefix}
                  onChange={(_, d) => updateRule(idx, { destination_address_prefix: d.value })}
                  placeholder="* / CIDR / VnetLocal"
                />
                <Input
                  size="small"
                  value={rule.destination_port_range}
                  onChange={(_, d) => updateRule(idx, { destination_port_range: d.value })}
                  placeholder="* / 443 / 1024-65535"
                />
                <Dropdown
                  size="small"
                  value={rule.action}
                  selectedOptions={[rule.action]}
                  onOptionSelect={(_, d) => updateRule(idx, { action: (d.optionValue as Action) ?? 'allow' })}
                >
                  <Option value="allow" text="allow">allow</Option>
                  <Option value="deny" text="deny">deny</Option>
                </Dropdown>
                <Button
                  appearance="subtle"
                  icon={<Delete20Regular />}
                  aria-label="Remove rule"
                  onClick={() => removeRule(idx)}
                />
              </div>
              {errors[vIdx] && <div className={styles.errorMsg}>{errors[vIdx]}</div>}
            </div>
          ))}
        </div>
      )}

      <Body2 style={{ display: 'block', marginTop: tokens.spacingVerticalL, color: tokens.colorNeutralForeground3 }}>
        Lower priority numbers evaluate first. Use <code>*</code> for any. Use a CIDR
        like <code>10.0.0.0/8</code> or the special value <code>VnetLocal</code> for
        the parent VNet&apos;s address space.
      </Body2>
    </Card>
  );
}

// ─── Attachments tab ──────────────────────────────────────────────────────────

function AttachmentsTab({ nsg, tenantId, projectId }: { nsg: NSG; tenantId: string; projectId: string }) {
  const styles = useStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster-att');
  const { dispatchToast } = useToastController(toasterId);
  const confirmDialog = useConfirmDialog();

  const [pickedVnetId, setPickedVnetId] = useState('');
  const [pickedSubnetId, setPickedSubnetId] = useState('');

  const vnetsQuery = useQuery({
    queryKey: ['vnets', tenantId, projectId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/vnets', {
        params: { path: { tenant_id: tenantId, project_id: projectId } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as VNet[];
    },
  });

  const subnetsQuery = useQuery({
    queryKey: ['subnets', tenantId, projectId, pickedVnetId],
    enabled: Boolean(tenantId) && Boolean(projectId) && Boolean(pickedVnetId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets', {
        params: { path: { tenant_id: tenantId, project_id: projectId, vnet_id: pickedVnetId } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as Subnet[];
    },
  });

  // Need ALL subnets across all VNets for the existing-attachments display.
  // Lazy: ask for them on demand (one VNet's subnets at a time isn't enough
  // because attachments list contains subnet IDs from any VNet). For v1, just
  // show the subnet UUID if we don't have it cached.
  const allSubnetsByVnet = useQuery({
    queryKey: ['all-subnets', tenantId, projectId],
    queryFn: async () => {
      const vnets = vnetsQuery.data ?? [];
      const out: Subnet[] = [];
      for (const v of vnets) {
        const { data } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets', {
          params: { path: { tenant_id: tenantId, project_id: projectId, vnet_id: v.id } },
        });
        if (data) out.push(...((data ?? []) as Subnet[]));
      }
      return out;
    },
    enabled: (vnetsQuery.data?.length ?? 0) > 0,
  });
  const subnetById = new Map((allSubnetsByVnet.data ?? []).map((s) => [s.id, s]));
  const vnetById = new Map((vnetsQuery.data ?? []).map((v) => [v.id, v]));

  const attachMutation = useMutation({
    mutationFn: async () => {
      const { error } = await api.POST('/v1/tenants/{tenant_id}/projects/{project_id}/security-groups/{sg_id}/attachments', {
        params: { path: { tenant_id: tenantId, project_id: projectId, sg_id: nsg.id } },
        body: { target_type: 'subnet', target_id: pickedSubnetId } as never,
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Subnet attached</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['nsg', nsg.id] });
      setPickedVnetId('');
      setPickedSubnetId('');
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Attach failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const detachMutation = useMutation({
    mutationFn: async (attachmentId: string) => {
      const { error } = await api.DELETE('/v1/tenants/{tenant_id}/projects/{project_id}/security-groups/{sg_id}/attachments/{attachment_id}', {
        params: { path: { tenant_id: tenantId, project_id: projectId, sg_id: nsg.id, attachment_id: attachmentId } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Detached</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['nsg', nsg.id] });
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Detach failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  // Filter candidate subnets to ACTIVE and not already attached.
  const attachedIds = new Set((nsg.attachments ?? []).map((a) => a.target_id));
  const candidateSubnets = (subnetsQuery.data ?? []).filter(
    (s) => s.status === 'ACTIVE' && !attachedIds.has(s.id)
  );

  return (
    <Card className={styles.card}>
      <Toaster toasterId={toasterId} />
      <div className={styles.cardHeader}>Attachments</div>

      {(nsg.attachments ?? []).length === 0 ? (
        <div className={styles.emptyBox}>
          Not attached to any subnet yet. Attach to a subnet to apply these rules to its
          traffic.
        </div>
      ) : (
        <Table size="small" aria-label="Attachments">
          <TableHeader>
            <TableRow>
              <TableHeaderCell>VNet</TableHeaderCell>
              <TableHeaderCell>Subnet</TableHeaderCell>
              <TableHeaderCell>CIDR</TableHeaderCell>
              <TableHeaderCell>Attached</TableHeaderCell>
              <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(nsg.attachments ?? []).map((a) => {
              const s = subnetById.get(a.target_id);
              const v = s ? vnetById.get(s.vnet_id) : null;
              return (
                <TableRow key={a.id}>
                  <TableCell>{v?.name ?? '—'}</TableCell>
                  <TableCell>{s?.name ?? a.target_id}</TableCell>
                  <TableCell className={styles.mono}>{s?.cidr ?? '—'}</TableCell>
                  <TableCell style={{ color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 }}>
                    {fmtDate(a.created_at)}
                  </TableCell>
                  <TableCell>
                    <Button
                      appearance="subtle"
                      icon={<Delete20Regular />}
                      aria-label="Detach"
                      onClick={async () => {
                        const ok = await confirmDialog({
                          title: 'Detach subnet?',
                          body: `Remove the association between "${s?.name ?? a.target_id}" and this security group. Rules will no longer apply to that subnet.`,
                          confirmLabel: 'Detach',
                          destructive: true,
                        });
                        if (ok) detachMutation.mutate(a.id);
                      }}
                      disabled={detachMutation.isPending}
                    />
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}

      <div className={styles.attachRow} style={{ marginTop: tokens.spacingVerticalL }}>
        <Field label="VNet" style={{ flex: 1 }}>
          <Dropdown
            placeholder={vnetsQuery.isLoading ? 'Loading…' : 'Select a VNet'}
            value={vnetById.get(pickedVnetId)?.name ?? ''}
            selectedOptions={pickedVnetId ? [pickedVnetId] : []}
            onOptionSelect={(_, d) => {
              setPickedVnetId(d.optionValue ?? '');
              setPickedSubnetId('');
            }}
          >
            {(vnetsQuery.data ?? [])
              .filter((v) => v.status === 'ACTIVE')
              .map((v) => (
                <Option key={v.id} value={v.id} text={v.name}>{v.name}</Option>
              ))}
          </Dropdown>
        </Field>
        <Field label="Subnet" style={{ flex: 1 }}>
          <Dropdown
            placeholder={
              !pickedVnetId
                ? 'Pick a VNet first'
                : candidateSubnets.length === 0
                ? 'No eligible subnets'
                : 'Select a subnet'
            }
            value={candidateSubnets.find((s) => s.id === pickedSubnetId)?.name ?? ''}
            selectedOptions={pickedSubnetId ? [pickedSubnetId] : []}
            onOptionSelect={(_, d) => setPickedSubnetId(d.optionValue ?? '')}
            disabled={!pickedVnetId}
          >
            {candidateSubnets.map((s) => (
              <Option key={s.id} value={s.id} text={s.name}>
                {s.name} ({s.cidr})
              </Option>
            ))}
          </Dropdown>
        </Field>
        <Button
          appearance="primary"
          onClick={() => attachMutation.mutate()}
          disabled={!pickedSubnetId || attachMutation.isPending}
        >
          {attachMutation.isPending ? 'Attaching…' : 'Attach'}
        </Button>
      </div>
    </Card>
  );
}
