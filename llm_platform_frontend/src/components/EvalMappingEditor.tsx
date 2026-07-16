import { Typography } from '@meesho/merlin-ui-tailwind';

type TEvalMappingEditorProps = {
  title: string;
  fields: string[];
  mapping: Record<string, string>;
  disabled: boolean;
  onChange: (field: string, value: string) => void;
};

const INPUT_CLS =
  'w-full border border-solid border-primary-border rounded-md px-2 py-1.5 text-sm bg-primary-bg text-primary-text';

const EvalMappingEditor = ({
  title,
  fields,
  mapping,
  disabled,
  onChange,
}: TEvalMappingEditorProps) => (
  <div className="rounded-md border border-solid border-tertiary-border p-3">
    <Typography variant="body" size="2" className="text-primary-text font-medium mb-2">
      {title}
    </Typography>
    {fields.length === 0 ? (
      <Typography variant="body" size="1" className="text-tertiary-text">
        No schema fields.
      </Typography>
    ) : (
      <div className="flex flex-col gap-2">
        {fields.map((field) => (
          <label key={field} className="grid grid-cols-2 gap-2 items-center">
            <Typography variant="body" size="1" className="text-tertiary-text truncate">
              {field}
            </Typography>
            <input
              className={INPUT_CLS}
              value={mapping[field] ?? ''}
              disabled={disabled}
              onChange={(e) => onChange(field, e.target.value)}
            />
          </label>
        ))}
      </div>
    )}
  </div>
);

export default EvalMappingEditor;
