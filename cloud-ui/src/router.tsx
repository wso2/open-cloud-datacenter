import { createBrowserRouter, Navigate, type RouteObject } from 'react-router-dom';
import RequireAuth from './auth/RequireAuth';
import RequireTenants from './auth/RequireTenants';
import RequireProject from './auth/RequireProject';
import AppShell from './layout/AppShell';
import CallbackPage from './pages/CallbackPage';
import LoginPage from './pages/LoginPage';
import PlaceholderPage from './pages/PlaceholderPage';
import ProjectPickerPage from './pages/ProjectPickerPage';
import SilentCallbackPage from './pages/SilentCallbackPage';
import TenantPickerPage from './pages/TenantPickerPage';
import ClusterDetailPage from './pages/ClusterDetailPage';
import ClustersListPage from './pages/ClustersListPage';
import ImagesPage from './pages/ImagesPage';
import KeyVaultDetailPage from './pages/KeyVaultDetailPage';
import KeyVaultsListPage from './pages/KeyVaultsListPage';
import DatabaseDetailPage from './pages/DatabaseDetailPage';
import DatabasesListPage from './pages/DatabasesListPage';
import MembersPage from './pages/MembersPage';
import ServiceAccountsPage from './pages/ServiceAccountsPage';
import NSGDetailPage from './pages/NSGDetailPage';
import BastionsListPage from './pages/BastionsListPage';
import BastionDetailPage from './pages/BastionDetailPage';
import NSGsListPage from './pages/NSGsListPage';
import VNetDetailPage from './pages/VNetDetailPage';
import VNetsListPage from './pages/VNetsListPage';
import VmDetailPage from './pages/VmDetailPage';
import VmsListPage from './pages/VmsListPage';

interface BuildRouterArgs {
  isDark: boolean;
  onToggleDark: (next: boolean) => void;
}

export function buildRouter({ isDark, onToggleDark }: BuildRouterArgs) {
  // Project-scoped routes — every resource that lives under /projects/:projectId/
  const projectScopedChildren: RouteObject[] = [
    { index: true, element: <Navigate to="dashboard" replace /> },
    {
      path: 'dashboard',
      element: <PlaceholderPage title="Dashboard" subtitle="Project overview" />,
    },
    {
      path: 'activity',
      element: <PlaceholderPage title="Activity" subtitle="Audit trail across this project" />,
    },
    { path: 'vms', element: <VmsListPage /> },
    { path: 'vms/:vmId', element: <VmDetailPage /> },
    { path: 'bastions', element: <BastionsListPage /> },
    { path: 'bastions/:bastionId', element: <BastionDetailPage /> },
    { path: 'clusters', element: <ClustersListPage /> },
    { path: 'clusters/:clusterId', element: <ClusterDetailPage /> },
    { path: 'vnets', element: <VNetsListPage /> },
    { path: 'vnets/:vnetId', element: <VNetDetailPage /> },
    { path: 'nsgs', element: <NSGsListPage /> },
    { path: 'nsgs/:nsgId', element: <NSGDetailPage /> },
    {
      path: 'dns',
      element: (
        <PlaceholderPage
          title="Private DNS"
          subtitle="Available once a DNS resolver is wired into the data plane"
          description="Private DNS zones are accepted by the API today but no resolver consumes them — no VM looks at the stored ConfigMaps. Re-enabled in the UI when the DNS resolver path lands (tracked as F13 in FOLLOWUPS.md)."
        />
      ),
    },
    { path: 'service-accounts', element: <ServiceAccountsPage /> },
    { path: 'keyvaults', element: <KeyVaultsListPage /> },
    { path: 'keyvaults/:kvId', element: <KeyVaultDetailPage /> },
    { path: 'databases', element: <DatabasesListPage /> },
    { path: 'databases/:dbId', element: <DatabaseDetailPage /> },
    { path: 'access', element: <MembersPage /> },
  ];

  const routes: RouteObject[] = [
    { path: '/', element: <Navigate to="/login" replace /> },
    { path: '/login', element: <LoginPage /> },
    { path: '/callback', element: <CallbackPage /> },
    { path: '/silent-callback', element: <SilentCallbackPage /> },
    {
      element: <RequireAuth />,
      children: [
        {
          element: <RequireTenants />,
          children: [
            { path: '/tenants', element: <TenantPickerPage /> },
            {
              path: '/tenants/:tenantId',
              element: <AppShell isDark={isDark} onToggleDark={onToggleDark} />,
              children: [
                // Tenant root → project picker
                { index: true, element: <ProjectPickerPage /> },

                // Tenant-scoped pages (not under a project)
                { path: 'members', element: <MembersPage /> },
                { path: 'iam', element: <MembersPage /> },
                { path: 'images', element: <ImagesPage /> },
                { path: 'keys', element: <PlaceholderPage title="SSH keys" subtitle="Coming in a later chunk" /> },

                // Project-scoped pages
                {
                  path: 'projects/:projectId',
                  element: <RequireProject />,
                  children: projectScopedChildren,
                },
              ],
            },
          ],
        },
      ],
    },
    { path: '*', element: <Navigate to="/login" replace /> },
  ];

  return createBrowserRouter(routes);
}
