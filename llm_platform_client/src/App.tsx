import { useCallback, useEffect, useState } from 'react';
import type { TTask } from './types';
import { api } from './api/client';
import { principal } from './auth/token';
import { Badge, Spinner } from './components/ui';
import CatalogPage from './pages/CatalogPage';
import TaskDetailPage from './pages/TaskDetailPage';

// Client portal: the consumer-facing side of the platform. Browse the task
// catalog, try predictions against the real /v1/tasks/{id}/predict endpoint,
// and check per-task usage. Authenticated as a fixed service principal — no
// login flow (see src/auth/token.ts).
const App = () => {
  const [tasks, setTasks] = useState<TTask[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [healthy, setHealthy] = useState<boolean | null>(null);

  const refresh = useCallback(async () => {
    try {
      const { tasks } = await api.listTasks();
      setTasks(tasks);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not load the task catalog.');
    } finally {
      setLoading(false);
    }
  }, []);

  // Initial load + keep the catalog in sync with the server: refetch when the
  // window regains focus (e.g. after a redeploy in the Studio) and every 30s.
  useEffect(() => {
    void Promise.resolve().then(refresh);
    api.health().then(() => setHealthy(true)).catch(() => setHealthy(false));

    const onFocus = () => void refresh();
    window.addEventListener('focus', onFocus);
    const timer = setInterval(() => void refresh(), 30_000);
    return () => {
      window.removeEventListener('focus', onFocus);
      clearInterval(timer);
    };
  }, [refresh]);

  const selected = tasks.find((t) => t.id === selectedId) ?? null;

  return (
    <div className="min-h-screen flex flex-col">
      <header className="bg-white border-b border-neutral-200 px-6 py-3 flex items-center gap-4">
        <button
          onClick={() => setSelectedId(null)}
          className="text-base font-semibold text-neutral-900 bg-transparent border-none cursor-pointer p-0"
        >
          LLM Platform <span className="text-neutral-400 font-normal">/ Client Portal</span>
        </button>
        <div className="flex-1" />
        {healthy !== null && (
          <Badge tone={healthy ? 'ok' : 'error'}>
            {healthy ? 'platform up' : 'platform unreachable'}
          </Badge>
        )}
        <div className="text-xs text-neutral-500">
          {principal ? (
            <>
              authenticated as{' '}
              <span className="font-mono text-neutral-700">{principal.sub}</span>
            </>
          ) : (
            'no credentials configured'
          )}
        </div>
      </header>

      <main className="flex-1 px-6 py-6 max-w-5xl w-full mx-auto">
        {loading ? (
          <div className="flex justify-center pt-20">
            <Spinner />
          </div>
        ) : error ? (
          <div className="text-sm text-red-700 bg-red-50 border border-red-200 rounded-lg px-4 py-3">
            {error} — is the backend running on :8000?
          </div>
        ) : selected ? (
          <TaskDetailPage key={selected.id} task={selected} onBack={() => setSelectedId(null)} />
        ) : (
          <CatalogPage tasks={tasks} onSelect={setSelectedId} />
        )}
      </main>
    </div>
  );
};

export default App;
