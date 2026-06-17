import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { Typography, Spinner, Button, cn } from '@meesho/merlin-ui-tailwind';
import type { TModelHealthStatus, THealthEvent } from '../types';
import { api, ApiError } from '../api/client';

// ModelHealthPage is the admin view of the per-(task, model) circuit breaker:
// a live table of which models are healthy / unhealthy / probing for each task,
// a one-click "Mark healthy" override, and the persisted fallback/health event
// log. It polls every few seconds so state stays live as traffic flows.
const ModelHealthPage = () => {
  const [enabled, setEnabled] = useState(true);
  const [statuses, setStatuses] = useState<TModelHealthStatus[]>([]);
  const [events, setEvents] = useState<THealthEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [forbidden, setForbidden] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  // When set, the events log is filtered to one (task, model).
  const [focus, setFocus] = useState<{ task: string; model: string } | null>(null);

  const refresh = useCallback(() => {
    api
      .modelHealth()
      .then((d) => {
        setEnabled(d.enabled);
        setStatuses(d.statuses);
        setForbidden(false);
      })
      .catch((e) => {
        if (e instanceof ApiError && e.status === 403) setForbidden(true);
      })
      .finally(() => setLoading(false));
  }, []);

  const refreshEvents = useCallback(() => {
    api
      .modelHealthEvents(focus?.task ?? '', focus?.model ?? '', 1, 50)
      .then((d) => setEvents(d.events))
      .catch(() => {});
  }, [focus]);

  // Poll live state every 4s; refetch events alongside.
  useEffect(() => {
    refresh();
    const id = setInterval(refresh, 4000);
    return () => clearInterval(id);
  }, [refresh]);

  useEffect(() => {
    refreshEvents();
  }, [refreshEvents]);

  const markHealthy = async (s: TModelHealthStatus) => {
    setBusy(`${s.task_id}:${s.model}`);
    try {
      await api.resetModelHealth(s.task_id, s.model);
      refresh();
      refreshEvents();
    } catch {
      /* surfaced by the row staying unhealthy */
    } finally {
      setBusy(null);
    }
  };

  if (forbidden) {
    return (
      <div className="flex-1 flex items-center justify-center bg-primary-bg">
        <Typography variant="body" size="3" className="text-tertiary-text">
          Model health is available to admins only.
        </Typography>
      </div>
    );
  }

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center bg-primary-bg">
        <Spinner />
      </div>
    );
  }

  return (
    <div className="flex-1 overflow-y-auto bg-primary-bg p-6">
      <div className="mx-auto max-w-6xl flex flex-col gap-5">
        <div className="flex items-start justify-between gap-4">
          <div>
            <Typography variant="heading" size="6" className="text-primary-text">
              Model health
            </Typography>
            <Typography variant="body" size="3" className="text-tertiary-text">
              Per-task circuit breaker. A model that repeatedly errors (network, auth,
              rate-limit, or schema-invalid output) is skipped for that task until it
              recovers — or until you reset it here.
            </Typography>
          </div>
          {!enabled && <Pill tone="warn">breaker disabled</Pill>}
        </div>

        {/* Live status table */}
        <div className="border border-solid border-primary-border rounded-lg overflow-hidden">
          <div className="bg-secondary-bg px-4 py-2.5">
            <Typography variant="body" size="2" className="text-primary-text font-semi-bold">
              Live status — {statuses.length} tracked
            </Typography>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-left">
              <thead className="bg-secondary-bg">
                <tr>
                  <Th>Task</Th>
                  <Th>Model</Th>
                  <Th>State</Th>
                  <Th right>Consec. fails</Th>
                  <Th right>Trips</Th>
                  <Th right>Fail / OK</Th>
                  <Th>Last reason</Th>
                  <Th>Action</Th>
                </tr>
              </thead>
              <tbody>
                {statuses.map((s) => {
                  const key = `${s.task_id}:${s.model}`;
                  const focused = focus?.task === s.task_id && focus?.model === s.model;
                  return (
                    <tr
                      key={key}
                      className={cn(
                        'border-t border-solid border-tertiary-border cursor-pointer hover:bg-secondary-bg',
                        focused && 'bg-secondary-bg',
                      )}
                      onClick={() => setFocus(focused ? null : { task: s.task_id, model: s.model })}
                    >
                      <Td>{s.task_id}</Td>
                      <Td>
                        <span className="whitespace-nowrap">{s.model}</span>
                        {s.provider && (
                          <span className="text-tertiary-text"> · {s.provider}</span>
                        )}
                      </Td>
                      <Td>
                        <StatePill s={s} />
                      </Td>
                      <Td right>{s.consecutive_failures}</Td>
                      <Td right>{s.trips}</Td>
                      <Td right>
                        <span className="text-error-text">{s.total_failures}</span>
                        {' / '}
                        <span className="text-primary-text">{s.total_successes}</span>
                      </Td>
                      <Td>
                        <span className="block max-w-[260px] truncate text-tertiary-text" title={s.last_reason}>
                          {s.last_reason || '—'}
                        </span>
                      </Td>
                      <Td>
                        {s.state !== 'healthy' ? (
                          <Button
                            variant="outline"
                            size="s"
                            disabled={busy === key}
                            onClick={(e) => {
                              e.stopPropagation();
                              markHealthy(s);
                            }}
                          >
                            {busy === key ? '…' : 'Mark healthy'}
                          </Button>
                        ) : (
                          <span className="text-tertiary-text text-xs">—</span>
                        )}
                      </Td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          {statuses.length === 0 && (
            <div className="py-12 text-center">
              <Typography variant="body" size="3" className="text-tertiary-text">
                No models tracked yet — health is recorded as production predictions run.
              </Typography>
            </div>
          )}
        </div>

        {/* Event log */}
        <div className="border border-solid border-primary-border rounded-lg overflow-hidden">
          <div className="bg-secondary-bg px-4 py-2.5 flex items-center gap-3">
            <Typography variant="body" size="2" className="text-primary-text font-semi-bold">
              Events
            </Typography>
            {focus && (
              <>
                <span className="text-xs text-tertiary-text">
                  filtered: {focus.task} · {focus.model}
                </span>
                <button
                  onClick={() => setFocus(null)}
                  className="text-xs text-accent underline bg-transparent border-none cursor-pointer p-0"
                >
                  clear
                </button>
              </>
            )}
            <span className="ml-auto text-xs text-tertiary-text">
              click a row above to filter
            </span>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-left">
              <thead className="bg-secondary-bg">
                <tr>
                  <Th>Time</Th>
                  <Th>Task</Th>
                  <Th>Model</Th>
                  <Th>Event</Th>
                  <Th>Reason</Th>
                  <Th right>Cooldown</Th>
                </tr>
              </thead>
              <tbody>
                {events.map((e) => (
                  <tr key={e.id} className="border-t border-solid border-tertiary-border">
                    <Td>
                      <span className="whitespace-nowrap text-tertiary-text">{fmtTime(e.created_at)}</span>
                    </Td>
                    <Td>{e.task_id}</Td>
                    <Td>
                      <span className="whitespace-nowrap">{e.model}</span>
                    </Td>
                    <Td>
                      <EventPill event={e.event} />
                    </Td>
                    <Td>
                      <span className="block max-w-[320px] truncate text-tertiary-text" title={e.reason}>
                        {e.reason || '—'}
                      </span>
                    </Td>
                    <Td right>{e.cooldown_ms > 0 ? `${Math.round(e.cooldown_ms / 1000)}s` : '—'}</Td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {events.length === 0 && (
            <div className="py-10 text-center">
              <Typography variant="body" size="3" className="text-tertiary-text">
                No events recorded yet.
              </Typography>
            </div>
          )}
        </div>
      </div>
    </div>
  );
};

// ── building blocks ───────────────────────────────────────────────────────────

const StatePill = ({ s }: { s: TModelHealthStatus }) => {
  if (s.state === 'healthy') return <Pill tone="ok">healthy</Pill>;
  if (s.state === 'probing') return <Pill tone="warn">probing</Pill>;
  // unhealthy — show remaining cooldown
  const mins = Math.floor(s.open_for_seconds / 60);
  const secs = s.open_for_seconds % 60;
  const left = s.open_for_seconds > 0 ? ` ${mins > 0 ? `${mins}m` : ''}${secs}s` : '';
  return <Pill tone="error">unhealthy{left}</Pill>;
};

const EVENT_TONE: Record<string, Tone> = {
  failure: 'warn',
  tripped: 'error',
  recovered: 'ok',
  manual_reset: 'neutral',
};
const EventPill = ({ event }: { event: string }) => (
  <Pill tone={EVENT_TONE[event] ?? 'neutral'}>{event.replace('_', ' ')}</Pill>
);

type Tone = 'ok' | 'error' | 'warn' | 'neutral';
const TONE: Record<Tone, string> = {
  ok: 'bg-green-100 text-green-800',
  error: 'bg-red-100 text-red-800',
  warn: 'bg-amber-100 text-amber-800',
  neutral: 'bg-tertiary-bg text-secondary-text',
};
const Pill = ({ tone, children }: { tone: Tone; children: ReactNode }) => (
  <span className={cn('rounded px-1.5 py-px text-[10px] font-medium uppercase tracking-wide whitespace-nowrap', TONE[tone])}>
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
  return d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

export default ModelHealthPage;
