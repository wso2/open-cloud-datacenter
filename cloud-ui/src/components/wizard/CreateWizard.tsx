/**
 * CreateWizard — shared wizard scaffolding for all create drawers.
 *
 * Usage pattern:
 *
 *   const wizard = useWizard({ steps, issues, onSubmit, onCancel, submitLabel });
 *
 *   <OverlayDrawer ...>
 *     <DrawerHeader>
 *       <DrawerHeaderTitle ...>Create foo</DrawerHeaderTitle>
 *       <Body1>Subtitle copy.</Body1>
 *       {wizard.tabList}
 *     </DrawerHeader>
 *     <DrawerBody className={styles.body}>
 *       {wizard.stepContent}
 *     </DrawerBody>
 *     <DrawerFooter className={styles.footer}>
 *       {wizard.footer}
 *     </DrawerFooter>
 *   </OverlayDrawer>
 *
 * The wizard owns navigation state only. Form state lives in the consumer.
 *
 * IMPORTANT: tabList / stepContent / footer are ELEMENTS, not components, and are
 * dropped in as {wizard.stepContent}. This matters: if the wizard returned
 * components (re-created every render) and consumers rendered <wizard.StepContent/>,
 * React would see a new component type each render and unmount+remount the whole
 * step subtree on EVERY keystroke — stealing focus from form inputs and re-firing
 * autoFocus after a single character. Returning elements lets React reconcile the
 * step content in place, so inputs keep their focus and cursor.
 */

import {
  Body1,
  Button,
  Link,
  MessageBar,
  MessageBarBody,
  MessageBarTitle,
  Subtitle2,
  Tab,
  TabList,
  makeStyles,
  tokens,
} from '@fluentui/react-components';
import { type ReactNode, useState } from 'react';

// ── Public types ─────────────────────────────────────────────────────────────

export interface WizardStep {
  /** Stable key used to match ValidationIssue.targetStep. */
  id: string;
  /** Tab label visible in the step navigator. */
  title: string;
  /** The step's form content, rendered when this step is active. */
  content: ReactNode;
}

export interface ValidationIssue {
  /** Human-readable description shown in the Review issue list. */
  message: string;
  /** step.id to jump to when the user clicks "Go to …". */
  targetStep: string;
}

export interface UseWizardOptions {
  steps: WizardStep[];
  issues: ValidationIssue[];
  /** Optional content rendered above the issue list on the Review step. */
  reviewSummary?: ReactNode;
  onSubmit: () => void;
  onCancel: () => void;
  submitLabel: string;
  submitting?: boolean;
  /** API error string shown on the Review step. */
  submitError?: string | null;
  /**
   * Render-prop for an extra button in the right footer slot of a specific
   * step. Receives a `goToNext` callback so the button can advance the wizard.
   * Typical use: "Skip" on the optional worker pools step.
   */
  extraFooterAction?: (goToNext: () => void) => ReactNode;
  /** Zero-based index of the step that shows extraFooterAction. */
  extraFooterActionStep?: number;
}

// ── Styles ────────────────────────────────────────────────────────────────────

const useStyles = makeStyles({
  tabListWrapper: {
    marginTop: tokens.spacingVerticalS,
    marginBottom: tokens.spacingVerticalXS,
    marginLeft: `-${tokens.spacingHorizontalS}`,
  },
  reviewBody: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  reviewHeader: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXS,
  },
  reviewSubtitle: { display: 'block', color: tokens.colorNeutralForeground3 },
  issueTitle: {
    display: 'block',
    marginTop: tokens.spacingVerticalS,
  },
  issueList: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalS,
    marginTop: tokens.spacingVerticalS,
    paddingLeft: 0,
    listStyle: 'none',
    fontSize: tokens.fontSizeBase200,
  },
  issueRow: {
    display: 'flex',
    alignItems: 'baseline',
    justifyContent: 'space-between',
    gap: tokens.spacingHorizontalM,
    color: tokens.colorPaletteRedForeground1,
    paddingLeft: tokens.spacingHorizontalM,
    position: 'relative',
    ':before': {
      content: '"\\2022"',
      position: 'absolute',
      left: 0,
      color: tokens.colorPaletteRedForeground1,
      fontSize: tokens.fontSizeBase300,
      lineHeight: 1,
    },
  },
  issueMessage: { flex: 1 },
  footerRight: { display: 'flex', gap: tokens.spacingHorizontalS },
});

// ── Hook ─────────────────────────────────────────────────────────────────────

const REVIEW_TAB_TITLE = 'Review';

export function useWizard({
  steps,
  issues,
  reviewSummary,
  onSubmit,
  onCancel,
  submitLabel,
  submitting = false,
  submitError,
  extraFooterAction,
  extraFooterActionStep,
}: UseWizardOptions) {
  const styles = useStyles();

  const [stepIndex, setStepIndex] = useState(0);
  const [showAllErrors, setShowAllErrors] = useState(false);

  const lastIndex = steps.length; // Review is always at steps.length
  const isReview = stepIndex === lastIndex;

  const goToStep = (target: number) => {
    if (target === lastIndex) setShowAllErrors(true);
    setStepIndex(target);
  };

  const jumpToIssueStep = (targetId: string) => {
    const idx = steps.findIndex((s) => s.id === targetId);
    if (idx !== -1) setStepIndex(idx);
  };

  const reset = () => {
    setStepIndex(0);
    setShowAllErrors(false);
  };

  const canCreate = issues.length === 0 && !submitting;

  // ── Elements (not components — see file header) ─────────────────────────────

  const tabList = (
    <div className={styles.tabListWrapper}>
      <TabList
        size="small"
        selectedValue={String(stepIndex)}
        onTabSelect={(_, d) => goToStep(Number(d.value))}
      >
        {steps.map((tab, i) => (
          <Tab key={tab.id} value={String(i)}>
            {tab.title}
          </Tab>
        ))}
        <Tab value={String(lastIndex)}>{REVIEW_TAB_TITLE}</Tab>
      </TabList>
    </div>
  );

  const stepContent = !isReview ? (
    <>{steps[stepIndex].content}</>
  ) : (
    <div className={styles.reviewBody}>
      <div className={styles.reviewHeader}>
        <Subtitle2 block>Review and create</Subtitle2>
        <Body1 className={styles.reviewSubtitle}>
          Review all settings before submitting. Provisioning begins immediately.
        </Body1>
      </div>

      {showAllErrors && issues.length > 0 && (
        <MessageBar intent="error">
          <MessageBarBody>
            <MessageBarTitle className={styles.issueTitle}>
              Fix the following before creating
            </MessageBarTitle>
            <ul className={styles.issueList}>
              {issues.map((issue, i) => {
                const stepTitle =
                  steps.find((s) => s.id === issue.targetStep)?.title ?? issue.targetStep;
                return (
                  <li key={i} className={styles.issueRow}>
                    <span className={styles.issueMessage}>{issue.message}</span>
                    <Link
                      as="button"
                      type="button"
                      onClick={() => jumpToIssueStep(issue.targetStep)}
                      style={{ fontSize: tokens.fontSizeBase200, whiteSpace: 'nowrap' }}
                    >
                      Go to {stepTitle}
                    </Link>
                  </li>
                );
              })}
            </ul>
          </MessageBarBody>
        </MessageBar>
      )}

      {reviewSummary}

      {submitError && (
        <MessageBar intent="error">
          <MessageBarBody>
            <MessageBarTitle>Create failed</MessageBarTitle>
            {submitError}
          </MessageBarBody>
        </MessageBar>
      )}
    </div>
  );

  const footer = (
    <>
      <Button appearance="subtle" onClick={onCancel} disabled={submitting}>
        Cancel
      </Button>
      <div className={styles.footerRight}>
        {stepIndex > 0 && (
          <Button
            appearance="secondary"
            onClick={() => setStepIndex((s) => s - 1)}
            disabled={submitting}
          >
            Back
          </Button>
        )}
        {extraFooterAction !== undefined &&
          stepIndex === (extraFooterActionStep ?? -1) && (
            <>{extraFooterAction(() => goToStep(stepIndex + 1))}</>
          )}
        {isReview ? (
          <Button appearance="primary" onClick={onSubmit} disabled={!canCreate}>
            {submitting ? 'Creating…' : submitLabel}
          </Button>
        ) : (
          <Button
            appearance="primary"
            onClick={() => goToStep(stepIndex + 1)}
            disabled={submitting}
          >
            Next
          </Button>
        )}
      </div>
    </>
  );

  return { tabList, stepContent, footer, reset, stepIndex, goToStep };
}
