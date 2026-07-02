import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Button, Text, Title2, makeStyles, tokens } from '@fluentui/react-components';
import { ErrorCircle48Regular } from '@fluentui/react-icons';
import { useRouteError } from 'react-router-dom';

/**
 * Render-error fallbacks, in two layers:
 *
 *  - RouteErrorFallback is the one that actually fires for page crashes.
 *    Data routers (createBrowserRouter) install their own error boundary
 *    at every route level and NEVER let render errors propagate out of
 *    RouterProvider — without an errorElement of our own, users get
 *    react-router's unthemed "Unexpected Application Error!" screen. The
 *    router mounts this on a pathless root route so every route inherits it.
 *
 *  - ErrorBoundary is the last resort for render errors react-router can't
 *    see: the provider chain it wraps in ThemedApp (Auth/Api/ConfirmDialog).
 *    Error boundaries must be class components (React exposes
 *    getDerivedStateFromError only on classes), so the class stays minimal
 *    and delegates all presentation to the same themed function component.
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
    maxWidth: '480px',
    width: '100%',
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    textAlign: 'center',
    gap: tokens.spacingVerticalL,
    padding: `${tokens.spacingVerticalXXL} ${tokens.spacingHorizontalXXL}`,
    borderRadius: tokens.borderRadiusXLarge,
    backgroundColor: tokens.colorNeutralBackground1,
    boxShadow: tokens.shadow16,
  },
  icon: {
    color: tokens.colorStatusDangerForeground1,
  },
  details: {
    width: '100%',
    textAlign: 'left',
  },
  summary: {
    cursor: 'pointer',
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
  },
  message: {
    marginTop: tokens.spacingVerticalS,
    marginBottom: 0,
    padding: tokens.spacingHorizontalM,
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: tokens.colorNeutralBackground3,
    color: tokens.colorNeutralForeground2,
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase200,
    whiteSpace: 'pre-wrap',
    overflowWrap: 'anywhere',
  },
});

function ErrorFallback({ error }: { error: Error }) {
  const styles = useStyles();
  return (
    <div className={styles.root} role="alert">
      <div className={styles.card}>
        <ErrorCircle48Regular className={styles.icon} />
        <Title2>Something went wrong</Title2>
        <Text>
          The console hit an unexpected error. Reload to continue — if it keeps happening, share the
          details below with the platform team.
        </Text>
        <details className={styles.details}>
          <summary className={styles.summary}>Error details</summary>
          <pre className={styles.message}>{error.message}</pre>
        </details>
        <Button appearance="primary" onClick={() => window.location.reload()}>
          Reload
        </Button>
      </div>
    </div>
  );
}

/** Anything can be thrown; normalize so the fallback always has a message. */
function toError(raw: unknown): Error {
  return raw instanceof Error ? raw : new Error(String(raw));
}

/**
 * Route-level error element: react-router catches the render error and
 * exposes it via useRouteError; we render the same themed fallback.
 */
export function RouteErrorFallback() {
  return <ErrorFallback error={toError(useRouteError())} />;
}

interface ErrorBoundaryProps {
  children: ReactNode;
}

interface ErrorBoundaryState {
  error: Error | null;
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: unknown): ErrorBoundaryState {
    return { error: toError(error) };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Surface the component stack for debugging — the fallback only shows
    // the message.
    console.error('Unhandled render error:', error, info.componentStack);
  }

  render() {
    if (this.state.error) {
      return <ErrorFallback error={this.state.error} />;
    }
    return this.props.children;
  }
}
