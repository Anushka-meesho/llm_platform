import { Typography } from '@meesho/merlin-ui-tailwind';
import EvalRunOutputPanel from './EvalRunOutputPanel';
import type { TEvalRun } from '../types';

type TEvalDatasetCheckPanelProps = {
  run: TEvalRun | null;
  hasInputSchema: boolean;
};

const EvalDatasetCheckPanel = ({ run, hasInputSchema }: TEvalDatasetCheckPanelProps) => {
  if (run) return <EvalRunOutputPanel run={run} title="Dataset check output" />;

  return (
    <div className="rounded-md border border-solid border-primary-border bg-secondary-bg p-3">
      <Typography variant="body" size="2" className="text-primary-text font-medium">
        Dataset check output
      </Typography>
      <div className="mt-3 rounded-md border border-dashed border-tertiary-border bg-primary-bg p-4">
        <Typography variant="body" size="2" className="text-tertiary-text">
          {hasInputSchema
            ? 'Upload a CSV/XLSX dataset, then Download CSV to get row-level prompt outputs.'
            : 'Add an input schema before uploading eval data.'}
        </Typography>
      </div>
    </div>
  );
};

export default EvalDatasetCheckPanel;
