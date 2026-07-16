export function schemaFieldNames(schema?: Record<string, unknown>): string[] {
  const properties = schema?.properties;
  if (!properties || typeof properties !== 'object' || Array.isArray(properties)) return [];
  return Object.keys(properties).sort();
}

export function schemaRequiredFieldNames(schema?: Record<string, unknown>): string[] {
  return Array.isArray(schema?.required)
    ? schema.required.filter((field): field is string => typeof field === 'string')
    : [];
}

export function defaultInputMapping(fields: string[]): Record<string, string> {
  return Object.fromEntries(fields.map((field) => [field, field]));
}

export function defaultOutputMapping(fields: string[]): Record<string, string> {
  return Object.fromEntries(fields.map((field) => [field, `expected_${field}`]));
}

export function parseCSVHeader(text: string): string[] {
  const firstLine = text.split(/\r?\n/, 1)[0] ?? '';
  const values: string[] = [];
  let current = '';
  let quoted = false;
  for (let i = 0; i < firstLine.length; i += 1) {
    const ch = firstLine[i];
    const next = firstLine[i + 1];
    if (ch === '"' && quoted && next === '"') {
      current += ch;
      i += 1;
    } else if (ch === '"') {
      quoted = !quoted;
    } else if (ch === ',' && !quoted) {
      values.push(current.trim());
      current = '';
    } else {
      current += ch;
    }
  }
  values.push(current.trim());
  return values.filter(Boolean);
}

export function missingMappedColumns(
  mapping: Record<string, string>,
  headers: string[],
  fieldsToCheck?: string[],
): string[] {
  const headerSet = new Set(headers);
  const allowedFields = fieldsToCheck ? new Set(fieldsToCheck) : null;
  return Object.entries(mapping)
    .filter(([field, column]) => column && (!allowedFields || allowedFields.has(field)) && !headerSet.has(column))
    .map(([, column]) => column)
    .sort();
}

export function mappingWithExistingColumns(
  mapping: Record<string, string>,
  headers: string[] | null,
  requiredFields: string[],
): Record<string, string> {
  if (!headers) return mapping;
  const headerSet = new Set(headers);
  const requiredSet = new Set(requiredFields);
  return Object.fromEntries(
    Object.entries(mapping).filter(([field, column]) => column && (requiredSet.has(field) || headerSet.has(column))),
  );
}
