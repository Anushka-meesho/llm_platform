import { useEffect, useState, type ReactNode } from 'react';
import { Typography, Spinner, Button, cn } from '@meesho/merlin-ui-tailwind';
import type {
  TRunListItem,
  TRunListResponse,
  TRunDetail,
  TRunFilters,
  TTask,
} from '../types';
import { api, ApiError } from '../api/client';
import { formatCost } from '../utils/tokens';

// AdminRunsPage is the cross-tenant prompt history: every user's runs, newest
// first, filterable and paginated. The list endpoint returns lightweight rows
// (truncated prompt preview, image count only) so the table stays fast and
// elegant no matter how large the underlying prompts or base64 images are; the
// full prompt / responses / images load on demand in the detail drawer.
const AdminRunsPage = () => {
  const [filters, setFilters] = useState<TRunFilters>({ page: 1, pageSize: 25 });
  const [data, setData] = useState<TRunListResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [forbidden, setForbidden] = useState(false);

  const [tasks, setTasks] = useState<TTask[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [selected, setSelected] = useState<string | null>(null);

  // Filter dropdown options — best-effort, the page works without them.
  useEffect(() => {
    api.listTasks().then((d) => setTasks(d.tasks)).catch(() => {});
    api.adminRunModels().then((d) => setModels(d.models)).catch(() => {});
  }, []);

  // Debounced fetch: every filter change (incl. typing) settles for 300ms
  // before hitting the server, so fast typing doesn't spam requests.
  useEffect(() => {
    const t = setTimeout(() => {
      setLoading(true);
      api
        .adminRuns(filters)
        .then((d) => {
          setData(d);
          setError(null);
          setForbidden(false);
        })
        .catch((e) => {
          if (e instanceof ApiError && e.status === 403) {
            setForbidden(true);
          } else {
            setError('Could not load run history.');
          }
        })
        .finally(() => setLoading(false));
    }, 300);
    return () => clearTimeout(t);
  }, [filters]);

  // Merge a filter patch; any change other than page resets to page 1.
  const patch = (p: Partial<TRunFilters>) =>
    setFilters((prev) => ({ ...prev, ...p, page: p.page ?? 1 }));

  if (forbidden) {
    return (
      <div className="flex-1 flex items-center justify-center bg-primary-bg">
        <Typography variant="body" size="3" className="text-tertiary-text">
          Prompt history is available to admins only.
        </Typography>
      </div>
    );
  }

  const totalPages = data?.total_pages ?? 1;
  const page = filters.page ?? 1;

  return (
    <div className="flex-1 overflow-y-auto bg-primary-bg p-6">
      <div className="mx-auto max-w-6xl flex flex-col gap-5">
        <div>
          <Typography variant="heading" size="6" className="text-primary-text">
            Prompt history
          </Typography>
          <Typography variant="body" size="3" className="text-tertiary-text">
            Every prediction across all users — prompts, models, cost, and outputs.
          </Typography>
        </div>

        {/* Filters */}
        <div className="flex flex-wrap items-end gap-3 bg-secondary-bg border border-solid border-primary-border rounded-lg p-3">
          <FilterField label="Search prompt" className="flex-1 min-w-[200px]">
            <input
              value={filters.q ?? ''}
              onChange={(e) => patch({ q: e.target.value })}
              placeholder="text contained in the prompt…"
              className={inputCls}
            />
          </FilterField>
          <FilterField label="User email">
            <input
              value={filters.userEmail ?? ''}
              onChange={(e) => patch({ userEmail: e.target.value })}
              placeholder="anyone…"
              className={inputCls}
            />
          </FilterField>
          <FilterField label="Task">
            <select
              value={filters.taskId ?? ''}
              onChange={(e) => patch({ taskId: e.target.value })}
              className={inputCls}
            >
              <option value="">All tasks</option>
              {tasks.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.id}
                </option>
              ))}
            </select>
          </FilterField>
          <FilterField label="Model">
            <select
              value={filters.model ?? ''}
              onChange={(e) => patch({ model: e.target.value })}
              className={inputCls}
            >
              <option value="">All models</option>
              {models.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </select>
          </FilterField>
          <FilterField label="Status">
            <select
              value={filters.status ?? ''}
              onChange={(e) => patch({ status: e.target.value as TRunFilters['status'] })}
              className={inputCls}
            >
              <option value="">Any</option>
              <option value="success">Success</option>
              <option value="error">Error</option>
            </select>
          </FilterField>
          <FilterField label="Type">
            <select
              value={filters.type ?? ''}
              onChange={(e) => patch({ type: e.target.value as TRunFilters['type'] })}
              className={inputCls}
            >
              <option value="">All</option>
              <option value="production">Production</option>
              <option value="test">Test</option>
            </select>
          </FilterField>
        </div>

        {/* Table */}
        <div className="border border-solid border-primary-border rounded-lg overflow-hidden">
          <div className="bg-secondary-bg px-4 py-2.5 flex items-center justify-between">
            <Typography variant="body" size="2" className="text-primary-text font-semi-bold">
              {data ? `${data.total_runs.toLocaleString()} runs` : 'Runs'}
            </Typography>
            {loading && <Spinner />}
          </div>

          <div className="overflow-x-auto">
            <table className="w-full text-left">
              <thead className="bg-secondary-bg">
                <tr>
                  <Th>Time</Th>
                  <Th>Task</Th>
                  <Th>User</Th>
                  <Th>Model</Th>
                  <Th>Prompt</Th>
                  <Th>Status</Th>
                  <Th right>Tokens</Th>
                  <Th right>Cost</Th>
                  <Th right>Latency</Th>
                </tr>
              </thead>
              <tbody>
                {data?.runs.map((r) => (
                  <RunRow key={r.id} run={r} onOpen={() => setSelected(r.run_id)} />
                ))}
              </tbody>
            </table>
          </div>

          {data && data.runs.length === 0 && !loading && (
            <div className="py-16 text-center">
              <Typography variant="body" size="3" className="text-tertiary-text">
                No runs match these filters.
              </Typography>
            </div>
          )}
          {error && (
            <div className="py-6 text-center">
              <Typography variant="body" size="3" className="text-error-text">
                {error}
              </Typography>
            </div>
          )}
        </div>

        {/* Pagination */}
        {data && data.total_pages > 1 && (
          <div className="flex items-center justify-center gap-3">
            <Button
              variant="outline"
              size="s"
              disabled={page <= 1}
              onClick={() => patch({ page: page - 1 })}
            >
              ← Prev
            </Button>
            <Typography variant="body" size="2" className="text-tertiary-text">
              Page {page} of {totalPages}
            </Typography>
            <Button
              variant="outline"
              size="s"
              disabled={page >= totalPages}
              onClick={() => patch({ page: page + 1 })}
            >
              Next →
            </Button>
          </div>
        )}
      </div>

      {selected && (
        <RunDetailDrawer key={selected} runId={selected} onClose={() => setSelected(null)} />
      )}
    </div>
  );
};

// ── Table row ───────────────────────────────────────────────────────────────

const RunRow = ({ run, onOpen }: { run: TRunListItem; onOpen: () => void }) => (
  <tr
    onClick={onOpen}
    className="border-t border-solid border-tertiary-border cursor-pointer hover:bg-secondary-bg transition-colors"
  >
    <Td>
      <span className="whitespace-nowrap text-tertiary-text">{fmtTime(run.created_at)}</span>
    </Td>
    <Td>{run.task_id ?? <span className="text-tertiary-text">—</span>}</Td>
    <Td>
      <span className="whitespace-nowrap">{run.user_email ?? '—'}</span>
    </Td>
    <Td>
      <span className="whitespace-nowrap">{run.model}</span>
    </Td>
    <Td>
      <div className="flex items-center gap-2 max-w-[320px]">
        {run.has_image && (
          <span title={`${run.image_count} image(s)`} className="shrink-0">
            🖼️{run.image_count > 1 ? `×${run.image_count}` : ''}
          </span>
        )}
        <span className="truncate text-secondary-text">{run.prompt_preview}</span>
      </div>
    </Td>
    <Td>
      <div className="flex flex-wrap gap-1">
        <Pill tone={run.success ? 'ok' : 'error'}>{run.success ? 'ok' : 'error'}</Pill>
        {run.cache_hit && <Pill tone="neutral">cached</Pill>}
        {run.fallback_used && <Pill tone="warn">fallback</Pill>}
        {run.is_test && <Pill tone="neutral">test</Pill>}
      </div>
    </Td>
    <Td right>{run.total_tokens.toLocaleString()}</Td>
    <Td right>{formatCost(run.cost_usd)}</Td>
    <Td right>{run.latency_ms}ms</Td>
  </tr>
);

// ── Detail drawer ─────────────────────────────────────────────────────────────

const RunDetailDrawer = ({ runId, onClose }: { runId: string; onClose: () => void }) => {
  const [detail, setDetail] = useState<TRunDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  // The parent remounts this component per run (key={runId}), so state starts
  // fresh — the effect only needs to fetch.
  useEffect(() => {
    api
      .adminRun(runId)
      .then(setDetail)
      .catch(() => setError('Could not load this run.'));
  }, [runId]);

  // Close on Escape.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-50 flex">
      {/* Backdrop */}
      <div className="flex-1 bg-black/40" onClick={onClose} />
      {/* Panel */}
      <div className="w-[min(720px,92vw)] h-full bg-primary-bg border-l border-solid border-primary-border shadow-xl flex flex-col">
        <div className="flex items-center justify-between px-5 h-14 flex-shrink-0 border-b border-solid border-primary-border bg-secondary-bg">
          <Typography variant="heading" size="4" className="text-primary-text">
            Run detail
          </Typography>
          <Button variant="ghost" size="s" onClick={onClose}>
            ✕ Close
          </Button>
        </div>

        <div className="flex-1 overflow-y-auto p-5 flex flex-col gap-5">
          {error && (
            <Typography variant="body" size="3" className="text-error-text">
              {error}
            </Typography>
          )}
          {!detail && !error && (
            <div className="flex justify-center py-10">
              <Spinner />
            </div>
          )}

          {detail && (
            <>
              {/* Meta */}
              <div className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-tertiary-text">
                <Meta label="Run">{detail.run_id}</Meta>
                <Meta label="Task">{detail.task_id ?? '—'}</Meta>
                <Meta label="User">{detail.user_email ?? '—'}</Meta>
                <Meta label="Prompt v">{detail.prompt_version}</Meta>
                <Meta label="When">{fmtTime(detail.created_at)}</Meta>
                {detail.is_test && <Meta label="Type">test run</Meta>}
              </div>

              {/* Images */}
              {detail.images.length > 0 && (
                <DetailSection title={`Images (${detail.images.length})`}>
                  <div className="grid grid-cols-2 sm:grid-cols-3 gap-3">
                    {detail.images.map((src, i) => (
                      <a
                        key={i}
                        href={src}
                        target="_blank"
                        rel="noreferrer"
                        className="block border border-solid border-primary-border rounded-md overflow-hidden bg-secondary-bg"
                      >
                        <img
                          src={src}
                          alt={`image ${i + 1}`}
                          loading="lazy"
                          className="w-full h-32 object-contain"
                        />
                      </a>
                    ))}
                  </div>
                </DetailSection>
              )}

              {/* System prompt */}
              {detail.system_prompt && (
                <DetailSection title="System prompt">
                  <Pre>{detail.system_prompt}</Pre>
                </DetailSection>
              )}

              {/* Prompt */}
              <DetailSection title="Prompt">
                <Pre>{detail.prompt}</Pre>
              </DetailSection>

              {/* Results */}
              <DetailSection title={`Results (${detail.results.length})`}>
                <div className="flex flex-col gap-3">
                  {detail.results.map((res, i) => (
                    <div
                      key={i}
                      className="border border-solid border-primary-border rounded-md overflow-hidden"
                    >
                      <div className="flex items-center gap-2 flex-wrap bg-secondary-bg px-3 py-2">
                        <span className="text-sm font-medium text-primary-text">{res.model}</span>
                        {res.provider && (
                          <span className="text-xs text-tertiary-text">({res.provider})</span>
                        )}
                        <Pill tone={res.success ? 'ok' : 'error'}>
                          {res.success ? 'ok' : 'error'}
                        </Pill>
                        {res.cache_hit && <Pill tone="neutral">cached</Pill>}
                        {res.fallback_used && <Pill tone="warn">fallback</Pill>}
                        <span className="ml-auto text-xs text-tertiary-text">
                          {res.total_tokens.toLocaleString()} tok · {formatCost(res.cost_usd)} ·{' '}
                          {res.latency_ms}ms
                        </span>
                      </div>
                      <Pre>{res.error ? `⚠️ ${res.error}` : res.response ?? '(no response)'}</Pre>
                    </div>
                  ))}
                </div>
              </DetailSection>
            </>
          )}
        </div>
      </div>
    </div>
  );
};

// ── Small building blocks ───────────────────────────────────────────────────

const inputCls =
  'border border-solid border-primary-border rounded-md px-2 py-1.5 text-sm bg-primary-bg text-primary-text outline-none focus:border-accent';

const FilterField = ({
  label,
  children,
  className,
}: {
  label: string;
  children: ReactNode;
  className?: string;
}) => (
  <label className={cn('flex flex-col gap-1', className)}>
    <span className="text-[10px] uppercase tracking-wider text-tertiary-text">{label}</span>
    {children}
  </label>
);

const DetailSection = ({ title, children }: { title: string; children: ReactNode }) => (
  <div className="flex flex-col gap-2">
    <Typography variant="body" size="2" className="text-primary-text font-semi-bold">
      {title}
    </Typography>
    {children}
  </div>
);

// Pre renders long text safely: wraps, scrolls vertically past a cap, and never
// blows out the drawer width — the guard against huge prompts/responses.
const Pre = ({ children }: { children: ReactNode }) => (
  <pre className="m-0 px-3 py-2 text-xs whitespace-pre-wrap break-words bg-secondary-bg rounded-md max-h-80 overflow-y-auto text-primary-text">
    {children}
  </pre>
);

const Meta = ({ label, children }: { label: string; children: ReactNode }) => (
  <span className="flex items-center gap-1">
    <span className="uppercase tracking-wider text-[10px]">{label}</span>
    <span className="font-mono text-primary-text break-all">{children}</span>
  </span>
);

type Tone = 'ok' | 'error' | 'warn' | 'neutral';
const TONE: Record<Tone, string> = {
  ok: 'bg-green-100 text-green-800',
  error: 'bg-red-100 text-red-800',
  warn: 'bg-amber-100 text-amber-800',
  neutral: 'bg-tertiary-bg text-secondary-text',
};
const Pill = ({ tone, children }: { tone: Tone; children: ReactNode }) => (
  <span className={cn('rounded px-1.5 py-px text-[10px] font-medium uppercase tracking-wide', TONE[tone])}>
    {children}
  </span>
);

const Th = ({ children, right }: { children: ReactNode; right?: boolean }) => (
  <th className={`px-4 py-2 ${right ? 'text-right' : 'text-left'}`}>
    <Typography variant="body" size="1" className="text-tertiary-text uppercase tracking-wider">
      {children}
    </Typography>
  </th>
);

const Td = ({ children, right }: { children: ReactNode; right?: boolean }) => (
  <td className={`px-4 py-2.5 align-top ${right ? 'text-right tabular-nums whitespace-nowrap' : ''}`}>
    <Typography variant="body" size="2" className="text-primary-text">
      {children}
    </Typography>
  </td>
);

function fmtTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString([], {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

export default AdminRunsPage;
