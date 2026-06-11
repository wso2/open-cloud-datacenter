import {
  Avatar,
  Badge,
  Button,
  Divider,
  Popover,
  PopoverSurface,
  PopoverTrigger,
  Tooltip,
  makeStyles,
  tokens,
} from '@fluentui/react-components';
import { Copy16Regular, SignOut20Regular } from '@fluentui/react-icons';
import { useState } from 'react';
import { useAuth } from '../auth/useAuth';

/**
 * Profile popover for the top bar: who is signed in (name, email, admin
 * badge), the account ID (handy for DCAPI_PLATFORM_ADMIN_SUBS and support
 * requests), session expiry, and sign out.
 */

const useStyles = makeStyles({
  surface: {
    width: '320px',
    padding: 0,
  },
  header: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalM,
    padding: `${tokens.spacingVerticalL} ${tokens.spacingHorizontalL}`,
  },
  identity: {
    display: 'flex',
    flexDirection: 'column',
    gap: '2px',
    minWidth: 0,
  },
  name: {
    fontSize: tokens.fontSizeBase400,
    fontWeight: tokens.fontWeightSemibold,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  email: {
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  badgeRow: {
    display: 'flex',
    gap: tokens.spacingHorizontalS,
    padding: `0 ${tokens.spacingHorizontalL} ${tokens.spacingVerticalM}`,
  },
  detailRows: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalS,
    padding: `${tokens.spacingVerticalM} ${tokens.spacingHorizontalL}`,
  },
  detailLabel: {
    fontSize: tokens.fontSizeBase100,
    fontWeight: tokens.fontWeightSemibold,
    color: tokens.colorNeutralForeground3,
    textTransform: 'uppercase',
    letterSpacing: '0.05em',
  },
  detailValueRow: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalXS,
  },
  detailValue: {
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase200,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
    flex: 1,
  },
  footer: {
    display: 'flex',
    justifyContent: 'flex-end',
    padding: `${tokens.spacingVerticalM} ${tokens.spacingHorizontalL}`,
  },
});

/** Derive a display name and two-letter initials for the avatar. */
function deriveIdentity(name?: string, email?: string, sub?: string): {
  displayName: string;
  initials: string;
} {
  const displayName = name?.trim() || email?.trim() || sub?.trim() || 'User';
  const base = name?.trim() || (email ? email.split('@')[0] : sub) || 'User';
  const parts = base.split(/[\s._-]+/).filter(Boolean);
  const initials =
    ((parts[0]?.[0] ?? '') + (parts.length > 1 ? (parts.at(-1)?.[0] ?? '') : (parts[0]?.[1] ?? '')))
      .toUpperCase()
      .slice(0, 2) || 'U';
  return { displayName, initials };
}

function formatExpiry(expiresAt?: string): string {
  if (!expiresAt) return '';
  const exp = new Date(expiresAt);
  if (Number.isNaN(exp.getTime())) return '';
  const mins = Math.round((exp.getTime() - Date.now()) / 60000);
  if (mins <= 0) return 'expired';
  if (mins < 60) return `in ${mins} min`;
  const hours = Math.floor(mins / 60);
  return `in ${hours} h ${mins % 60} min`;
}

export default function UserMenu() {
  const styles = useStyles();
  const { user, logout } = useAuth();
  const [copied, setCopied] = useState(false);

  const { displayName, initials } = deriveIdentity(user?.name, user?.email, user?.sub);
  const expiry = formatExpiry(user?.expiresAt);

  const copySub = () => {
    if (!user?.sub) return;
    void navigator.clipboard.writeText(user.sub).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <Popover positioning="below-end" withArrow>
      <PopoverTrigger disableButtonEnhancement>
        <Button appearance="subtle" aria-label={`Account: ${displayName}`}>
          <Avatar size={28} name={displayName} initials={initials} color="brand" />
        </Button>
      </PopoverTrigger>
      <PopoverSurface className={styles.surface}>
        <div className={styles.header}>
          <Avatar size={48} name={displayName} initials={initials} color="brand" />
          <div className={styles.identity}>
            <span className={styles.name}>{displayName}</span>
            {user?.email && <span className={styles.email}>{user.email}</span>}
          </div>
        </div>

        <div className={styles.badgeRow}>
          {user?.isAdmin ? (
            <Badge appearance="filled" color="brand">
              Platform administrator
            </Badge>
          ) : (
            <Badge appearance="outline" color="informative">
              Member
            </Badge>
          )}
        </div>

        <Divider />

        <div className={styles.detailRows}>
          <div>
            <div className={styles.detailLabel}>Account ID</div>
            <div className={styles.detailValueRow}>
              <span className={styles.detailValue}>{user?.sub ?? '—'}</span>
              <Tooltip content={copied ? 'Copied' : 'Copy account ID'} relationship="label">
                <Button
                  appearance="subtle"
                  size="small"
                  icon={<Copy16Regular />}
                  onClick={copySub}
                  aria-label="Copy account ID"
                />
              </Tooltip>
            </div>
          </div>
          {expiry && (
            <div>
              <div className={styles.detailLabel}>Session expires</div>
              <div className={styles.detailValue}>{expiry}</div>
            </div>
          )}
        </div>

        <Divider />

        <div className={styles.footer}>
          <Button appearance="secondary" icon={<SignOut20Regular />} onClick={() => void logout()}>
            Sign out
          </Button>
        </div>
      </PopoverSurface>
    </Popover>
  );
}
