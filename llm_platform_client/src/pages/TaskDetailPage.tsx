import { useCallback, useEffect, useMemo, useState, type ChangeEvent } from 'react';
import type { TJSONSchema, TPredictResult, TTask, TTaskStatsDetail } from '../types';
import { api, ApiError, type PredictOutcome } from '../api/client';
import { API_TOKEN } from '../auth/token';
import { Badge, Section, Stat } from '../components/ui';
import { formatCost } from '../utils/format';

// TaskDetailPage is what a consumer team sees for one task: the I/O contract,
// a live Try-it panel hitting the real production /predict endpoint, recent
// usage, and copy-paste integration snippets.
const TaskDetailPage = ({ task, onBack }: { task: TTask; onBack: () => void }) => {
  const [stats, setStats] = useState<TTaskStatsDetail | null>(null);

  const loadStats = useCallback(() => {
    api.taskStats(task.id, 30).then(setStats).catch(() => {});
  }, [task.id]);

  // Stats must track the server: reload on mount, after every Try-it
  // prediction (via onPredicted), and when the window regains focus.
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
          className="text-xs text-neutral-500 bg-transparent border-none cursor-pointer p-0 mb-2 hover:text-neutral-800"
        >
          ← Back to catalog
        </button>
        <div className="flex items-center gap-2">
          <h1 className="text-xl font-semibold text-neutral-900 m-0">{task.name}</h1>
          {!task.active && <Badge tone="warn">inactive — predict returns 409</Badge>}
        </div>
        <p className="text-sm text-neutral-500 mt-1 mb-0">
          <code className="bg-neutral-100 px-1 py-0.5 rounded text-xs">
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
        </p>
        {task.description && (
          <p className="text-sm text-neutral-600 mt-1 mb-0">{task.description}</p>
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

type Field = { name: string; required: boolean; schema: TJSONSchema };

const TryItPanel = ({ task, onPredicted }: { task: TTask; onPredicted: () => void }) => {
  const fields = useMemo<Field[]>(() => {
    const props = task.input_schema?.properties ?? {};
    const required = new Set(task.input_schema?.required ?? []);
    return Object.entries(props).map(([name, schema]) => ({
      name,
      required: required.has(name),
      schema,
    }));
  }, [task.input_schema]);

  const [values, setValues] = useState<Record<string, string>>({});
  const [outcome, setOutcome] = useState<PredictOutcome | null>(null);
  const [running, setRunning] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const run = async () => {
    setRunning(true);
    setErr(null);
    setOutcome(null);
    try {
      const inputs: Record<string, unknown> = {};
      for (const f of fields) {
        const raw = values[f.name] ?? '';
        if (raw === '') continue;
        inputs[f.name] = coerce(raw, f.schema);
      }
      setOutcome(await api.predict(task.id, inputs));
    } catch (e) {
      if (e instanceof ApiError && e.status === 429) {
        const hours = e.retryAfterSeconds ? Math.ceil(e.retryAfterSeconds / 3600) : null;
        setErr(
          `Daily budget exhausted (429).${hours ? ` Resets in ~${hours}h (UTC midnight).` : ''}`,
        );
      } else {
        setErr(e instanceof Error ? e.message : 'Prediction failed');
      }
    } finally {
      setRunning(false);
      // Failed calls are run rows too — refresh usage either way.
      onPredicted();
    }
  };

  return (
    <Section title="Try it — live production call">
      {fields.length === 0 ? (
        <div className="text-sm text-neutral-500">
          This task declares no input schema; call it from code with arbitrary inputs.
        </div>
      ) : (
        <div className="flex flex-col gap-2">
          {fields.map((f) => (
            <label key={f.name} className="block">
              <div className="text-xs text-neutral-500 mb-0.5">
                {f.name}
                {f.required && <span className="text-red-600"> *</span>}
                {f.schema.type && f.schema.type !== 'string' && (
                  <span className="text-neutral-400"> ({f.schema.type})</span>
                )}
                {f.schema.description && (
                  <span className="text-neutral-400"> — {f.schema.description}</span>
                )}
              </div>
              {isImageField(f) ? (
                <ImageField
                  value={values[f.name] ?? ''}
                  onChange={(dataUrl) =>
                    setValues((prev) => ({ ...prev, [f.name]: dataUrl }))
                  }
                />
              ) : (
                <textarea
                  value={values[f.name] ?? ''}
                  onChange={(e) =>
                    setValues((prev) => ({ ...prev, [f.name]: e.target.value }))
                  }
                  rows={1}
                  className="w-full border border-neutral-300 rounded-md px-2 py-1.5 text-sm font-mono resize-y bg-white"
                />
              )}
            </label>
          ))}
        </div>
      )}

      <div className="mt-3">
        <button
          onClick={run}
          disabled={running || !task.active}
          className="bg-neutral-900 text-white text-sm font-medium px-4 py-1.5 rounded-md border-none cursor-pointer disabled:opacity-50 disabled:cursor-default"
        >
          {running ? 'Predicting…' : '▶ Predict'}
        </button>
        {!task.active && (
          <span className="text-xs text-neutral-500 ml-3">
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
    </Section>
  );
};

// Coerce textarea strings to the schema-declared type so e.g. numeric fields
// don't fail input validation as strings.
function coerce(raw: string, schema: TJSONSchema): unknown {
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
        return raw;
      }
    default:
      return raw;
  }
}

// A field is an image input when it's named "image" or its description mentions
// a data URL / image — the platform attaches it to the model as a vision block.
function isImageField(f: Field): boolean {
  if (f.schema.type && f.schema.type !== 'string') return false;
  if (f.name.toLowerCase() === 'image') return true;
  const d = (f.schema.description ?? '').toLowerCase();
  return d.includes('data url') || d.includes('image url') || d.includes('product photo');
}

// ImageField lets a seller pick a photo; it's read into a base64 data URL (the
// string the predict API expects) and previewed inline.
const ImageField = ({
  value,
  onChange,
}: {
  value: string;
  onChange: (dataUrl: string) => void;
}) => {
  const [name, setName] = useState<string | null>(null);

  const onPick = (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    setName(file.name);
    const reader = new FileReader();
    reader.onload = () => onChange(typeof reader.result === 'string' ? reader.result : '');
    reader.readAsDataURL(file);
  };

  return (
    <div className="flex flex-col gap-2">
      <input
        type="file"
        accept="image/*"
        onChange={onPick}
        className="text-sm text-neutral-700 file:mr-3 file:rounded-md file:border-0 file:bg-neutral-900 file:text-white file:px-3 file:py-1.5 file:cursor-pointer"
      />
      {value && (
        <div className="flex items-center gap-3">
          <img src={value} alt="preview" className="h-20 w-20 object-cover rounded-md border border-neutral-200" />
          <button
            type="button"
            onClick={() => {
              setName(null);
              onChange('');
            }}
            className="text-xs text-neutral-500 underline bg-transparent border-none cursor-pointer p-0"
          >
            Remove {name ? `(${name})` : ''}
          </button>
        </div>
      )}
    </div>
  );
};

const ResultCard = ({ outcome }: { outcome: PredictOutcome }) => {
  const r: TPredictResult = outcome.result;
  return (
    <div className="mt-3 border border-neutral-200 rounded-md overflow-hidden">
      <div className="flex items-center gap-2 bg-neutral-50 px-3 py-2 flex-wrap">
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
        <span className="text-xs text-neutral-500">
          v{r.prompt_version} · {r.model} ({r.provider}) · {r.latency_ms}ms ·{' '}
          {r.usage.total_tokens} tok · {formatCost(r.usage.cost_usd)}
        </span>
        <span className="text-xs text-neutral-400 ml-auto font-mono">{r.task_run_id}</span>
      </div>
      <pre className="m-0 px-3 py-2 text-xs overflow-x-auto whitespace-pre-wrap bg-white">
        {r.error
          ? `⚠️ ${r.error}`
          : JSON.stringify(r.output ?? r.raw_response, null, 2)}
      </pre>
    </div>
  );
};

// ── Schema + usage + snippets ─────────────────────────────────────────────────

const SchemaBlock = ({ title, schema }: { title: string; schema?: TJSONSchema }) => (
  <Section title={title}>
    {schema ? (
      <pre className="m-0 text-xs overflow-x-auto whitespace-pre-wrap text-neutral-700">
        {JSON.stringify(schema, null, 2)}
      </pre>
    ) : (
      <div className="text-sm text-neutral-500">Not declared.</div>
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
            <span className="w-20 text-neutral-500 font-mono">{d.date.slice(5)}</span>
            <div className="flex-1 bg-neutral-100 rounded h-4 overflow-hidden">
              <div
                className="bg-neutral-800 h-full rounded"
                style={{ width: `${Math.max((d.cost_usd / max) * 100, 1)}%` }}
              />
            </div>
            <span className="w-24 text-right text-neutral-600">{formatCost(d.cost_usd)}</span>
            <span className="w-16 text-right text-neutral-400">{d.runs} runs</span>
          </div>
        ))}
      </div>
    </Section>
  );
};

const IntegrationSnippets = ({ task }: { task: TTask }) => {
  const [copied, setCopied] = useState(false);

  const exampleInputs = Object.fromEntries(
    Object.keys(task.input_schema?.properties ?? {}).map((k) => [k, '…']),
  );
  const curl = [
    `curl -X POST 'http://localhost:8000/v1/tasks/${task.id}/predict' \\`,
    `  -H 'Authorization: Bearer ${API_TOKEN.slice(0, 24)}…' \\`,
    `  -H 'Content-Type: application/json' \\`,
    `  -d '${JSON.stringify({ inputs: exampleInputs })}'`,
  ].join('\n');

  const copyToken = async () => {
    await navigator.clipboard.writeText(API_TOKEN);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <Section title="Integrate">
      <div className="text-xs text-neutral-500 mb-2">
        Service callers authenticate with a long-lived Bearer token (mint one with{' '}
        <code className="bg-neutral-100 px-1 rounded">go run ./cmd/issue-token</code>).{' '}
        <button
          onClick={copyToken}
          className="text-neutral-700 underline bg-transparent border-none cursor-pointer p-0 text-xs"
        >
          {copied ? 'Copied!' : 'Copy this portal’s demo token'}
        </button>
      </div>
      <pre className="m-0 text-xs overflow-x-auto bg-neutral-900 text-neutral-100 rounded-md px-3 py-2">
        {curl}
      </pre>
    </Section>
  );
};

export default TaskDetailPage;
