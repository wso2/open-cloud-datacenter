import {
  Body1,
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
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
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

export interface KeyVaultCreated {
  id: string;
  name: string;
}

interface Props {
  open: boolean;
  onClose: () => void;
  onCreated: (kv: KeyVaultCreated) => void;
}

const STEP_BASICS = 'basics';

export default function KeyVaultCreateDrawer({ open, onClose, onCreated }: Props) {
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
  const [softDeleteDays, setSoftDeleteDays] = useState(7);

  // ── Validation ────────────────────────────────────────────────────────────

  const nameValid = /^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$/.test(name);
  const softDeleteValid = softDeleteDays >= 1 && softDeleteDays <= 90;

  const validationIssues: ValidationIssue[] = [];
  if (!nameValid)
    validationIssues.push({ message: 'Key vault name is missing or invalid', targetStep: STEP_BASICS });
  if (!softDeleteValid)
    validationIssues.push({ message: 'Soft-delete retention must be 1-90 days', targetStep: STEP_BASICS });

  // ── Reset ─────────────────────────────────────────────────────────────────

  const resetFormState = () => {
    setName('');
    setSoftDeleteDays(7);
  };

  // ── Mutation ──────────────────────────────────────────────────────────────

  const createMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST(
        '/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId! } },
          body: { name, soft_delete_days: softDeleteDays },
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data!;
    },
    onSuccess: (kv) => {
      queryClient.invalidateQueries({ queryKey: ['keyvaults', tenantId, projectId] });
      onCreated({ id: kv.id, name: kv.name });
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

  // ── Review summary ────────────────────────────────────────────────────────

  const reviewSummaryContent = (
    <>
      <ReviewSummary
        rows={[
          { key: 'Name', value: name || '—' },
          { key: 'Soft-delete (days)', value: String(softDeleteDays) },
        ]}
      />
      <MessageBar intent="info">
        <MessageBarBody>
          After the vault becomes <strong>Active</strong>, retrieve its AppRole credentials
          from the vault detail page. The credentials are shown <em>once</em> — save them
          into your workload&apos;s secret store immediately.
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
            <Body1 className={styles.hint}>
              A key vault provides per-project secret storage. First vault in a tenant takes
              ~2-3 minutes to provision (cluster bootstrap); subsequent vaults are seconds.
            </Body1>

            <Field
              label="Name"
              required
              hint="Lowercase + hyphens, 2-32 chars. Unique within the project."
              validationState={name && !nameValid ? 'error' : 'none'}
              validationMessage={
                name && !nameValid ? 'Must match [a-z0-9][a-z0-9-]*[a-z0-9].' : undefined
              }
            >
              <Input
                value={name}
                onChange={(_, d) => setName(d.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))}
                placeholder="e.g. billing-secrets"
                autoFocus
              />
            </Field>

            <Field
              label="Soft-delete (days)"
              required
              hint="How long a deleted secret version is recoverable before being purged. 1-90."
              validationState={!softDeleteValid ? 'error' : 'none'}
              validationMessage={!softDeleteValid ? 'Must be between 1 and 90.' : undefined}
            >
              <SpinButton
                value={softDeleteDays}
                onChange={(_, d) => {
                  if (typeof d.value === 'number') setSoftDeleteDays(d.value);
                  else if (d.displayValue) {
                    const n = parseInt(d.displayValue, 10);
                    if (!Number.isNaN(n)) setSoftDeleteDays(n);
                  }
                }}
                min={1}
                max={90}
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
    submitLabel: 'Create key vault',
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
          Create key vault
        </DrawerHeaderTitle>
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
