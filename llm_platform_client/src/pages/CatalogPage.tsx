import type { TTask } from '../types';
import { Badge } from '../components/ui';

// Task catalog: every registered task is an API product. Inactive tasks are
// listed (so clients can see what exists) but flagged — predict returns 409.
const CatalogPage = ({
  tasks,
  onSelect,
}: {
  tasks: TTask[];
  onSelect: (id: string) => void;
}) => (
  <div>
    <h1 className="text-xl font-semibold text-neutral-900 mb-1">Task catalog</h1>
    <p className="text-sm text-neutral-500 mb-5">
      Each task is a ready-to-call prediction endpoint:{' '}
      <code className="bg-neutral-100 px-1 py-0.5 rounded text-xs">
        POST /v1/tasks/{'{id}'}/predict
      </code>
    </p>

    <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
      {tasks.map((t) => (
        <button
          key={t.id}
          onClick={() => onSelect(t.id)}
          className="text-left bg-white border border-neutral-200 rounded-lg p-4 cursor-pointer hover:border-neutral-400 transition-colors"
        >
          <div className="flex items-center gap-2 mb-1">
            <span className="font-mono text-sm font-semibold text-neutral-900">{t.id}</span>
            {!t.active && <Badge tone="warn">inactive</Badge>}
          </div>
          <div className="text-sm text-neutral-700 mb-2">{t.name}</div>
          {t.description && (
            <div className="text-xs text-neutral-500 mb-2 line-clamp-2">{t.description}</div>
          )}
          <div className="text-xs text-neutral-400">
            {t.model}
            {t.fallback_models?.length ? ` (+${t.fallback_models.length} fallback)` : ''} · prompt
            v{t.prompt_version}
            {t.daily_budget_usd ? ` · $${t.daily_budget_usd}/day cap` : ''}
          </div>
        </button>
      ))}
    </div>

    {tasks.length === 0 && (
      <div className="text-sm text-neutral-500">No tasks are registered yet.</div>
    )}
  </div>
);

export default CatalogPage;
