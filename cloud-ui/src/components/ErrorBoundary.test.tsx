import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { FluentProvider } from '@fluentui/react-components';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { ErrorBoundary, RouteErrorFallback } from './ErrorBoundary';
import { wso2LightTheme } from '../theme/themes';

/** Renders fine until told to throw — the classic boundary probe. */
function Boom(): never {
  throw new Error('kaboom from render');
}

function renderWithTheme(ui: React.ReactNode) {
  return render(<FluentProvider theme={wso2LightTheme}>{ui}</FluentProvider>);
}

describe('ErrorBoundary', () => {
  // React logs caught render errors via console.error; silence them so the
  // intentional throw doesn't pollute the test output.
  let consoleError: ReturnType<typeof vi.spyOn>;
  beforeEach(() => {
    consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
  });
  afterEach(() => {
    consoleError.mockRestore();
  });

  it('renders its children when nothing throws', () => {
    renderWithTheme(
      <ErrorBoundary>
        <div>ALL GOOD</div>
      </ErrorBoundary>
    );
    expect(screen.getByText('ALL GOOD')).toBeInTheDocument();
    expect(screen.queryByText('Something went wrong')).not.toBeInTheDocument();
  });

  it('renders the fallback with the error message when a child throws', () => {
    renderWithTheme(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>
    );
    expect(screen.getByRole('alert')).toBeInTheDocument();
    expect(screen.getByText('Something went wrong')).toBeInTheDocument();
    expect(screen.getByText('kaboom from render')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Reload' })).toBeInTheDocument();
  });
});

describe('RouteErrorFallback', () => {
  let consoleError: ReturnType<typeof vi.spyOn>;
  beforeEach(() => {
    consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
  });
  afterEach(() => {
    consoleError.mockRestore();
  });

  it('shows the themed fallback when a route render throws', () => {
    // Mirrors buildRouter: a pathless root route carrying the errorElement,
    // so route crashes hit our fallback instead of react-router's default
    // "Unexpected Application Error!" screen.
    const router = createMemoryRouter([
      {
        errorElement: <RouteErrorFallback />,
        children: [{ path: '/', element: <Boom /> }],
      },
    ]);
    renderWithTheme(<RouterProvider router={router} />);
    expect(screen.getByRole('alert')).toBeInTheDocument();
    expect(screen.getByText('Something went wrong')).toBeInTheDocument();
    expect(screen.getByText('kaboom from render')).toBeInTheDocument();
  });

  it('wraps non-Error route errors so the message still renders', async () => {
    const router = createMemoryRouter([
      {
        errorElement: <RouteErrorFallback />,
        children: [
          {
            path: '/',
            loader: () => {
              throw 'plain string failure';
            },
            element: <div>never reached</div>,
          },
        ],
      },
    ]);
    renderWithTheme(<RouterProvider router={router} />);
    expect(await screen.findByText('plain string failure')).toBeInTheDocument();
    expect(screen.getByRole('alert')).toBeInTheDocument();
  });
});
