import { useMemo, useState } from 'react';
import { Button, Typography, cn } from '@meesho/merlin-ui-tailwind';
import { api, ApiError } from '../api/client';
import type { TPromptVersion } from '../types';

const PAGE_SIZES = [5, 10, 25] as const;
const ALL = Number.MAX_SAFE_INTEGER;

// formatDateTime renders the RFC3339 timestamp the API returns as "YYYY-MM-DD
// HH:MM". Falls back to the raw string if it isn't parseable.
function formatDateTime(iso: string): string {
  if (!iso || iso.startsWith('0001')) return '—';
  const date = iso.slice(0, 10);
  const time = iso.slice(11, 16);
  return time ? `${date} ${time}` : date;
}

// VersionHistory is the reusable prompt-version section: paginated list, compare
// against the live prompt, deploy (gated by canDeploy), and delete (gated by
// canDelete — admin-only on the server). It owns no fetching: the parent passes
// the versions and an onReload callback so the same component works inside the
// task detail and on the standalone Versions page.
const VersionHistory = ({
  taskId,
  versions,
  activeVersion,
  livePrompt,
  liveSystem,
  canDeploy,
  canDelete,
  onReload,
  onActiveChanged,
  defaultPageSize = 10,
}: {
  taskId: string;
  versions: TPromptVersion[];
  activeVersion: number;
  livePrompt: string;
  liveSystem: string;
  canDeploy: boolean;
  canDelete: boolean;
  onReload: () => Promise<void> | void;
  onActiveChanged?: () => Promise<void> | void;
  defaultPageSize?: number;
}) => {
  const [pageSize, setPageSize] = useState<number>(defaultPageSize);
  const [page, setPage] = useState(0);
  const [compareVersion, setCompareVersion] = useState<TPromptVersion | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [flash, setFlash] = useState<string | null>(null);

  const total = versions.length;
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const safePage = Math.min(page, pageCount - 1);
  const start = safePage * pageSize;
  const shown = useMemo(() => versions.slice(start, start + pageSize), [versions, start, pageSize]);

  const deploy = async (version: number) => {
    if (!window.confirm(`Deploy v${version}? Production traffic switches immediately.`)) return;
    setBusy(`deploy-${version}`);
    try {
      await api.deployVersion(taskId, version);
      setFlash(`v${version} is now live.`);
      await onReload();
      await onActiveChanged?.();
    } catch (e) {
      setFlash(e instanceof Error ? e.message : 'Deploy failed');
    } finally {
      setBusy(null);
    }
  };

  const remove = async (version: number) => {
    if (!window.confirm(`Delete v${version}? This permanently removes it from the history.`)) return;
    setBusy(`delete-${version}`);
    try {
      await api.deleteVersion(taskId, version);
      setFlash(`v${version} deleted.`);
      if (compareVersion?.version === version) setCompareVersion(null);
      await onReload();
    } catch (e) {
      setFlash(
        e instanceof ApiError && e.status === 409
          ? 'Cannot delete the active version — deploy another version first.'
          : e instanceof Error
            ? e.message
            : 'Delete failed',
      );
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="flex flex-col gap-2">
      {/* Pagination controls */}
      {total > 0 && (
        <div className="flex items-center gap-3 flex-wrap text-tertiary-text">
          <label className="flex items-center gap-1.5 text-sm">
            Show
            <select
              value={pageSize === ALL ? 'all' : pageSize}
              onChange={(e) => {
                setPageSize(e.target.value === 'all' ? ALL : Number(e.target.value));
                setPage(0);
              }}
              className="border border-solid border-primary-border rounded-md px-2 py-1 text-sm bg-primary-bg text-primary-text"
            >
              {PAGE_SIZES.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
              <option value="all">All</option>
            </select>
            at a time
          </label>
          <Typography variant="body" size="1" className="text-tertiary-text">
            {`Showing ${start + 1}–${Math.min(start + pageSize, total)} of ${total}`}
          </Typography>
          {pageCount > 1 && (
            <div className="flex items-center gap-1 ml-auto">
              <Button variant="ghost" size="s" disabled={safePage === 0} onClick={() => setPage(safePage - 1)}>
                ‹ Prev
              </Button>
              <Typography variant="body" size="1" className="text-tertiary-text">
                {safePage + 1}/{pageCount}
              </Typography>
              <Button variant="ghost" size="s" disabled={safePage >= pageCount - 1} onClick={() => setPage(safePage + 1)}>
                Next ›
              </Button>
            </div>
          )}
        </div>
      )}

      {flash && (
        <Typography variant="body" size="1" className="text-secondary-text">
          {flash}
        </Typography>
      )}

      {total === 0 ? (
        <Typography variant="body" size="2" className="text-tertiary-text">
          No versions yet.
        </Typography>
      ) : (
        <div className="flex flex-col gap-1">
          {shown.map((v) => (
            <div
              key={v.version}
              className="flex items-center gap-3 px-3 py-2 rounded-md border border-solid border-tertiary-border"
            >
              <Typography variant="body" size="3" className="text-primary-text font-medium w-12">
                v{v.version}
              </Typography>
              <Typography variant="body" size="1" className="text-tertiary-text flex-1 truncate">
                {v.version === activeVersion ? '● live — ' : ''}
                {v.note || v.prompt_template.slice(0, 60)}
              </Typography>
              <Typography variant="body" size="1" className="text-tertiary-text whitespace-nowrap">
                {formatDateTime(v.created_at)}
              </Typography>
              <Button
                variant="ghost"
                size="s"
                onClick={() => setCompareVersion(compareVersion?.version === v.version ? null : v)}
              >
                {compareVersion?.version === v.version ? 'Hide' : 'View'}
              </Button>
              {v.version !== activeVersion && canDeploy && (
                <Button variant="outline" size="s" disabled={busy !== null} onClick={() => deploy(v.version)}>
                  {busy === `deploy-${v.version}` ? 'Deploying…' : 'Deploy'}
                </Button>
              )}
              {v.version !== activeVersion && canDelete && (
                <Button
                  variant="ghost"
                  size="s"
                  disabled={busy !== null}
                  onClick={() => remove(v.version)}
                  title="Delete this version (admin)"
                  className="text-error-text"
                >
                  {busy === `delete-${v.version}` ? '…' : 'Delete'}
                </Button>
              )}
            </div>
          ))}
        </div>
      )}

      {compareVersion && (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-3 mt-2">
          <PromptBlock
            title={`v${compareVersion.version}${compareVersion.version === activeVersion ? ' (live)' : ''}`}
            system={compareVersion.system_prompt}
            prompt={compareVersion.prompt_template}
          />
          <PromptBlock title={`v${activeVersion} (live)`} system={liveSystem} prompt={livePrompt} />
        </div>
      )}
    </div>
  );
};

const PromptBlock = ({ title, system, prompt }: { title: string; system: string; prompt: string }) => (
  <div className={cn('border border-solid border-tertiary-border rounded-md overflow-hidden')}>
    <div className="bg-secondary-bg px-3 py-1.5">
      <Typography variant="body" size="1" className="text-primary-text font-semi-bold">
        {title}
      </Typography>
    </div>
    <pre className="m-0 px-3 py-2 text-xs text-primary-text whitespace-pre-wrap">
      {system ? `[system]\n${system}\n\n` : ''}
      {prompt}
    </pre>
  </div>
);

export default VersionHistory;
