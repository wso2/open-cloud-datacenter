import {
  Button,
  Tooltip,
  makeStyles,
  shorthands,
  tokens,
} from '@fluentui/react-components';
import {
  Question20Regular,
  Alert20Regular,
  WeatherMoon20Regular,
  WeatherSunny20Regular,
} from '@fluentui/react-icons';
import TenantSwitcher from './TenantSwitcher';
import ProjectSwitcher from './ProjectSwitcher';
import UserMenu from '../components/UserMenu';
import wso2LogoBlack from '../assets/wso2-logo-black.webp';
import wso2LogoWhite from '../assets/wso2-logo-white.webp';

const useStyles = makeStyles({
  bar: {
    height: '48px',
    display: 'flex',
    alignItems: 'center',
    paddingLeft: tokens.spacingHorizontalL,
    paddingRight: tokens.spacingHorizontalL,
    gap: tokens.spacingHorizontalM,
    backgroundColor: tokens.colorNeutralBackground1,
    ...shorthands.borderBottom('1px', 'solid', tokens.colorNeutralStroke2),
    position: 'sticky',
    top: 0,
    zIndex: 100,
  },
  brand: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
    minWidth: '224px',
    paddingRight: tokens.spacingHorizontalM,
    ...shorthands.borderRight('1px', 'solid', tokens.colorNeutralStroke2),
    height: '100%',
  },
  brandLogo: {
    height: '20px',
    display: 'block',
  },
  brandText: {
    display: 'flex',
    flexDirection: 'column',
    lineHeight: 1.1,
    gap: '2px',
  },
  brandTitle: { fontSize: tokens.fontSizeBase300, fontWeight: 600 },
  brandSub: { fontSize: tokens.fontSizeBase100, color: tokens.colorNeutralForeground3 },
  spacer: { flex: 1 },
  rightGroup: { display: 'flex', alignItems: 'center', gap: tokens.spacingHorizontalXS },
});

interface TopBarProps {
  isDark: boolean;
  onToggleDark: (next: boolean) => void;
}

export default function TopBar({ isDark, onToggleDark }: TopBarProps) {
  const styles = useStyles();

  return (
    <header className={styles.bar}>
      <div className={styles.brand}>
        <img
          className={styles.brandLogo}
          src={isDark ? wso2LogoWhite : wso2LogoBlack}
          alt="WSO2"
        />
        <div className={styles.brandText}>
          <div className={styles.brandTitle}>Infrastructure Platform</div>
          <div className={styles.brandSub}>lk-dev</div>
        </div>
      </div>

      <TenantSwitcher />
      <ProjectSwitcher />

      <div className={styles.spacer} />

      <div className={styles.rightGroup}>
        <Tooltip content={isDark ? 'Switch to light mode' : 'Switch to dark mode'} relationship="label">
          <Button
            appearance="subtle"
            icon={isDark ? <WeatherSunny20Regular /> : <WeatherMoon20Regular />}
            onClick={() => onToggleDark(!isDark)}
          />
        </Tooltip>
        <Tooltip content="Help" relationship="label">
          <Button appearance="subtle" icon={<Question20Regular />} />
        </Tooltip>
        <Tooltip content="Notifications" relationship="label">
          <Button appearance="subtle" icon={<Alert20Regular />} />
        </Tooltip>

        <UserMenu />
      </div>
    </header>
  );
}
