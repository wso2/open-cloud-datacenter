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
  MessageBar,
  MessageBarBody,
  Option,
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

  // ── Validation ─────────────────────────────────────────────────────────────

  const nameValid = /^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$/.test(name);
  const storageValid = storageGb >= 1;

  const validationIssues: ValidationIssue[] = [];
  if (!nameValid)
    validationIssues.push({ message: 'Database name is missing or invalid', targetStep: STEP_BASICS });
  if (!storageValid)
    validationIssues.push({ message: 'Storage must be at least 1 GB', targetStep: STEP_BASICS });

  // ── Reset ──────────────────────────────────────────────────────────────────

  const resetFormState = () => {
    setName('');
    setInstanceClass('db.t3.medium');
    setStorageGb(50);
  };

  // ── Mutation ───────────────────────────────────────────────────────────────

  const createMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST(
        '/v1/tenants/{tenant_id}/projects/{project_id}/databases',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId! } },
          body: {
            name,
            engine: 'postgres',
            instance_class: instanceClass,
            allocated_storage_gb: storageGb,
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
