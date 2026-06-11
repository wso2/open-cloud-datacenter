import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { FluentProvider } from '@fluentui/react-components';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ApiProvider } from './api/ApiContext';
import { AuthProvider } from './auth/AuthContext';
import LoginPage from './pages/LoginPage';
import { wso2LightTheme } from './theme/themes';

function renderWithProviders(initialPath: string, ui: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <FluentProvider theme={wso2LightTheme}>
      <QueryClientProvider client={queryClient}>
        <AuthProvider>
          <ApiProvider>
            <MemoryRouter initialEntries={[initialPath]}>{ui}</MemoryRouter>
          </ApiProvider>
        </AuthProvider>
      </QueryClientProvider>
    </FluentProvider>
  );
}

describe('routing', () => {
  it('renders the login page brand and sign-in button', () => {
    renderWithProviders(
      '/login',
      <Routes>
        <Route path="/login" element={<LoginPage />} />
      </Routes>
    );
    expect(screen.getByText('Infrastructure Platform')).toBeInTheDocument();
    expect(screen.getByText('Sign in')).toBeInTheDocument();
  });
});
