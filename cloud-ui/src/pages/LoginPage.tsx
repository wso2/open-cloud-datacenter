import {
  Body1,
  Button,
  Card,
  Subtitle2,
  Title1,
  makeStyles,
  tokens,
} from '@fluentui/react-components';
import { ArrowRight24Regular } from '@fluentui/react-icons';
import { useNavigate } from 'react-router-dom';
import { useAuth } from '../auth/useAuth';
import wso2LogoBlack from '../assets/wso2-logo-black.webp';

const useStyles = makeStyles({
  root: {
    minHeight: '100vh',
    display: 'grid',
    placeItems: 'center',
    backgroundColor: tokens.colorNeutralBackground2,
    padding: tokens.spacingHorizontalXXL,
  },
  card: {
    width: '100%',
    maxWidth: '420px',
    padding: tokens.spacingHorizontalXXL,
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXL,
  },
  brand: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalM,
  },
  brandLogo: {
    height: '24px',
    display: 'block',
  },
  brandText: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXXS,
    lineHeight: 1.2,
  },
  brandSub: {
    color: tokens.colorNeutralForeground3,
  },
  headingBlock: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalS,
  },
  headingDescription: {
    color: tokens.colorNeutralForeground2,
  },
  buttonStack: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalS,
  },
  footer: {
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
    display: 'flex',
    justifyContent: 'space-between',
    paddingTop: tokens.spacingVerticalM,
    borderTop: `1px solid ${tokens.colorNeutralStroke2}`,
  },
});

export default function LoginPage() {
  const styles = useStyles();
  const navigate = useNavigate();
  const { user, login } = useAuth();

  const onSignIn = () => {
    if (user) {
      navigate('/tenants', { replace: true });
      return;
    }
    // BFF flow: top-level redirect to dc-api, which redirects to Asgardeo.
    // dc-api handles the callback and sets the dcapi_session cookie, then
    // 302s back to cloud-ui. No token exchange happens in the browser.
    login();
  };

  return (
    <div className={styles.root}>
      <Card className={styles.card}>
        <div className={styles.brand}>
          <img className={styles.brandLogo} src={wso2LogoBlack} alt="WSO2" />
          <div className={styles.brandText}>
            <Subtitle2>Infrastructure Platform</Subtitle2>
            <Body1 className={styles.brandSub}>Datacenter control plane (lk-dev)</Body1>
          </div>
        </div>

        <div className={styles.headingBlock}>
          <Title1>Sign in</Title1>
          <Body1 className={styles.headingDescription}>
            Use your WSO2 identity to access the lk-dev control plane. Access is limited to
            engineering and SRE personnel.
          </Body1>
        </div>

        <div className={styles.buttonStack}>
          <Button
            appearance="primary"
            size="large"
            icon={<ArrowRight24Regular />}
            iconPosition="after"
            onClick={onSignIn}
          >
            {user ? 'Continue to console' : 'Sign in with Asgardeo'}
          </Button>
          <Button appearance="subtle" size="large" disabled>
            Sign in with service account
          </Button>
        </div>

        <div className={styles.footer}>
          <span>WSO2 Infrastructure Platform · lk-dev</span>
          <span>v0.1.0</span>
        </div>
      </Card>
    </div>
  );
}
