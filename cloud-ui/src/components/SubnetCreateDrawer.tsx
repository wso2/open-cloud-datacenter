import {
  Body1,
  Button,
  DrawerBody,
  DrawerFooter,
  DrawerHeader,
  DrawerHeaderTitle,
  Field,
  Input,
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
import { cidrsOverlap, findReservedOverlap, parseCidr, validateCidr } from '../lib/cidr';
import { ReviewSummary } from './wizard/ReviewSummary';
import { useWizard } from './wizard/CreateWizard';
import type { ValidationIssue } from './wizard/CreateWizard';

// Single region today (`lk`); when F12 lands and regions become dynamic,
// plumb the parent VNet's region through as a prop.
const REGION = 'lk';

const useStyles = makeStyles({
  drawer: { width: '520px' },
  subtitle: { color: tokens.colorNeutralForeground3 },
  body: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  footer: { justifyContent: 'space-between' },
  parentInfo: {
    backgroundColor: tokens.colorNeutralBackground2,
    border: `1px solid ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    padding: tokens.spacingHorizontalM,
    display: 'grid',
    gridTemplateColumns: '180px 1fr',
    gap: tokens.spacingHorizontalM,
    fontSize: tokens.fontSizeBase200,
  },
  parentInfoKey: { color: tokens.colorNeutralForeground3 },
  parentInfoValue: { fontFamily: tokens.fontFamilyMonospace },
  cidrPreview: {
    backgroundColor: tokens.colorNeutralBackground2,
    border: `1px solid ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    padding: tokens.spacingHorizontalM,
    fontSize: tokens.fontSizeBase200,
    display: 'grid',
    gridTemplateColumns: '120px 1fr',
    rowGap: tokens.spacingVerticalXXS,
    fontFamily: tokens.fontFamilyMonospace,
  },
  cidrPreviewKey: { color: tokens.colorNeutralForeground3 },
});

interface ExistingSubnet {
  id: string;
  name: string;
  cidr: string;
  status: string;
}

interface CreateResponse {
  resource: { id: string; name: string };
  note: string;
}

export interface SubnetCreateResult {
  subnetId: string;
  subnetName: string;
}

interface SubnetCreateDrawerProps {
  open: boolean;
  onClose: () => void;
  onCreated: (result: SubnetCreateResult) => void;
  /** Parent VNet's id — the subnet is created inside this VNet. */
  vnetId: string;
  /** Parent VNet's address_space — used to enforce subnet ⊂ VNet. */
  vnetAddressSpace: string[];
}

const STEP_BASICS = 'basics';

export default function SubnetCreateDrawer({
  open,
  onClose,
  onCreated,
  vnetId,
  vnetAddressSpace,
}: SubnetCreateDrawerProps) {
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
  const [cidr, setCidr] = useState('');
  const [gateway, setGateway] = useState('');
  const [description, setDescription] = useState('');

  // ── Data fetching ─────────────────────────────────────────────────────────

  // We need to check the proposed CIDR against existing subnets in this VNet
  // to refuse overlaps. Re-uses the cache key the rest of the app already
  // populates so opening this drawer doesn't always force a network round-trip.
  const existingSubnetsQuery = useQuery({
    queryKey: ['subnets', tenantId, projectId, vnetId],
    enabled: open && Boolean(tenantId) && Boolean(projectId) && Boolean(vnetId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets',
        { params: { path: { tenant_id: tenantId!, project_id: projectId!, vnet_id: vnetId } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as ExistingSubnet[];
    },
  });
  const existingSubnets = existingSubnetsQuery.data ?? [];

  // ── Validation ────────────────────────────────────────────────────────────

  // Validation: CIDR must be a valid RFC1918 /8-/28, contained within one of
  // the parent VNet's address_space entries, and not overlap any existing
  // subnet or reserved range. Mirrors the rules in SubnetsTab.
  const nameValid = /^[a-z][a-z0-9-]{0,61}[a-z0-9]$/.test(name);
  const cidrCheck = validateCidr(cidr, { minPrefix: 8, maxPrefix: 28, requireRFC1918: true });
  let containmentError: string | null = null;
  if (cidr && cidrCheck.ok) {
    const clash = findReservedOverlap(cidrCheck.parsed, REGION);
    if (clash) {
      containmentError = `Overlaps reserved CIDR ${clash.cidr} (${clash.label}) in region ${REGION}.`;
    }
  }
  if (cidr && cidrCheck.ok && !containmentError) {
    const contained = vnetAddressSpace.some((vc) => {
      const v = parseCidr(vc);
      if (!v) return false;
      return cidrsOverlap(v, cidrCheck.parsed) && cidrCheck.parsed.prefix >= v.prefix;
    });
    if (!contained) {
      containmentError = `CIDR must be contained within the VNet's address space (${vnetAddressSpace.join(', ')}).`;
    }
    const overlapsExisting = existingSubnets.some((s) => {
      const sParsed = parseCidr(s.cidr);
      return sParsed && cidrsOverlap(sParsed, cidrCheck.parsed);
    });
    if (overlapsExisting) {
      containmentError =
        (containmentError ? containmentError + ' ' : '') +
        'Overlaps an existing subnet in this VNet.';
    }
  }
  const cidrError = cidr ? (cidrCheck.ok ? containmentError : cidrCheck.reason) : null;

  let gatewayError: string | null = null;
  if (gateway && cidrCheck.ok) {
    const g = parseCidr(`${gateway}/32`);
    if (!g || !cidrsOverlap(g, cidrCheck.parsed)) {
      gatewayError = 'Gateway must be inside the CIDR.';
    }
  }
  const formValid = nameValid && !cidrError && !gatewayError && cidr.length > 0;

  const validationIssues: ValidationIssue[] = [];
  if (!nameValid)
    validationIssues.push({ message: 'Subnet name is missing or invalid', targetStep: STEP_BASICS });
  if (!cidr || cidrError)
    validationIssues.push({
      message: cidrError ?? 'CIDR is required',
      targetStep: STEP_BASICS,
    });
  if (gatewayError)
    validationIssues.push({ message: gatewayError, targetStep: STEP_BASICS });

  // ── Reset ─────────────────────────────────────────────────────────────────

  const resetFormState = () => {
    setName('');
    setCidr('');
    setGateway('');
    setDescription('');
  };

  // ── Mutation ──────────────────────────────────────────────────────────────

  const createMutation = useMutation({
    mutationFn: async () => {
      const body: Record<string, unknown> = { name, cidr };
      if (gateway.trim()) body.gateway = gateway.trim();
      if (description.trim()) body.description = description.trim();
      const { data, error } = await api.POST(
        '/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId!, vnet_id: vnetId } },
          body: body as never,
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as CreateResponse;
    },
    onSuccess: (resp) => {
      queryClient.invalidateQueries({ queryKey: ['subnets', tenantId, projectId, vnetId] });
      onCreated({ subnetId: resp.resource.id, subnetName: resp.resource.name });
      resetFormState();
      wizardResetRef.current();
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

  // ── Review summary ────────────────────────────────────────────────────────

  const cidrPreview = cidrCheck.ok ? cidrCheck.parsed : null;

  const reviewSummaryContent = (
    <ReviewSummary
      rows={[
        { key: 'Name', value: name || '—' },
        { key: 'CIDR', value: cidr || '—' },
        {
          key: 'Network',
          value: cidrPreview ? `${cidrPreview.network}/${cidrPreview.prefix}` : '—',
          hidden: !cidrPreview,
        },
        {
          key: 'Host range',
          value: cidrPreview
            ? `${cidrPreview.firstHost ?? '—'} – ${cidrPreview.lastHost ?? '—'}`
            : '—',
          hidden: !cidrPreview,
        },
        {
          key: 'Total IPs',
          value: cidrPreview ? cidrPreview.totalAddresses.toLocaleString() : '—',
          hidden: !cidrPreview,
        },
        { key: 'Gateway', value: gateway || 'auto (first usable IP)', hidden: !gateway },
        { key: 'Description', value: description || '—', hidden: !description },
      ]}
    />
  );

  // ── Wizard ────────────────────────────────────────────────────────────────

  const wizard = useWizard({
    steps: [
      {
        id: STEP_BASICS,
        title: 'Basics',
        content: (
          <>
            <div className={styles.parentInfo}>
              <span className={styles.parentInfoKey}>VNet address space</span>
              <span className={styles.parentInfoValue}>
                {vnetAddressSpace.length > 0 ? vnetAddressSpace.join(', ') : '—'}
              </span>
            </div>

            <Field
              label="Name"
              required
              hint="Lowercase letters, numbers, hyphens. Unique within this VNet."
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
                placeholder="e.g. app-subnet"
                autoFocus
              />
            </Field>

            <Field
              label="CIDR"
              required
              hint="Must be inside one of the VNet's address-space entries above and not overlap any existing subnet."
              validationState={cidrError ? 'error' : 'none'}
              validationMessage={cidrError ?? undefined}
            >
              <Input
                value={cidr}
                onChange={(_, d) => setCidr(d.value)}
                placeholder="10.10.1.0/24"
              />
            </Field>

            {cidrPreview && !cidrError && (
              <div className={styles.cidrPreview}>
                <span className={styles.cidrPreviewKey}>Network</span>
                <span>
                  {cidrPreview.network}/{cidrPreview.prefix}
                </span>
                <span className={styles.cidrPreviewKey}>First host</span>
                <span>{cidrPreview.firstHost ?? '—'}</span>
                <span className={styles.cidrPreviewKey}>Last host</span>
                <span>{cidrPreview.lastHost ?? '—'}</span>
                <span className={styles.cidrPreviewKey}>Total IPs</span>
                <span>{cidrPreview.totalAddresses.toLocaleString()}</span>
              </div>
            )}

            <Field
              label="Gateway"
              hint="Optional. Defaults to the first usable IP in the CIDR."
              validationState={gatewayError ? 'error' : 'none'}
              validationMessage={gatewayError ?? undefined}
            >
              <Input
                value={gateway}
                onChange={(_, d) => setGateway(d.value)}
                placeholder="10.10.1.1"
              />
            </Field>

            <Field label="Description" hint="Optional, max 256 chars.">
              <Textarea
                value={description}
                onChange={(_, d) => setDescription(d.value)}
                rows={2}
                placeholder="Application tier"
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
    submitLabel: 'Create subnet',
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

  // SubnetCreateDrawer does not call onClose on success — the parent
  // (BastionCreateDrawer, ClusterCreateDrawer, SubnetsTab) closes it after
  // pre-selecting the returned subnet. We keep that contract intact: onCreated
  // is called from onSuccess and the parent decides whether to close. The
  // drawer itself calls reset via wizardResetRef so next open starts fresh.
  // formValid is still derived above so the old inline-submit path compiles
  // (unused now but kept to avoid breaking any future direct-submit callers).
  void formValid;

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
          Create subnet
        </DrawerHeaderTitle>
        <Body1 className={styles.subtitle}>
          A subnet carves an IP range out of the parent VNet&apos;s address space. VMs and
          bastions attach to a subnet to get an IP.
        </Body1>
        {wizard.tabList}
      </DrawerHeader>

      <DrawerBody className={styles.body}>
        {wizard.stepContent}
      </DrawerBody>

      <DrawerFooter className={styles.footer}>
        {wizard.footer}
      </DrawerFooter>
    </OverlayDrawer>
  );
}
