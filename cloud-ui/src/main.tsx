import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import ThemedApp from './ThemedApp';
import './index.css';

// Calm global fetching posture: the async-provisioning pages (VM / cluster /
// VNet lists and detail pages) already poll explicitly with per-query
// refetchInterval, so aggressive global refetch-on-focus and retry storms
// only multiply dc-api load without making anything fresher. Per-query
// options still override these defaults.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: 1,
      staleTime: 30_000,
    },
  },
});

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ThemedApp />
    </QueryClientProvider>
  </StrictMode>
);
