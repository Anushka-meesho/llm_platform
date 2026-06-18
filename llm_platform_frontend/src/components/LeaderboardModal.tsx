import { useEffect, useState, useCallback } from 'react';
import { Typography } from '@meesho/merlin-ui-tailwind';
import { api, errorMessage } from '../api/client';
import { useToast } from '../toast/context';
import type { TLeaderboardEntry } from '../types';

type TLeaderboardModalProps = {
  sessionId: string;
  onClose: () => void;
};

const LeaderboardModal = ({ sessionId, onClose }: TLeaderboardModalProps) => {
  const toast = useToast();
  const [entries, setEntries] = useState<TLeaderboardEntry[]>([]);
  const [loading, setLoading] = useState(true);

  // setState lives only in the promise callbacks (loading initializes to true),
  // so calling this from the effect doesn't cascade renders.
  const fetchData = useCallback(() => {
    api
      .getLeaderboard(sessionId)
      .then((res) => setEntries(res.entries))
      .catch((e) => {
        // The user opened/refreshed the leaderboard — clearing it would silently
        // look like "no ratings", so surface the real reason via a toast.
        setEntries([]);
        toast.error(errorMessage(e));
      })
      .finally(() => setLoading(false));
  }, [sessionId, toast]);

  useEffect(() => { fetchData(); }, [fetchData]);

  const top = entries[0];
  const tied = entries.filter((e) => e.avg_score === top?.avg_score);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center"
      style={{ backgroundColor: 'rgba(0,0,0,0.4)' }}
      onClick={onClose}
    >
      <div
        className="bg-primary-bg border border-solid border-primary-border rounded-2xl shadow-xl w-full max-w-sm mx-4 p-6"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-1">
          <Typography variant="heading" size="3" className="text-primary-text">
            🏆 Leaderboard
          </Typography>
          <div className="flex items-center gap-1">
            <button
              onClick={fetchData}
              disabled={loading}
              title="Refresh"
              className="text-tertiary-text hover:text-primary-text text-base leading-none bg-transparent border-0 cursor-pointer focus:outline-none disabled:opacity-40"
            >
              ↻
            </button>
            <button
              onClick={onClose}
              className="text-tertiary-text hover:text-primary-text text-xl leading-none bg-transparent border-0 cursor-pointer focus:outline-none"
            >
              ×
            </button>
          </div>
        </div>

        <Typography variant="body" size="2" className="text-tertiary-text mb-4">
          Average manual score per model for this session.
        </Typography>

        {loading ? (
          <Typography variant="body" size="3" className="text-tertiary-text text-center py-4">
            Loading…
          </Typography>
        ) : entries.length === 0 ? (
          <Typography variant="body" size="3" className="text-tertiary-text text-center py-4">
            No ratings yet — rate some responses first.
          </Typography>
        ) : (
          <>
            <div className="flex flex-col gap-4 mb-4">
              {entries.map((e, i) => (
                <div key={e.model}>
                  <div className="flex items-center justify-between mb-1">
                    <div className="flex items-center gap-2">
                      <span className="text-[11px] text-tertiary-text w-4">{i + 1}</span>
                      <Typography variant="body" size="3" className="text-primary-text font-semi-bold">
                        {e.model}
                      </Typography>
                    </div>
                    <Typography variant="body" size="3" className="font-semi-bold" style={{ color: '#F97316' }}>
                      {e.avg_score.toFixed(1)}/5
                    </Typography>
                  </div>
                  <div className="w-full rounded-full h-2 bg-secondary-bg overflow-hidden">
                    <div
                      className="h-2 rounded-full"
                      style={{
                        width: `${(e.avg_score / 5) * 100}%`,
                        backgroundColor: i === 0 ? '#F97316' : '#FED7AA',
                      }}
                    />
                  </div>
                  <Typography variant="body" size="1" className="text-tertiary-text mt-0.5">
                    {e.rating_count} {e.rating_count === 1 ? 'rating' : 'ratings'}
                  </Typography>
                </div>
              ))}
            </div>

            {top && (
              <div className="border-t border-solid border-primary-border pt-3">
                <Typography variant="body" size="2" className="text-tertiary-text">
                  {tied.length > 1
                    ? `${tied.map((e) => e.model).join(' and ')} are tied at ${top.avg_score.toFixed(1)}/5.`
                    : `"${top.model}" leads with an average of ${top.avg_score.toFixed(1)}/5.`}{' '}
                  This manual leaderboard is the precursor to the automated eval layer.
                </Typography>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
};

export default LeaderboardModal;
