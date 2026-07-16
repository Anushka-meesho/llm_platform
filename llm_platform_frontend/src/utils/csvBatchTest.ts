import type { TPredictResult, TTask } from '../types';

export type TCsvBatchField = {
  name: string;
  required: boolean;
  schema: Record<string, unknown>;
};

export type TCsvRecord = {
  rowNo: number;
  values: Record<string, string>;
};

export type TCsvBatchResultRow = {
  rowNo: number;
  inputs: Record<string, unknown>;
  result: TPredictResult | null;
  error: string;
};

export function csvBatchFields(task: TTask): TCsvBatchField[] {
  const props =
    (task.input_schema as { properties?: Record<string, Record<string, unknown>> })?.properties ??
    {};
  const required = new Set<string>(
    (task.input_schema as { required?: string[] })?.required ?? [],
  );
  return Object.entries(props).map(([name, schema]) => ({
    name,
    required: required.has(name),
    schema,
  }));
}

export function parseCSV(text: string): { headers: string[]; records: TCsvRecord[] } {
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = '';
  let quoted = false;

  for (let i = 0; i < text.length; i += 1) {
    const ch = text[i];
    const next = text[i + 1];
    if (ch === '"' && quoted && next === '"') {
      cell += ch;
      i += 1;
    } else if (ch === '"') {
      quoted = !quoted;
    } else if (ch === ',' && !quoted) {
      row.push(cell);
      cell = '';
    } else if ((ch === '\n' || ch === '\r') && !quoted) {
      if (ch === '\r' && next === '\n') i += 1;
      row.push(cell);
      if (row.some((value) => value.trim() !== '')) rows.push(row);
      row = [];
      cell = '';
    } else {
      cell += ch;
    }
  }
  row.push(cell);
  if (row.some((value) => value.trim() !== '')) rows.push(row);

  const headers = (rows[0] ?? []).map((value) => value.trim());
  const records = rows.slice(1).map((values, index) => ({
    rowNo: index + 2,
    values: Object.fromEntries(headers.map((header, i) => [header, values[i] ?? ''])),
  }));
  return { headers, records };
}

export function defaultCsvMapping(fields: TCsvBatchField[], headers: string[]) {
  const headerSet = new Set(headers);
  return Object.fromEntries(
    fields.map((field) => [field.name, headerSet.has(field.name) ? field.name : '']),
  );
}

export function missingRequiredColumns(fields: TCsvBatchField[], mapping: Record<string, string>) {
  return fields
    .filter((field) => field.required && !mapping[field.name])
    .map((field) => field.name)
    .sort();
}

export function buildInputs(
  record: TCsvRecord,
  fields: TCsvBatchField[],
  mapping: Record<string, string>,
) {
  const inputs: Record<string, unknown> = {};
  for (const field of fields) {
    const column = mapping[field.name];
    if (!column) continue;
    const raw = record.values[column] ?? '';
    if (raw === '') continue;
    inputs[field.name] = coerceCSVValue(raw, field.schema);
  }
  return inputs;
}

export function exportBatchResultsCSV(rows: TCsvBatchResultRow[]) {
  return toCSV([
    [
      'row_no',
      'input_json',
      'output_json',
      'raw_response',
      'success',
      'error',
      'model',
      'prompt_version',
      'latency_ms',
      'cost_usd',
    ],
    ...rows.map((row) => [
      String(row.rowNo),
      JSON.stringify(row.inputs),
      JSON.stringify(row.result?.output ?? null),
      row.result?.raw_response ?? '',
      String(Boolean(row.result && !row.result.error)),
      row.error || row.result?.error || '',
      row.result?.model ?? '',
      row.result?.prompt_version ? String(row.result.prompt_version) : '',
      row.result?.latency_ms ? String(row.result.latency_ms) : '',
      row.result?.usage?.cost_usd !== undefined ? String(row.result.usage.cost_usd) : '',
    ]),
  ]);
}

function coerceCSVValue(raw: string, schema: Record<string, unknown>): unknown {
  switch (schema.type) {
    case 'number':
    case 'integer': {
      const n = Number(raw);
      return Number.isNaN(n) ? raw : n;
    }
    case 'boolean':
      return raw === 'true' ? true : raw === 'false' ? false : raw;
    case 'object':
    case 'array':
      try {
        return JSON.parse(raw);
      } catch {
        return raw;
      }
    default:
      return raw;
  }
}

function toCSV(rows: string[][]) {
  return rows
    .map((row) =>
      row
        .map((cell) => {
          if (!/[",\n\r]/.test(cell)) return cell;
          return `"${cell.replace(/"/g, '""')}"`;
        })
        .join(','),
    )
    .join('\n');
}
