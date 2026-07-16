import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Button, Checkbox, Input, Spinner, TextArea, Typography, cn } from '@meesho/merlin-ui-tailwind';
import type {
  TPromptVersion,
  TTask,
  TTaskStatsDetail,
} from '../types';
import { DEFAULT_COMPARE_MODELS, MODELS, MODEL_GROUPS } from '../types';
import { api, errorMessage } from '../api/client';
import { useAuth } from '../auth/useAuth';
import { can } from '../auth/permissions';
import SchemaEditor, { type SchemaEditorState } from '../components/SchemaEditor';
import OutputSchemaEditor from '../components/OutputSchemaEditor';
import VersionHistory from '../components/VersionHistory';
import EvalDatasetSection from '../components/EvalDatasetSection';
import ErrorState from '../components/ErrorState';
import { stableStringify } from '../utils/schema';
import { buildDefaultPrompts, canBuildDefaultPrompts } from '../utils/defaultPrompts';
import { usePersistentState, clearPersisted } from '../hooks/usePersistentState';
import { countTokens, estimateCost, formatCost } from '../utils/tokens';
import { useToast } from '../toast/context';

// TasksPage is the Prompt Studio: browse registered tasks, edit prompts as
// drafts, test any version against any model, and deploy — the
// edit → test → deploy loop.
const TasksPage = () => {
  const { user } = useAuth();
  const canWrite = can(user?.role, 'task:write');
  const [tasks, setTasks] = useState<TTask[]>([]);
  // The open task and the create-form toggle persist, so Tasks reopens where you
  // left it. A persisted id for a since-deleted task harmlessly falls back to the
  // catalog (selected resolves to null below).
  const [selectedId, setSelectedId] = usePersistentState<string | null>('tasks.selectedId', null);
  const [creating, setCreating] = usePersistentState('tasks.creating', false);
  const [focusEvalAfterCreate, setFocusEvalAfterCreate] = useState(false);
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
              setFocusEvalAfterCreate(true);
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
            focusEval={focusEvalAfterCreate}
            onEvalFocused={() => setFocusEvalAfterCreate(false)}
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
  focusEval,
  onEvalFocused,
  onChanged,
  onDeleted,
}: {
  task: TTask;
  focusEval: boolean;
  onEvalFocused: () => void;
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
  // In-progress prompt edits persist per task (TaskDetail mounts with key={id},
  // so the keys are stable), so an unsaved draft survives a reload.
  const [draft, setDraft] = usePersistentState(`tasks.draft.${task.id}`, task.prompt_template);
  const [draftSystem, setDraftSystem] = usePersistentState(
    `tasks.draftSystem.${task.id}`,
    task.system_prompt ?? '',
  );
  const [note, setNote] = usePersistentState(`tasks.note.${task.id}`, '');
  const [busy, setBusy] = useState<string | null>(null);
  const [flash, setFlash] = useState<string | null>(null);
  const evalSectionRef = useRef<HTMLDivElement | null>(null);

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

  useEffect(() => {
    if (!focusEval) return;
    const timer = window.setTimeout(() => {
      evalSectionRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' });
      onEvalFocused();
    }, 100);
    return () => window.clearTimeout(timer);
  }, [focusEval, onEvalFocused]);

  const draftDirty =
    draft !== task.prompt_template || draftSystem !== (task.system_prompt ?? '');
  const draftTokens = useMemo(
    () => countTokens(draftSystem, task.model) + countTokens(draft, task.model),
    [draft, draftSystem, task.model],
  );
  const draftCost = estimateCost(task.model, draftTokens, task.max_tokens);

  const canGenerate = canBuildDefaultPrompts(
    task.description,
    task.input_schema,
    task.output_schema,
  );
  const writeDefaults = () => {
    if (
      (draft.trim() || draftSystem.trim()) &&
      !window.confirm(
        "Replace the current draft prompts with defaults generated from this task's config?",
      )
    )
      return;
    const { systemPrompt, promptTemplate } = buildDefaultPrompts({
      taskName: task.name,
      taskDescription: task.description,
      inputSchema: task.input_schema,
      outputSchema: task.output_schema,
    });
    setDraftSystem(systemPrompt);
    setDraft(promptTemplate);
  };

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

      {/* Identity (name + description; id is immutable) */}
      <IdentitySection task={task} onSaved={onChanged} setFlash={setFlash} canWrite={canWrite} />

      {/* Model routing */}
      <ModelSection task={task} onSaved={onChanged} setFlash={setFlash} canWrite={canWrite} />

      {/* Sampling (max output tokens) */}
      <SamplingSection task={task} onSaved={onChanged} setFlash={setFlash} canWrite={canWrite} />

      {/* Input / output schema */}
      <SchemaSection key={task.id} task={task} onChanged={onChanged} setFlash={setFlash} canWrite={canWrite} />

      {/* Prompt editor */}
      <Section title="Prompt editor">
        <div className="flex items-center justify-between gap-3 mb-3 flex-wrap">
          <Typography variant="body" size="1" className="text-tertiary-text">
            Generate a starting draft from this task's description and input/output schema, then edit.
          </Typography>
          <Button
            variant="outline"
            size="s"
            disabled={!canGenerate || !canWrite}
            onClick={writeDefaults}
            title={
              !canWrite
                ? 'Your role cannot edit prompts'
                : canGenerate
                  ? 'Fill the system and user prompts from the input/output schema'
                  : 'This task has no input or output schema to generate from'
            }
          >
            ✨ Write default prompts from configs
          </Button>
        </div>
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

      {/* Eval datasets */}
      <div ref={evalSectionRef}>
        <Section title="Eval datasets">
          <EvalDatasetSection task={task} canWrite={canWrite} />
        </Section>
      </div>

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
  // The whole new-task draft persists under the tasks.create.* namespace, so a
  // partially filled form survives a reload; it's cleared once the task is
  // created (clearPersisted in submit).
  const [id, setId] = usePersistentState('tasks.create.id', '');
  const [name, setName] = usePersistentState('tasks.create.name', '');
  const [description, setDescription] = usePersistentState('tasks.create.description', '');
  const [systemPrompt, setSystemPrompt] = usePersistentState('tasks.create.systemPrompt', '');
  const [promptTemplate, setPromptTemplate] = usePersistentState('tasks.create.promptTemplate', '');
  const [model, setModel] = usePersistentState('tasks.create.model', '');
  const [fallbacks, setFallbacks] = usePersistentState<string[]>('tasks.create.fallbacks', []);
  const [temperature, setTemperature] = usePersistentState('tasks.create.temperature', '0.2');
  const [maxTokens, setMaxTokens] = usePersistentState('tasks.create.maxTokens', '1000');
  const [dailyBudget, setDailyBudget] = usePersistentState('tasks.create.dailyBudget', '');
  const [cacheEnabled, setCacheEnabled] = usePersistentState('tasks.create.cacheEnabled', true);
  const [cacheTtlHours, setCacheTtlHours] = usePersistentState('tasks.create.cacheTtlHours', '24');

  const [inputEnabled, setInputEnabled] = usePersistentState('tasks.create.inputEnabled', false);
  const [outputEnabled, setOutputEnabled] = usePersistentState('tasks.create.outputEnabled', false);
  const [input, setInput] = usePersistentState<SchemaEditorState>('tasks.create.input', {
    schema: EMPTY_OBJECT_SCHEMA,
    valid: true,
  });
  const [output, setOutput] = usePersistentState<SchemaEditorState>('tasks.create.output', {
    schema: EMPTY_OBJECT_SCHEMA,
    valid: true,
  });

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

  // Prefill the prompts from the schemas the author just defined, so they start
  // from a working draft instead of a blank page. Only the enabled schemas feed
  // it, and we confirm before clobbering anything already typed.
  const liveInput = inputEnabled ? (input.schema as Record<string, unknown>) : null;
  const liveOutput = outputEnabled ? (output.schema as Record<string, unknown>) : null;
  const canGenerate = canBuildDefaultPrompts(description, liveInput, liveOutput);
  const writeDefaults = () => {
    if (
      (systemPrompt.trim() || promptTemplate.trim()) &&
      !window.confirm('Replace the current prompts with defaults generated from the task config?')
    )
      return;
    const { systemPrompt: sp, promptTemplate: pt } = buildDefaultPrompts({
      taskName: name.trim() || id.trim(),
      taskDescription: description,
      inputSchema: liveInput,
      outputSchema: liveOutput,
    });
    setSystemPrompt(sp);
    setPromptTemplate(pt);
  };

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
      // The draft is committed — discard the persisted copy so it doesn't
      // resurface the next time the create form is opened.
      clearPersisted('tasks.create.');
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
        <div className="flex items-center justify-between gap-3 mb-3 flex-wrap">
          <Typography variant="body" size="1" className="text-tertiary-text">
            Start from defaults generated from the description and input/output schema below, then edit.
          </Typography>
          <Button
            variant="outline"
            size="s"
            disabled={!canGenerate}
            onClick={writeDefaults}
            title={
              canGenerate
                ? 'Fill the system and user prompts from the input/output schema'
                : 'Define an input or output schema below first'
            }
          >
            ✨ Write default prompts from configs
          </Button>
        </div>
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
            hint="The API response contract: pick what the caller receives (string, number, JSON, …). The model's answer is coerced to that type. Off = raw text (no output_valid flag)."
            enabled={outputEnabled}
            onToggle={setOutputEnabled}
            canWrite={true}
          >
            <OutputSchemaEditor initial={output.schema} readOnly={false} onChange={setOutput} />
          </SchemaPane>
        </div>
      </Section>

      <Section title="Eval dataset">
        <Typography variant="body" size="2" className="text-primary-text">
          CSV/XLSX upload becomes available after this task is created.
        </Typography>
        <Typography variant="body" size="1" className="text-tertiary-text mt-1">
          The uploaded file is validated against the saved task schema, then you can run the saved prompt and download row-level LLM outputs as CSV.
        </Typography>
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
          hint="The API response contract: pick what the caller receives (string, number, JSON, …). The model's answer is coerced to that type. Off = raw text (no output_valid flag)."
          enabled={outputEnabled}
          onToggle={setOutputEnabled}
          canWrite={canWrite}
        >
          <OutputSchemaEditor
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
// IdentitySection edits a task's human-facing name and description. The id is
// the task's primary key — it pins runs, cache entries, and integration URLs, so
// the backend refuses to change it (a rename is effectively a new task); it's
// shown read-only. The description matters beyond docs: it drives the "write
// default prompts" generator, so editing it here improves generated prompts.
const IdentitySection = ({
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
  const [name, setName] = useState(task.name);
  const [description, setDescription] = useState(task.description ?? '');
  const [saving, setSaving] = useState(false);

  const dirty = name !== task.name || description !== (task.description ?? '');
  const nameValid = name.trim() !== '';

  const save = async () => {
    setSaving(true);
    try {
      await api.updateTask(task.id, { name: name.trim(), description: description.trim() });
      setFlash('Task name and description updated.');
      await onSaved();
    } catch (e) {
      setFlash(e instanceof Error ? e.message : 'Update failed');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Section title="Identity">
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
        <label className="block">
          <Typography variant="body" size="1" className="text-tertiary-text mb-1">
            Task id (immutable)
          </Typography>
          <input className={cn(INPUT_CLS, 'opacity-60 cursor-not-allowed')} value={task.id} readOnly disabled />
          <Typography variant="body" size="1" className="text-tertiary-text mt-0.5">
            The id keys runs, cache, and integration URLs — create a new task to change it.
          </Typography>
        </label>
        <label className="block">
          <Typography variant="body" size="1" className="text-tertiary-text mb-1">
            Name
          </Typography>
          <input
            className={INPUT_CLS}
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={!canWrite}
            placeholder="Human-readable name"
          />
        </label>
      </div>
      <label className="block mt-3">
        <Typography variant="body" size="1" className="text-tertiary-text mb-1">
          Description — describe what the task should do; it drives the default-prompt generator
        </Typography>
        <TextArea
          value={description}
          onChange={({ value }) => setDescription(value)}
          rows={3}
          disabled={!canWrite}
        />
      </label>
      <div className="mt-3">
        <Button
          variant="primary"
          size="s"
          disabled={!dirty || saving || !nameValid || !canWrite}
          onClick={save}
          title={canWrite ? undefined : 'Your role cannot edit this task'}
        >
          {saving ? 'Saving…' : 'Save identity'}
        </Button>
      </div>
    </Section>
  );
};

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

// ── Sampling ──────────────────────────────────────────────────────────────────

// SamplingSection edits the task's max output token cap on an existing task.
// Saves via PUT merge semantics — only max_tokens changes; production traffic
// picks up the new cap on the next predict. The prediction cache keys on
// max_tokens, so a changed cap can't replay answers generated under the old one.
const SamplingSection = ({
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
  const [maxTokens, setMaxTokens] = useState(String(task.max_tokens));
  const [saving, setSaving] = useState(false);

  const tokNum = Number(maxTokens);
  const valid = Number.isInteger(tokNum) && tokNum > 0;
  const dirty = maxTokens.trim() !== String(task.max_tokens);

  const save = async () => {
    setSaving(true);
    try {
      await api.updateTask(task.id, { max_tokens: tokNum });
      setFlash(`Max output tokens set to ${tokNum} — takes effect on the next predict.`);
      await onSaved();
    } catch (e) {
      setFlash(e instanceof Error ? e.message : 'Max tokens change failed');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Section title="Sampling">
      <label className="block max-w-xs">
        <Typography variant="body" size="1" className="text-tertiary-text mb-1">
          Max output tokens
        </Typography>
        <input
          className={INPUT_CLS}
          type="number"
          min={1}
          step={1}
          value={maxTokens}
          disabled={!canWrite}
          onChange={(e) => setMaxTokens(e.target.value)}
        />
        {maxTokens.trim() !== '' && !valid && (
          <Typography variant="body" size="1" className="text-error-text mt-0.5">
            Must be a positive whole number.
          </Typography>
        )}
      </label>
      {canWrite ? (
        <div className="flex items-center gap-3 mt-3">
          <Button variant="primary" size="s" disabled={!dirty || !valid || saving} onClick={save}>
            {saving ? 'Saving…' : 'Save'}
          </Button>
          {dirty && (
            <Button
              variant="ghost"
              size="s"
              disabled={saving}
              onClick={() => setMaxTokens(String(task.max_tokens))}
            >
              Reset
            </Button>
          )}
          {dirty && valid && (
            <Typography variant="body" size="1" className="text-tertiary-text">
              Unsaved — caps each model's response length on the next predict.
            </Typography>
          )}
        </div>
      ) : (
        <Typography variant="body" size="1" className="text-tertiary-text mt-2">
          Your role cannot edit task config.
        </Typography>
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
