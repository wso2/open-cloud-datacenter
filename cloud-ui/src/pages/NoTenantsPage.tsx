import {
  Body1,
  Button,
  Card,
  Subtitle1,
  Title2,
  makeStyles,
  tokens,
} from '@fluentui/react-components';
import {
  BuildingBank20Regular,
  Copy20Regular,
  PersonAdd20Regular,
} from '@fluentui/react-icons';
import { useState } from 'react';
import { useAuth } from '../auth/useAuth';
import RegisterTenantDialog from '../components/RegisterTenantDialog';

/**
 * Full-screen gate shown when a session is valid but GET /v1/tenants
 * returns an empty array (dc-api owns tenancy; the IdP is authn-only).
 *
 * Two variants:
 *   - Regular user (is_admin: false): ask-an-admin-to-invite-you screen.
 *     Invites are by email/directory picker, so the user only needs to
 *     share their name or email — never an opaque account ID.
 *   - Platform admin (is_admin: true) with no tenants registered yet:
 *     opens the register-tenant dialog (shared with TenantSwitcher
 *     and TenantPickerPage via RegisterTenantDialog).
 */

const useStyles = makeStyles({
  root: {
    minHeight: '100vh',
    display: 'grid',
    placeItems: 'center',
    backgroundColor: tokens.colorNeutralBackground2,
    padding: tokens.spacingHorizontalXXL,
  },
  card: {
    maxWidth: '600px',
    width: '100%',
    padding: `${tokens.spacingVerticalXXL} ${tokens.spacingHorizontalXXL}`,
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
    boxShadow: tokens.shadow16,
  },
  iconRow: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
    color: tokens.colorBrandForeground1,
  },
  iconWrap: {
    width: '40px',
    height: '40px',
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: tokens.colorBrandBackground2,
    color: tokens.colorBrandForeground1,
    display: 'grid',
    placeItems: 'center',
    flexShrink: 0,
  },
  body: {
    color: tokens.colorNeutralForeground2,
    lineHeight: tokens.lineHeightBase400,
  },
  subBlock: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXS,
  },
  subLabel: {
    fontSize: tokens.fontSizeBase200,
    fontWeight: tokens.fontWeightSemibold,
    color: tokens.colorNeutralForeground2,
    textTransform: 'uppercase',
    letterSpacing: '0.06em',
  },
  subValueWrap: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
    backgroundColor: tokens.colorNeutralBackground3,
    borderRadius: tokens.borderRadiusMedium,
    padding: `${tokens.spacingVerticalS} ${tokens.spacingHorizontalM}`,
    border: `1px solid ${tokens.colorNeutralStroke2}`,
  },
  subValue: {
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground1,
    flex: 1,
    wordBreak: 'break-all',
  },
  actions: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalM,
    flexWrap: 'wrap',
  },
  signOutLink: {
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
  },
});

export default function NoTenantsPage() {
  const styles = useStyles();
  const { user, logout } = useAuth();
  const [copied, setCopied] = useState(false);
  const [dialogOpen, setDialogOpen] = useState(false);

  const handleCopy = () => {
    if (!user?.email) return;
    void navigator.clipboard.writeText(user.email).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };

  const isAdmin = user?.isAdmin ?? false;

  return (
    <div className={styles.root}>
      <Card className={styles.card}>
        <div className={styles.iconRow}>
          <div className={styles.iconWrap}>
            {isAdmin ? <BuildingBank20Regular /> : <PersonAdd20Regular />}
          </div>
        </div>

        {isAdmin ? (
          <>
            <Title2>No tenants registered yet</Title2>
            <Subtitle1 className={styles.body}>
              You&apos;re signed in as a platform administrator. No tenants have been
              registered in the system yet. Create the first tenant below — you can invite
              members afterwards.
            </Subtitle1>
            <div className={styles.actions}>
              <Button appearance="primary" onClick={() => setDialogOpen(true)}>
                Register your first tenant
              </Button>
              <Button appearance="subtle" className={styles.signOutLink} onClick={() => void logout()}>
                Sign out
              </Button>
            </div>
          </>
        ) : (
          <>
            <Title2>You don&apos;t have access to any tenants yet</Title2>
            <Body1 className={styles.body}>
              Your account is authenticated but you haven&apos;t been invited to any
              tenant. Ask an administrator of your team&apos;s tenant to invite you —
              they can find you in the member picker by your name, or invite the
              email below directly. Access applies as soon as you&apos;re added;
              just refresh this page.
            </Body1>

            {user?.email && (
              <div className={styles.subBlock}>
                <span className={styles.subLabel}>Your email</span>
                <div className={styles.subValueWrap}>
                  <span className={styles.subValue}>{user.email}</span>
                  <Button
                    appearance="subtle"
                    size="small"
                    icon={<Copy20Regular />}
                    onClick={handleCopy}
                    aria-label="Copy email"
                  >
                    {copied ? 'Copied' : 'Copy'}
                  </Button>
                </div>
              </div>
            )}

            <div className={styles.actions}>
              <Button appearance="subtle" className={styles.signOutLink} onClick={() => void logout()}>
                Sign out
              </Button>
            </div>
          </>
        )}
      </Card>

      <RegisterTenantDialog open={dialogOpen} onOpenChange={setDialogOpen} />
    </div>
  );
}
