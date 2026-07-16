import { Typography } from '@meesho/merlin-ui-tailwind';
import EvalRunOutputPanel from './EvalRunOutputPanel';
import EvalRunRow from './EvalRunRow';
import type { TEvalRun } from '../types';

type TEvalRecentRunsProps = {
  runs: TEvalRun[];
  selectedRunId: number | null;
  onSelectRun: (run: TEvalRun) => void;
};

const EvalRecentRuns = ({ runs, selectedRunId, onSelectRun }: TEvalRecentRunsProps) => {
  if (runs.length === 0) return null;

  const selectedRun = runs.find((run) => run.id === selectedRunId) ?? null;

  return (
    <div className="flex flex-col gap-2">
      <Typography variant="body" size="2" className="text-primary-text font-medium">
        Recent eval runs
      </Typography>
      {runs.slice(0, 5).map((run) => (
        <EvalRunRow
          key={run.id}
          run={run}
          selected={run.id === selectedRunId}
          onSelect={onSelectRun}
        />
      ))}
      {selectedRun && <EvalRunOutputPanel run={selectedRun} />}
    </div>
  );
};

export default EvalRecentRuns;
