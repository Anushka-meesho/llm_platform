import { useState, type ReactNode } from 'react';
import { Button, TextArea, Typography, cn } from '@meesho/merlin-ui-tailwind';
import {
  FIELD_TYPES,
  INPUT_FIELD_TYPES,
  emptyField,
  fieldNameIssues,
  fieldsToSchema,
  schemaToFields,
  type FieldType,
  type JsonSchema,
  type SchemaField,
} from '../utils/schema';

// What the editor reports up on every change: the current schema plus whether
// it is well-formed (raw-JSON mode can be mid-edit and invalid).
export type SchemaEditorState = { schema: JsonSchema; valid: boolean };

type Mode = 'fields' | 'json';

const selectCls =
  'border border-solid border-primary-border rounded-md px-2 py-1.5 text-sm bg-primary-bg text-primary-text';

// SchemaEditor edits one JSON Schema. It is intentionally uncontrolled w.r.t.
// `initial` — mount it with a key (e.g. the task id + which schema) so a fresh
// instance is created when the underlying task changes, and read updates via
// onChange. This avoids fighting the user's keystrokes by re-deriving state on
// every render.
const SchemaEditor = ({
  initial,
  onChange,
  readOnly = false,
  allowImages = false,
}: {
  initial: JsonSchema | undefined;
  onChange: (s: SchemaEditorState) => void;
  readOnly?: boolean;
  // Offer the image pseudo-type (task input schemas only). Off for outputs.
  allowImages?: boolean;
}) => {
  const initialFields = schemaToFields(initial, { allowImages });
  const representable = initialFields !== null;

  // Standard types, plus the image option only where images make sense.
  const typeOptions = allowImages ? INPUT_FIELD_TYPES : FIELD_TYPES;

  const [mode, setMode] = useState<Mode>(representable ? 'fields' : 'json');
  const [fields, setFields] = useState<SchemaField[]>(initialFields ?? []);
  const [rawText, setRawText] = useState(() => JSON.stringify(initial ?? { type: 'object', properties: {} }, null, 2));
  const [rawError, setRawError] = useState<string | null>(null);

  const { duplicates, hasEmpty } = fieldNameIssues(fields);

  // Propagate field edits upward and keep them as the single source of truth.
  const commitFields = (next: SchemaField[]) => {
    setFields(next);
    onChange({ schema: fieldsToSchema(next), valid: duplicatesIn(next).length === 0 });
  };

  const updateField = (i: number, patch: Partial<SchemaField>) => {
    commitFields(fields.map((f, idx) => (idx === i ? { ...f, ...patch } : f)));
  };
  const addField = () => commitFields([...fields, emptyField()]);
  const removeField = (i: number) => commitFields(fields.filter((_, idx) => idx !== i));

  const onRawChange = (text: string) => {
    setRawText(text);
    try {
      const parsed = JSON.parse(text);
      if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
        throw new Error('schema must be a JSON object');
      }
      setRawError(null);
      onChange({ schema: parsed as JsonSchema, valid: true });
    } catch (e) {
      setRawError(e instanceof Error ? e.message : 'invalid JSON');
      onChange({ schema: {}, valid: false });
    }
  };

  const switchTo = (next: Mode) => {
    if (next === mode) return;
    if (next === 'json') {
      // Visual → JSON: serialise the current fields so the two views agree.
      setRawText(JSON.stringify(fieldsToSchema(fields), null, 2));
      setRawError(null);
      setMode('json');
      return;
    }
    // JSON → Visual: only possible if the JSON maps cleanly onto fields.
    try {
      const parsed = JSON.parse(rawText) as JsonSchema;
      const asFields = schemaToFields(parsed, { allowImages });
      if (asFields === null) {
        setRawError('This schema uses advanced features — keep editing it as JSON.');
        return;
      }
      setFields(asFields);
      setRawError(null);
      setMode('fields');
      onChange({ schema: fieldsToSchema(asFields), valid: true });
    } catch {
      setRawError('Fix the JSON before switching to the field editor.');
    }
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-1">
        <ModeTab active={mode === 'fields'} onClick={() => switchTo('fields')}>
          Fields
        </ModeTab>
        <ModeTab active={mode === 'json'} onClick={() => switchTo('json')}>
          JSON
        </ModeTab>
        {!representable && mode === 'fields' && (
          <Typography variant="body" size="1" className="text-tertiary-text ml-2">
            advanced schema — edit as JSON
          </Typography>
        )}
      </div>

      {mode === 'fields' ? (
        <div className="flex flex-col gap-2">
          {fields.length === 0 && (
            <Typography variant="body" size="1" className="text-tertiary-text">
              No fields yet — add one, or switch to JSON. An empty schema accepts any object.
            </Typography>
          )}
          {fields.map((f, i) => (
            <FieldRow
              key={i}
              field={f}
              readOnly={readOnly}
              typeOptions={typeOptions}
              duplicate={duplicates.includes(f.name.trim()) && f.name.trim() !== ''}
              onChange={(patch) => updateField(i, patch)}
              onRemove={() => removeField(i)}
            />
          ))}
          {!readOnly && (
            <div>
              <Button variant="outline" size="s" onClick={addField}>
                + Add field
              </Button>
            </div>
          )}
          {(duplicates.length > 0 || hasEmpty) && (
            <Typography variant="body" size="1" className="text-error-text">
              {duplicates.length > 0 && `Duplicate field name: ${duplicates.join(', ')}. `}
              {hasEmpty && 'Unnamed fields are ignored.'}
            </Typography>
          )}
        </div>
      ) : (
        <div className="flex flex-col gap-1">
          <TextArea value={rawText} onChange={({ value }) => onRawChange(value)} rows={10} disabled={readOnly} />
          {rawError && (
            <Typography variant="body" size="1" className="text-error-text">
              {rawError}
            </Typography>
          )}
        </div>
      )}
    </div>
  );
};

function duplicatesIn(fields: SchemaField[]): string[] {
  return fieldNameIssues(fields).duplicates;
}

const FieldRow = ({
  field,
  readOnly,
  typeOptions,
  duplicate,
  onChange,
  onRemove,
}: {
  field: SchemaField;
  readOnly: boolean;
  typeOptions: FieldType[];
  duplicate: boolean;
  onChange: (patch: Partial<SchemaField>) => void;
  onRemove: () => void;
}) => {
  const isImage = field.type === 'image';
  return (
    <div className="border border-solid border-tertiary-border rounded-md p-2 flex flex-col gap-2">
      <div className="flex items-center gap-2 flex-wrap">
        <input
          className={cn(selectCls, 'flex-1 min-w-[8rem]', duplicate && 'border-error-text')}
          placeholder="field name"
          value={field.name}
          disabled={readOnly}
          onChange={(e) => onChange({ name: e.target.value })}
        />
        <select
          className={selectCls}
          value={field.type}
          disabled={readOnly}
          onChange={(e) => onChange({ type: e.target.value as FieldType })}
        >
          {typeOptions.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
        {field.type === 'array' && (
          <select
            className={selectCls}
            value={field.itemType}
            disabled={readOnly}
            onChange={(e) => onChange({ itemType: e.target.value as FieldType })}
            title="array element type"
          >
            {FIELD_TYPES.filter((t) => t !== 'array').map((t) => (
              <option key={t} value={t}>
                of {t}
              </option>
            ))}
          </select>
        )}
        {isImage && (
          // Max images this field accepts. 1 = a single image (the `image`
          // string); ≥2 caps the `images` array (maxItems); blank/0 = no limit.
          <label className="flex items-center gap-1 text-sm text-secondary-text select-none" title="Maximum number of images (1 = single image; blank/0 = no limit)">
            max
            <input
              className={cn(selectCls, 'w-16')}
              type="number"
              min={0}
              step={1}
              value={field.maxImages || ''}
              placeholder="∞"
              disabled={readOnly}
              onChange={(e) => {
                const n = parseInt(e.target.value, 10);
                onChange({ maxImages: Number.isNaN(n) || n < 0 ? 0 : n });
              }}
            />
            images
          </label>
        )}
        <label className="flex items-center gap-1 text-sm text-secondary-text select-none">
          <input
            type="checkbox"
            checked={field.required}
            disabled={readOnly}
            onChange={(e) => onChange({ required: e.target.checked })}
          />
          required
        </label>
        {!readOnly && (
          <Button variant="ghost" size="s" onClick={onRemove} title="remove field">
            ✕
          </Button>
        )}
      </div>
      <input
        className={cn(selectCls, 'w-full')}
        placeholder="description (optional)"
        value={field.description}
        disabled={readOnly}
        onChange={(e) => onChange({ description: e.target.value })}
      />
      {field.type === 'string' && (
        <input
          className={cn(selectCls, 'w-full')}
          placeholder="allowed values, comma-separated (optional enum)"
          value={field.enumValues.join(', ')}
          disabled={readOnly}
          onChange={(e) =>
            onChange({
              enumValues: e.target.value
                .split(',')
                .map((s) => s.trim())
                .filter(Boolean),
            })
          }
        />
      )}
    </div>
  );
};

const ModeTab = ({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) => (
  <button
    type="button"
    onClick={onClick}
    className={cn(
      'px-2.5 py-1 text-xs rounded-md border border-solid',
      active
        ? 'border-primary-border bg-secondary-bg text-primary-text font-medium'
        : 'border-transparent text-tertiary-text hover:text-primary-text',
    )}
  >
    {children}
  </button>
);

export default SchemaEditor;
