// Round-trip conversion between a JSON Schema (the wire format the platform
// stores per task) and a flat list of fields the visual editor renders.
//
// The visual editor only covers the common shape — an object whose properties
// are typed scalars, arrays of scalars, or opaque nested objects. Anything
// richer (oneOf/anyOf/$ref, nested object properties, tuple items, numeric
// bounds, …) is fully valid and must not be silently dropped, so schemaToFields
// returns null for those and the editor falls back to raw-JSON mode. That keeps
// the simple path simple without ever losing a schema it can't draw.

export type FieldType = 'string' | 'number' | 'integer' | 'boolean' | 'array' | 'object';

export const FIELD_TYPES: FieldType[] = [
  'string',
  'number',
  'integer',
  'boolean',
  'array',
  'object',
];

export type SchemaField = {
  name: string;
  type: FieldType;
  required: boolean;
  description: string;
  enumValues: string[]; // string-type only: constrains to an allowed set
  itemType: FieldType; // array-type only: element type
};

export type JsonSchema = Record<string, unknown>;

export function emptyField(): SchemaField {
  return { name: '', type: 'string', required: false, description: '', enumValues: [], itemType: 'string' };
}

function isFieldType(v: unknown): v is FieldType {
  return typeof v === 'string' && (FIELD_TYPES as string[]).includes(v);
}

// Keywords the visual editor understands; anything else forces raw mode so we
// never represent (and then overwrite) a schema we didn't fully parse.
const TOP_KEYS = new Set(['type', 'properties', 'required', 'title', 'description', 'additionalProperties']);
const PROP_KEYS = new Set(['type', 'description', 'enum', 'items', 'title']);

/**
 * Convert a JSON Schema to editor fields, or null if it isn't representable as
 * a flat field list. An empty/absent schema converts to an empty field list.
 */
export function schemaToFields(schema: JsonSchema | undefined | null): SchemaField[] | null {
  if (!schema || Object.keys(schema).length === 0) return [];
  if (schema.type !== 'object') return null;
  if (Object.keys(schema).some((k) => !TOP_KEYS.has(k))) return null;

  const props = schema.properties;
  if (props !== undefined && (typeof props !== 'object' || props === null)) return null;

  const required = Array.isArray(schema.required) ? (schema.required as unknown[]).filter((x): x is string => typeof x === 'string') : [];

  const fields: SchemaField[] = [];
  for (const [name, rawProp] of Object.entries((props ?? {}) as Record<string, unknown>)) {
    if (typeof rawProp !== 'object' || rawProp === null) return null;
    const p = rawProp as JsonSchema;
    if (!isFieldType(p.type)) return null;
    if (Object.keys(p).some((k) => !PROP_KEYS.has(k))) return null;

    let enumValues: string[] = [];
    if (p.enum !== undefined) {
      if (p.type !== 'string' || !Array.isArray(p.enum) || !p.enum.every((v) => typeof v === 'string')) return null;
      enumValues = p.enum as string[];
    }

    let itemType: FieldType = 'string';
    if (p.type === 'array') {
      const items = p.items as JsonSchema | undefined;
      if (items !== undefined) {
        if (typeof items !== 'object' || items === null) return null;
        if (Object.keys(items).some((k) => k !== 'type') || !isFieldType(items.type)) return null;
        itemType = items.type;
      }
    } else if (p.items !== undefined) {
      return null;
    }

    fields.push({
      name,
      type: p.type,
      required: required.includes(name),
      description: typeof p.description === 'string' ? p.description : '',
      enumValues,
      itemType,
    });
  }
  return fields;
}

/** Build a JSON Schema from editor fields. Unnamed fields are skipped. */
export function fieldsToSchema(fields: SchemaField[]): JsonSchema {
  const properties: Record<string, JsonSchema> = {};
  const required: string[] = [];
  for (const f of fields) {
    const name = f.name.trim();
    if (!name) continue;
    const prop: JsonSchema = { type: f.type };
    if (f.description.trim()) prop.description = f.description.trim();
    if (f.type === 'string' && f.enumValues.length > 0) prop.enum = f.enumValues;
    if (f.type === 'array') prop.items = { type: f.itemType };
    properties[name] = prop;
    if (f.required) required.push(name);
  }
  const schema: JsonSchema = { type: 'object', properties };
  if (required.length > 0) schema.required = required;
  return schema;
}

/** Stable JSON stringify (sorted keys) for order-independent equality checks. */
export function stableStringify(v: unknown): string {
  if (v === null || typeof v !== 'object') return JSON.stringify(v);
  if (Array.isArray(v)) return `[${v.map(stableStringify).join(',')}]`;
  const obj = v as Record<string, unknown>;
  const keys = Object.keys(obj).sort();
  return `{${keys.map((k) => `${JSON.stringify(k)}:${stableStringify(obj[k])}`).join(',')}}`;
}

/** Duplicate or empty field names, for inline validation in the editor. */
export function fieldNameIssues(fields: SchemaField[]): { duplicates: string[]; hasEmpty: boolean } {
  const seen = new Map<string, number>();
  let hasEmpty = false;
  for (const f of fields) {
    const name = f.name.trim();
    if (!name) {
      hasEmpty = true;
      continue;
    }
    seen.set(name, (seen.get(name) ?? 0) + 1);
  }
  const duplicates = [...seen.entries()].filter(([, n]) => n > 1).map(([k]) => k);
  return { duplicates, hasEmpty };
}
