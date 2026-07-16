import { Button, Typography } from '@meesho/merlin-ui-tailwind';

type TEvalDatasetActionsProps = {
  mode: 'csv' | 'prism';
  canWrite: boolean;
  busy: string | null;
  readyForUpload: boolean;
  readyForPrism: boolean;
  onUpload: () => void;
  onRegisterPrism: () => void;
};

const EvalDatasetActions = ({
  mode,
  canWrite,
  busy,
  readyForUpload,
  readyForPrism,
  onUpload,
  onRegisterPrism,
}: TEvalDatasetActionsProps) => (
  <div className="flex items-center gap-2">
    {mode === 'csv' ? (
      <Button variant="primary" size="s" disabled={!readyForUpload || busy !== null} onClick={onUpload}>
        {busy === 'upload' ? 'Uploading…' : 'Upload dataset'}
      </Button>
    ) : (
      <Button variant="primary" size="s" disabled={!readyForPrism || busy !== null} onClick={onRegisterPrism}>
        {busy === 'prism' ? 'Registering…' : 'Register source'}
      </Button>
    )}
    {!canWrite && (
      <Typography variant="body" size="1" className="text-tertiary-text">
        Read-only
      </Typography>
    )}
  </div>
);

export default EvalDatasetActions;
