import {
  Body1,
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
  Option,
  OverlayDrawer,
  Radio,
  RadioGroup,
  SpinButton,
  Toast,
  ToastTitle,
  Toaster,
  makeStyles,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import { Dismiss24Regular } from '@fluentui/react-icons';
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

const useStyles = makeStyles({
  drawer: { width: '480px' },
  hint: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
  body: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  footer: { justifyContent: 'space-between' },
});

// RDS-style instance classes supported by the dbaas controller.
const INSTANCE_CLASSES = [
  'db.t3.micro',
  'db.t3.small',
  'db.t3.medium',
  'db.t3.large',
  'db.t3.xlarge',
  'db.t3.2xlarge',
  'db.m5.large',
  'db.m5.xlarge',
  'db.m5.2xlarge',
  'db.r5.large',
  'db.r5.xlarge',
  'db.r5.2xlarge',
] as const;

type InstanceClass = typeof INSTANCE_CLASSES[number];

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

interface Network {
  id: string;
  display_name: string;
  namespace: string;
}

export interface DatabaseCreated {
  id: string;
  name: string;
}

interface Props {
  open: boolean;
  onClose: () => void;
  onCreated: (db: DatabaseCreated) => void;
}

const STEP_BASICS = 'basics';
const STEP_NETWORK = 'network';

export default function DatabaseCreateDrawer({ open, onClose, onCreated }: Props) {
  const styles = useStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();

  const wizardResetRef = useRef<() => void>(() => undefined);

  // ── Form state ─────────────────────────────────────────────────────────────

  const [name, setName] = useState('');
  const [instanceClass, setInstanceClass] = useState<InstanceClass>('db.t3.medium');
  const [storageGb, setStorageGb] = useState(50);
  const [networkMode, setNetworkMode] = useState<'vpc' | 'legacy'>('vpc');
  const [vnetId, setVnetId] = useState('');
  const [subnetId, setSubnetId] = useState('');
  const [nadRef, setNadRef] = useState('');
  const [createVnetOpen, setCreateVnetOpen] = useState(false);
  const [createSubnetOpen, setCreateSubnetOpen] = useState(false);

  // ── Data fetching ──────────────────────────────────────────────────────────

  // Poll while any VNet is PENDING so a freshly-created one flips to ACTIVE
  // without the user refreshing.
  const vnetsQuery = useQuery({
    queryKey: ['vnets', tenantId, projectId],
    enabled: open && networkMode === 'vpc' && Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/vnets',
        { params: { path: { tenant_id: tenantId!, project_id: projectId! } } },
      );
      if (error) throw new Error(JSON.stringify(error));
      return (data ?? []) as VNet[];
    },
    refetchInterval: (query) => {
      const data = query.state.data as VNet[] | undefined;
      return data?.some((v) => v.status === 'PENDING') ? 3000 : false;
    },
  });

  const subnetsQuery = useQuery({
    queryKey: ['subnets', tenantId, projectId, vnetId],
    enabled: open && networkMode === 'vpc' && Boolean(tenantId) && Boolean(projectId) && Boolean(vnetId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets',
        { params: { path: { tenant_id: tenantId!, project_id: projectId!, vnet_id: vnetId } } },
      );
      if (error) throw new Error(JSON.stringify(error));
      return (data ?? []) as Subnet[];
    },
    refetchInterval: (query) => {
      const data = query.state.data as Subnet[] | undefined;
      return data?.some((s) => s.status === 'PENDING') ? 3000 : false;
    },
  });

  const networksQuery = useQuery({
    queryKey: ['networks', tenantId],
    enabled: open && networkMode === 'legacy' && Boolean(tenantId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/networks', {
        params: { path: { tenant_id: tenantId! } },
      });
      if (error) throw new Error(JSON.stringify(error));
      return (data ?? []) as Network[];
    },
  });

  // ── Derived ────────────────────────────────────────────────────────────────

  const vnets = vnetsQuery.data ?? [];
  const subnets = subnetsQuery.data ?? [];
  const selectedVnet = vnets.find((v) => v.id === vnetId);
  const selectedSubnet = subnets.find((s) => s.id === subnetId);
  const selectedNetwork = networksQuery.data?.find((n) => n.id === nadRef);

  // ── Reset ──────────────────────────────────────────────────────────────────

  const resetFormState = () => {
    setName('');
    setInstanceClass('db.t3.medium');
    setStorageGb(50);
    setNetworkMode('vpc');
    setVnetId('');
    setSubnetId('');
    setNadRef('');
  };

  // ── Validation ─────────────────────────────────────────────────────────────

  const nameValid = /^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$/.test(name);
  const storageValid = storageGb >= 1;

  const validationIssues: ValidationIssue[] = [];
  if (!nameValid)
    validationIssues.push({ message: 'Database name is missing or invalid', targetStep: STEP_BASICS });
  if (!storageValid)
    validationIssues.push({ message: 'Storage must be at least 1 GB', targetStep: STEP_BASICS });
  if (networkMode === 'legacy' && !nadRef)
    validationIssues.push({ message: 'Select a bridge network', targetStep: STEP_NETWORK });

  // ── Mutation ───────────────────────────────────────────────────────────────

  const createMutation = useMutation({
    mutationFn: async () => {
      let network: Record<string, unknown> | undefined;
      if (networkMode === 'vpc' && vnetId && subnetId) {
        network = { mode: 'vpc', vnet_id: vnetId, subnet_id: subnetId };
      } else if (networkMode === 'legacy' && nadRef) {
        network = { mode: 'legacy', nad_ref: nadRef };
      }

      const { data, error } = await api.POST(
        '/v1/tenants/{tenant_id}/projects/{project_id}/databases',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId! } },
          body: {
            name,
            engine: 'postgres',
            instance_class: instanceClass,
            allocated_storage_gb: storageGb,
            ...(network ? { network: network as never } : {}),
          },
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data!;
    },
    onSuccess: (db) => {
      queryClient.invalidateQueries({ queryKey: ['databases', tenantId, projectId] });
      onCreated({ id: db.id, name: db.name });
      resetFormState();
      wizardResetRef.current();
      onClose();
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>Create failed: {e.message}</ToastTitle></Toast>,
        { intent: 'error' },
      );
    },
  });

  // ── Review summary ─────────────────────────────────────────────────────────

  const reviewSummaryContent = (
    <>
      <ReviewSummary
        rows={[
          { key: 'Name', value: name || '—' },
          { key: 'Engine', value: 'PostgreSQL' },
          { key: 'Instance class', value: instanceClass },
          { key: 'Storage', value: `${storageGb} GB` },
          ...(networkMode === 'vpc'
            ? [
                { key: 'VNet', value: selectedVnet?.name ?? '(auto)' },
                {
                  key: 'Subnet',
                  value: selectedSubnet
                    ? `${selectedSubnet.name} (${selectedSubnet.cidr})`
                    : '(auto)',
                },
              ]
            : [
                { key: 'Bridge network', value: selectedNetwork?.display_name ?? (nadRef || '—') },
              ]),
        ]}
      />
      <MessageBar intent="info">
        <MessageBarBody>
          After the database becomes <strong>Active</strong>, retrieve its master credentials
          from the database detail page. The credentials are shown <em>once</em> — save them
          immediately.
        </MessageBarBody>
      </MessageBar>
    </>
  );

  // ── Wizard ─────────────────────────────────────────────────────────────────

  const wizard = useWizard({
    steps: [
      {
        id: STEP_BASICS,
        title: 'Basics',
        content: (
          <>
            <Body1 className={styles.hint}>
              Creates a managed PostgreSQL database on a dedicated VM. Provisioning takes
              2–5 minutes; the page refreshes automatically while the database is starting up.
            </Body1>

            <Field
              label="Name"
              required
              hint="Lowercase + hyphens, 2–32 chars. Unique within the project."
              validationState={name && !nameValid ? 'error' : 'none'}
              validationMessage={
                name && !nameValid ? 'Must match [a-z0-9][a-z0-9-]*[a-z0-9].' : undefined
              }
            >
              <Input
                value={name}
                onChange={(_, d) => setName(d.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))}
                placeholder="e.g. orders-db"
                autoFocus
              />
            </Field>

            <Field label="Instance class" required hint="Controls CPU and memory allocated to the database VM.">
              <Dropdown
                value={instanceClass}
                selectedOptions={[instanceClass]}
                onOptionSelect={(_, d) => setInstanceClass((d.optionValue ?? 'db.t3.medium') as InstanceClass)}
              >
                {INSTANCE_CLASSES.map((cls) => (
                  <Option key={cls} value={cls}>{cls}</Option>
                ))}
              </Dropdown>
            </Field>

            <Field
              label="Storage (GB)"
              required
              hint="Data volume size. Cannot be reduced after creation."
              validationState={!storageValid ? 'error' : 'none'}
              validationMessage={!storageValid ? 'Must be at least 1 GB.' : undefined}
            >
              <SpinButton
                value={storageGb}
                onChange={(_, d) => {
                  if (typeof d.value === 'number') setStorageGb(d.value);
                  else if (d.displayValue) {
                    const n = parseInt(d.displayValue, 10);
                    if (!Number.isNaN(n)) setStorageGb(n);
                  }
                }}
                min={1}
                max={16000}
              />
            </Field>
          </>
        ),
      },
      {
        id: STEP_NETWORK,
        title: 'Network',
        content: (
          <>
            <Field label="Network mode" required hint="Where does this database live in the network?">
              <RadioGroup
                value={networkMode}
                onChange={(_, d) => {
                  setNetworkMode(d.value as 'vpc' | 'legacy');
                  setVnetId('');
                  setSubnetId('');
                  setNadRef('');
                }}
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
                    hint="Optional — leave blank to auto-select the project default. Freshly-created VNets show as provisioning until ready."
                  >
                    <Dropdown
                      placeholder={
                        vnetsQuery.isLoading
                          ? 'Loading VNets…'
                          : vnets.length === 0
                            ? 'No VNets yet — use Create new below'
                            : 'Auto-select (recommended)'
                      }
                      value={
                        selectedVnet
                          ? `${selectedVnet.name} (${selectedVnet.address_space.join(', ')})${selectedVnet.status === 'ACTIVE' ? '' : ' (provisioning…)'}`
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
                          {`${v.name} (${v.address_space.join(', ')})${v.status === 'ACTIVE' ? '' : ' (provisioning…)'}`}
                        </Option>
                      ))}
                    </Dropdown>
                  </Field>
                  <Link
                    as="button"
                    type="button"
                    onClick={() => setCreateVnetOpen(true)}
                    style={{ fontSize: tokens.fontSizeBase200, marginTop: tokens.spacingVerticalXS }}
                  >
                    Create new
                  </Link>
                </div>

                <div>
                  <Field
                    label="Subnet"
                    hint="Optional — leave blank to auto-select. Freshly-created subnets show as provisioning until ready."
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
                                : 'Auto-select (recommended)'
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
                    style={{ fontSize: tokens.fontSizeBase200, marginTop: tokens.spacingVerticalXS }}
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
                hint="Pre-provisioned bridge network (Multus NAD). Most new databases should use VPC instead."
              >
                <Dropdown
                  placeholder={networksQuery.isLoading ? 'Loading networks…' : 'Select a network'}
                  value={selectedNetwork?.display_name ?? ''}
                  selectedOptions={nadRef ? [nadRef] : []}
                  onOptionSelect={(_, d) => setNadRef(d.optionValue ?? '')}
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
    onCancel: () => onCloseInternal(),
    submitLabel: 'Create database',
    submitting: createMutation.isPending,
    submitError: createMutation.isError ? (createMutation.error as Error).message : null,
  });

  useEffect(() => {
    wizardResetRef.current = wizard.reset;
  });

  const onCloseInternal = () => {
    if (createMutation.isPending) return;
    resetFormState();
    wizard.reset();
    onClose();
  };

  // ── Render ─────────────────────────────────────────────────────────────────

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
          Create database
        </DrawerHeaderTitle>
        <wizard.TabList />
      </DrawerHeader>

      <DrawerBody className={styles.body}>
        <wizard.StepContent />
      </DrawerBody>

      <DrawerFooter className={styles.footer}>
        <wizard.Footer />
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
