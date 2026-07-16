import { Typography, cn } from '@meesho/merlin-ui-tailwind';

type TEvalRunControlsProps = {
  version: string;
  maxItems: string;
  onVersionChange: (value: string) => void;
  onMaxItemsChange: (value: string) => void;
};

const INPUT_CLS =
  'w-full border border-solid border-primary-border rounded-md px-2 py-1.5 text-sm bg-primary-bg text-primary-text';

const EvalRunControls = ({
  version,
  maxItems,
  onVersionChange,
  onMaxItemsChange,
}: TEvalRunControlsProps) => (
  <div className="flex items-center gap-3 flex-wrap mb-2">
    <label className="flex items-center gap-2">
      <Typography variant="body" size="1" className="text-tertiary-text">
        Prompt version
      </Typography>
      <input className={cn(INPUT_CLS, 'w-24')} value={version} onChange={(e) => onVersionChange(e.target.value)} />
    </label>
    <label className="flex items-center gap-2">
      <Typography variant="body" size="1" className="text-tertiary-text">
        Rows to run/export
      </Typography>
      <input className={cn(INPUT_CLS, 'w-24')} value={maxItems} onChange={(e) => onMaxItemsChange(e.target.value)} />
    </label>
  </div>
);

export default EvalRunControls;
