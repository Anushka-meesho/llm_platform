import { useCallback, useEffect, useState } from 'react';
import { Spinner, Typography, cn } from '@meesho/merlin-ui-tailwind';
import { api } from '../api/client';
import { useAuth } from '../auth/useAuth';
import { can } from '../auth/permissions';
import VersionHistory from '../components/VersionHistory';
import type { TPromptVersion, TTask } from '../types';

// VersionsPage is a dedicated, task-agnostic home for prompt version history:
// pick a task on the left, manage its versions on the right. It reuses the same
// VersionHistory component the Studio task detail embeds, so behaviour (compare,
// deploy, admin delete, pagination, timestamps) is identical in both places.
const VersionsPage = () => {
  const { user } = useAuth();
  const canDeploy = can(user?.role, 'task:deploy');
  const canDelete = can(user?.role, 'task:delete');

  const [tasks, setTasks] = useState<TTask[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [versions, setVersions] = useState<TPromptVersion[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    void (async () => {
      try {
        const { tasks } = await api.listTasks();
        setTasks(tasks);
        setSelectedId((prev) => prev ?? tasks[0]?.id ?? null);
      } catch {
        setError('Could not load tasks.');
      } finally {
        setLoading(false);
      }
    })();
  }, []);

  const selected = tasks.find((t) => t.id === selectedId) ?? null;

  const loadVersions = useCallback(async () => {
    if (!selectedId) return;
    const data = await api.listVersions(selectedId).catch(() => null);
    if (data) setVersions(data.versions);
  }, [selectedId]);

  useEffect(() => {
    void Promise.resolve().then(async () => {
      setVersions([]);
      await loadVersions();
    });
  }, [loadVersions]);

  // After a deploy the selected task's active version changes — refresh the
  // task list so the live flag and live-prompt comparison stay correct.
  const refreshTasks = useCallback(async () => {
    const { tasks } = await api.listTasks().catch(() => ({ tasks: [] as TTask[] }));
    if (tasks.length) setTasks(tasks);
  }, []);

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center bg-primary-bg">
        <Spinner />
      </div>
    );
  }

  return (
    <div className="flex flex-1 overflow-hidden">
      <aside className="w-72 flex-shrink-0 border-r border-solid border-primary-border bg-secondary-bg overflow-y-auto">
        <div className="px-4 py-3">
          <Typography variant="body" size="1" className="text-tertiary-text uppercase tracking-wider">
            Tasks
          </Typography>
        </div>
        {error && (
          <Typography variant="body" size="2" className="text-error-text px-4">
            {error}
          </Typography>
        )}
        {tasks.map((t) => (
          <button
            key={t.id}
            onClick={() => setSelectedId(t.id)}
            className={cn(
              'w-full text-left px-4 py-2.5 border-b border-solid border-tertiary-border transition-colors',
              t.id === selectedId ? 'bg-tertiary-bg' : 'hover:bg-tertiary-bg',
            )}
          >
            <Typography variant="body" size="2" className="text-primary-text font-medium truncate">
              {t.name}
            </Typography>
            <Typography variant="body" size="1" className="text-tertiary-text">
              {t.id} · active v{t.prompt_version}
            </Typography>
          </button>
        ))}
      </aside>

      <main className="flex-1 overflow-y-auto p-6 bg-primary-bg">
        {selected ? (
          <div className="mx-auto max-w-4xl flex flex-col gap-4">
            <div>
              <Typography variant="heading" size="6" className="text-primary-text">
                {selected.name}
              </Typography>
              <Typography variant="body" size="2" className="text-tertiary-text">
                Prompt version history · active v{selected.prompt_version}
              </Typography>
            </div>
            <div className="border border-solid border-primary-border rounded-lg p-4">
              <VersionHistory
                taskId={selected.id}
                versions={versions}
                activeVersion={selected.prompt_version}
                livePrompt={selected.prompt_template}
                liveSystem={selected.system_prompt ?? ''}
                canDeploy={canDeploy}
                canDelete={canDelete}
                onReload={loadVersions}
                onActiveChanged={refreshTasks}
              />
            </div>
          </div>
        ) : (
          <Typography variant="body" size="2" className="text-tertiary-text">
            No tasks registered yet.
          </Typography>
        )}
      </main>
    </div>
  );
};

export default VersionsPage;
