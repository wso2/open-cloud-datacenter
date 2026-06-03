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
  MessageBarTitle,
  Option,
  OverlayDrawer,
  Textarea,
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
  drawer: { width: '520px' },
  subtitle: { color: tokens.colorNeutralForeground3 },
  body: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  footer: { justifyContent: 'space-between' },
});

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
}

export interface BastionCreateResult {
  id: string;
  name: string;
  privateKey: string;
  consolePassword: string;
}

interface BastionCreateDrawerProps {
  open: boolean;
  onClose: () => void;
  onCreated: (result: BastionCreateResult) => void;
}

const STEP_BASICS = 'basics';

export default function BastionCreateDrawer({
  open,
  onClose,
  onCreated,
}: BastionCreateDrawerProps) {
  const styles = useStyles();
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
  const [description, setDescription] = useState('');
  const [vnetId, setVnetId] = useState<string>('');
  const [subnetId, setSubnetId] = useState<string>('');
  const [createVnetOpen, setCreateVnetOpen] = useState(false);
  const [createSubnetOpen, setCreateSubnetOpen] = useState(false);

  // ── Data fetching ─────────────────────────────────────────────────────────

  // Poll while any VNet is PENDING so the freshly-created one (from the
  // nested Create-new flow) flips to ACTIVE without the user refreshing.
  const vnetsQuery = useQuery({
    queryKey: ['vnets', tenantId, projectId],
    enabled: open && Boolean(tenantId) && Boolean(projectId),
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

  // Same auto-refetch trick as vnetsQuery: poll while any subnet is PENDING
  // so a freshly-created one (from the nested Create-new flow) flips to
  // ACTIVE without the user refreshing.
  const subnetsQuery = useQuery({
    queryKey: ['subnets', tenantId, projectId, vnetId],
    enabled: open && Boolean(tenantId) && Boolean(projectId) && Boolean(vnetId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets',
        { params: { path: { tenant_id: tenantId!, project_id: projectId!, vnet_id: vnetId } } },
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
        vnet_id: vnetId,
        subnet_id: subnetId,
        description,
      };
      const { data, error } = await api.POST(
        '/v1/tenants/{tenant_id}/projects/{project_id}/bastions',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId! } },
          body: body as never,
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as CreateResponse;
    },
    onSuccess: (resp) => {
      queryClient.invalidateQueries({ queryKey: ['bastions', tenantId, projectId] });
      onCreated({
        id: resp.resource.id,
        name: resp.resource.name,
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

  const resetFormState = () => {
    setName('');
    setDescription('');
    setVnetId('');
    setSubnetId('');
  };

  // ── Derived / validation ──────────────────────────────────────────────────

  const selectedVnet = vnetsQuery.data?.find((v) => v.id === vnetId);
  const selectedSubnet = subnetsQuery.data?.find((s) => s.id === subnetId);
  const vnets = vnetsQuery.data ?? [];
  const subnets = subnetsQuery.data ?? [];

  const nameValid = /^[a-z][a-z0-9-]{2,62}$/.test(name);

  const validationIssues: ValidationIssue[] = [];
  if (!nameValid)
    validationIssues.push({ message: 'Bastion name is missing or invalid', targetStep: STEP_BASICS });
  if (!vnetId)
    validationIssues.push({ message: 'No VNet selected', targetStep: STEP_BASICS });
  else if (selectedVnet?.status !== 'ACTIVE')
    validationIssues.push({ message: 'Selected VNet is not yet Active', targetStep: STEP_BASICS });
  if (!subnetId)
    validationIssues.push({ message: 'No subnet selected', targetStep: STEP_BASICS });
  else if (selectedSubnet?.status !== 'ACTIVE')
    validationIssues.push({ message: 'Selected subnet is not yet Active', targetStep: STEP_BASICS });

  // ── Review summary ────────────────────────────────────────────────────────

  const reviewSummaryContent = (
    <>
      <ReviewSummary
        rows={[
          { key: 'Name', value: name || '—' },
          {
            key: 'VNet',
            value: selectedVnet
              ? `${selectedVnet.name} (${selectedVnet.address_space.join(', ')})`
              : vnetId || '—',
          },
          {
            key: 'Subnet',
            value: selectedSubnet
              ? `${selectedSubnet.name} (${selectedSubnet.cidr})`
              : subnetId || '—',
          },
          { key: 'Description', value: description || '—', hidden: !description },
        ]}
      />
      <MessageBar intent="info">
        <MessageBarBody>
          <MessageBarTitle>How to connect once it&apos;s active</MessageBarTitle>
          <code>ssh -i bastion.pem -A ubuntu@&lt;ssh-endpoint&gt;</code> from your workstation
          (load your VM keys into <code>ssh-agent</code> first so they forward through),
          then <code>ssh ubuntu@&lt;private-ip&gt;</code>. The SSH private key and a console
          password are returned <em>once</em> when you click Create.
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
              hint="Lowercase letters, numbers and hyphens. 3-63 chars."
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
                placeholder="e.g. prod-bastion"
                autoFocus
              />
            </Field>

            <div>
              <Field
                label="VNet"
                required
                hint="The bastion will sit in this VPC and ProxyJump to its internal VMs. Each VNet's address space is shown after the name. Freshly-created VNets show as provisioning until ready."
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
                style={{ fontSize: tokens.fontSizeBase200, marginTop: tokens.spacingVerticalXS }}
              >
                Create new
              </Link>
            </div>

            <div>
              <Field
                label="Subnet"
                required
                hint="Which subnet of the VNet does the bastion attach to. Freshly-created subnets show as provisioning until ready."
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
                style={{ fontSize: tokens.fontSizeBase200, marginTop: tokens.spacingVerticalXS }}
              >
                Create new
              </Link>
            </div>

            <Field label="Description" hint="Optional free-text note.">
              <Textarea
                value={description}
                onChange={(_, d) => setDescription(d.value)}
                placeholder="e.g. Bastion for the prod-vnet team."
                rows={2}
              />
            </Field>
          </>
        ),
      },
    ],
    issues: validationIssues,
    reviewSummary: reviewSummaryContent,
    onSubmit: () => createMutation.mutate(),
    onCancel: () => onCloseInternal(),
    submitLabel: 'Create bastion',
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
          Create bastion
        </DrawerHeaderTitle>
        <Body1 className={styles.subtitle}>
          A small dual-NIC VM giving you SSH access into one of your VPCs. Sized and
          imaged by the platform — you only choose the VPC.
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
          // at which point the suffix disappears and Create becomes enabled.
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
          // via subnetsQuery refetchInterval; Create becomes enabled then.
          setSubnetId(result.subnetId);
          setCreateSubnetOpen(false);
        }}
        vnetId={vnetId}
        vnetAddressSpace={selectedVnet?.address_space ?? []}
      />
    </OverlayDrawer>
  );
}
