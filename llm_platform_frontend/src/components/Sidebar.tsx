import { useState, useCallback } from 'react';
import {
  Button,
  Checkbox,
  Slider,
  Typography,
  Divider,
  cn,
} from '@meesho/merlin-ui-tailwind';
import type { TSessionSummary, TSessionDetail } from '../types';
import { MODEL_GROUPS } from '../types';

type TSidebarProps = {
  selectedModels: string[];
  setSelectedModels: (models: string[]) => void;
  temperature: number;
  setTemperature: (t: number) => void;
  sessions: TSessionSummary[];
  page: number;
  totalPages: number;
  sessionsLoading: boolean;
  currentSessionId: string | null;
  isOpen: boolean;
  onToggle: () => void;
  onNewChat: () => void;
  onLoadSession: (id: string) => Promise<TSessionDetail>;
  onSessionLoaded: (detail: TSessionDetail) => void;
  onDeleteSession: (id: string) => Promise<void>;
  onFetchPage: (n: number) => void;
};

const Sidebar = ({
  selectedModels,
  setSelectedModels,
  temperature,
  setTemperature,
  sessions,
  page,
  totalPages,
  sessionsLoading,
  currentSessionId,
  isOpen,
  onToggle,
  onNewChat,
  onLoadSession,
  onSessionLoaded,
  onDeleteSession,
  onFetchPage,
}: TSidebarProps) => {
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
  // Per-provider expand/collapse. Unset = default: open when a model in the
  // group is selected, collapsed otherwise.
  const [openProviders, setOpenProviders] = useState<Record<string, boolean>>({});

  const handleModelChange = useCallback(
    (model: string, checked: boolean) => {
      if (checked) {
        setSelectedModels([...selectedModels, model]);
      } else {
        setSelectedModels(selectedModels.filter((m) => m !== model));
      }
    },
    [selectedModels, setSelectedModels],
  );

  const handleLoad = useCallback(
    async (id: string) => {
      const detail = await onLoadSession(id);
      onSessionLoaded(detail);
    },
    [onLoadSession, onSessionLoaded],
  );

  const handleDelete = useCallback(
    async (id: string) => {
      await onDeleteSession(id);
      setConfirmDeleteId(null);
    },
    [onDeleteSession],
  );

  return (
    <aside className="w-64 h-full flex-shrink-0 bg-secondary-bg flex flex-col overflow-hidden border-r border-solid border-primary-border">
      <div className="flex items-center gap-2 px-2 py-3">
        <Button variant="ghost" size="s" onClick={onToggle} className="flex-shrink-0" title={isOpen ? 'Collapse sidebar' : 'Expand sidebar'}>
          {isOpen ? '❮' : '❯'}
        </Button>
        <Typography variant="heading" size="5" className="text-primary-text whitespace-nowrap overflow-hidden">
          ⚡ LLM Platform
        </Typography>
      </div>

      {isOpen && (
        <div className="px-4 pb-3">
          <Button variant="primary" onClick={onNewChat} className="w-full">
            ＋ New Chat
          </Button>
        </div>
      )}

      <Divider />

      <div className="px-4 py-3">
        <Typography
          variant="body"
          size="1"
          className="text-tertiary-text font-semi-bold uppercase tracking-wider mb-2"
        >
          Models
        </Typography>
        <div className="flex flex-col gap-1">
          {MODEL_GROUPS.map((group) => {
            const selectedCount = group.models.filter((m) =>
              selectedModels.includes(m),
            ).length;
            const open = openProviders[group.provider] ?? selectedCount > 0;
            return (
              <div key={group.provider}>
                <button
                  onClick={() =>
                    setOpenProviders((prev) => ({ ...prev, [group.provider]: !open }))
                  }
                  className="w-full flex items-center justify-between py-1 rounded-md hover:bg-tertiary-bg px-1 transition-colors"
                  title={open ? `Collapse ${group.provider}` : `Expand ${group.provider}`}
                >
                  <Typography variant="body" size="3" className="text-primary-text font-medium">
                    {group.provider}
                  </Typography>
                  <Typography variant="body" size="1" className="text-tertiary-text">
                    {selectedCount > 0
                      ? `${selectedCount}/${group.models.length}`
                      : group.models.length}{' '}
                    {open ? '▾' : '▸'}
                  </Typography>
                </button>
                {open && (
                  <div className="flex flex-col gap-2 pl-3 py-1">
                    {group.models.map((model) => (
                      <Checkbox
                        key={model}
                        checked={selectedModels.includes(model)}
                        onChange={({ checked }) => handleModelChange(model, checked)}
                        label={
                          <Typography variant="body" size="3" className="text-primary-text">
                            {model}
                          </Typography>
                        }
                      />
                    ))}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>

      <Divider />

      <div className="px-4 py-3">
        <div className="flex items-center justify-between mb-2">
          <Typography
            variant="body"
            size="1"
            className="text-tertiary-text font-semi-bold uppercase tracking-wider"
          >
            Temperature
          </Typography>
          <Typography variant="body" size="2" className="text-primary-text font-semi-bold">
            {temperature.toFixed(2)}
          </Typography>
        </div>
        <div className="flex justify-between mb-1">
          <Typography variant="body" size="1" className="text-tertiary-text">focused</Typography>
          <Typography variant="body" size="1" className="text-tertiary-text">creative</Typography>
        </div>
        <Slider
          variant="single"
          value={Math.round(temperature * 10)}
          min={0}
          max={20}
          stepSize={1}
          onChange={(v) => setTemperature(Math.round(v as number) / 10)}
          onDrag={(v) => setTemperature(Math.round(v as number) / 10)}
        />
      </div>

      <Divider />

      <div className="flex-1 flex flex-col overflow-hidden px-4 py-3">
        <div className="flex items-center justify-between mb-2">
          <Typography
            variant="body"
            size="1"
            className="text-tertiary-text font-semi-bold uppercase tracking-wider"
          >
            History
          </Typography>
          <div className="flex items-center gap-1">
            <Button
              variant="ghost"
              size="s"
              disabled={page <= 1 || sessionsLoading}
              onClick={() => onFetchPage(page - 1)}
            >
              ◀
            </Button>
            <Button
              variant="ghost"
              size="s"
              disabled={page >= totalPages || sessionsLoading}
              onClick={() => onFetchPage(page + 1)}
            >
              ▶
            </Button>
          </div>
        </div>

        <div className="flex-1 overflow-y-auto flex flex-col gap-1 min-h-0">
          {sessionsLoading && (
            <Typography variant="body" size="2" className="text-tertiary-text text-center py-4">
              Loading…
            </Typography>
          )}
          {!sessionsLoading && sessions.length === 0 && (
            <Typography variant="body" size="2" className="text-tertiary-text text-center py-4">
              No sessions yet.
            </Typography>
          )}
          {sessions.map((session) => {
            const preview =
              session.first_prompt.length > 35
                ? session.first_prompt.slice(0, 35) + '…'
                : session.first_prompt;
            const isActive = session.session_id === currentSessionId;
            const isConfirming = confirmDeleteId === session.session_id;

            return (
              <div
                key={session.session_id}
                className={cn(
                  'rounded-md p-2 border border-solid border-transparent transition-colors',
                  isActive ? 'bg-tertiary-bg border-primary-border' : 'hover:bg-tertiary-bg',
                )}
              >
                <div className="flex items-start gap-1">
                  <button
                    onClick={() => handleLoad(session.session_id)}
                    className="flex-1 text-left min-w-0"
                  >
                    <Typography
                      variant="body"
                      size="3"
                      className={cn(
                        'truncate block',
                        isActive ? 'text-primary-text font-medium' : 'text-primary-text',
                      )}
                    >
                      {preview}
                    </Typography>
                    <Typography
                      variant="body"
                      size="1"
                      className={cn('mt-0.5', 'text-tertiary-text')}
                    >
                      {session.created_at.slice(0, 10)} ·{' '}
                      {session.turn_count} turn{session.turn_count !== 1 ? 's' : ''}
                    </Typography>
                  </button>
                  <button
                    onClick={() =>
                      setConfirmDeleteId(isConfirming ? null : session.session_id)
                    }
                    className="flex-shrink-0 text-tertiary-text hover:text-error-text transition-colors p-0.5 text-lg"
                    title="Delete session"
                  >
                    🗑
                  </button>
                </div>

                {isConfirming && (
                  <div className="mt-2 flex gap-1">
                    <Button
                      variant="primary"
                      size="s"
                      onClick={() => handleDelete(session.session_id)}
                      className="flex-1"
                    >
                      Delete
                    </Button>
                    <Button
                      variant="outline"
                      size="s"
                      onClick={() => setConfirmDeleteId(null)}
                      className="flex-1"
                    >
                      Cancel
                    </Button>
                  </div>
                )}
              </div>
            );
          })}
        </div>

        {sessions.length > 0 && (
          <Typography variant="body" size="1" className="text-tertiary-text text-center pt-2">
            Page {page} of {totalPages}
          </Typography>
        )}
      </div>
    </aside>
  );
};

export default Sidebar;
