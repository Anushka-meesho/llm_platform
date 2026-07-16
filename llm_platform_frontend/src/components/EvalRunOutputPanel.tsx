import { Typography } from '@meesho/merlin-ui-tailwind';
import type { TEvalRun } from '../types';

type TEvalRunOutputPanelProps = {
  run: TEvalRun;
  title?: string;
};

const formatJSON = (value: unknown) => JSON.stringify(value, null, 2);

const EvalRunOutputPanel = ({ run, title = 'Output samples' }: TEvalRunOutputPanelProps) => {
  const samples = run.details.output_samples ?? [];

  if (samples.length === 0) {
    return (
      <div className="rounded-md border border-solid border-tertiary-border p-3">
        <Typography variant="body" size="2" className="text-tertiary-text">
          No output samples were stored for this eval run.
        </Typography>
      </div>
    );
  }

  return (
    <div className="rounded-md border border-solid border-primary-border bg-secondary-bg p-3">
      <Typography variant="body" size="2" className="text-primary-text font-medium">
        {title} · {run.dataset_name} v{run.dataset_version} · prompt v{run.prompt_version}
      </Typography>
      <div className="mt-3 flex flex-col gap-3">
        {samples.map((sample) => (
          <div key={`${sample.item}-${sample.row_no}`} className="rounded-md border border-solid border-tertiary-border bg-primary-bg p-3">
            <div className="flex items-center gap-2 mb-2">
              <Typography variant="body" size="1" className="text-primary-text font-medium">
                Row {sample.row_no}
              </Typography>
              <Typography variant="body" size="1" className={sample.matched ? 'text-success-text' : 'text-error-text'}>
                {sample.matched ? 'Matched' : 'Mismatch'}
              </Typography>
              {sample.error && (
                <Typography variant="body" size="1" className="text-error-text">
                  {sample.error}
                </Typography>
              )}
            </div>
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
              <OutputBlock title="Expected output" value={sample.expected_output} />
              <OutputBlock title="Actual output" value={sample.actual_output} />
            </div>
          </div>
        ))}
      </div>
    </div>
  );
};

const OutputBlock = ({ title, value }: { title: string; value: unknown }) => (
  <div className="rounded-md border border-solid border-tertiary-border overflow-hidden">
    <div className="px-3 py-1.5 bg-secondary-bg">
      <Typography variant="body" size="1" className="text-primary-text font-medium">
        {title}
      </Typography>
    </div>
    <pre className="m-0 max-h-64 overflow-auto px-3 py-2 text-sm text-primary-text whitespace-pre-wrap">
      {formatJSON(value)}
    </pre>
  </div>
);

export default EvalRunOutputPanel;
