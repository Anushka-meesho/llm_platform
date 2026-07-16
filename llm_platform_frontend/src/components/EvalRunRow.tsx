import { Button, Typography, cn } from '@meesho/merlin-ui-tailwind';
import type { TEvalRun } from '../types';

type TEvalRunRowProps = {
  run: TEvalRun;
  selected: boolean;
  onSelect: (run: TEvalRun) => void;
};

const formatPct = (value: number) => `${Math.round(value * 1000) / 10}%`;
const formatDate = (iso: string) => (iso ? `${iso.slice(0, 10)} ${iso.slice(11, 16)}` : '—');

const EvalRunRow = ({ run, selected, onSelect }: TEvalRunRowProps) => (
  <div
    className={cn(
      'flex items-center gap-3 rounded-md border border-solid px-3 py-2',
      selected ? 'border-primary-border bg-secondary-bg' : 'border-tertiary-border',
    )}
  >
    <Typography variant="body" size="2" className="text-primary-text font-medium w-16">
      {formatPct(run.match_rate)}
    </Typography>
    <Typography variant="body" size="1" className="text-tertiary-text flex-1">
      {run.dataset_name} v{run.dataset_version} · prompt v{run.prompt_version} · {run.passed}/{run.total} passed
    </Typography>
    <Typography variant="body" size="1" className="text-tertiary-text whitespace-nowrap">
      {formatDate(run.created_at)}
    </Typography>
    <Button variant="ghost" size="s" onClick={() => onSelect(run)}>
      {selected ? 'Hide output' : 'View output'}
    </Button>
  </div>
);

export default EvalRunRow;
