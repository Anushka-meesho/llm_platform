import { Spinner, Typography } from '@meesho/merlin-ui-tailwind';
import EvalDatasetRow from './EvalDatasetRow';
import type { TEvalDataset } from '../types';

type TEvalDatasetListProps = {
  datasets: TEvalDataset[];
  loading: boolean;
  error: string | null;
  canWrite: boolean;
  busy: string | null;
  onCheck: (dataset: TEvalDataset) => void;
  onDownload: (dataset: TEvalDataset) => void;
  onSaveRun: (dataset: TEvalDataset) => void;
};

const EvalDatasetList = ({
  datasets,
  loading,
  error,
  canWrite,
  busy,
  onCheck,
  onDownload,
  onSaveRun,
}: TEvalDatasetListProps) => {
  if (loading) return <Spinner />;

  if (error) {
    return (
      <Typography variant="body" size="2" className="text-error-text">
        {error}
      </Typography>
    );
  }

  if (datasets.length === 0) {
    return (
      <Typography variant="body" size="2" className="text-tertiary-text">
        No eval datasets yet. Upload a CSV/XLSX file above, then Download CSV to run the prompt and export row-level outputs.
      </Typography>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      {datasets.map((dataset) => (
        <EvalDatasetRow
          key={dataset.id}
          dataset={dataset}
          busy={busy}
          disabled={!canWrite || busy !== null || dataset.status !== 'ready'}
          onCheck={onCheck}
          onDownload={onDownload}
          onSaveRun={onSaveRun}
        />
      ))}
    </div>
  );
};

export default EvalDatasetList;
