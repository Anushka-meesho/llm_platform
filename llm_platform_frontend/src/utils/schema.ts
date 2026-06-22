// Round-trip conversion between a JSON Schema (the wire format the platform
// stores per task) and a flat list of fields the visual editor renders.
//
// The visual editor only covers the common shape — an object whose properties
// are typed scalars, arrays of scalars, or opaque nested objects. Anything
// richer (oneOf/anyOf/$ref, nested object properties, tuple items, numeric
// bounds, …) is fully valid and must not be silently dropped, so schemaToFields
// returns null for those and the editor falls back to raw-JSON mode. That keeps
// the simple path simple without ever losing a schema it can't draw.

// 'image' is an editor-only pseudo-type — not a real JSON Schema type. An image
// field is always an array of image strings (a single image is just a one-element
// array), tagged with format:"image" on the array's items so the backend and the
// test panel recognise it by shape rather than by a fixed property name — the
// author can name it anything. maxItems caps how many images are allowed. It is
// offered only where images make sense (task input schemas), gated by allowImages.
export type FieldType = 'string' | 'number' | 'integer' | 'boolean' | 'array' | 'object' | 'image';

// The JSON Schema marker that flags a string (an array's items) as an image.
export const IMAGE_FORMAT = 'image';

// The real JSON Schema scalar/compound types — used for the array element-type
// picker and the output schema, where 'image' has no meaning.
export const FIELD_TYPES: FieldType[] = [
  'string',
  'number',
  'integer',
  'boolean',
  'array',
  'object',
];

// Field types offered for task *input* schemas: the standard types plus the
// image pseudo-type.
export const INPUT_FIELD_TYPES: FieldType[] = [...FIELD_TYPES, 'image'];

export type SchemaField = {
  name: string;
  type: FieldType;
  required: boolean;
  description: string;
  enumValues: string[]; // string-type only: constrains to an allowed set
  itemType: FieldType; // array-type only: element type
  maxImages: number; // image-type only: max images allowed (1 = single image; 0 = no limit)
};

export type JsonSchema = Record<string, unknown>;

export function emptyField(): SchemaField {
  return { name: '', type: 'string', required: false, description: '', enumValues: [], itemType: 'string', maxImages: 1 };
}

function isFieldType(v: unknown): v is FieldType {
  return typeof v === 'string' && (FIELD_TYPES as string[]).includes(v);
}

// Keywords the visual editor understands; anything else forces raw mode so we
// never represent (and then overwrite) a schema we didn't fully parse.
const TOP_KEYS = new Set(['type', 'properties', 'required', 'title', 'description', 'additionalProperties']);
const PROP_KEYS = new Set(['type', 'description', 'enum', 'items', 'title', 'maxItems']);

/**
 * Convert a JSON Schema to editor fields, or null if it isn't representable as
 * a flat field list. An empty/absent schema converts to an empty field list.
 *
 * With opts.allowImages, an image field is recognised by its format:"image"
 * marker — an array whose items are format:"image" strings (its maxItems is the
 * image cap). The marker is name-agnostic, so the field keeps its author-chosen
 * name. format / maxItems are otherwise unrepresentable in the field editor, so
 * any other property carrying them falls back to raw-JSON mode (no silent drop).
 */
export function schemaToFields(
  schema: JsonSchema | undefined | null,
  opts: { allowImages?: boolean } = {},
): SchemaField[] | null {
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
    // 'format' is representable only as our image marker on a string (used on an
    // array's items); any other format keeps the schema in raw-JSON mode so it
    // is never dropped on save.
    const propImageString = !!opts.allowImages && p.type === 'string' && p.format === IMAGE_FORMAT;
    if (p.format !== undefined && !propImageString) return null;
    if (Object.keys(p).some((k) => k !== 'format' && !PROP_KEYS.has(k))) return null;

    const base = {
      name,
      required: required.includes(name),
      description: typeof p.description === 'string' ? p.description : '',
      enumValues: [] as string[],
      itemType: 'string' as FieldType,
      maxImages: 1,
    };

    let isImageArray = false;

    if (p.enum !== undefined) {
      if (p.type !== 'string' || !Array.isArray(p.enum) || !p.enum.every((v) => typeof v === 'string')) return null;
      base.enumValues = p.enum as string[];
    }

    if (p.type === 'array') {
      const items = p.items as JsonSchema | undefined;
      let itemImage = false;
      if (items !== undefined) {
        if (typeof items !== 'object' || items === null) return null;
        if (Object.keys(items).some((k) => k !== 'type' && k !== 'format')) return null;
        if (!isFieldType(items.type)) return null;
        base.itemType = items.type;
        itemImage = !!opts.allowImages && items.type === 'string' && items.format === IMAGE_FORMAT;
        // An items format we can't represent (anything but our image marker)
        // stays raw JSON rather than being silently dropped.
        if (items.format !== undefined && !itemImage) return null;
      }
      isImageArray = itemImage;
      // maxItems is representable only as the image cap; any other array
      // carrying it stays raw JSON so the cap isn't silently dropped.
      if (p.maxItems !== undefined) {
        if (!isImageArray || typeof p.maxItems !== 'number') return null;
        base.maxImages = p.maxItems;
      } else if (isImageArray) {
        base.maxImages = 0; // images with no cap → "no limit"
      }
    } else {
      if (p.items !== undefined) return null;
      if (p.maxItems !== undefined) return null;
    }

    fields.push({ ...base, type: isImageArray ? 'image' : (p.type as FieldType) });
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

    let prop: JsonSchema;
    if (f.type === 'image') {
      // Always an array of image strings — a single image is a one-element
      // array. The format marker is what the backend / test panel detect.
      prop = { type: 'array', items: { type: 'string', format: IMAGE_FORMAT } };
      if (f.maxImages > 0) prop.maxItems = f.maxImages; // 0 = no limit
    } else {
      prop = { type: f.type };
      if (f.type === 'string' && f.enumValues.length > 0) prop.enum = f.enumValues;
      if (f.type === 'array') prop.items = { type: f.itemType };
    }
    if (f.description.trim()) prop.description = f.description.trim();
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
