import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { Button, Checkbox, Input, Spinner, TextArea, Typography, cn } from '@meesho/merlin-ui-tailwind';
import type {
  TPromptVersion,
  TTask,
  TTaskStatsDetail,
} from '../types';
import { DEFAULT_COMPARE_MODELS, MODELS, MODEL_GROUPS } from '../types';
import { api } from '../api/client';
import { useAuth } from '../auth/useAuth';
import { can } from '../auth/permissions';
import SchemaEditor, { type SchemaEditorState } from '../components/SchemaEditor';
import VersionHistory from '../components/VersionHistory';
import { stableStringify } from '../utils/schema';
import { countTokens, estimateCost, formatCost } from '../utils/tokens';

// TasksPage is the Prompt Studio: browse registered tasks, edit prompts as
// drafts, test any version against any model, and deploy — the
// edit → test → deploy loop.
const TasksPage = () => {
  const [tasks, setTasks] = useState<TTask[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const { tasks } = await api.listTasks();
      setTasks(tasks);
      setError(null);
    } catch {
      setError('Could not load tasks.');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    // Deferred so the effect body itself never sets state synchronously.
    void Promise.resolve().then(refresh);
  }, [refresh]);

  const selected = tasks.find((t) => t.id === selectedId) ?? null;

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center bg-primary-bg">
        <Spinner />
      </div>
    );
  }

  return (
    <div className="flex flex-1 overflow-hidden">
      {/* Task list */}
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
              'w-full text-left px-4 py-3 border-b border-solid border-tertiary-border transition-colors',
              selectedId === t.id ? 'bg-tertiary-bg' : 'hover:bg-tertiary-bg',
            )}
          >
            <Typography variant="body" size="3" className="text-primary-text font-medium">
              {t.id}
            </Typography>
            <Typography variant="body" size="1" className="text-tertiary-text">
              v{t.prompt_version} · {t.model}
              {!t.active && ' · inactive'}
            </Typography>
          </button>
        ))}
      </aside>

      {/* Detail */}
      <div className="flex-1 overflow-y-auto bg-primary-bg p-6">
        {!selected ? (
          <div className="h-full flex items-center justify-center">
            <Typography variant="body" size="3" className="text-tertiary-text">
              Select a task to view its config, prompt history, and test panel.
            </Typography>
          </div>
        ) : (
          <TaskDetail key={selected.id} task={selected} onChanged={refresh} />
        )}
      </div>
    </div>
  );
};

// ── Detail view ───────────────────────────────────────────────────────────────

const TaskDetail = ({ task, onChanged }: { task: TTask; onChanged: () => Promise<void> }) => {
  const { user } = useAuth();
  const canWrite = can(user?.role, 'task:write');
  const canDeploy = can(user?.role, 'task:deploy');
  const canDelete = can(user?.role, 'task:delete');
  const [versions, setVersions] = useState<TPromptVersion[]>([]);
  const [stats, setStats] = useState<TTaskStatsDetail | null>(null);
  const [draft, setDraft] = useState(task.prompt_template);
  const [draftSystem, setDraftSystem] = useState(task.system_prompt ?? '');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState<string | null>(null);
  const [flash, setFlash] = useState<string | null>(null);

  const loadVersions = useCallback(async () => {
    const data = await api.listVersions(task.id).catch(() => null);
    if (data) setVersions(data.versions);
  }, [task.id]);

  useEffect(() => {
    void Promise.resolve().then(loadVersions);
    api.taskStats(task.id, 30).then(setStats).catch(() => {});
  }, [task.id, loadVersions]);

  const draftDirty =
    draft !== task.prompt_template || draftSystem !== (task.system_prompt ?? '');
  const draftTokens = useMemo(
    () => countTokens(draftSystem, task.model) + countTokens(draft, task.model),
    [draft, draftSystem, task.model],
  );
  const draftCost = estimateCost(task.model, draftTokens, task.max_tokens);

  const saveDraft = async () => {
    setBusy('save');
    try {
      const { version } = await api.saveDraft(task.id, draft, draftSystem, note);
      setFlash(`Saved as draft v${version} — test it, then deploy.`);
      setNote('');
      await loadVersions();
    } catch (e) {
      setFlash(e instanceof Error ? e.message : 'Save failed');
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="mx-auto max-w-4xl flex flex-col gap-6">
      <div>
        <Typography variant="heading" size="6" className="text-primary-text">
          {task.name}
        </Typography>
        <Typography variant="body" size="2" className="text-tertiary-text">
          {task.id} · {task.model}
          {task.fallback_models?.length ? ` (fallback: ${task.fallback_models.join(', ')})` : ''} ·
          active v{task.prompt_version}
          {task.daily_budget_usd ? ` · budget $${task.daily_budget_usd}/day` : ' · no budget cap'}
        </Typography>
        {task.description && (
          <Typography variant="body" size="2" className="text-secondary-text mt-1">
            {task.description}
          </Typography>
        )}
      </div>

      {/* Usage strip */}
      {stats && stats.totals.runs > 0 && (
        <div className="flex gap-4 flex-wrap">
          <Stat label="Runs (30d)" value={stats.totals.runs.toLocaleString()} />
          <Stat label="Cost (30d)" value={formatCost(stats.totals.cost_usd)} />
          <Stat label="Avg latency" value={`${Math.round(stats.totals.avg_latency_ms)}ms`} />
          <Stat label="Success" value={`${Math.round(stats.totals.success_rate * 100)}%`} />
        </div>
      )}

      {flash && (
        <div className="bg-secondary-bg border border-solid border-primary-border rounded-md px-3 py-2">
          <Typography variant="body" size="2" className="text-primary-text">
            {flash}
          </Typography>
        </div>
      )}

      {!canWrite && (
        <div className="bg-secondary-bg border border-solid border-tertiary-border rounded-md px-3 py-2">
          <Typography variant="body" size="2" className="text-tertiary-text">
            {canDeploy
              ? 'Approver access: you can publish versions but not edit prompts.'
              : 'Read-only access: editing and publishing are disabled for your role.'}
          </Typography>
        </div>
      )}

      {/* Model routing */}
      <ModelSection task={task} onSaved={onChanged} setFlash={setFlash} canWrite={canWrite} />

      {/* Input / output schema */}
      <SchemaSection key={task.id} task={task} onChanged={onChanged} setFlash={setFlash} canWrite={canWrite} />

      {/* Prompt editor */}
      <Section title="Prompt editor">
        <Typography variant="body" size="1" className="text-tertiary-text mb-1">
          System prompt
        </Typography>
        <TextArea
          value={draftSystem}
          onChange={({ value }) => setDraftSystem(value)}
          rows={2}
        />
        <Typography variant="body" size="1" className="text-tertiary-text mb-1 mt-3">
          Prompt template (Go template: {'{{.field}}'})
        </Typography>
        <TextArea value={draft} onChange={({ value }) => setDraft(value)} rows={8} />
        <div className="flex items-center gap-3 mt-2">
          <TextArea
            value={note}
            onChange={({ value }) => setNote(value)}
            placeholder="Change note (optional)"
            rows={1}
            wrapperClassName="flex-1"
          />
          <Button
            variant="primary"
            size="s"
            disabled={!draftDirty || busy !== null || !canWrite}
            onClick={saveDraft}
            title={canWrite ? undefined : 'Your role cannot edit prompts'}
          >
            {busy === 'save' ? 'Saving…' : 'Save draft'}
          </Button>
        </div>
        <Typography variant="body" size="1" className="text-tertiary-text mt-2">
          ~{draftTokens} template tokens · est. {formatCost(draftCost)}/prediction at{' '}
          {task.max_tokens} output tokens (before input values)
        </Typography>
      </Section>

      {/* Cost estimate */}
      <EstimateSection draft={draft} draftSystem={draftSystem} />

      {/* Version history */}
      <Section title="Version history">
        <VersionHistory
          taskId={task.id}
          versions={versions}
          activeVersion={task.prompt_version}
          livePrompt={task.prompt_template}
          liveSystem={task.system_prompt ?? ''}
          canDeploy={canDeploy}
          canDelete={canDelete}
          onReload={loadVersions}
          onActiveChanged={onChanged}
        />
      </Section>
    </div>
  );
};

// ── Schema editor ─────────────────────────────────────────────────────────────

const hasSchema = (s?: Record<string, unknown>) => !!s && Object.keys(s).length > 0;
const EMPTY_OBJECT_SCHEMA = { type: 'object', properties: {} };

// SchemaSection edits a task's input and output JSON Schemas. Each schema can be
// toggled off entirely (free-form input / raw-text output) or edited via the
// visual field editor / JSON. Both schemas save in one PUT; the backend
// re-validates and rejects (422) anything that won't compile, so the editor's
// client-side checks are a convenience, not the gate. Authoring is gated on the
// same task:write permission as the prompt editor (readOnly when absent).
const SchemaSection = ({
  task,
  onChanged,
  setFlash,
  canWrite,
}: {
  task: TTask;
  onChanged: () => Promise<void>;
  setFlash: (msg: string) => void;
  canWrite: boolean;
}) => {
  const [inputEnabled, setInputEnabled] = useState(hasSchema(task.input_schema));
  const [outputEnabled, setOutputEnabled] = useState(hasSchema(task.output_schema));
  const [input, setInput] = useState<SchemaEditorState>({
    schema: task.input_schema ?? EMPTY_OBJECT_SCHEMA,
    valid: true,
  });
  const [output, setOutput] = useState<SchemaEditorState>({
    schema: task.output_schema ?? EMPTY_OBJECT_SCHEMA,
    valid: true,
  });
  const [saving, setSaving] = useState(false);

  const currentInput = inputEnabled ? input.schema : null;
  const currentOutput = outputEnabled ? output.schema : null;
  const originalInput = hasSchema(task.input_schema) ? task.input_schema! : null;
  const originalOutput = hasSchema(task.output_schema) ? task.output_schema! : null;

  const dirty =
    stableStringify(currentInput) !== stableStringify(originalInput) ||
    stableStringify(currentOutput) !== stableStringify(originalOutput);
  const invalid = (inputEnabled && !input.valid) || (outputEnabled && !output.valid);

  const save = async () => {
    setSaving(true);
    try {
      const patch: Record<string, unknown> = {
        input_schema: inputEnabled ? input.schema : null,
        output_schema: outputEnabled ? output.schema : null,
      };
      await api.updateTask(task.id, patch as Partial<TTask>);
      setFlash('Schemas saved.');
      await onChanged();
    } catch (e) {
      setFlash(e instanceof Error ? e.message : 'Schema save failed');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Section title="Input & output schema">
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <SchemaPane
          title="Input schema"
          hint="Validates request inputs (422 on mismatch) and auto-populates prompt variables. Off = free-form."
          enabled={inputEnabled}
          onToggle={setInputEnabled}
          canWrite={canWrite}
        >
          <SchemaEditor
            initial={input.schema}
            readOnly={!canWrite}
            onChange={setInput}
          />
        </SchemaPane>
        <SchemaPane
          title="Output schema"
          hint="Validates model output. Off = raw text output (no output_valid flag)."
          enabled={outputEnabled}
          onToggle={setOutputEnabled}
          canWrite={canWrite}
        >
          <SchemaEditor
            initial={output.schema}
            readOnly={!canWrite}
            onChange={setOutput}
          />
        </SchemaPane>
      </div>
      {canWrite && (
        <div className="flex items-center gap-3 mt-3">
          <Button variant="primary" size="s" disabled={!dirty || invalid || saving} onClick={save}>
            {saving ? 'Saving…' : 'Save schemas'}
          </Button>
          {invalid && (
            <Typography variant="body" size="1" className="text-error-text">
              Fix the highlighted errors before saving.
            </Typography>
          )}
          {dirty && !invalid && (
            <Typography variant="body" size="1" className="text-tertiary-text">
              Unsaved changes — takes effect on the next predict.
            </Typography>
          )}
        </div>
      )}
    </Section>
  );
};

const SchemaPane = ({
  title,
  hint,
  enabled,
  onToggle,
  canWrite,
  children,
}: {
  title: string;
  hint: string;
  enabled: boolean;
  onToggle: (v: boolean) => void;
  canWrite: boolean;
  children: ReactNode;
}) => (
  <div className="border border-solid border-tertiary-border rounded-md p-3 flex flex-col gap-2">
    <label className="flex items-center gap-2 select-none">
      <input type="checkbox" checked={enabled} disabled={!canWrite} onChange={(e) => onToggle(e.target.checked)} />
      <Typography variant="body" size="2" className="text-primary-text font-medium">
        {title}
      </Typography>
    </label>
    <Typography variant="body" size="1" className="text-tertiary-text">
      {hint}
    </Typography>
    {enabled && children}
  </div>
);

// ── Model routing ─────────────────────────────────────────────────────────────

// ModelSection edits the task's model chain as one ordered list: position 0 is
// the primary, the rest are fallbacks tried in sequence. Add models from the
// registry, drag rows to reorder. Saves via PUT merge semantics — only
// model/fallback_models change. Production traffic switches on the next
// predict (config cache invalidates on write), and the prediction cache keys
// on the model, so stale answers can't leak. At call time, models with an
// open circuit are skipped instantly; the background prober re-engages a
// higher-priority model once it's healthy again.
const ModelSection = ({
  task,
  onSaved,
  setFlash,
  canWrite,
}: {
  task: TTask;
  onSaved: () => Promise<void>;
  setFlash: (msg: string) => void;
  canWrite: boolean;
}) => {
  const savedChain = [task.model, ...(task.fallback_models ?? [])];
  const [chain, setChain] = useState<string[]>(savedChain);
  const [dragIndex, setDragIndex] = useState<number | null>(null);
  const [overIndex, setOverIndex] = useState<number | null>(null);
  const [saving, setSaving] = useState(false);

  const available = MODELS.filter((m) => !chain.includes(m));
  const dirty = chain.join('→') !== savedChain.join('→');

  const moveItem = (from: number, to: number) => {
    if (from === to) return;
    setChain((prev) => {
      const next = [...prev];
      const [item] = next.splice(from, 1);
      next.splice(to, 0, item);
      return next;
    });
  };

  const save = async () => {
    if (!window.confirm(
      `Route ${task.id} as ${chain.join(' → ')}? Production traffic switches immediately.`,
    )) return;
    setSaving(true);
    try {
      await api.updateTask(task.id, { model: chain[0], fallback_models: chain.slice(1) });
      setFlash(`Now serving on ${chain[0]}${chain.length > 1 ? ` → ${chain.slice(1).join(' → ')}` : ' (no fallbacks)'}.`);
      await onSaved();
    } catch (e) {
      setFlash(e instanceof Error ? e.message : 'Model change failed');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Section title="Model routing — tried top to bottom; drag to reorder">
      <div className="flex flex-col gap-1 max-w-md">
        {chain.map((m, i) => (
          <div
            key={m}
            draggable
            onDragStart={() => setDragIndex(i)}
            onDragOver={(e) => {
              e.preventDefault();
              setOverIndex(i);
            }}
            onDrop={() => {
              if (dragIndex !== null) moveItem(dragIndex, i);
              setDragIndex(null);
              setOverIndex(null);
            }}
            onDragEnd={() => {
              setDragIndex(null);
              setOverIndex(null);
            }}
            className={cn(
              'flex items-center gap-3 px-3 py-2 rounded-md border border-solid bg-primary-bg cursor-grab active:cursor-grabbing',
              overIndex === i && dragIndex !== null && dragIndex !== i
                ? 'border-primary-text'
                : 'border-primary-border',
              dragIndex === i && 'opacity-50',
            )}
          >
            <span className="text-tertiary-text select-none" aria-hidden>
              ⠿
            </span>
            <Typography variant="body" size="3" className="text-primary-text font-medium flex-1">
              {m}
            </Typography>
            <Typography variant="body" size="1" className="text-tertiary-text uppercase tracking-wider">
              {i === 0 ? 'primary' : `fallback #${i}`}
            </Typography>
            <Button
              variant="ghost"
              size="s"
              disabled={chain.length === 1}
              onClick={() => setChain((prev) => prev.filter((_, idx) => idx !== i))}
            >
              ✕
            </Button>
          </div>
        ))}

        <div className="flex items-center gap-3 mt-2">
          {available.length > 0 && (
            <select
              value=""
              onChange={(e) => {
                if (e.target.value) setChain((prev) => [...prev, e.target.value]);
              }}
              className="border border-solid border-primary-border rounded-md px-2 py-1.5 text-sm bg-primary-bg text-primary-text"
            >
              <option value="">+ Add model to chain…</option>
              {MODEL_GROUPS.map((group) => {
                const groupAvailable = group.models.filter((m) => !chain.includes(m));
                if (groupAvailable.length === 0) return null;
                return (
                  <optgroup key={group.provider} label={group.provider}>
                    {groupAvailable.map((m) => (
                      <option key={m} value={m}>
                        {m}
                      </option>
                    ))}
                  </optgroup>
                );
              })}
            </select>
          )}
          <Button
            variant="primary"
            size="s"
            disabled={!dirty || saving || !canWrite}
            onClick={save}
            title={canWrite ? undefined : 'Your role cannot edit task config'}
          >
            {saving ? 'Saving…' : 'Save routing'}
          </Button>
          {dirty && (
            <Button variant="ghost" size="s" disabled={saving} onClick={() => setChain(savedChain)}>
              Reset
            </Button>
          )}
        </div>

        <Typography variant="body" size="1" className="text-tertiary-text mt-1">
          Unhealthy models are skipped instantly (circuit breaker); a background
          probe re-engages the higher-priority model once it recovers.
        </Typography>
      </div>
    </Section>
  );
};

// ── Small building blocks ─────────────────────────────────────────────────────

const Section = ({ title, children }: { title: string; children: ReactNode }) => (
  <div className="border border-solid border-primary-border rounded-lg p-4">
    <Typography variant="body" size="2" className="text-primary-text font-semi-bold mb-3">
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

// ── Inline cost estimator ─────────────────────────────────────────────────────

const EstimateSection = ({ draft, draftSystem }: { draft: string; draftSystem: string }) => {
  const [expectedOutput, setExpectedOutput] = useState(500);
  const [models, setModels] = useState<string[]>([...DEFAULT_COMPARE_MODELS]);

  const perModel = useMemo(
    () =>
      models.map((model) => {
        const outPer = Math.max(0, expectedOutput);
        const inputTokens = countTokens(draftSystem, model) + countTokens(draft, model);
        return {
          model,
          inputTokens,
          outputTokens: outPer,
          cost: estimateCost(model, inputTokens, outPer),
        };
      }),
    [models, draft, draftSystem, expectedOutput],
  );

  const grandTotal = perModel.reduce((a, m) => a + m.cost, 0);

  const toggleModel = (model: string, checked: boolean) =>
    setModels((prev) => (checked ? [...prev, model] : prev.filter((m) => m !== model)));

  return (
    <Section title="Cost estimate">
      <div className="flex flex-wrap items-end gap-6 mb-4">
        <div>
          <Typography variant="body" size="2" className="text-primary-text mb-1 font-medium">
            Expected output tokens / prompt
          </Typography>
          <Input
            type="number"
            value={String(expectedOutput)}
            onChange={({ value }) => setExpectedOutput(Number(value) || 0)}
            wrapperClassName="w-40"
          />
        </div>
        <div>
          <Typography variant="body" size="2" className="text-primary-text mb-1 font-medium">
            Models
          </Typography>
          <div className="flex flex-wrap gap-6">
            {MODEL_GROUPS.map((group) => (
              <div key={group.provider}>
                <Typography
                  variant="body"
                  size="1"
                  className="text-tertiary-text font-semi-bold uppercase tracking-wider mb-1"
                >
                  {group.provider}
                </Typography>
                <div className="flex flex-col gap-1">
                  {group.models.map((m) => (
                    <Checkbox
                      key={m}
                      checked={models.includes(m)}
                      onChange={({ checked }) => toggleModel(m, checked)}
                      label={
                        <Typography variant="body" size="2" className="text-primary-text">
                          {m}
                        </Typography>
                      }
                    />
                  ))}
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>

      <div className="flex flex-wrap gap-4 mb-4">
        <EstSummaryCard label="Prompts" value="1" />
        <EstSummaryCard label="Total input tokens" value={(perModel[0]?.inputTokens ?? 0).toLocaleString()} />
        <EstSummaryCard label="Est. total cost" value={formatCost(grandTotal)} accent />
      </div>

      {models.length > 0 && draft.trim() && (
        <div className="border border-solid border-primary-border rounded-lg overflow-hidden">
          <table className="w-full text-left">
            <thead className="bg-secondary-bg">
              <tr>
                <EstTh>Model</EstTh>
                <EstTh right>Input tok</EstTh>
                <EstTh right>Output tok</EstTh>
                <EstTh right>Est. cost</EstTh>
              </tr>
            </thead>
            <tbody>
              {perModel.map((row) => (
                <tr key={row.model} className="border-t border-solid border-tertiary-border">
                  <EstTd>{row.model}</EstTd>
                  <EstTd right>{row.inputTokens.toLocaleString()}</EstTd>
                  <EstTd right>{row.outputTokens.toLocaleString()}</EstTd>
                  <EstTd right>{formatCost(row.cost)}</EstTd>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Typography variant="body" size="1" className="text-tertiary-text mt-3">
        Token counts use the cl100k_base tokenizer — an approximation for non-OpenAI models.
        Rates come from the backend pricing table; actual usage may vary.
      </Typography>
    </Section>
  );
};

const EstSummaryCard = ({
  label,
  value,
  accent,
}: {
  label: string;
  value: string;
  accent?: boolean;
}) => (
  <div className="flex-1 min-w-[160px] bg-secondary-bg border border-solid border-primary-border rounded-lg px-4 py-3">
    <Typography variant="body" size="1" className="text-tertiary-text uppercase tracking-wider">
      {label}
    </Typography>
    <Typography
      variant="heading"
      size="5"
      className={accent ? 'text-accent' : 'text-primary-text'}
    >
      {value}
    </Typography>
  </div>
);

const EstTh = ({ children, right }: { children: ReactNode; right?: boolean }) => (
  <th className={`px-4 py-2 ${right ? 'text-right' : 'text-left'}`}>
    <Typography variant="body" size="1" className="text-tertiary-text uppercase tracking-wider">
      {children}
    </Typography>
  </th>
);

const EstTd = ({ children, right }: { children: ReactNode; right?: boolean }) => (
  <td className={`px-4 py-2.5 ${right ? 'text-right tabular-nums' : ''}`}>
    <Typography variant="body" size="3" className="text-primary-text">
      {children}
    </Typography>
  </td>
);

export default TasksPage;
