import { Button, Typography } from '@meesho/merlin-ui-tailwind';
import type { TEvalDataset } from '../types';

type TEvalDatasetRowProps = {
  dataset: TEvalDataset;
  busy: string | null;
  disabled: boolean;
  onCheck: (dataset: TEvalDataset) => void;
  onDownload: (dataset: TEvalDataset) => void;
  onSaveRun: (dataset: TEvalDataset) => void;
};

const formatDate = (iso: string) => (iso ? `${iso.slice(0, 10)} ${iso.slice(11, 16)}` : '—');

const EvalDatasetRow = ({
  dataset,
  busy,
  disabled,
  onCheck,
  onDownload,
  onSaveRun,
}: TEvalDatasetRowProps) => (
  <div className="flex items-center gap-3 rounded-md border border-solid border-tertiary-border px-3 py-2">
    <div className="flex-1 min-w-0">
      <Typography variant="body" size="2" className="text-primary-text font-medium truncate">
        {dataset.name} v{dataset.version}
      </Typography>
      <Typography variant="body" size="1" className="text-tertiary-text">
        {dataset.source_type} · {dataset.row_count} rows · {dataset.status}
      </Typography>
    </div>
    <Typography variant="body" size="1" className="text-tertiary-text whitespace-nowrap">
      {formatDate(dataset.created_at)}
    </Typography>
    <Button variant="primary" size="s" disabled={disabled} onClick={() => onDownload(dataset)}>
      {busy === `csv-${dataset.id}` ? 'Preparing…' : 'Download CSV'}
    </Button>
    <Button variant="outline" size="s" disabled={disabled} onClick={() => onCheck(dataset)}>
      {busy === `check-${dataset.id}` ? 'Checking…' : 'Check'}
    </Button>
    <Button variant="outline" size="s" disabled={disabled} onClick={() => onSaveRun(dataset)}>
      {busy === `run-${dataset.id}` ? 'Saving…' : 'Save eval run'}
    </Button>
  </div>
);

export default EvalDatasetRow;
