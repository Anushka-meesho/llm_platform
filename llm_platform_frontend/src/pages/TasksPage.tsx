import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { Button, Spinner, TextArea, Typography, cn } from '@meesho/merlin-ui-tailwind';
import type {
  TPredictResult,
  TPromptVersion,
  TTask,
  TTaskStatsDetail,
} from '../types';
import { MODELS, MODEL_GROUPS } from '../types';
import { api, errorMessage } from '../api/client';
import { useToast } from '../toast/context';
import { useAuth } from '../auth/useAuth';
import { can } from '../auth/permissions';
import SchemaEditor, { type SchemaEditorState } from '../components/SchemaEditor';
import VersionHistory from '../components/VersionHistory';
import ErrorState from '../components/ErrorState';
import { stableStringify } from '../utils/schema';
import { countTokens, estimateCost, formatCost } from '../utils/tokens';

// TasksPage is the Prompt Studio: browse registered tasks, edit prompts as
// drafts, test any version against any model, and deploy — the
// edit → test → deploy loop.
const TasksPage = () => {
  const { user } = useAuth();
  const canWrite = can(user?.role, 'task:write');
  const [tasks, setTasks] = useState<TTask[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
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
        <div className="px-4 py-3 flex items-center justify-between gap-2">
          <Typography variant="body" size="1" className="text-tertiary-text uppercase tracking-wider">
            Tasks
          </Typography>
          {canWrite && (
            <Button
              variant="primary"
              size="s"
              onClick={() => {
                setCreating(true);
                setSelectedId(null);
              }}
              title="Author a new task"
            >
              + New
            </Button>
          )}
        </div>
        {error && <ErrorState message={error} onRetry={refresh} compact />}
        {tasks.map((t) => (
          <button
            key={t.id}
            onClick={() => {
              setSelectedId(t.id);
              setCreating(false);
            }}
            className={cn(
              'w-full text-left px-4 py-3 border-b border-solid border-tertiary-border transition-colors',
              selectedId === t.id && !creating ? 'bg-tertiary-bg' : 'hover:bg-tertiary-bg',
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
        {creating ? (
          <CreateTaskForm
            existingIds={tasks.map((t) => t.id)}
            onCancel={() => setCreating(false)}
            onCreated={async (id) => {
              setCreating(false);
              await refresh();
              setSelectedId(id);
            }}
          />
        ) : !selected ? (
          <div className="h-full flex items-center justify-center">
            <Typography variant="body" size="3" className="text-tertiary-text">
              Select a task to view its config, prompt history, and test panel.
            </Typography>
          </div>
        ) : (
          <TaskDetail
            key={selected.id}
            task={selected}
            onChanged={refresh}
            onDeleted={async () => {
              setSelectedId(null);
              await refresh();
            }}
          />
        )}
      </div>
    </div>
  );
};

// ── Detail view ───────────────────────────────────────────────────────────────

const TaskDetail = ({
  task,
  onChanged,
  onDeleted,
}: {
  task: TTask;
  onChanged: () => Promise<void>;
  onDeleted: () => Promise<void>;
}) => {
  const { user } = useAuth();
  const toast = useToast();
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
    const data = await api.listVersions(task.id).catch((e) => {
      console.error('load versions:', errorMessage(e));
      return null;
    });
    if (data) setVersions(data.versions);
  }, [task.id]);

  useEffect(() => {
    void Promise.resolve().then(loadVersions);
    api
      .taskStats(task.id, 30)
      .then(setStats)
      .catch((e) => console.error('load task stats:', errorMessage(e)));
  }, [task.id, loadVersions]);

  const draftDirty =
    draft !== task.prompt_template || draftSystem !== (task.system_prompt ?? '');
  const draftTokens = useMemo(
    () => countTokens(draftSystem) + countTokens(draft),
    [draft, draftSystem],
  );
  const draftCost = estimateCost(task.model, draftTokens, task.max_tokens);

  const saveDraft = async () => {
    setBusy('save');
    try {
      const { version } = await api.saveDraft(task.id, draft, draftSystem, note);
      setFlash(`Saved as draft v${version} — test it, then deploy.`);
      toast.success(`Saved as draft v${version} — test it, then deploy.`);
      setNote('');
      await loadVersions();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };

  const deleteTask = async () => {
    if (
      !window.confirm(
        `Permanently delete task "${task.id}" and its entire prompt history? This cannot be undone.`,
      )
    )
      return;
    setBusy('delete');
    try {
      await api.deleteTask(task.id);
      toast.success(`Task "${task.id}" deleted.`);
      await onDeleted();
    } catch (e) {
      toast.error(errorMessage(e));
      setBusy(null);
    }
  };

  return (
    <div className="mx-auto max-w-4xl flex flex-col gap-6">
      <div className="flex items-start justify-between gap-4">
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
        {canDelete && task.id !== 'playground' && (
          <Button
            variant="ghost"
            size="s"
            className="flex-shrink-0 text-error-text"
            disabled={busy !== null}
            onClick={deleteTask}
            title="Permanently delete this task (admin only)"
          >
            {busy === 'delete' ? 'Deleting…' : 'Delete task'}
          </Button>
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

      {/* Test panel */}
      <TestPanel task={task} versions={versions} />

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

// ── Create task ─────────────────────────────────────────────────────────────

const hasSchema = (s?: Record<string, unknown>) => !!s && Object.keys(s).length > 0;
const EMPTY_OBJECT_SCHEMA = { type: 'object', properties: {} };

const INPUT_CLS =
  'w-full border border-solid border-primary-border rounded-md px-2 py-1.5 text-sm bg-primary-bg text-primary-text';
// Task id contract — mirrors the backend slug rule (internal/tasks/task.go).
const ID_SLUG = /^[a-z0-9][a-z0-9-]{1,63}$/;

// CreateTaskForm authors a brand-new task and POSTs it to /v1/tasks. Now that
// the YAML seed layer is gone, this is the only path that brings a task into
// existence — it fills in every field that used to live in tasks.d/*.yaml.
// Gated by task:write at the call site (creator/admin); the backend re-enforces
// it and re-validates the slug, schemas, template, and model on submit.
const CreateTaskForm = ({
  existingIds,
  onCreated,
  onCancel,
}: {
  existingIds: string[];
  onCreated: (id: string) => Promise<void>;
  onCancel: () => void;
}) => {
  const [id, setId] = useState('');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [systemPrompt, setSystemPrompt] = useState('');
  const [promptTemplate, setPromptTemplate] = useState('');
  const [model, setModel] = useState('');
  const [fallbacks, setFallbacks] = useState<string[]>([]);
  const [temperature, setTemperature] = useState('0.2');
  const [maxTokens, setMaxTokens] = useState('1000');
  const [dailyBudget, setDailyBudget] = useState('');
  const [cacheEnabled, setCacheEnabled] = useState(true);
  const [cacheTtlHours, setCacheTtlHours] = useState('24');

  const [inputEnabled, setInputEnabled] = useState(false);
  const [outputEnabled, setOutputEnabled] = useState(false);
  const [input, setInput] = useState<SchemaEditorState>({ schema: EMPTY_OBJECT_SCHEMA, valid: true });
  const [output, setOutput] = useState<SchemaEditorState>({ schema: EMPTY_OBJECT_SCHEMA, valid: true });

  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const idTaken = existingIds.includes(id);
  const idValid = ID_SLUG.test(id);
  const schemaInvalid = (inputEnabled && !input.valid) || (outputEnabled && !output.valid);
  const tempNum = Number(temperature);
  const tokNum = Number(maxTokens);
  const ready =
    idValid &&
    !idTaken &&
    name.trim() !== '' &&
    promptTemplate.trim() !== '' &&
    model !== '' &&
    tempNum >= 0 &&
    tempNum <= 2 &&
    Number.isFinite(tokNum) &&
    tokNum > 0 &&
    !schemaInvalid;

  // Models still selectable as fallbacks: anything not already the primary or
  // an existing fallback.
  const usedModels = [model, ...fallbacks].filter(Boolean);

  const submit = async () => {
    setBusy(true);
    setErr(null);
    try {
      const payload: Partial<TTask> = {
        id,
        name,
        description: description.trim() || undefined,
        prompt_template: promptTemplate,
        system_prompt: systemPrompt.trim() || undefined,
        model,
        fallback_models: fallbacks.length ? fallbacks : undefined,
        temperature: tempNum,
        max_tokens: tokNum,
        daily_budget_usd: dailyBudget.trim() ? Number(dailyBudget) : undefined,
        cache_enabled: cacheEnabled,
        cache_ttl_seconds: cacheEnabled && cacheTtlHours.trim()
          ? Math.round(Number(cacheTtlHours) * 3600)
          : undefined,
        input_schema: inputEnabled ? (input.schema as Record<string, unknown>) : undefined,
        output_schema: outputEnabled ? (output.schema as Record<string, unknown>) : undefined,
      };
      await api.createTask(payload);
      await onCreated(id);
    } catch (e) {
      setErr(errorMessage(e));
      setBusy(false);
    }
  };

  return (
    <div className="mx-auto max-w-4xl flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <Typography variant="heading" size="6" className="text-primary-text">
          New task
        </Typography>
        <Button variant="ghost" size="s" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
      </div>

      <Section title="Identity">
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
          <label className="block">
            <Typography variant="body" size="1" className="text-tertiary-text mb-1">
              Task id (slug — lowercase, digits, hyphens)
            </Typography>
            <input
              className={INPUT_CLS}
              value={id}
              onChange={(e) => setId(e.target.value)}
              placeholder="e.g. attribute-extraction"
            />
            {id !== '' && !idValid && (
              <Typography variant="body" size="1" className="text-error-text mt-0.5">
                Must match {`^[a-z0-9][a-z0-9-]{1,63}$`} (no spaces or uppercase).
              </Typography>
            )}
            {id !== '' && idValid && idTaken && (
              <Typography variant="body" size="1" className="text-error-text mt-0.5">
                A task with this id already exists.
              </Typography>
            )}
          </label>
          <label className="block">
            <Typography variant="body" size="1" className="text-tertiary-text mb-1">
              Name
            </Typography>
            <input
              className={INPUT_CLS}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Human-readable name"
            />
          </label>
        </div>
        <label className="block mt-3">
          <Typography variant="body" size="1" className="text-tertiary-text mb-1">
            Description (optional)
          </Typography>
          <TextArea value={description} onChange={({ value }) => setDescription(value)} rows={2} />
        </label>
      </Section>

      <Section title="Model routing">
        <label className="block max-w-md">
          <Typography variant="body" size="1" className="text-tertiary-text mb-1">
            Primary model
          </Typography>
          <select
            className={INPUT_CLS}
            value={model}
            onChange={(e) => {
              const m = e.target.value;
              setModel(m);
              // Drop it from fallbacks if it was there.
              setFallbacks((prev) => prev.filter((f) => f !== m));
            }}
          >
            <option value="">Select a model…</option>
            {MODEL_GROUPS.map((group) => (
              <optgroup key={group.provider} label={group.provider}>
                {group.models.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        </label>

        {fallbacks.length > 0 && (
          <div className="flex flex-col gap-1 max-w-md mt-3">
            {fallbacks.map((m, i) => (
              <div
                key={m}
                className="flex items-center gap-3 px-3 py-2 rounded-md border border-solid border-primary-border bg-primary-bg"
              >
                <Typography variant="body" size="3" className="text-primary-text font-medium flex-1">
                  {m}
                </Typography>
                <Typography variant="body" size="1" className="text-tertiary-text uppercase tracking-wider">
                  fallback #{i + 1}
                </Typography>
                <Button
                  variant="ghost"
                  size="s"
                  onClick={() => setFallbacks((prev) => prev.filter((f) => f !== m))}
                >
                  ✕
                </Button>
              </div>
            ))}
          </div>
        )}

        <div className="mt-2 max-w-md">
          <select
            value=""
            disabled={!model}
            onChange={(e) => {
              if (e.target.value) setFallbacks((prev) => [...prev, e.target.value]);
            }}
            className={INPUT_CLS}
          >
            <option value="">+ Add fallback model…</option>
            {MODEL_GROUPS.map((group) => {
              const avail = group.models.filter((m) => !usedModels.includes(m));
              if (avail.length === 0) return null;
              return (
                <optgroup key={group.provider} label={group.provider}>
                  {avail.map((m) => (
                    <option key={m} value={m}>
                      {m}
                    </option>
                  ))}
                </optgroup>
              );
            })}
          </select>
          <Typography variant="body" size="1" className="text-tertiary-text mt-1">
            Fallbacks are tried in order when the primary fails or its circuit is open.
          </Typography>
        </div>
      </Section>

      <Section title="Sampling & budget">
        <div className="grid grid-cols-1 lg:grid-cols-3 gap-3">
          <label className="block">
            <Typography variant="body" size="1" className="text-tertiary-text mb-1">
              Temperature (0–2)
            </Typography>
            <input
              className={INPUT_CLS}
              type="number"
              min={0}
              max={2}
              step={0.1}
              value={temperature}
              onChange={(e) => setTemperature(e.target.value)}
            />
          </label>
          <label className="block">
            <Typography variant="body" size="1" className="text-tertiary-text mb-1">
              Max output tokens
            </Typography>
            <input
              className={INPUT_CLS}
              type="number"
              min={1}
              step={1}
              value={maxTokens}
              onChange={(e) => setMaxTokens(e.target.value)}
            />
          </label>
          <label className="block">
            <Typography variant="body" size="1" className="text-tertiary-text mb-1">
              Daily budget USD (optional, blank = no cap)
            </Typography>
            <input
              className={INPUT_CLS}
              type="number"
              min={0}
              step={1}
              value={dailyBudget}
              onChange={(e) => setDailyBudget(e.target.value)}
              placeholder="no cap"
            />
          </label>
        </div>
        <div className="flex items-center gap-3 mt-3">
          <label className="flex items-center gap-2 select-none">
            <input
              type="checkbox"
              checked={cacheEnabled}
              onChange={(e) => setCacheEnabled(e.target.checked)}
            />
            <Typography variant="body" size="2" className="text-primary-text">
              Cache predictions
            </Typography>
          </label>
          {cacheEnabled && (
            <label className="flex items-center gap-2">
              <Typography variant="body" size="1" className="text-tertiary-text">
                TTL (hours)
              </Typography>
              <input
                className={cn(INPUT_CLS, 'w-24')}
                type="number"
                min={1}
                step={1}
                value={cacheTtlHours}
                onChange={(e) => setCacheTtlHours(e.target.value)}
              />
            </label>
          )}
        </div>
      </Section>

      <Section title="Prompt">
        <Typography variant="body" size="1" className="text-tertiary-text mb-1">
          System prompt (optional)
        </Typography>
        <TextArea value={systemPrompt} onChange={({ value }) => setSystemPrompt(value)} rows={2} />
        <Typography variant="body" size="1" className="text-tertiary-text mb-1 mt-3">
          Prompt template (Go template: {'{{.field}}'}) — required
        </Typography>
        <TextArea value={promptTemplate} onChange={({ value }) => setPromptTemplate(value)} rows={8} />
      </Section>

      <Section title="Input & output schema (optional)">
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
          <SchemaPane
            title="Input schema"
            hint="Validates request inputs (422 on mismatch) and auto-populates prompt variables. Off = free-form."
            enabled={inputEnabled}
            onToggle={setInputEnabled}
            canWrite={true}
          >
            <SchemaEditor initial={input.schema} readOnly={false} onChange={setInput} />
          </SchemaPane>
          <SchemaPane
            title="Output schema"
            hint="Validates model output. Off = raw text output (no output_valid flag)."
            enabled={outputEnabled}
            onToggle={setOutputEnabled}
            canWrite={true}
          >
            <SchemaEditor initial={output.schema} readOnly={false} onChange={setOutput} />
          </SchemaPane>
        </div>
      </Section>

      {err && (
        <Typography variant="body" size="2" className="text-error-text">
          {err}
        </Typography>
      )}

      <div className="flex items-center gap-3">
        <Button variant="primary" size="m" disabled={!ready || busy} onClick={submit}>
          {busy ? 'Creating…' : 'Create task'}
        </Button>
        <Button variant="ghost" size="m" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        {!ready && (
          <Typography variant="body" size="1" className="text-tertiary-text">
            Fill in a valid id, name, model, and prompt template to continue.
          </Typography>
        )}
      </div>
    </div>
  );
};

// ── Schema editor ─────────────────────────────────────────────────────────────

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

// ── Test panel ────────────────────────────────────────────────────────────────

const TestPanel = ({ task, versions }: { task: TTask; versions: TPromptVersion[] }) => {
  const fields = useMemo(() => {
    const props =
      (task.input_schema?.properties as Record<string, unknown> | undefined) ?? {};
    return Object.keys(props);
  }, [task.input_schema]);

  const [values, setValues] = useState<Record<string, string>>({});
  const [version, setVersion] = useState<number>(0); // 0 = active
  const [result, setResult] = useState<TPredictResult | null>(null);
  // Round-trip latency measured on the client: from the Run click to the moment
  // the prediction lands here (includes network + server time, what the user
  // actually waits), shown instead of the server-reported latency_ms.
  const [clientLatencyMs, setClientLatencyMs] = useState<number | null>(null);
  const [running, setRunning] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const run = async () => {
    setRunning(true);
    setErr(null);
    setResult(null);
    setClientLatencyMs(null);
    const start = performance.now();
    try {
      const inputs: Record<string, unknown> = {};
      for (const [k, v] of Object.entries(values)) {
        if (v !== '') inputs[k] = v;
      }
      const res = await api.testTask(task.id, inputs, version > 0 ? { version } : undefined);
      setClientLatencyMs(Math.round(performance.now() - start));
      setResult(res);
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Test failed');
    } finally {
      setRunning(false);
    }
  };

  return (
    <Section title="Test panel (not counted as production traffic)">
      {fields.length === 0 ? (
        <Typography variant="body" size="2" className="text-tertiary-text">
          This task has no input schema — use the Compare playground instead.
        </Typography>
      ) : (
        <>
          <div className="flex flex-col gap-2">
            {fields.map((f) => (
              <div key={f}>
                <Typography variant="body" size="1" className="text-tertiary-text mb-0.5">
                  {f}
                </Typography>
                <TextArea
                  value={values[f] ?? ''}
                  onChange={({ value }) => setValues((prev) => ({ ...prev, [f]: value }))}
                  rows={1}
                />
              </div>
            ))}
          </div>
          <div className="flex items-center gap-3 mt-3">
            <select
              value={version}
              onChange={(e) => setVersion(Number(e.target.value))}
              className="border border-solid border-primary-border rounded-md px-2 py-1.5 text-sm bg-primary-bg text-primary-text"
            >
              <option value={0}>Active version (v{task.prompt_version})</option>
              {versions
                .filter((v) => !v.active)
                .map((v) => (
                  <option key={v.version} value={v.version}>
                    v{v.version} {v.note ? `— ${v.note.slice(0, 30)}` : '(draft)'}
                  </option>
                ))}
            </select>
            <Button variant="primary" size="s" disabled={running} onClick={run}>
              {running ? 'Running…' : '▶ Run test'}
            </Button>
          </div>
        </>
      )}

      {err && (
        <Typography variant="body" size="2" className="text-error-text mt-2">
          {err}
        </Typography>
      )}

      {result && (
        <div className="mt-3 border border-solid border-primary-border rounded-md overflow-hidden">
          <div className="flex items-center gap-3 bg-secondary-bg px-3 py-2">
            <Badge ok={result.output_valid !== false}>
              {result.output_valid === null
                ? 'no output schema'
                : result.output_valid
                  ? 'schema valid'
                  : 'SCHEMA INVALID'}
            </Badge>
            {result.fallback_used && <Badge ok={false}>fallback used</Badge>}
            <Typography variant="body" size="1" className="text-tertiary-text">
              v{result.prompt_version} · {result.model} · {clientLatencyMs ?? result.latency_ms}ms ·{' '}
              {result.usage.total_tokens} tok · {formatCost(result.usage.cost_usd)}
            </Typography>
          </div>
          <pre className="m-0 px-3 py-2 text-xs text-primary-text overflow-x-auto whitespace-pre-wrap">
            {result.error
              ? `⚠️ ${result.error}`
              : JSON.stringify(result.output ?? result.raw_response, null, 2)}
          </pre>
        </div>
      )}
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

const Badge = ({ ok, children }: { ok: boolean; children: ReactNode }) => (
  <span
    className={cn(
      'text-[10px] font-semi-bold uppercase tracking-wider px-2 py-0.5 rounded-full',
      ok ? 'bg-tertiary-bg text-primary-text' : 'bg-error-bg text-error-text',
    )}
  >
    {children}
  </span>
);

export default TasksPage;
