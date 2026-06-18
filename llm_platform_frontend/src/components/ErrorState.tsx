import { Button, Typography } from '@meesho/merlin-ui-tailwind';

// ErrorState is the standard inline block for a failed data load: it shows the
// message (already including a request_id ref when produced by errorMessage) and
// an optional Retry. Use it where a page/section can't render its data.
interface Props {
  message: string;
  onRetry?: () => void;
  compact?: boolean;
}

function ErrorState({ message, onRetry, compact }: Props) {
  return (
    <div
      className={`flex flex-col items-center justify-center gap-3 text-center ${
        compact ? 'py-6' : 'py-12'
      }`}
    >
      <div className="max-w-md rounded-lg border border-solid border-error-border bg-error-bg px-4 py-3">
        <Typography variant="body" size="3" className="text-error-text">
          {message}
        </Typography>
      </div>
      {onRetry && (
        <Button variant="outline" size="s" onClick={onRetry}>
          Retry
        </Button>
      )}
    </div>
  );
}

export default ErrorState;
