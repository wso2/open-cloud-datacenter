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
  Input,
  Link,
  MessageBar,
  MessageBarBody,
  MessageBarTitle,
  Option,
  OverlayDrawer,
  Radio,
  RadioGroup,
  Slider,
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
import { Dismiss24Regular, Key24Regular } from '@fluentui/react-icons';
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
import { useCan } from '../api/useCan';
import { PermissionTooltip } from './PermissionTooltip';

// ── Constants ─────────────────────────────────────────────────────────────────

const SIZES = [
  { id: 'small', label: 'Small', specs: '2 vCPU · 4 GB RAM · 40 GB disk', desc: 'Light workloads, dev sandboxes' },
  { id: 'medium', label: 'Medium', specs: '4 vCPU · 8 GB RAM · 40 GB disk', desc: 'Standard apps, single-node services' },
  { id: 'large', label: 'Large', specs: '8 vCPU · 16 GB RAM · 80 GB disk', desc: 'API gateways, identity workloads' },
  { id: 'xlarge', label: 'X-Large', specs: '16 vCPU · 32 GB RAM · 160 GB disk', desc: 'Memory-heavy workloads, db nodes' },
] as const;

type Size = (typeof SIZES)[number]['id'];

const STEP_BASICS = 'basics';
const STEP_SIZE = 'size';
const STEP_IMAGE_NET = 'image-net';

// ── Styles ────────────────────────────────────────────────────────────────────

const useStyles = makeStyles({
  drawer: { width: '600px' },
  subtitle: { color: tokens.colorNeutralForeground3 },
  body: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  footer: { justifyContent: 'space-between' },
  sizeGrid: {
    display: 'grid',
    gridTemplateColumns: '1fr 1fr',
    gap: tokens.spacingHorizontalM,
  },
  sizeCard: {
    cursor: 'pointer',
    padding: tokens.spacingHorizontalL,
    ...shorthands.border('1px', 'solid', tokens.colorNeutralStroke2),
    borderRadius: tokens.borderRadiusMedium,
    transition: 'border-color 0.1s, background-color 0.1s',
    ':hover': {
      ...shorthands.borderColor(tokens.colorBrandStroke1),
    },
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXS,
  },
  sizeCardSelected: {
    ...shorthands.borderColor(tokens.colorBrandStroke1),
    backgroundColor: tokens.colorBrandBackground2,
  },
  sizeCardName: { fontWeight: tokens.fontWeightSemibold },
  sizeCardSpecs: { fontSize: tokens.fontSizeBase200, color: tokens.colorNeutralForeground2 },
  sizeCardDesc: { fontSize: tokens.fontSizeBase200, color: tokens.colorNeutralForeground3 },
  sliderRow: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalM,
  },
  sliderValue: { minWidth: '60px', fontWeight: tokens.fontWeightSemibold },
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
  private_key: string;
  console_password: string;
  note: string;
}

export interface VmCreateResult {
  vmId: string;
  vmName: string;
  privateKey: string;
  consolePassword: string;
}

interface VmCreateDrawerProps {
  open: boolean;
  onClose: () => void;
  onCreated: (result: VmCreateResult) => void;
}

// ── Component ─────────────────────────────────────────────────────────────────

export default function VmCreateDrawer({ open, onClose, onCreated }: VmCreateDrawerProps) {
  const styles = useStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const { can } = useCan(tenantId, ['network/vnets/write', 'network/subnets/write'], projectId);
  const canCreateVnet = can('network/vnets/write');
  const canCreateSubnet = can('network/subnets/write');

  // Stable ref so mutation callbacks can call wizard.reset() even though the
  // wizard object is declared later in the render body.
  const wizardResetRef = useRef<() => void>(() => undefined);

  // ── Form state ────────────────────────────────────────────────────────────

  const [name, setName] = useState('');
  const [size, setSize] = useState<Size>('medium');
  const [diskGb, setDiskGb] = useState(40);
  const [imageId, setImageId] = useState<string>('');
  const [networkMode, setNetworkMode] = useState<'vpc' | 'legacy'>('vpc');
  const [networkId, setNetworkId] = useState<string>('');
  const [vnetId, setVnetId] = useState<string>('');
  const [subnetId, setSubnetId] = useState<string>('');
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

  // Poll while any VNet is PENDING so a freshly-created one flips to ACTIVE
  // without the user refreshing — the "(provisioning…)" suffix disappears
  // on the same render.
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

  // Same auto-refetch trick: poll while any subnet is PENDING.
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
      const body: Record<string, unknown> = {
        name,
        size,
        disk_gb: diskGb,
        image_name: imageId,
      };
      if (networkMode === 'vpc') {
        body.vnet_id = vnetId;
        body.subnet_id = subnetId;
      } else {
        body.network_name = networkId;
      }
      const { data, error } = await api.POST(
        '/v1/tenants/{tenant_id}/projects/{project_id}/virtual-machines',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId! } },
          body: body as never,
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as CreateResponse;
    },
    onSuccess: (resp) => {
      queryClient.invalidateQueries({ queryKey: ['vms', tenantId, projectId] });
      onCreated({
        vmId: resp.resource.id,
        vmName: resp.resource.name,
        privateKey: resp.private_key,
        consolePassword: resp.console_password,
      });
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
    setSize('medium');
    setDiskGb(40);
    setImageId('');
    setNetworkMode('vpc');
    setNetworkId('');
    setVnetId('');
    setSubnetId('');
  };

  // ── Derived / validation ──────────────────────────────────────────────────

  const selectedSize = SIZES.find((s) => s.id === size)!;
  const selectedImage = imagesQuery.data?.find((i) => i.id === imageId);
  const selectedNetwork = networksQuery.data?.find((n) => n.id === networkId);
  const selectedVnet = vnetsQuery.data?.find((v) => v.id === vnetId);
  const selectedSubnet = subnetsQuery.data?.find((s) => s.id === subnetId);
  const vnets = vnetsQuery.data ?? [];
  const subnets = subnetsQuery.data ?? [];

  const nameValid = /^[a-z][a-z0-9-]{2,62}$/.test(name);

  const networkReady =
    networkMode === 'vpc'
      ? Boolean(vnetId) &&
        Boolean(subnetId) &&
        selectedVnet?.status === 'ACTIVE' &&
        selectedSubnet?.status === 'ACTIVE'
      : Boolean(networkId);

  const validationIssues: ValidationIssue[] = [];
  if (!nameValid)
    validationIssues.push({ message: 'VM name is missing or invalid', targetStep: STEP_BASICS });
  if (!size)
    validationIssues.push({ message: 'No size selected', targetStep: STEP_SIZE });
  if (!imageId)
    validationIssues.push({ message: 'No image selected', targetStep: STEP_IMAGE_NET });
  if (!networkReady)
    validationIssues.push({
      message:
        networkMode === 'vpc'
          ? 'VNet and subnet must both be Active'
          : 'No bridge network selected',
      targetStep: STEP_IMAGE_NET,
    });

  // ── Review summary content ────────────────────────────────────────────────

  const reviewSummaryContent = (
    <>
      <ReviewSummary
        rows={[
          { key: 'Name', value: name || '—' },
          { key: 'Size', value: `${selectedSize.label} · ${selectedSize.specs}` },
          { key: 'Disk size', value: `${diskGb} GB` },
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
        ]}
      />
      <MessageBar intent="warning" icon={<Key24Regular />}>
        <MessageBarBody>
          <MessageBarTitle>Connection details shown once</MessageBarTitle>
          After clicking Create, the SSH private key and console password will appear at the
          top of the VMs page. Save them — they cannot be retrieved later.
        </MessageBarBody>
      </MessageBar>
    </>
  );

  // ── Wizard ────────────────────────────────────────────────────────────────

  const wizard = useWizard({
    steps: [
      {
        id: STEP_BASICS,
        title: 'Basics',
        content: (
          <>
            <Field
              label="Name"
              required
              hint="Lowercase letters, numbers and hyphens. 3-63 chars. Must be unique within the tenant."
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
                placeholder="e.g. web-server-03"
                autoFocus
              />
            </Field>
          </>
        ),
      },
      {
        id: STEP_SIZE,
        title: 'Size',
        content: (
          <>
            <Field label="VM size" hint="Choose a profile based on your workload.">
              <></>
            </Field>
            <div className={styles.sizeGrid}>
              {SIZES.map((s) => (
                <div
                  key={s.id}
                  className={mergeClasses(styles.sizeCard, size === s.id && styles.sizeCardSelected)}
                  onClick={() => setSize(s.id)}
                  role="button"
                  tabIndex={0}
                  onKeyDown={(e) => e.key === 'Enter' && setSize(s.id)}
                >
                  <Body1 className={styles.sizeCardName}>{s.label}</Body1>
                  <Body2 className={styles.sizeCardSpecs}>{s.specs}</Body2>
                  <Body2 className={styles.sizeCardDesc}>{s.desc}</Body2>
                </div>
              ))}
            </div>
            <Field label="Disk size" hint="Override the default disk size. 10-500 GB.">
              <div className={styles.sliderRow}>
                <Slider
                  min={10}
                  max={500}
                  step={10}
                  value={diskGb}
                  onChange={(_, d) => setDiskGb(d.value)}
                  style={{ flex: 1 }}
                />
                <span className={styles.sliderValue}>{diskGb} GB</span>
              </div>
            </Field>
          </>
        ),
      },
      {
        id: STEP_IMAGE_NET,
        title: 'Image & Network',
        content: (
          <>
            <Field label="Image" required hint="Base OS image for the VM.">
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
            <Field label="Network" required hint="Where does this VM live in the network?">
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
                    hint="Pick the virtual network the VM joins. Each VNet's address space is shown after the name. Freshly-created VNets show as provisioning until ready."
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
                  <PermissionTooltip
                    when={!canCreateVnet}
                    reason="You need write access on this tenant to create VNets"
                  >
                    <Link
                      as="button"
                      type="button"
                      onClick={() => setCreateVnetOpen(true)}
                      disabledFocusable={!canCreateVnet}
                      style={{
                        fontSize: tokens.fontSizeBase200,
                        marginTop: tokens.spacingVerticalXS,
                      }}
                    >
                      Create new
                    </Link>
                  </PermissionTooltip>
                </div>
                <div>
                  <Field
                    label="Subnet"
                    required
                    hint="The VM gets an IP from this subnet's CIDR. Freshly-created subnets show as provisioning until ready."
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
                  <PermissionTooltip
                    when={!canCreateSubnet}
                    reason="You need write access on this tenant to create subnets"
                  >
                    <Link
                      as="button"
                      type="button"
                      onClick={() => setCreateSubnetOpen(true)}
                      disabled={
                        canCreateSubnet ? !vnetId || selectedVnet?.status !== 'ACTIVE' : undefined
                      }
                      disabledFocusable={!canCreateSubnet}
                      style={{
                        fontSize: tokens.fontSizeBase200,
                        marginTop: tokens.spacingVerticalXS,
                      }}
                    >
                      Create new
                    </Link>
                  </PermissionTooltip>
                </div>
              </>
            )}

            {networkMode === 'legacy' && (
              <Field
                label="Bridge network"
                required
                hint="Pre-provisioned bridge networks. Most new workloads should use a VPC instead."
              >
                <Dropdown
                  placeholder={networksQuery.isLoading ? 'Loading networks…' : 'Select a network'}
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
    submitLabel: 'Create virtual machine',
    submitting: createMutation.isPending,
    submitError: createMutation.isError ? (createMutation.error as Error).message : null,
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
          Create virtual machine
        </DrawerHeaderTitle>
        <Body1 className={styles.subtitle}>
          Provision a new VM. Provisioning is asynchronous and takes 2-5 minutes.
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
          // Pre-select the freshly-created VNet. It's PENDING for ~30s;
          // refetchInterval on vnetsQuery polls until it flips ACTIVE,
          // at which point the suffix disappears and Next becomes enabled.
          setVnetId(result.vnetId);
          setSubnetId('');
          setCreateVnetOpen(false);
        }}
      />

      <SubnetCreateDrawer
        open={createSubnetOpen}
        onClose={() => setCreateSubnetOpen(false)}
        onCreated={(result) => {
          // Pre-select the freshly-created subnet. PENDING → ACTIVE in ~30s
          // via subnetsQuery refetchInterval; Next becomes enabled then.
          setSubnetId(result.subnetId);
          setCreateSubnetOpen(false);
        }}
        vnetId={vnetId}
        vnetAddressSpace={selectedVnet?.address_space ?? []}
      />
    </OverlayDrawer>
  );
}
