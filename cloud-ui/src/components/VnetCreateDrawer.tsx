import {
  Body1,
  Body2,
  Button,
  DrawerBody,
  DrawerFooter,
  DrawerHeader,
  DrawerHeaderTitle,
  Field,
  Input,
  MessageBar,
  MessageBarBody,
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
import { Dismiss24Regular, Info20Regular } from '@fluentui/react-icons';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { findReservedOverlap, validateCidr } from '../lib/cidr';
import { ReviewSummary } from './wizard/ReviewSummary';
import { useWizard } from './wizard/CreateWizard';
import type { ValidationIssue } from './wizard/CreateWizard';

// ── Constants ─────────────────────────────────────────────────────────────────

const STEP_BASICS = 'basics';
const STEP_ADDRESS = 'address';

// Region "lk" is the only one seeded in dc-api's regions table today
// (see dc-api/internal/db/schema.sql). Multi-region needs F12 — replace
// with GET /v1/regions when EU/US datacenters launch.
const HARDCODED_REGION = 'lk';

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
  cidrRow: { display: 'flex', gap: tokens.spacingHorizontalS, alignItems: 'flex-start' },
  cidrAdd: { alignSelf: 'flex-start' },
  monoBlock: { fontFamily: tokens.fontFamilyMonospace },
});

// ── Local types ───────────────────────────────────────────────────────────────

interface CreateResponse {
  resource: { id: string; name: string };
  note: string;
}

export interface VnetCreateResult {
  vnetId: string;
  vnetName: string;
}

interface VnetCreateDrawerProps {
  open: boolean;
  onClose: () => void;
  onCreated: (result: VnetCreateResult) => void;
}

// ── Component ─────────────────────────────────────────────────────────────────

export default function VnetCreateDrawer({ open, onClose, onCreated }: VnetCreateDrawerProps) {
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
  const [cidrInputs, setCidrInputs] = useState<string[]>(['10.10.0.0/16']);

  // ── Mutation ──────────────────────────────────────────────────────────────

  const createMutation = useMutation({
    mutationFn: async () => {
      const body = {
        name,
        address_space: cidrInputs.map((c) => c.trim()).filter(Boolean),
        region: HARDCODED_REGION,
        description: description.trim() || undefined,
      };
      const { data, error } = await api.POST(
        '/v1/tenants/{tenant_id}/projects/{project_id}/vnets',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId! } },
          body: body as never,
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data as CreateResponse;
    },
    onSuccess: (resp) => {
      queryClient.invalidateQueries({ queryKey: ['vnets', tenantId, projectId] });
      onCreated({ vnetId: resp.resource.id, vnetName: resp.resource.name });
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
    setDescription('');
    setCidrInputs(['10.10.0.0/16']);
  };

  // ── Derived / validation ──────────────────────────────────────────────────

  const nameValid = /^[a-z][a-z0-9-]{0,61}[a-z0-9]$/.test(name);

  const cidrValidations = cidrInputs.map((c) => {
    const v = validateCidr(c, { minPrefix: 8, maxPrefix: 28, requireRFC1918: true });
    if (!v.ok) return v;
    const clash = findReservedOverlap(v.parsed, HARDCODED_REGION);
    if (clash) {
      return {
        ok: false as const,
        reason: `Overlaps reserved CIDR ${clash.cidr} (${clash.label}) in region ${HARDCODED_REGION}.`,
      };
    }
    return v;
  });
  const cidrAllValid = cidrInputs.length > 0 && cidrValidations.every((v) => v.ok);

  const setCidr = (idx: number, value: string) => {
    setCidrInputs((cs) => cs.map((c, i) => (i === idx ? value : c)));
  };
  const addCidr = () => setCidrInputs((cs) => (cs.length < 5 ? [...cs, ''] : cs));
  const removeCidr = (idx: number) =>
    setCidrInputs((cs) => (cs.length > 1 ? cs.filter((_, i) => i !== idx) : cs));

  const validationIssues: ValidationIssue[] = [];
  if (!nameValid)
    validationIssues.push({ message: 'VNet name is missing or invalid', targetStep: STEP_BASICS });
  if (!cidrAllValid)
    validationIssues.push({
      message: 'One or more address-space CIDRs are invalid',
      targetStep: STEP_ADDRESS,
    });

  // ── Review summary content ────────────────────────────────────────────────

  const reviewSummaryContent = (
    <>
      <ReviewSummary
        rows={[
          { key: 'Name', value: name || '—' },
          {
            key: 'Description',
            value: description || '—',
            hidden: !description,
          },
          { key: 'Region', value: HARDCODED_REGION },
          {
            key: 'Address space',
            value: (
              <>
                {cidrInputs.map((c, i) => (
                  <span key={i} className={styles.monoBlock} style={{ display: 'block' }}>
                    {c}
                  </span>
                ))}
              </>
            ),
          },
        ]}
      />
      <Body2 className={styles.subtitle}>
        You&apos;ll add subnets to this VNet from its detail page after it&apos;s Active.
      </Body2>
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
              hint="Lowercase letters, numbers, hyphens. Must be unique within the tenant."
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
                placeholder="e.g. prod-vnet"
                autoFocus
              />
            </Field>
            <Field label="Description" hint="Optional, max 256 chars.">
              <Textarea
                value={description}
                onChange={(_, d) => setDescription(d.value)}
                placeholder="Production tier VPC"
                rows={2}
              />
            </Field>
            <Field label="Region">
              <Input value={HARDCODED_REGION} disabled />
            </Field>
            <MessageBar intent="info" icon={<Info20Regular />}>
              <MessageBarBody>
                Only <code>{HARDCODED_REGION}</code> is available today. Multi-region selection
                comes when EU/US datacenters launch.
              </MessageBarBody>
            </MessageBar>
          </>
        ),
      },
      {
        id: STEP_ADDRESS,
        title: 'Address space',
        content: (
          <>
            <Field
              label="Address space"
              hint="One or more RFC1918 CIDRs (10.x, 172.16-31.x, 192.168.x). /8 to /28. Max 5."
            >
              <></>
            </Field>
            {cidrInputs.map((cidr, idx) => {
              const v = cidrValidations[idx];
              const parsed = v.ok ? v.parsed : null;
              return (
                <div key={idx}>
                  <div className={styles.cidrRow}>
                    <Field
                      style={{ flex: 1 }}
                      validationState={cidr && !v.ok ? 'error' : 'none'}
                      validationMessage={cidr && !v.ok ? v.reason : undefined}
                    >
                      <Input
                        value={cidr}
                        onChange={(_, d) => setCidr(idx, d.value)}
                        placeholder="10.10.0.0/16"
                      />
                    </Field>
                    {cidrInputs.length > 1 && (
                      <Button
                        appearance="subtle"
                        onClick={() => removeCidr(idx)}
                        style={{ marginTop: '4px' }}
                      >
                        Remove
                      </Button>
                    )}
                  </div>
                  {parsed && (
                    <div
                      className={styles.cidrPreview}
                      style={{ marginTop: tokens.spacingVerticalXS }}
                    >
                      <span className={styles.cidrPreviewKey}>Network</span>
                      <span>
                        {parsed.network}/{parsed.prefix}
                      </span>
                      <span className={styles.cidrPreviewKey}>First host</span>
                      <span>{parsed.firstHost ?? '—'}</span>
                      <span className={styles.cidrPreviewKey}>Last host</span>
                      <span>{parsed.lastHost ?? '—'}</span>
                      <span className={styles.cidrPreviewKey}>Total IPs</span>
                      <span>{parsed.totalAddresses.toLocaleString()}</span>
                    </div>
                  )}
                </div>
              );
            })}
            {cidrInputs.length < 5 && (
              <Button appearance="subtle" onClick={addCidr} className={styles.cidrAdd}>
                + Add CIDR
              </Button>
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
    submitLabel: 'Create virtual network',
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
          Create virtual network
        </DrawerHeaderTitle>
        <Body1 className={styles.subtitle}>
          A VNet is the top-level network scope. Subnets, peerings, and route tables live inside
          it.
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
