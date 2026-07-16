import { Typography } from '@meesho/merlin-ui-tailwind';
import type { TEvalValidationError } from '../types';

type TEvalValidationErrorsProps = {
  errors: TEvalValidationError[];
};

const EvalValidationErrors = ({ errors }: TEvalValidationErrorsProps) => {
  if (errors.length === 0) return null;

  return (
    <div className="border border-solid border-error-border rounded-md p-3 bg-secondary-bg">
      <Typography variant="body" size="2" className="text-error-text font-medium">
        {errors.length} row issue{errors.length === 1 ? '' : 's'}
      </Typography>
      <div className="mt-2 flex flex-col gap-1 max-h-40 overflow-y-auto">
        {errors.slice(0, 20).map((err, i) => (
          <Typography key={`${err.row}-${err.field ?? 'row'}-${i}`} variant="body" size="1" className="text-primary-text">
            Row {err.row}
            {err.field ? ` · ${err.field}` : ''}: {err.message}
          </Typography>
        ))}
      </div>
    </div>
  );
};

export default EvalValidationErrors;
