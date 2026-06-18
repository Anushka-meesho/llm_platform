import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { Typography, Spinner } from '@meesho/merlin-ui-tailwind';
import type { TDashboard } from '../types';
import { api, errorMessage } from '../api/client';
import ErrorState from '../components/ErrorState';
import { formatCost } from '../utils/tokens';

const DashboardPage = () => {
  const [data, setData] = useState<TDashboard | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // State updates live only in the promise callbacks (never synchronously in the
  // effect body) so the loader can be reused for retry without cascading renders.
  const load = useCallback(() => {
    api
      .dashboard()
      .then((d) => {
        setData(d);
        setError(null);
      })
      .catch((e) => setError(errorMessage(e)))
      .finally(() => setLoading(false));
  }, []);

  const retry = useCallback(() => {
    setLoading(true);
    load();
  }, [load]);

  useEffect(() => {
    load();
  }, [load]);

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center bg-primary-bg">
        <Spinner />
      </div>
    );
  }

  if (error || !data) {
    return (
      <div className="flex-1 flex items-center justify-center bg-primary-bg">
        <ErrorState message={error ?? 'No data.'} onRetry={retry} />
      </div>
    );
  }

  const empty = data.total_runs === 0;

  return (
    <div className="flex-1 overflow-y-auto bg-primary-bg p-6">
      <div className="mx-auto max-w-5xl flex flex-col gap-6">
        <div>
          <Typography variant="heading" size="6" className="text-primary-text">
            Usage &amp; cost
          </Typography>
          <Typography variant="body" size="3" className="text-tertiary-text">
            Your runs, tokens, and spend across all models.
          </Typography>
        </div>

        {empty ? (
          <div className="border border-dashed border-primary-border rounded-lg py-16 text-center">
            <Typography variant="body" size="3" className="text-tertiary-text">
              No runs yet. Head to Compare and send a prompt to start tracking cost.
            </Typography>
          </div>
        ) : (
          <>
            {/* Summary cards */}
            <div className="flex flex-wrap gap-4">
              <Card label="Total runs" value={data.total_runs.toLocaleString()} />
              <Card label="Total tokens" value={data.total_tokens.toLocaleString()} />
              <Card label="Total spend" value={formatCost(data.total_cost_usd)} accent />
            </div>

            {/* Per-task table — the platform's primary cost dimension */}
            <div className="border border-solid border-primary-border rounded-lg overflow-hidden">
              <div className="bg-secondary-bg px-4 py-2.5">
                <Typography variant="body" size="2" className="text-primary-text font-semi-bold">
                  By task
                </Typography>
              </div>
              <table className="w-full text-left">
                <thead className="bg-secondary-bg">
                  <tr>
                    <Th>Task</Th>
                    <Th right>Runs</Th>
                    <Th right>Tokens</Th>
                    <Th right>Cost</Th>
                    <Th right>Avg latency</Th>
                    <Th right>Success</Th>
                  </tr>
                </thead>
                <tbody>
                  {data.by_task.map((t) => (
                    <tr key={t.task_id} className="border-t border-solid border-tertiary-border">
                      <Td>{t.task_id}</Td>
                      <Td right>{t.runs.toLocaleString()}</Td>
                      <Td right>{t.total_tokens.toLocaleString()}</Td>
                      <Td right>{formatCost(t.cost_usd)}</Td>
                      <Td right>{Math.round(t.avg_latency_ms)}ms</Td>
                      <Td right>{Math.round(t.success_rate * 100)}%</Td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            {/* Per-model table */}
            <div className="border border-solid border-primary-border rounded-lg overflow-hidden">
              <div className="bg-secondary-bg px-4 py-2.5">
                <Typography variant="body" size="2" className="text-primary-text font-semi-bold">
                  By model
                </Typography>
              </div>
              <table className="w-full text-left">
                <thead className="bg-secondary-bg">
                  <tr>
                    <Th>Model</Th>
                    <Th right>Runs</Th>
                    <Th right>Tokens</Th>
                    <Th right>Cost</Th>
                    <Th right>Avg latency</Th>
                    <Th right>Avg rating</Th>
                  </tr>
                </thead>
                <tbody>
                  {data.by_model.map((m) => (
                    <tr key={m.model} className="border-t border-solid border-tertiary-border">
                      <Td>{m.model}</Td>
                      <Td right>{m.runs.toLocaleString()}</Td>
                      <Td right>{m.total_tokens.toLocaleString()}</Td>
                      <Td right>{formatCost(m.cost_usd)}</Td>
                      <Td right>{Math.round(m.avg_latency_ms)}ms</Td>
                      <Td right>
                        {m.rating_count > 0
                          ? `★ ${m.avg_rating.toFixed(1)} (${m.rating_count})`
                          : '—'}
                      </Td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

          </>
        )}
      </div>
    </div>
  );
};

const Card = ({ label, value, accent }: { label: string; value: string; accent?: boolean }) => (
  <div className="flex-1 min-w-[160px] bg-secondary-bg border border-solid border-primary-border rounded-lg px-4 py-3">
    <Typography variant="body" size="1" className="text-tertiary-text uppercase tracking-wider">
      {label}
    </Typography>
    <Typography variant="heading" size="5" className={accent ? 'text-accent' : 'text-primary-text'}>
      {value}
    </Typography>
  </div>
);

const Th = ({ children, right }: { children: ReactNode; right?: boolean }) => (
  <th className={`px-4 py-2 ${right ? 'text-right' : 'text-left'}`}>
    <Typography variant="body" size="1" className="text-tertiary-text uppercase tracking-wider">
      {children}
    </Typography>
  </th>
);

const Td = ({ children, right }: { children: ReactNode; right?: boolean }) => (
  <td className={`px-4 py-2.5 ${right ? 'text-right tabular-nums' : ''}`}>
    <Typography variant="body" size="3" className="text-primary-text">
      {children}
    </Typography>
  </td>
);

export default DashboardPage;
