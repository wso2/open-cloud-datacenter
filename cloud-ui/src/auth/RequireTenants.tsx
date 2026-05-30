import { Button, Card, Spinner, Text, makeStyles, tokens } from '@fluentui/react-components';
import { useQuery } from '@tanstack/react-query';
import { Outlet } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useAuth } from './useAuth';
import NoTenantsPage from '../pages/NoTenantsPage';

const useStyles = makeStyles({
  loading: {
    minHeight: '100vh',
    display: 'grid',
    placeItems: 'center',
    backgroundColor: tokens.colorNeutralBackground2,
  },
  errorWrap: {
    minHeight: '100vh',
    display: 'grid',
    placeItems: 'center',
    backgroundColor: tokens.colorNeutralBackground2,
    padding: tokens.spacingHorizontalXXL,
  },
  errorCard: {
    maxWidth: '480px',
    width: '100%',
    padding: `${tokens.spacingVerticalXXL} ${tokens.spacingHorizontalXXL}`,
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
    boxShadow: tokens.shadow16,
  },
});

/**
 * Layout route that gates its children on the user having at least one
 * accessible tenant per GET /v1/tenants (DB-backed, not JWT-derived).
 *
 * Admins are NOT bypassed here — an admin with no tenants registered yet
 * should land on the admin variant of NoTenantsPage so they can create
 * the first tenant, not fall through to an empty TenantPickerPage.
 *
 * The tenant list is read through the shared react-query ['tenants'] cache —
 * the same key TenantPickerPage and TenantSwitcher use. That coupling is
 * deliberate: RegisterTenantDialog invalidates ['tenants'] after a successful
 * create, which makes this gate refetch and flip empty → ready in place. So a
 * freshly registered first tenant resolves its Outlet immediately, without the
 * user having to reload the page.
 */
export default function RequireTenants() {
  const styles = useStyles();
  const { user } = useAuth();
  const api = useApi();

  const tenantsQuery = useQuery({
    queryKey: ['tenants'],
    enabled: Boolean(user),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants');
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data ?? [];
    },
  });

  // RequireAuth above us already handles the loading/unauthenticated states;
  // we only render once user is non-null.
  if (!user) return null;

  if (tenantsQuery.isLoading) {
    return (
      <div className={styles.loading}>
        <Spinner size="large" label="Loading tenants…" />
      </div>
    );
  }

  if (tenantsQuery.isError) {
    return (
      <div className={styles.errorWrap}>
        <Card className={styles.errorCard}>
          <Text weight="semibold" size={500}>Could not load tenants</Text>
          <Text>There was a problem reaching the API. Check your connection and try again.</Text>
          <Button appearance="primary" onClick={() => void tenantsQuery.refetch()}>
            Retry
          </Button>
        </Card>
      </div>
    );
  }

  if ((tenantsQuery.data ?? []).length === 0) {
    // NoTenantsPage reads user.isAdmin internally and renders the correct variant.
    return <NoTenantsPage />;
  }

  return <Outlet />;
}
