import { useCallback, useEffect, useMemo, useState, type ChangeEvent, type ReactNode } from 'react';
import { Typography, Button } from '@meesho/merlin-ui-tailwind';
import type { TTask, TTaskStatsDetail, TPredictResult, TGatewayAttempt } from '../types';
import { type PredictOutcome, api, ApiError, errorMessage } from '../api/client';
import { usePersistentState } from '../hooks/usePersistentState';
import { formatCost } from '../utils/tokens';

// ── Local UI primitives ───────────────────────────────────────────────────────

const Section = ({ title, children }: { title: string; children: ReactNode }) => (
  <div className="border border-solid border-primary-border rounded-lg p-4">
    <Typography variant="body" size="2" className="text-primary-text font-semibold mb-3">
      {title}
    </Typography>
    {children}
  </div>
);

const Stat = ({ label, value }: { label: string; value: string }) => (
  <div className="bg-secondary-bg border border-solid border-primary-border rounded-lg px-4 py-2">
    <Typography variant="body" size="1" className="text-tertiary-text uppercase tracking-wider">
      {label}
    </Typography>
    <Typography variant="heading" size="5" className="text-primary-text">
      {value}
    </Typography>
  </div>
);

const Badge = ({
  tone,
  children,
}: {
  tone: 'ok' | 'warn' | 'error' | 'neutral';
  children: ReactNode;
}) => {
  const tones = {
    ok: 'bg-emerald-50 text-emerald-700 border-emerald-200',
    warn: 'bg-amber-50 text-amber-700 border-amber-200',
    error: 'bg-red-50 text-red-700 border-red-200',
    neutral: 'bg-neutral-100 text-neutral-600 border-neutral-200',
  } as const;
  return (
    <span
      className={`text-[10px] font-semibold uppercase tracking-wider px-2 py-0.5 rounded-full border ${tones[tone]}`}
    >
      {children}
    </span>
  );
};

const Spinner = () => (
  <div className="h-6 w-6 rounded-full border-2 border-neutral-300 border-t-neutral-700 animate-spin" />
);

// ── Main page ─────────────────────────────────────────────────────────────────

const ClientPortalPage = () => {
  const [tasks, setTasks] = useState<TTask[]>([]);
  const [selectedId, setSelectedId] = usePersistentState<string | null>(
    'clientportal.selectedId',
    null,
  );
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const { tasks } = await api.listTasks();
      setTasks(tasks);
      setError(null);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void Promise.resolve().then(refresh);
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
    <div className="overflow-y-auto flex-1 px-6 py-6">
      <div className="max-w-5xl mx-auto">
        {loading ? (
          <div className="flex justify-center pt-20">
            <Spinner />
          </div>
        ) : error ? (
          <div className="text-sm text-red-700 bg-red-50 border border-red-200 rounded-lg px-4 py-3">
            {error}
          </div>
        ) : selected ? (
          <TaskDetailPage key={selected.id} task={selected} onBack={() => setSelectedId(null)} />
        ) : (
          <CatalogPage tasks={tasks} onSelect={setSelectedId} />
        )}
      </div>
    </div>
  );
};

// ── Catalog ───────────────────────────────────────────────────────────────────

const CatalogPage = ({
  tasks,
  onSelect,
}: {
  tasks: TTask[];
  onSelect: (id: string) => void;
}) => (
  <div>
    <Typography variant="heading" size="6" className="text-primary-text mb-1">
      Task catalog
    </Typography>
    <Typography variant="body" size="2" className="text-secondary-text mb-5">
      Each task is a ready-to-call prediction endpoint:{' '}
      <code className="bg-secondary-bg px-1 py-0.5 rounded text-xs">
        POST /v1/tasks/{'{id}'}/predict
      </code>
    </Typography>

    <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
      {tasks.map((t) => (
        <button
          key={t.id}
          onClick={() => onSelect(t.id)}
          className="text-left bg-primary-bg border border-primary-border rounded-lg p-4 cursor-pointer hover:border-primary-text transition-colors"
        >
          <div className="flex items-center gap-2 mb-1">
            <span className="font-mono text-sm font-semibold text-primary-text">{t.id}</span>
            {!t.active && <Badge tone="warn">inactive</Badge>}
          </div>
          <div className="text-sm text-primary-text mb-2">{t.name}</div>
          {t.description && (
            <div className="text-xs text-secondary-text mb-2 line-clamp-2">{t.description}</div>
          )}
          <div className="text-xs text-tertiary-text">
            {t.model}
            {t.fallback_models?.length ? ` (+${t.fallback_models.length} fallback)` : ''} · prompt
            v{t.prompt_version}
            {t.daily_budget_usd ? ` · $${t.daily_budget_usd}/day cap` : ''}
          </div>
        </button>
      ))}
    </div>

    {tasks.length === 0 && (
      <div className="text-sm text-secondary-text">No tasks are registered yet.</div>
    )}
  </div>
);

// ── Task detail ───────────────────────────────────────────────────────────────

const TaskDetailPage = ({ task, onBack }: { task: TTask; onBack: () => void }) => {
  const [stats, setStats] = useState<TTaskStatsDetail | null>(null);

  const loadStats = useCallback(() => {
    api.taskStats(task.id, 30).then(setStats).catch(() => {});
  }, [task.id]);

  useEffect(() => {
    loadStats();
    window.addEventListener('focus', loadStats);
    return () => window.removeEventListener('focus', loadStats);
  }, [loadStats]);

  return (
    <div className="flex flex-col gap-5">
      <div>
        <button
          onClick={onBack}
          className="text-xs text-secondary-text bg-transparent border-none cursor-pointer p-0 mb-2 hover:text-primary-text"
        >
          ← Back to catalog
        </button>
        <div className="flex items-center gap-2">
          <Typography variant="heading" size="6" className="text-primary-text m-0">
            {task.name}
          </Typography>
          {!task.active && <Badge tone="warn">inactive — predict returns 409</Badge>}
        </div>
        <Typography variant="body" size="2" className="text-secondary-text mt-1 mb-0">
          <code className="bg-secondary-bg px-1 py-0.5 rounded text-xs">
            POST /v1/tasks/{task.id}/predict
          </code>{' '}
          · {task.model}
          {task.fallback_models?.length
            ? ` (fallback: ${task.fallback_models.join(', ')})`
            : ''}{' '}
          · prompt v{task.prompt_version}
          {task.daily_budget_usd
            ? ` · budget $${task.daily_budget_usd}/day`
            : ' · no budget cap'}
          {task.cache_enabled &&
            ` · cached ${task.cache_ttl_seconds ? `${Math.round(task.cache_ttl_seconds / 3600)}h` : '24h'}`}
        </Typography>
        {task.description && (
          <Typography variant="body" size="2" className="text-secondary-text mt-1 mb-0">
            {task.description}
          </Typography>
        )}
      </div>

      {stats && stats.totals.runs > 0 && (
        <div className="flex gap-3 flex-wrap">
          <Stat label="Runs (30d)" value={stats.totals.runs.toLocaleString()} />
          <Stat label="Cost (30d)" value={formatCost(stats.totals.cost_usd)} />
          <Stat label="Avg latency" value={`${Math.round(stats.totals.avg_latency_ms)}ms`} />
          <Stat label="Success" value={`${Math.round(stats.totals.success_rate * 100)}%`} />
        </div>
      )}

      <TryItPanel task={task} onPredicted={loadStats} />

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <SchemaBlock title="Input schema" schema={task.input_schema} />
        <SchemaBlock title="Output schema" schema={task.output_schema} />
      </div>

      {stats && stats.daily.length > 0 && <UsageChart stats={stats} />}

      <IntegrationSnippets task={task} />
    </div>
  );
};

// ── Try it ────────────────────────────────────────────────────────────────────

type Field = {
  name: string;
  required: boolean;
  schema: Record<string, unknown>;
};

const TryItPanel = ({ task, onPredicted }: { task: TTask; onPredicted: () => void }) => {
  const fields = useMemo<Field[]>(() => {
    const props = (task.input_schema as { properties?: Record<string, Record<string, unknown>> })?.properties ?? {};
    const required = new Set<string>((task.input_schema as { required?: string[] })?.required ?? []);
    return Object.entries(props).map(([name, schema]) => ({
      name,
      required: required.has(name),
      schema,
    }));
  }, [task.input_schema]);

  // The filled input values persist per task, so a test you set up is still there
  // after a reload. Keyed by task id; TryItPanel remounts per task (parent keys
  // on task.id), so the storage key is stable for the mount.
  const [values, setValues] = usePersistentState<Record<string, string>>(
    `clientportal.values.${task.id}`,
    {},
  );
  const [outcome, setOutcome] = useState<PredictOutcome | null>(null);
  const [attempts, setAttempts] = useState<TGatewayAttempt[] | null>(null);
  const [running, setRunning] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const run = async () => {
    setRunning(true);
    setErr(null);
    setOutcome(null);
    setAttempts(null);
    try {
      const inputs: Record<string, unknown> = {};
      for (const f of fields) {
        const raw = values[f.name] ?? '';
        if (raw === '') continue;
        inputs[f.name] = coerce(raw, f.schema);
      }
      const res = await api.predict(task.id, inputs);
      setOutcome(res);
      // Show exactly what happened behind the answer: the full gateway trace
      // (every model tried, each fallback's reason, errors, retries, and the
      // output each model returned). Best-effort — the run row + attempts are
      // written asynchronously, so poll briefly; non-admins simply don't see it.
      void fetchTrace(res.result.task_run_id).then(setAttempts);
    } catch (e) {
      if (e instanceof ApiError && e.status === 429) {
        const match = e.code?.match(/^retry_after:(\d+)$/);
        const hours = match ? Math.ceil(Number(match[1]) / 3600) : null;
        setErr(
          `Daily budget exhausted (429).${hours ? ` Resets in ~${hours}h (UTC midnight).` : ''}`,
        );
      } else {
        setErr(errorMessage(e));
      }
    } finally {
      setRunning(false);
      onPredicted();
    }
  };

  return (
    <Section title="Try it — live production call">
      {fields.length === 0 ? (
        <div className="text-sm text-secondary-text">
          This task declares no input schema; call it from code with arbitrary inputs.
        </div>
      ) : (
        <div className="flex flex-col gap-2">
          {fields.map((f) => {
            const filled = (values[f.name] ?? '') !== '';
            return (
              <div key={f.name} className="block">
                <div className="flex items-center justify-between gap-2 mb-0.5">
                  <span className="text-xs text-tertiary-text">
                    {f.name}
                    {f.required && <span className="text-red-600"> *</span>}
                    {typeof f.schema.type === 'string' && f.schema.type !== 'string' && (
                      <span className="text-tertiary-text"> ({f.schema.type})</span>
                    )}
                    {typeof f.schema.description === 'string' && (
                      <span className="text-tertiary-text"> — {f.schema.description}</span>
                    )}
                  </span>
                  {filled && (
                    <button
                      type="button"
                      onClick={() => setValues((prev) => ({ ...prev, [f.name]: '' }))}
                      className="flex-shrink-0 text-[11px] text-secondary-text hover:text-primary-text bg-transparent border-none cursor-pointer p-0"
                      title={`Clear ${f.name}`}
                    >
                      Clear
                    </button>
                  )}
                </div>
                {imageFieldMode(f) ? (
                  <ImagePicker
                    value={values[f.name] ?? ''}
                    multi={imageFieldMode(f) === 'multi'}
                    onChange={(next) => setValues((prev) => ({ ...prev, [f.name]: next }))}
                  />
                ) : (
                  <textarea
                    value={values[f.name] ?? ''}
                    onChange={(e) => setValues((prev) => ({ ...prev, [f.name]: e.target.value }))}
                    rows={1}
                    placeholder={f.schema.type === 'array' ? arrayPlaceholder(f.schema) : undefined}
                    className="w-full border border-solid border-primary-border rounded-md px-2 py-1.5 text-sm font-mono resize-y bg-primary-bg text-primary-text"
                  />
                )}
              </div>
            );
          })}
        </div>
      )}

      <div className="mt-3">
        <Button variant="primary" size="s" onClick={run} disabled={running || !task.active}>
          {running ? 'Predicting…' : '▶ Predict'}
        </Button>
        {!task.active && (
          <span className="text-xs text-secondary-text ml-3">
            Task is inactive — the platform rejects predictions until it is re-activated.
          </span>
        )}
      </div>

      {err && (
        <div className="text-sm text-red-700 bg-red-50 border border-red-200 rounded-md px-3 py-2 mt-3">
          {err}
        </div>
      )}

      {outcome && <ResultCard outcome={outcome} />}
      {outcome && attempts && attempts.length > 0 && <GatewayTrace attempts={attempts} />}
    </Section>
  );
};

// fetchTrace pulls the gateway trace for a run by id. The run row and its
// attempts are persisted asynchronously, so it polls briefly until they land;
// it returns [] on access errors (e.g. a non-admin caller) so the panel just
// omits the trace rather than failing the prediction view.
async function fetchTrace(runId: string): Promise<TGatewayAttempt[]> {
  for (let i = 0; i < 6; i++) {
    try {
      const detail = await api.adminRun(runId);
      if (detail.attempts && detail.attempts.length > 0) return detail.attempts;
    } catch {
      // run not written yet (404), or no admin access — stop trying on access errors.
    }
    await new Promise((r) => setTimeout(r, 400));
  }
  try {
    return (await api.adminRun(runId)).attempts ?? [];
  } catch {
    return [];
  }
}

// GatewayTrace shows every model the gateway touched for one run, in order:
// each fallback, why it advanced, the error and its classification, retries,
// per-call latency/cost, and the output the model returned (kept even when it
// failed validation) — the full "what actually happened" behind the answer.
const GatewayTrace = ({ attempts }: { attempts: TGatewayAttempt[] }) => {
  const outcomeTone: Record<string, 'ok' | 'warn' | 'error' | 'neutral'> = {
    success: 'ok',
    error: 'error',
    schema_invalid: 'warn',
    skipped_unhealthy: 'warn',
    cache_hit: 'neutral',
  };
  return (
    <div className="mt-3">
      <div className="text-[10px] uppercase tracking-wider text-tertiary-text mb-1.5">
        Gateway trace — {attempts.length} attempt{attempts.length === 1 ? '' : 's'}
      </div>
      <div className="flex flex-col gap-2">
        {attempts.map((a) => {
          const reason =
            a.fallback_reason && a.fallback_reason !== a.error ? a.fallback_reason : '';
          return (
            <div
              key={a.id || a.seq}
              className="border border-solid border-primary-border rounded-md overflow-hidden"
            >
              <div className="flex items-center gap-2 bg-secondary-bg px-3 py-2 flex-wrap">
                <span className="font-mono text-[10px] text-tertiary-text">#{a.seq}</span>
                <span className="text-sm font-medium text-primary-text">{a.model}</span>
                {a.provider && <span className="text-xs text-tertiary-text">({a.provider})</span>}
                <Badge tone={outcomeTone[a.outcome] ?? 'neutral'}>
                  {a.outcome.replace(/_/g, ' ')}
                </Badge>
                {a.fallback_used && <Badge tone="warn">fallback</Badge>}
                {a.infra_failure && <Badge tone="error">infra</Badge>}
                {a.retry_count > 1 && <Badge tone="neutral">{a.retry_count}× tries</Badge>}
                {a.http_status > 0 && (
                  <span className="text-xs text-tertiary-text">HTTP {a.http_status}</span>
                )}
                <span className="text-xs text-tertiary-text ml-auto">
                  {a.total_tokens.toLocaleString()} tok · {formatCost(a.cost_usd)} · {a.latency_ms}ms
                </span>
              </div>
              {(a.error || reason) && (
                <pre className="m-0 px-3 py-2 text-xs whitespace-pre-wrap break-words bg-primary-bg text-primary-text">
                  {[a.error ? `⚠️ ${a.error}` : '', reason ? `↪ advanced to next model: ${reason}` : '']
                    .filter(Boolean)
                    .join('\n')}
                </pre>
              )}
              {a.outcome === 'schema_invalid' && a.response && (
                <div className="border-t border-solid border-primary-border">
                  <div className="px-3 pt-2 text-[10px] uppercase tracking-wider text-tertiary-text">
                    Returned (failed validation · still cost {a.total_tokens.toLocaleString()} tok ·{' '}
                    {formatCost(a.cost_usd)})
                  </div>
                  <pre className="m-0 px-3 py-2 text-xs whitespace-pre-wrap break-words bg-primary-bg text-primary-text">
                    {a.response}
                  </pre>
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
};

function arrayPlaceholder(schema: Record<string, unknown>): string {
  const items = schema.items as Record<string, unknown> | undefined;
  if (items?.type === 'object') {
    const props = (items.properties ?? {}) as Record<string, Record<string, unknown>>;
    const example: Record<string, unknown> = {};
    for (const [key, propSchema] of Object.entries(props)) {
      switch (propSchema.type) {
        case 'array':   example[key] = ['value1']; break;
        case 'number':
        case 'integer': example[key] = 0; break;
        case 'boolean': example[key] = true; break;
        default:        example[key] = '...'; break;
      }
    }
    return JSON.stringify([example]);
  }
  return '["item1", "item2"] or item1, item2';
}

function coerce(raw: string, schema: Record<string, unknown>): unknown {
  switch (schema.type) {
    case 'number':
    case 'integer': {
      const n = Number(raw);
      return Number.isNaN(n) ? raw : n;
    }
    case 'boolean':
      return raw === 'true' ? true : raw === 'false' ? false : raw;
    case 'object':
    case 'array':
      try {
        return JSON.parse(raw);
      } catch {
        if (schema.type === 'array') {
          const itemType = (schema.items as Record<string, unknown> | undefined)?.type;
          if (!itemType || itemType === 'string') {
            const items = raw
              .split(',')
              .map((s) => s.trim())
              .filter((s) => s.length > 0);
            if (items.length > 0) return items;
          }
        }
        return raw;
      }
    default:
      return raw;
  }
}

function imageFieldMode(f: Field): 'single' | 'multi' | null {
  const name = f.name.toLowerCase();
  const d = ((f.schema.description as string | undefined) ?? '').toLowerCase();
  const looksImage =
    name === 'images' ||
    d.includes('data url') ||
    d.includes('image url') ||
    d.includes('product photo') ||
    d.includes('photo');
  if (!looksImage) return null;
  if (f.schema.type === 'array') {
    const items = f.schema.items as { type?: string } | undefined;
    const itemType = items?.type;
    return !itemType || itemType === 'string' ? 'multi' : null;
  }
  if (f.schema.type && f.schema.type !== 'string') return null;
  return 'single';
}

const ImagePicker = ({
  value,
  multi,
  onChange,
}: {
  value: string;
  multi: boolean;
  onChange: (next: string) => void;
}) => {
  const [zoom, setZoom] = useState<number | null>(null);

  let urls: string[] = [];
  if (value) {
    if (multi) {
      try {
        const parsed = JSON.parse(value);
        if (Array.isArray(parsed)) urls = parsed.filter((x): x is string => typeof x === 'string');
      } catch {
        urls = [];
      }
    } else {
      urls = [value];
    }
  }

  const commit = (next: string[]) =>
    onChange(multi ? (next.length ? JSON.stringify(next) : '') : (next[0] ?? ''));

  const onPick = (e: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? []);
    if (files.length === 0) return;
    Promise.all(
      files.map(
        (file) =>
          new Promise<string>((resolve) => {
            const reader = new FileReader();
            reader.onload = () => resolve(typeof reader.result === 'string' ? reader.result : '');
            reader.readAsDataURL(file);
          }),
      ),
    ).then((dataUrls) => {
      const picked = dataUrls.filter(Boolean);
      commit(multi ? [...urls, ...picked] : picked.slice(0, 1));
    });
    e.target.value = '';
  };

  const removeAt = (i: number) => {
    commit(urls.filter((_, j) => j !== i));
    setZoom(null);
  };

  return (
    <div className="flex flex-col gap-2">
      <input
        type="file"
        accept="image/*"
        multiple={multi}
        onChange={onPick}
        className="text-sm text-primary-text file:mr-3 file:rounded-md file:border file:border-solid file:border-primary-border file:bg-secondary-bg file:text-primary-text file:px-3 file:py-1.5 file:cursor-pointer file:font-medium hover:file:bg-tertiary-bg"
      />
      {urls.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {urls.map((src, i) => (
            <div key={i} className="relative">
              <img
                src={src}
                alt={`image ${i + 1}`}
                onClick={() => setZoom(i)}
                title="Click to zoom"
                className="h-20 w-20 object-cover rounded-md border border-primary-border cursor-zoom-in"
              />
              <button
                type="button"
                onClick={() => removeAt(i)}
                title="Remove"
                className="absolute -top-1.5 -right-1.5 h-5 w-5 rounded-full bg-neutral-900 text-white text-xs leading-none border-none cursor-pointer flex items-center justify-center"
              >
                ×
              </button>
            </div>
          ))}
        </div>
      )}

      {zoom !== null && urls[zoom] && (
        <div
          onClick={() => setZoom(null)}
          className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-6"
        >
          <div
            onClick={(e) => e.stopPropagation()}
            className="flex flex-col items-center gap-3 max-w-[90vw] max-h-[90vh]"
          >
            <img
              src={urls[zoom]}
              alt={`image ${zoom + 1}`}
              className="max-w-[90vw] max-h-[80vh] object-contain rounded-md"
            />
            <div className="flex items-center gap-3">
              <button
                type="button"
                onClick={() => removeAt(zoom)}
                className="bg-red-600 text-white text-sm font-medium px-4 py-1.5 rounded-md border-none cursor-pointer"
              >
                Remove picture
              </button>
              <button
                type="button"
                onClick={() => setZoom(null)}
                className="bg-primary-bg text-primary-text text-sm font-medium px-4 py-1.5 rounded-md border border-primary-border cursor-pointer"
              >
                Close
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

const ResultCard = ({ outcome }: { outcome: PredictOutcome }) => {
  const r: TPredictResult = outcome.result;
  const gatewayMs = r.gateway_latency_ms ?? r.latency_ms;
  const overhead = Math.max(0, gatewayMs - r.latency_ms);
  return (
    <div className="mt-3 border border-primary-border rounded-md overflow-hidden">
      <div className="flex items-center gap-2 bg-secondary-bg px-3 py-2 flex-wrap">
        <Badge tone={r.output_valid === false ? 'error' : 'ok'}>
          {r.output_valid === null
            ? 'no output schema'
            : r.output_valid
              ? 'schema valid'
              : 'schema invalid'}
        </Badge>
        {r.fallback_used && <Badge tone="warn">fallback used</Badge>}
        {outcome.degraded && <Badge tone="warn">degraded</Badge>}
        {r.cached && <Badge tone="neutral">cached — zero cost</Badge>}
        <span className="text-xs text-secondary-text">
          v{r.prompt_version} · {r.model} ({r.provider}) ·{' '}
          <span title="End-to-end platform wall-clock (incl. fallback attempts + validation)">
            {gatewayMs}ms gateway
          </span>{' '}
          /{' '}
          <span title="Winning model's call time">{r.latency_ms}ms model</span>{' '}
          <span
            className="text-tertiary-text"
            title="Platform overhead: gateway − model"
          >
            (+{overhead}ms overhead)
          </span>{' '}
          · {r.usage.total_tokens} tok · {formatCost(r.usage.cost_usd)}
        </span>
        <span className="text-xs text-tertiary-text ml-auto font-mono">{r.task_run_id}</span>
      </div>
      <pre className="m-0 px-3 py-2 text-xs overflow-x-auto whitespace-pre-wrap bg-primary-bg text-primary-text">
        {r.error
          ? `⚠️ ${r.error}`
          : JSON.stringify(r.output ?? r.raw_response, null, 2)}
      </pre>
    </div>
  );
};

// ── Schema + usage + snippets ─────────────────────────────────────────────────

const SchemaBlock = ({ title, schema }: { title: string; schema?: Record<string, unknown> }) => (
  <Section title={title}>
    {schema && Object.keys(schema).length > 0 ? (
      <pre className="m-0 text-xs overflow-x-auto whitespace-pre-wrap text-primary-text">
        {JSON.stringify(schema, null, 2)}
      </pre>
    ) : (
      <div className="text-sm text-secondary-text">Not declared.</div>
    )}
  </Section>
);

const UsageChart = ({ stats }: { stats: TTaskStatsDetail }) => {
  const max = Math.max(...stats.daily.map((d) => d.cost_usd), 1e-9);
  return (
    <Section title={`Daily spend — last ${stats.days} days (all callers)`}>
      <div className="flex flex-col gap-1">
        {stats.daily.map((d) => (
          <div key={d.date} className="flex items-center gap-2 text-xs">
            <span className="w-20 text-secondary-text font-mono">{d.date.slice(5)}</span>
            <div className="flex-1 bg-secondary-bg rounded h-4 overflow-hidden">
              <div
                className="bg-neutral-800 h-full rounded"
                style={{ width: `${Math.max((d.cost_usd / max) * 100, 1)}%` }}
              />
            </div>
            <span className="w-24 text-right text-secondary-text">{formatCost(d.cost_usd)}</span>
            <span className="w-16 text-right text-tertiary-text">{d.runs} runs</span>
          </div>
        ))}
      </div>
    </Section>
  );
};

const IntegrationSnippets = ({ task }: { task: TTask }) => {
  const [copied, setCopied] = useState(false);

  const exampleInputs = Object.fromEntries(
    Object.keys(
      (task.input_schema as { properties?: Record<string, unknown> })?.properties ?? {},
    ).map((k) => [k, '…']),
  );
  const curl = [
    `curl -X POST 'http://localhost:8000/v1/tasks/${task.id}/predict' \\`,
    `  -H 'Authorization: Bearer <service-token>' \\`,
    `  -H 'Content-Type: application/json' \\`,
    `  -d '${JSON.stringify({ inputs: exampleInputs })}'`,
  ].join('\n');

  const copySnippet = async () => {
    await navigator.clipboard.writeText(curl);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <Section title="Integrate">
      <div className="text-xs text-secondary-text mb-2">
        Service callers authenticate with a long-lived Bearer token (mint one with{' '}
        <code className="bg-secondary-bg px-1 rounded">go run ./cmd/issue-token</code>).
      </div>
      <pre className="m-0 text-xs overflow-x-auto bg-neutral-900 text-neutral-100 rounded-md px-3 py-2">
        {curl}
      </pre>
      <button
        onClick={copySnippet}
        className="mt-2 text-primary-text underline bg-transparent border-none cursor-pointer p-0 text-xs"
      >
        {copied ? 'Copied!' : 'Copy snippet'}
      </button>
    </Section>
  );
};

export default ClientPortalPage;
