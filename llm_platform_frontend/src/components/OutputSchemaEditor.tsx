import { useState, type ReactNode } from 'react';
import { Typography, cn } from '@meesho/merlin-ui-tailwind';
import SchemaEditor, { type SchemaEditorState } from './SchemaEditor';
import type { JsonSchema } from '../utils/schema';

// OutputSchemaEditor authors a task's *response contract* — what the predict API
// returns to the caller. The top "Response type" selector picks the shape the
// client receives: a JSON object (the full field editor), or a scalar/array
// (string, number, integer, boolean, array) with optional constraints. The
// backend coerces the model's plain-text answer into this type and validates the
// constraints (see tasks.ValidateOutput), so this is the contract, not a model
// instruction. It reports {schema, valid} up exactly like SchemaEditor.

type ResponseType = 'object' | 'string' | 'number' | 'integer' | 'boolean' | 'array';
type ScalarItemType = 'string' | 'number' | 'integer' | 'boolean';

const RESPONSE_TYPES: ResponseType[] = ['object', 'string', 'number', 'integer', 'boolean', 'array'];
const ITEM_TYPES: ScalarItemType[] = ['string', 'number', 'integer', 'boolean'];

const TYPE_LABEL: Record<ResponseType, string> = {
  object: 'JSON object',
  string: 'String',
  number: 'Number',
  integer: 'Integer',
  boolean: 'Boolean',
  array: 'Array',
};

const inputCls =
  'border border-solid border-primary-border rounded-md px-2 py-1.5 text-sm bg-primary-bg text-primary-text';

// Constraints are held as strings (raw input) and only interpreted on build.
type Constraints = {
  enumText: string; // comma-separated allowed values (strings, or numbers for number/integer)
  pattern: string; // string only: regex the value must match
  min: string; // minLength (string) / minimum (number) / minItems (array)
  max: string; // maxLength (string) / maximum (number) / maxItems (array)
  itemType: ScalarItemType; // array only: element type
};

const emptyConstraints = (): Constraints => ({
  enumText: '',
  pattern: '',
  min: '',
  max: '',
  itemType: 'string',
});

function detectType(s: JsonSchema | undefined | null): ResponseType {
  if (!s || Object.keys(s).length === 0) return 'object';
  const t = s.type;
  if (t === 'string' || t === 'number' || t === 'integer' || t === 'boolean' || t === 'array') return t;
  return 'object';
}

// An object-shaped schema to hand the field editor — the original when it is
// already an object, otherwise a blank object so switching to "JSON object"
// starts clean instead of showing a scalar schema as raw JSON.
function objectInitial(s: JsonSchema | undefined | null): JsonSchema {
  return detectType(s) === 'object' && s ? s : { type: 'object', properties: {} };
}

function initConstraints(s: JsonSchema | undefined | null): Constraints {
  const c = emptyConstraints();
  if (!s) return c;
  if (typeof s.pattern === 'string') c.pattern = s.pattern;
  if (Array.isArray(s.enum)) c.enumText = (s.enum as unknown[]).map(String).join(', ');
  const t = s.type;
  if (t === 'string') {
    if (typeof s.minLength === 'number') c.min = String(s.minLength);
    if (typeof s.maxLength === 'number') c.max = String(s.maxLength);
  } else if (t === 'number' || t === 'integer') {
    if (typeof s.minimum === 'number') c.min = String(s.minimum);
    if (typeof s.maximum === 'number') c.max = String(s.maximum);
  } else if (t === 'array') {
    if (typeof s.minItems === 'number') c.min = String(s.minItems);
    if (typeof s.maxItems === 'number') c.max = String(s.maxItems);
    const items = s.items as JsonSchema | undefined;
    const it = items?.type;
    if (it === 'string' || it === 'number' || it === 'integer' || it === 'boolean') c.itemType = it;
    if (items && Array.isArray(items.enum)) c.enumText = (items.enum as unknown[]).map(String).join(', ');
  }
  return c;
}

const splitEnum = (text: string) =>
  text
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);

const isNonNegInt = (n: number | null) => n === null || (Number.isInteger(n) && n >= 0);
const numOrNull = (s: string) => (s.trim() === '' ? null : Number(s));

// buildScalar turns the response type + constraints into a JSON Schema and a
// validity verdict (with a message for the first problem found).
function buildScalar(
  type: Exclude<ResponseType, 'object'>,
  c: Constraints,
): { schema: JsonSchema; valid: boolean; error?: string } {
  const enums = splitEnum(c.enumText);
  const min = numOrNull(c.min);
  const max = numOrNull(c.max);
  const rangeBad = min !== null && max !== null && min > max;

  if (type === 'boolean') return { schema: { type: 'boolean' }, valid: true };

  if (type === 'string') {
    const schema: JsonSchema = { type: 'string' };
    if (enums.length) schema.enum = enums;
    let patternBad = false;
    if (c.pattern.trim()) {
      schema.pattern = c.pattern.trim();
      try {
        new RegExp(c.pattern.trim());
      } catch {
        patternBad = true;
      }
    }
    if (min !== null) schema.minLength = min;
    if (max !== null) schema.maxLength = max;
    const lenBad = !isNonNegInt(min) || !isNonNegInt(max);
    return {
      schema,
      valid: !patternBad && !rangeBad && !lenBad,
      error: patternBad
        ? 'Invalid regular expression.'
        : lenBad
          ? 'Length bounds must be non-negative whole numbers.'
          : rangeBad
            ? 'Min length exceeds max length.'
            : undefined,
    };
  }

  if (type === 'number' || type === 'integer') {
    const schema: JsonSchema = { type };
    if (min !== null) schema.minimum = min;
    if (max !== null) schema.maximum = max;
    const numEnums = enums.map(Number);
    const enumBad = enums.length > 0 && numEnums.some((n) => !Number.isFinite(n));
    if (enums.length && !enumBad) schema.enum = type === 'integer' ? numEnums.map((n) => Math.trunc(n)) : numEnums;
    const boundsBad = (min !== null && !Number.isFinite(min)) || (max !== null && !Number.isFinite(max));
    return {
      schema,
      valid: !rangeBad && !enumBad && !boundsBad,
      error: boundsBad
        ? 'Bounds must be numbers.'
        : enumBad
          ? 'Allowed values must be numbers.'
          : rangeBad
            ? 'Minimum exceeds maximum.'
            : undefined,
    };
  }

  // array
  const item: JsonSchema = { type: c.itemType };
  if (c.itemType === 'string' && enums.length) item.enum = enums;
  const schema: JsonSchema = { type: 'array', items: item };
  if (min !== null) schema.minItems = min;
  if (max !== null) schema.maxItems = max;
  const itemsBad = !isNonNegInt(min) || !isNonNegInt(max);
  return {
    schema,
    valid: !rangeBad && !itemsBad,
    error: itemsBad
      ? 'Item counts must be non-negative whole numbers.'
      : rangeBad
        ? 'Min items exceeds max items.'
        : undefined,
  };
}

const OutputSchemaEditor = ({
  initial,
  onChange,
  readOnly = false,
}: {
  initial: JsonSchema | undefined;
  onChange: (s: SchemaEditorState) => void;
  readOnly?: boolean;
}) => {
  const [type, setType] = useState<ResponseType>(() => detectType(initial));
  const [c, setC] = useState<Constraints>(() => initConstraints(initial));

  const emitScalar = (t: Exclude<ResponseType, 'object'>, next: Constraints) => {
    const { schema, valid } = buildScalar(t, next);
    onChange({ schema, valid });
  };

  const onTypeChange = (next: ResponseType) => {
    setType(next);
    if (next === 'object') {
      // Hand control to the field editor; seed the parent with a blank object.
      onChange({ schema: { type: 'object', properties: {} }, valid: true });
    } else {
      emitScalar(next, c);
    }
  };

  const update = (patch: Partial<Constraints>) => {
    const next = { ...c, ...patch };
    setC(next);
    if (type !== 'object') emitScalar(type, next);
  };

  const scalar = type === 'object' ? null : buildScalar(type, c);
  const lenLabel = type === 'array' ? ['Min items', 'Max items'] : type === 'string' ? ['Min length', 'Max length'] : ['Minimum', 'Maximum'];

  return (
    <div className="flex flex-col gap-3">
      <label className="block">
        <Typography variant="body" size="1" className="text-tertiary-text mb-1">
          Response type — what the API returns to the caller
        </Typography>
        <select
          className={cn(inputCls, 'w-full')}
          value={type}
          disabled={readOnly}
          onChange={(e) => onTypeChange(e.target.value as ResponseType)}
        >
          {RESPONSE_TYPES.map((t) => (
            <option key={t} value={t}>
              {TYPE_LABEL[t]}
            </option>
          ))}
        </select>
      </label>

      {type === 'object' ? (
        <SchemaEditor initial={objectInitial(initial)} readOnly={readOnly} onChange={onChange} />
      ) : type === 'boolean' ? (
        <Typography variant="body" size="1" className="text-tertiary-text">
          The response is coerced to <code>true</code>/<code>false</code>. No further constraints.
        </Typography>
      ) : (
        <div className="flex flex-col gap-3 border border-solid border-tertiary-border rounded-md p-3">
          {type === 'array' && (
            <Row label="Element type">
              <select
                className={inputCls}
                value={c.itemType}
                disabled={readOnly}
                onChange={(e) => update({ itemType: e.target.value as ScalarItemType })}
              >
                {ITEM_TYPES.map((t) => (
                  <option key={t} value={t}>
                    of {t}
                  </option>
                ))}
              </select>
            </Row>
          )}

          <Row label={`${lenLabel[0]} / ${lenLabel[1]} (optional)`}>
            <input
              className={cn(inputCls, 'w-28')}
              type="number"
              placeholder="min"
              value={c.min}
              disabled={readOnly}
              onChange={(e) => update({ min: e.target.value })}
            />
            <input
              className={cn(inputCls, 'w-28')}
              type="number"
              placeholder="max"
              value={c.max}
              disabled={readOnly}
              onChange={(e) => update({ max: e.target.value })}
            />
          </Row>

          {type === 'string' && (
            <Row label="Pattern — regex the value must match (optional)">
              <input
                className={cn(inputCls, 'w-full font-mono')}
                placeholder="e.g. ^[A-Z]{2,5}$"
                value={c.pattern}
                disabled={readOnly}
                onChange={(e) => update({ pattern: e.target.value })}
              />
            </Row>
          )}

          {(type === 'string' || type === 'number' || type === 'integer' || (type === 'array' && c.itemType === 'string')) && (
            <Row label="Allowed values — comma-separated enum (optional)">
              <input
                className={cn(inputCls, 'w-full')}
                placeholder={type === 'string' || c.itemType === 'string' ? 'positive, negative, neutral' : '1, 2, 3'}
                value={c.enumText}
                disabled={readOnly}
                onChange={(e) => update({ enumText: e.target.value })}
              />
            </Row>
          )}

          {scalar && !scalar.valid && scalar.error && (
            <Typography variant="body" size="1" className="text-error-text">
              {scalar.error}
            </Typography>
          )}
        </div>
      )}
    </div>
  );
};

const Row = ({ label, children }: { label: string; children: ReactNode }) => (
  <label className="block">
    <Typography variant="body" size="1" className="text-tertiary-text mb-1">
      {label}
    </Typography>
    <div className="flex items-center gap-2 flex-wrap">{children}</div>
  </label>
);

export default OutputSchemaEditor;
