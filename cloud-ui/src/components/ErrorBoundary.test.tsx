import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { FluentProvider } from '@fluentui/react-components';
import { ErrorBoundary } from './ErrorBoundary';
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
