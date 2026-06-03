import {
  Body1,
  Body2,
  Button,
  DrawerBody,
  DrawerFooter,
  DrawerHeader,
  DrawerHeaderTitle,
  Dropdown,
  Field,
  InfoLabel,
  Input,
  Link,
  Option,
  OverlayDrawer,
  Radio,
  RadioGroup,
  Slider,
  Switch,
  Toast,
  ToastTitle,
  Toaster,
  makeStyles,
  mergeClasses,
  shorthands,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import { Add20Regular, Delete20Regular, Dismiss24Regular, Edit20Regular } from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import SubnetCreateDrawer from './SubnetCreateDrawer';
import VnetCreateDrawer from './VnetCreateDrawer';
import { ReviewSummary } from './wizard/ReviewSummary';
import { useWizard } from './wizard/CreateWizard';
import type { ValidationIssue } from './wizard/CreateWizard';
import { WorkerPoolForm } from './workerPool/WorkerPoolForm';
import { useSizeCardStyles } from './workerPool/useSizeCardStyles';
import {
  defaultWorkerPoolFormValue,
  workerPoolFormIsValid,
  type WorkerPoolFormValue,
} from './workerPool/workerPoolTypes';

// ── Constants ─────────────────────────────────────────────────────────────────

const SIZES = [
  { id: 'small', label: 'Small', specs: '2 vCPU · 4 GB RAM · 40 GB disk' },
  { id: 'medium', label: 'Medium', specs: '4 vCPU · 8 GB RAM · 40 GB disk' },
  { id: 'large', label: 'Large', specs: '8 vCPU · 16 GB RAM · 80 GB disk' },
  { id: 'xlarge', label: 'X-Large', specs: '16 vCPU · 32 GB RAM · 160 GB disk' },
] as const;

type Size = (typeof SIZES)[number]['id'];

const SYSTEM_COUNTS = [
  { value: 1, label: '1 node', hint: 'Development / non-HA' },
  { value: 3, label: '3 nodes', hint: 'High availability (recommended)' },
  { value: 5, label: '5 nodes', hint: 'Large HA' },
] as const;

type SystemCount = 1 | 3 | 5;

const K8S_VERSIONS = [
  { value: 'v1.33.10+rke2r3', label: 'Kubernetes 1.33.10 (recommended)' },
  { value: 'v1.30.5+rke2r1', label: 'Kubernetes 1.30.5' },
  { value: 'v1.29.9+rke2r1', label: 'Kubernetes 1.29.9' },
] as const;

const STEP_BASICS = 'basics';
const STEP_SYSTEM = 'system';
const STEP_WORKERS = 'workers';
const STEP_IMAGE_NET = 'image-net';

const MAX_WORKER_POOLS = 10;

// ── Styles ────────────────────────────────────────────────────────────────────

const useStyles = makeStyles({
  drawer: { width: '640px' },
  subtitle: { color: tokens.colorNeutralForeground3 },
  body: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  footer: { justifyContent: 'space-between' },
  tabListWrapper: {
    marginTop: tokens.spacingVerticalS,
    marginBottom: tokens.spacingVerticalXS,
    marginLeft: `-${tokens.spacingHorizontalS}`,
  },
  sliderRow: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalM,
  },
  sliderValue: {
    minWidth: '80px',
    fontWeight: tokens.fontWeightSemibold,
  },
  infoBox: {
    backgroundColor: tokens.colorNeutralBackground2,
    border: `1px solid ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    padding: tokens.spacingHorizontalM,
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground2,
    lineHeight: tokens.lineHeightBase300,
  },
  countGrid: {
    display: 'grid',
    gridTemplateColumns: '1fr 1fr 1fr',
    gap: tokens.spacingHorizontalM,
  },
  countCard: {
    cursor: 'pointer',
    padding: tokens.spacingHorizontalM,
    ...shorthands.border('1px', 'solid', tokens.colorNeutralStroke2),
    borderRadius: tokens.borderRadiusMedium,
    transition: 'border-color 0.1s, background-color 0.1s',
    ':hover': { ...shorthands.borderColor(tokens.colorBrandStroke1) },
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXS,
    textAlign: 'center',
  },
  countCardSelected: {
    ...shorthands.borderColor(tokens.colorBrandStroke1),
    backgroundColor: tokens.colorBrandBackground2,
  },
  countCardLabel: { fontWeight: tokens.fontWeightSemibold, fontSize: tokens.fontSizeBase300 },
  countCardHint: { fontSize: tokens.fontSizeBase100, color: tokens.colorNeutralForeground3 },
  workerPoolList: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalS,
  },
  workerPoolEntry: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: tokens.spacingHorizontalM,
    ...shorthands.border('1px', 'solid', tokens.colorNeutralStroke2),
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: tokens.colorNeutralBackground1,
  },
  workerPoolEntryInfo: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXS,
  },
  workerPoolEntryName: {
    fontWeight: tokens.fontWeightSemibold,
    fontSize: tokens.fontSizeBase300,
  },
  workerPoolEntrySpecs: {
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
  },
  workerPoolEntryActions: {
    display: 'flex',
    gap: tokens.spacingHorizontalXS,
  },
  inlineForm: {
    ...shorthands.border('1px', 'solid', tokens.colorBrandStroke1),
    borderRadius: tokens.borderRadiusMedium,
    padding: tokens.spacingHorizontalL,
    backgroundColor: tokens.colorBrandBackground2,
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalM,
  },
  inlineFormTitle: {
    fontWeight: tokens.fontWeightSemibold,
    fontSize: tokens.fontSizeBase300,
    marginBottom: tokens.spacingVerticalXS,
  },
  inlineFormActions: {
    display: 'flex',
    justifyContent: 'flex-end',
    gap: tokens.spacingHorizontalS,
    marginTop: tokens.spacingVerticalS,
  },
  workerReviewTable: {
    width: '100%',
    borderCollapse: 'collapse',
    fontSize: tokens.fontSizeBase200,
  },
  workerReviewTh: {
    textAlign: 'left',
    color: tokens.colorNeutralForeground3,
    fontWeight: tokens.fontWeightRegular,
    paddingBottom: tokens.spacingVerticalXS,
    borderBottom: `1px solid ${tokens.colorNeutralStroke2}`,
  },
  workerReviewTd: {
    paddingTop: tokens.spacingVerticalXS,
    paddingBottom: tokens.spacingVerticalXS,
    paddingRight: tokens.spacingHorizontalM,
    verticalAlign: 'middle',
  },
});

// ── Local types ───────────────────────────────────────────────────────────────

interface Image {
  id: string;
  display_name: string;
  namespace: string;
}

interface Network {
  id: string;
  display_name: string;
  namespace: string;
}

interface VNet {
  id: string;
  name: string;
  status: string;
  address_space: string[];
}

interface Subnet {
  id: string;
  vnet_id: string;
  name: string;
  cidr: string;
  status: string;
}

interface CreateResponse {
  resource: { id: string; name: string };
  note: string;
}

export interface ClusterCreateResult {
  clusterId: string;
  clusterName: string;
}

interface ClusterCreateDrawerProps {
  open: boolean;
  onClose: () => void;
  onCreated: (result: ClusterCreateResult) => void;
}

interface WorkerPoolEntry {
  id: string;
  form: WorkerPoolFormValue;
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function localId() {
  return Math.random().toString(36).slice(2);
}

function poolSummary(form: WorkerPoolFormValue): string {
  const sizeLabel = { small: 'Small', medium: 'Medium', large: 'Large', xlarge: 'X-Large' }[
    form.size
  ];
  const parts = [`${sizeLabel} · ${form.count} node${form.count !== 1 ? 's' : ''}`];
  if (form.diskOverride) parts.push(`${form.diskGb} GB disk`);
  if (form.taints.length > 0)
    parts.push(`${form.taints.length} taint${form.taints.length !== 1 ? 's' : ''}`);
  if (form.labels.length > 0)
    parts.push(`${form.labels.length} label${form.labels.length !== 1 ? 's' : ''}`);
  return parts.join(' · ');
}

const sysCpuPerNode: Record<Size, number> = { small: 2, medium: 4, large: 8, xlarge: 16 };
const sysRamPerNode: Record<Size, number> = { small: 4, medium: 8, large: 16, xlarge: 32 };

// ── Component ─────────────────────────────────────────────────────────────────

export default function ClusterCreateDrawer({
  open,
  onClose,
  onCreated,
}: ClusterCreateDrawerProps) {
  const styles = useStyles();
  const cardStyles = useSizeCardStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();

  // Stable ref so mutation callbacks can call wizard.reset() even though the
  // wizard object is declared later in the render body.
  const wizardResetRef = useRef<() => void>(() => undefined);

  // ── Form state ────────────────────────────────────────────────────────────

  const [name, setName] = useState('');
  const [sysSize, setSysSize] = useState<Size>('large');
  const [sysCount, setSysCount] = useState<SystemCount>(3);
  const [sysDiskOverride, setSysDiskOverride] = useState(false);
  const [sysDiskGb, setSysDiskGb] = useState(80);
  const [workerPools, setWorkerPools] = useState<WorkerPoolEntry[]>([]);
  const [inlineFormMode, setInlineFormMode] = useState<null | 'new' | string>(null);
  const [inlineFormValue, setInlineFormValue] = useState<WorkerPoolFormValue>(
    defaultWorkerPoolFormValue,
  );
  const [inlineFormShowErrors, setInlineFormShowErrors] = useState(false);
  const [imageId, setImageId] = useState<string>('');
  const [networkMode, setNetworkMode] = useState<'vpc' | 'legacy'>('vpc');
  const [networkId, setNetworkId] = useState<string>('');
  const [vnetId, setVnetId] = useState<string>('');
  const [subnetId, setSubnetId] = useState<string>('');
  const [k8sVersion, setK8sVersion] = useState<string>(K8S_VERSIONS[0].value);
  const [createVnetOpen, setCreateVnetOpen] = useState(false);
  const [createSubnetOpen, setCreateSubnetOpen] = useState(false);

  // ── Data fetching ─────────────────────────────────────────────────────────

  const imagesQuery = useQuery({
    queryKey: ['images', tenantId],
    enabled: open && Boolean(tenantId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/images', {
        params: { path: { tenant_id: tenantId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as Image[];
    },
  });

  const networksQuery = useQuery({
    queryKey: ['networks', tenantId],
    enabled: open && networkMode === 'legacy' && Boolean(tenantId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/networks', {
        params: { path: { tenant_id: tenantId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as Network[];
    },
  });

  const vnetsQuery = useQuery({
    queryKey: ['vnets', tenantId, projectId],
    enabled: open && networkMode === 'vpc' && Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/vnets',
        { params: { path: { tenant_id: tenantId!, project_id: projectId! } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as VNet[];
    },
    refetchInterval: (query) => {
      const data = query.state.data as VNet[] | undefined;
      return data?.some((v) => v.status === 'PENDING') ? 3000 : false;
    },
  });

  const subnetsQuery = useQuery({
    queryKey: ['subnets', tenantId, projectId, vnetId],
    enabled:
      open &&
      networkMode === 'vpc' &&
      Boolean(tenantId) &&
      Boolean(projectId) &&
      Boolean(vnetId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets',
        {
          params: {
            path: { tenant_id: tenantId!, project_id: projectId!, vnet_id: vnetId },
          },
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as Subnet[];
    },
    refetchInterval: (query) => {
      const data = query.state.data as Subnet[] | undefined;
      return data?.some((s) => s.status === 'PENDING') ? 3000 : false;
    },
  });

  // ── Mutation ──────────────────────────────────────────────────────────────

  const createMutation = useMutation({
    mutationFn: async () => {
      const systemPool: Record<string, unknown> = { size: sysSize, count: sysCount };
      if (sysDiskOverride) systemPool.disk_gb = sysDiskGb;

      const body: Record<string, unknown> = {
        name,
        k8s_version: k8sVersion,
        image_name: imageId,
        system_pool: systemPool,
      };

      if (workerPools.length > 0) {
        body.worker_pools = workerPools.map(({ form }) => {
          const labelMap: Record<string, string> = {};
          for (const { key, value } of form.labels) {
            if (key) labelMap[key] = value;
          }
          const pool: Record<string, unknown> = {
            name: form.name,
            size: form.size,
            count: form.count,
            taints: form.taints,
            labels: labelMap,
          };
          if (form.diskOverride) pool.disk_gb = form.diskGb;
          return pool;
        });
      }

      if (networkMode === 'vpc') {
        body.vnet_id = vnetId;
        body.subnet_id = subnetId;
      } else {
        body.network_name = networkId;
      }

      const { data, error } = await api.POST(
        '/v1/tenants/{tenant_id}/projects/{project_id}/clusters',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId! } },
          body: body as never,
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as CreateResponse;
    },
    onSuccess: (resp) => {
      queryClient.invalidateQueries({ queryKey: ['clusters', tenantId, projectId] });
      onCreated({ clusterId: resp.resource.id, clusterName: resp.resource.name });
      resetFormState();
      wizardResetRef.current();
      onClose();
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast>
          <ToastTitle>Create failed: {e.message}</ToastTitle>
        </Toast>,
        { intent: 'error' },
      );
    },
  });

  // ── Reset ─────────────────────────────────────────────────────────────────

  // Resets form state only — wizard navigation is reset separately via
  // wizard.reset() at call sites, because wizard is declared after this
  // function in the render body.
  const resetFormState = () => {
    setName('');
    setSysSize('large');
    setSysCount(3);
    setSysDiskOverride(false);
    setSysDiskGb(80);
    setImageId('');
    setNetworkMode('vpc');
    setNetworkId('');
    setVnetId('');
    setSubnetId('');
    setK8sVersion(K8S_VERSIONS[0].value);
    setWorkerPools([]);
    setInlineFormMode(null);
    setInlineFormValue(defaultWorkerPoolFormValue());
    setInlineFormShowErrors(false);
  };

  // ── Worker pool inline form helpers ───────────────────────────────────────

  const takenNames = (editingId?: string) =>
    workerPools.filter((p) => p.id !== editingId).map((p) => p.form.name);

  const openAddForm = () => {
    setInlineFormValue(defaultWorkerPoolFormValue());
    setInlineFormShowErrors(false);
    setInlineFormMode('new');
  };

  const openEditForm = (entry: WorkerPoolEntry) => {
    setInlineFormValue({ ...entry.form });
    setInlineFormShowErrors(false);
    setInlineFormMode(entry.id);
  };

  const cancelInlineForm = () => {
    setInlineFormMode(null);
    setInlineFormShowErrors(false);
  };

  const commitInlineForm = () => {
    const editingId = inlineFormMode === 'new' ? undefined : (inlineFormMode as string);
    const existing = takenNames(editingId);
    if (!workerPoolFormIsValid(inlineFormValue, existing)) {
      setInlineFormShowErrors(true);
      return;
    }
    if (inlineFormMode === 'new') {
      setWorkerPools((prev) => [...prev, { id: localId(), form: inlineFormValue }]);
    } else {
      setWorkerPools((prev) =>
        prev.map((p) => (p.id === inlineFormMode ? { ...p, form: inlineFormValue } : p)),
      );
    }
    setInlineFormMode(null);
    setInlineFormShowErrors(false);
  };

  const removePool = (id: string) => {
    setWorkerPools((prev) => prev.filter((p) => p.id !== id));
    if (inlineFormMode === id) setInlineFormMode(null);
  };

  // ── Derived / validation ──────────────────────────────────────────────────

  const selectedSysSize = SIZES.find((s) => s.id === sysSize)!;
  const selectedImage = imagesQuery.data?.find((i) => i.id === imageId);
  const selectedNetwork = networksQuery.data?.find((n) => n.id === networkId);
  const selectedVnet = vnetsQuery.data?.find((v) => v.id === vnetId);
  const selectedSubnet = subnetsQuery.data?.find((s) => s.id === subnetId);
  const vnets = vnetsQuery.data ?? [];
  const subnets = subnetsQuery.data ?? [];
  const selectedVersion = K8S_VERSIONS.find((v) => v.value === k8sVersion);

  const nameValid = /^[a-z][a-z0-9-]{2,30}$/.test(name);
  const atMaxWorkerPools = workerPools.length >= MAX_WORKER_POOLS;

  const networkReady =
    networkMode === 'vpc'
      ? Boolean(vnetId) &&
        Boolean(subnetId) &&
        selectedVnet?.status === 'ACTIVE' &&
        selectedSubnet?.status === 'ACTIVE'
      : Boolean(networkId);

  const validationIssues: ValidationIssue[] = [];
  if (!nameValid)
    validationIssues.push({ message: 'Cluster name is missing or invalid', targetStep: STEP_BASICS });
  if (!sysSize || ![1, 3, 5].includes(sysCount))
    validationIssues.push({ message: 'System pool configuration is incomplete', targetStep: STEP_SYSTEM });
  if (!imageId)
    validationIssues.push({ message: 'No image selected', targetStep: STEP_IMAGE_NET });
  if (!k8sVersion)
    validationIssues.push({ message: 'No Kubernetes version selected', targetStep: STEP_IMAGE_NET });
  if (!networkReady)
    validationIssues.push({
      message:
        networkMode === 'vpc'
          ? 'VNet and subnet must both be Active'
          : 'No bridge network selected',
      targetStep: STEP_IMAGE_NET,
    });
  if (inlineFormMode !== null)
    validationIssues.push({ message: 'Worker pool form has unsaved changes', targetStep: STEP_WORKERS });

  // ── Review summary content ────────────────────────────────────────────────

  const reviewSummaryContent = (
    <>
      <ReviewSummary
        rows={[
          { key: 'Name', value: name || '—' },
          { key: 'K8s version', value: selectedVersion?.label ?? k8sVersion },
          { key: 'System pool size', value: `${selectedSysSize.label} · ${selectedSysSize.specs}` },
          {
            key: 'System pool count',
            value: `${sysCount} node${sysCount !== 1 ? 's' : ''} ${
              sysCount === 1 ? '(dev)' : sysCount === 3 ? '(HA)' : '(large HA)'
            }`,
          },
          {
            key: 'System disk',
            value: `${sysDiskGb} GB per node`,
            hidden: !sysDiskOverride,
          },
          { key: 'Image', value: selectedImage?.display_name ?? (imageId || '—') },
          ...(networkMode === 'vpc'
            ? [
                { key: 'VNet', value: selectedVnet?.name ?? (vnetId || '—') },
                {
                  key: 'Subnet',
                  value: selectedSubnet
                    ? `${selectedSubnet.name} (${selectedSubnet.cidr})`
                    : (subnetId || '—'),
                },
              ]
            : [
                {
                  key: 'Bridge network',
                  value: selectedNetwork?.display_name ?? (networkId || '—'),
                },
              ]),
          {
            key: 'Worker pools',
            value:
              workerPools.length === 0
                ? 'None — add after cluster is Active'
                : `${workerPools.length} pool${workerPools.length !== 1 ? 's' : ''}`,
          },
        ]}
      />

      {workerPools.length > 0 && (
        <table className={styles.workerReviewTable}>
          <thead>
            <tr>
              <th className={styles.workerReviewTh}>Name</th>
              <th className={styles.workerReviewTh}>Size</th>
              <th className={styles.workerReviewTh}>Count</th>
              <th className={styles.workerReviewTh}>Disk</th>
              <th className={styles.workerReviewTh}>Taints</th>
              <th className={styles.workerReviewTh}>Labels</th>
            </tr>
          </thead>
          <tbody>
            {workerPools.map(({ id, form }) => {
              const sizeEntry = SIZES.find((s) => s.id === form.size);
              return (
                <tr key={id}>
                  <td className={styles.workerReviewTd}>{form.name}</td>
                  <td className={styles.workerReviewTd}>{sizeEntry?.label ?? form.size}</td>
                  <td className={styles.workerReviewTd}>{form.count}</td>
                  <td className={styles.workerReviewTd}>
                    {form.diskOverride ? `${form.diskGb} GB` : 'default'}
                  </td>
                  <td className={styles.workerReviewTd}>
                    {form.taints.length > 0
                      ? `${form.taints.length} taint${form.taints.length !== 1 ? 's' : ''}`
                      : '—'}
                  </td>
                  <td className={styles.workerReviewTd}>
                    {form.labels.length > 0
                      ? `${form.labels.length} label${form.labels.length !== 1 ? 's' : ''}`
                      : '—'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </>
  );

  // ── Wizard ────────────────────────────────────────────────────────────────

  const wizard = useWizard({
    steps: [
      {
        id: STEP_BASICS,
        title: 'Basics',
        content: (
          <Field
            label="Name"
            required
            hint="Lowercase letters, numbers and hyphens. 3-32 chars. Unique within the project."
            validationState={name && !nameValid ? 'error' : 'none'}
            validationMessage={
              name && !nameValid
                ? 'Must start with a letter; lowercase letters, numbers, hyphens only; 3-32 chars.'
                : undefined
            }
          >
            <Input
              value={name}
              onChange={(_, d) => setName(d.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))}
              placeholder="e.g. apim-cluster-prod"
              autoFocus
            />
          </Field>
        ),
      },
      {
        id: STEP_SYSTEM,
        title: 'System pool',
        content: (
          <>
            <div className={styles.infoBox}>
              The <strong>system node pool</strong> runs the cluster&apos;s control plane and
              etcd. User workloads do not schedule here. Use 3 or 5 nodes for high availability.
            </div>

            <div>
              <InfoLabel info="Each node in the system pool uses this size profile." required>
                System node size
              </InfoLabel>
            </div>
            <div className={cardStyles.sizeGrid}>
              {SIZES.map((s) => (
                <div
                  key={s.id}
                  className={mergeClasses(
                    cardStyles.sizeCard,
                    sysSize === s.id && cardStyles.sizeCardSelected,
                  )}
                  onClick={() => setSysSize(s.id)}
                  role="button"
                  tabIndex={0}
                  onKeyDown={(e) => e.key === 'Enter' && setSysSize(s.id)}
                >
                  <Body1 className={cardStyles.sizeCardName}>{s.label}</Body1>
                  <Body2 className={cardStyles.sizeCardSpecs}>{s.specs}</Body2>
                </div>
              ))}
            </div>

            <Field
              label="System node count"
              required
              hint="etcd quorum requires 1 (dev/test), 3 (HA), or 5 (large HA) nodes."
            >
              <div className={styles.countGrid}>
                {SYSTEM_COUNTS.map((c) => (
                  <div
                    key={c.value}
                    className={mergeClasses(
                      styles.countCard,
                      sysCount === c.value && styles.countCardSelected,
                    )}
                    onClick={() => setSysCount(c.value)}
                    role="button"
                    tabIndex={0}
                    onKeyDown={(e) => e.key === 'Enter' && setSysCount(c.value)}
                  >
                    <span className={styles.countCardLabel}>{c.label}</span>
                    <span className={styles.countCardHint}>{c.hint}</span>
                  </div>
                ))}
              </div>
            </Field>

            <Field label="Override disk size per node">
              <Switch
                checked={sysDiskOverride}
                onChange={(_, d) => setSysDiskOverride(d.checked)}
                label={sysDiskOverride ? 'Custom disk size' : 'Use size default'}
              />
              {sysDiskOverride && (
                <div className={styles.sliderRow} style={{ marginTop: tokens.spacingVerticalS }}>
                  <Slider
                    min={40}
                    max={500}
                    step={10}
                    value={sysDiskGb}
                    onChange={(_, d) => setSysDiskGb(d.value)}
                    style={{ flex: 1 }}
                  />
                  <span className={styles.sliderValue}>{sysDiskGb} GB</span>
                </div>
              )}
            </Field>

            <Body2 className={styles.subtitle}>
              {sysCount} × {selectedSysSize.label} = {sysCpuPerNode[sysSize] * sysCount} vCPU ·{' '}
              {sysRamPerNode[sysSize] * sysCount} GB RAM
              {sysDiskOverride ? ` · ${sysDiskGb} GB disk per node` : ''}
            </Body2>
          </>
        ),
      },
      {
        id: STEP_WORKERS,
        title: 'Worker pools',
        content: (
          <>
            <div className={styles.infoBox}>
              <strong>Worker pools</strong> run your workloads. Add one or more now, or skip
              and add them later from the cluster&apos;s Node pools tab once the cluster is Active.
            </div>

            {workerPools.length > 0 && (
              <div className={styles.workerPoolList}>
                {workerPools.map((entry) => {
                  const isEditing = inlineFormMode === entry.id;
                  if (isEditing) {
                    return (
                      <div key={entry.id} className={styles.inlineForm}>
                        <div className={styles.inlineFormTitle}>
                          Edit worker pool &quot;{entry.form.name}&quot;
                        </div>
                        <WorkerPoolForm
                          value={inlineFormValue}
                          onChange={(patch) =>
                            setInlineFormValue((prev) => ({ ...prev, ...patch }))
                          }
                          existingNames={takenNames(entry.id)}
                          showErrors={inlineFormShowErrors}
                        />
                        <div className={styles.inlineFormActions}>
                          <Button appearance="subtle" size="small" onClick={cancelInlineForm}>
                            Cancel
                          </Button>
                          <Button appearance="primary" size="small" onClick={commitInlineForm}>
                            Save
                          </Button>
                        </div>
                      </div>
                    );
                  }
                  return (
                    <div key={entry.id} className={styles.workerPoolEntry}>
                      <div className={styles.workerPoolEntryInfo}>
                        <span className={styles.workerPoolEntryName}>{entry.form.name}</span>
                        <span className={styles.workerPoolEntrySpecs}>
                          {poolSummary(entry.form)}
                        </span>
                      </div>
                      <div className={styles.workerPoolEntryActions}>
                        <Button
                          appearance="subtle"
                          icon={<Edit20Regular />}
                          size="small"
                          aria-label={`Edit ${entry.form.name}`}
                          onClick={() => openEditForm(entry)}
                          disabled={inlineFormMode !== null}
                        />
                        <Button
                          appearance="subtle"
                          icon={<Delete20Regular />}
                          size="small"
                          aria-label={`Remove ${entry.form.name}`}
                          onClick={() => removePool(entry.id)}
                        />
                      </div>
                    </div>
                  );
                })}
              </div>
            )}

            {inlineFormMode === 'new' && (
              <div className={styles.inlineForm}>
                <div className={styles.inlineFormTitle}>New worker pool</div>
                <WorkerPoolForm
                  value={inlineFormValue}
                  onChange={(patch) =>
                    setInlineFormValue((prev) => ({ ...prev, ...patch }))
                  }
                  existingNames={takenNames()}
                  showErrors={inlineFormShowErrors}
                />
                <div className={styles.inlineFormActions}>
                  <Button appearance="subtle" size="small" onClick={cancelInlineForm}>
                    Cancel
                  </Button>
                  <Button appearance="primary" size="small" onClick={commitInlineForm}>
                    Add pool
                  </Button>
                </div>
              </div>
            )}

            {inlineFormMode === null && (
              <Button
                appearance="secondary"
                icon={<Add20Regular />}
                onClick={openAddForm}
                disabled={atMaxWorkerPools}
                style={{ alignSelf: 'flex-start' }}
              >
                {atMaxWorkerPools
                  ? 'Maximum 10 worker pools reached'
                  : workerPools.length === 0
                    ? 'Add worker pool'
                    : 'Add another worker pool'}
              </Button>
            )}

            {workerPools.length === 0 && inlineFormMode === null && (
              <Body2 className={styles.subtitle}>
                No worker pools added. The cluster will have a system pool only.
              </Body2>
            )}
          </>
        ),
      },
      {
        id: STEP_IMAGE_NET,
        title: 'Image & Network',
        content: (
          <>
            <Field label="Kubernetes version" required>
              <Dropdown
                value={selectedVersion?.label ?? ''}
                selectedOptions={[k8sVersion]}
                onOptionSelect={(_, d) => setK8sVersion(d.optionValue ?? K8S_VERSIONS[0].value)}
              >
                {K8S_VERSIONS.map((v) => (
                  <Option key={v.value} value={v.value} text={v.label}>
                    {v.label}
                  </Option>
                ))}
              </Dropdown>
            </Field>
            <Field label="Image" required hint="Base OS image for every node.">
              <Dropdown
                placeholder={imagesQuery.isLoading ? 'Loading images…' : 'Select an image'}
                value={selectedImage?.display_name ?? ''}
                selectedOptions={imageId ? [imageId] : []}
                onOptionSelect={(_, d) => setImageId(d.optionValue ?? '')}
              >
                {imagesQuery.data?.map((i) => (
                  <Option key={i.id} value={i.id} text={i.display_name}>
                    {i.display_name}
                  </Option>
                ))}
              </Dropdown>
            </Field>
            <Field label="Network" required hint="Where do the cluster nodes live in the network?">
              <RadioGroup
                value={networkMode}
                onChange={(_, d) => setNetworkMode(d.value as 'vpc' | 'legacy')}
                layout="horizontal"
              >
                <Radio value="vpc" label="VPC (recommended)" />
                <Radio value="legacy" label="Legacy bridge network" />
              </RadioGroup>
            </Field>

            {networkMode === 'vpc' && (
              <>
                <div>
                  <Field
                    label="VNet"
                    required
                    hint="Pick the virtual network the cluster nodes join. Freshly-created VNets show as provisioning until ready."
                  >
                    <Dropdown
                      placeholder={
                        vnetsQuery.isLoading
                          ? 'Loading VNets…'
                          : vnets.length === 0
                            ? 'No VNets yet — use Create new below'
                            : 'Select a VNet'
                      }
                      value={
                        selectedVnet
                          ? `${selectedVnet.name} (${selectedVnet.address_space.join(', ')})${
                              selectedVnet.status === 'ACTIVE' ? '' : ' (provisioning…)'
                            }`
                          : ''
                      }
                      selectedOptions={vnetId ? [vnetId] : []}
                      onOptionSelect={(_, d) => {
                        setVnetId(d.optionValue ?? '');
                        setSubnetId('');
                      }}
                    >
                      {vnets.map((v) => (
                        <Option
                          key={v.id}
                          value={v.id}
                          text={`${v.name} (${v.address_space.join(', ')})`}
                        >
                          {`${v.name} (${v.address_space.join(', ')})${
                            v.status === 'ACTIVE' ? '' : ' (provisioning…)'
                          }`}
                        </Option>
                      ))}
                    </Dropdown>
                  </Field>
                  <Link
                    as="button"
                    type="button"
                    onClick={() => setCreateVnetOpen(true)}
                    style={{
                      fontSize: tokens.fontSizeBase200,
                      marginTop: tokens.spacingVerticalXS,
                    }}
                  >
                    Create new
                  </Link>
                </div>
                <div>
                  <Field
                    label="Subnet"
                    required
                    hint="Nodes get an IP from this subnet's CIDR. Freshly-created subnets show as provisioning until ready."
                  >
                    <Dropdown
                      placeholder={
                        !vnetId
                          ? 'Pick a VNet first'
                          : selectedVnet?.status !== 'ACTIVE'
                            ? 'Waiting for VNet to be Active…'
                            : subnetsQuery.isLoading
                              ? 'Loading subnets…'
                              : subnets.length === 0
                                ? 'No subnets yet — use Create new below'
                                : 'Select a subnet'
                      }
                      value={
                        selectedSubnet
                          ? selectedSubnet.status === 'ACTIVE'
                            ? `${selectedSubnet.name} (${selectedSubnet.cidr})`
                            : `${selectedSubnet.name} (${selectedSubnet.cidr}) (provisioning…)`
                          : ''
                      }
                      selectedOptions={subnetId ? [subnetId] : []}
                      onOptionSelect={(_, d) => setSubnetId(d.optionValue ?? '')}
                      disabled={!vnetId || selectedVnet?.status !== 'ACTIVE'}
                    >
                      {subnets.map((s) => (
                        <Option key={s.id} value={s.id} text={`${s.name} (${s.cidr})`}>
                          {s.status === 'ACTIVE'
                            ? `${s.name} (${s.cidr})`
                            : `${s.name} (${s.cidr}) (provisioning…)`}
                        </Option>
                      ))}
                    </Dropdown>
                  </Field>
                  <Link
                    as="button"
                    type="button"
                    onClick={() => setCreateSubnetOpen(true)}
                    disabled={!vnetId || selectedVnet?.status !== 'ACTIVE'}
                    style={{
                      fontSize: tokens.fontSizeBase200,
                      marginTop: tokens.spacingVerticalXS,
                    }}
                  >
                    Create new
                  </Link>
                </div>
              </>
            )}

            {networkMode === 'legacy' && (
              <Field
                label="Bridge network"
                required
                hint="Pre-provisioned bridge networks. Most new clusters should use a VPC instead."
              >
                <Dropdown
                  placeholder={
                    networksQuery.isLoading ? 'Loading networks…' : 'Select a network'
                  }
                  value={selectedNetwork?.display_name ?? ''}
                  selectedOptions={networkId ? [networkId] : []}
                  onOptionSelect={(_, d) => setNetworkId(d.optionValue ?? '')}
                >
                  {networksQuery.data?.map((n) => (
                    <Option key={n.id} value={n.id} text={n.display_name}>
                      {n.display_name}
                    </Option>
                  ))}
                </Dropdown>
              </Field>
            )}
          </>
        ),
      },
    ],
    issues: validationIssues,
    reviewSummary: reviewSummaryContent,
    onSubmit: () => createMutation.mutate(),
    // onCloseInternal is defined after wizard — safe because it's only
    // called from event handlers, never during synchronous render.
    onCancel: () => onCloseInternal(),
    submitLabel: 'Create cluster',
    submitting: createMutation.isPending,
    submitError: createMutation.isError ? (createMutation.error as Error).message : null,
    extraFooterAction: (goToNext) => (
      <Button appearance="subtle" onClick={goToNext}>
        Skip
      </Button>
    ),
    extraFooterActionStep: 2, // Worker pools step index
  });

  // Keep the ref current every render so mutation callbacks can call it.
  useEffect(() => {
    wizardResetRef.current = wizard.reset;
  });

  // Defined after wizard so it can call wizard.reset().
  const onCloseInternal = () => {
    if (createMutation.isPending) return;
    resetFormState();
    wizard.reset();
    onClose();
  };

  // ── Render ────────────────────────────────────────────────────────────────

  return (
    <OverlayDrawer
      open={open}
      onOpenChange={(_, d) => !d.open && onCloseInternal()}
      position="end"
      className={styles.drawer}
    >
      <Toaster toasterId={toasterId} />

      <DrawerHeader>
        <DrawerHeaderTitle
          action={
            <Button
              appearance="subtle"
              icon={<Dismiss24Regular />}
              onClick={onCloseInternal}
              aria-label="Close"
            />
          }
        >
          Create Kubernetes cluster
        </DrawerHeaderTitle>
        <Body1 className={styles.subtitle}>
          Provision a managed Kubernetes cluster. Provisioning is asynchronous and takes ~5
          minutes.
        </Body1>
        {wizard.tabList}
      </DrawerHeader>

      <DrawerBody className={styles.body}>
        {wizard.stepContent}
      </DrawerBody>

      <DrawerFooter className={styles.footer}>
        {wizard.footer}
      </DrawerFooter>

      <VnetCreateDrawer
        open={createVnetOpen}
        onClose={() => setCreateVnetOpen(false)}
        onCreated={(result) => {
          setVnetId(result.vnetId);
          setSubnetId('');
          setCreateVnetOpen(false);
        }}
      />

      <SubnetCreateDrawer
        open={createSubnetOpen}
        onClose={() => setCreateSubnetOpen(false)}
        onCreated={(result) => {
          setSubnetId(result.subnetId);
          setCreateSubnetOpen(false);
        }}
        vnetId={vnetId}
        vnetAddressSpace={selectedVnet?.address_space ?? []}
      />
    </OverlayDrawer>
  );
}
