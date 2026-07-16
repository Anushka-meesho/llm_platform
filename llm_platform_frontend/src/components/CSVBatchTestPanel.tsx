import { useEffect, useMemo, useState } from 'react';
import { Button, Typography } from '@meesho/merlin-ui-tailwind';
import { api, errorMessage } from '../api/client';
import {
  buildInputs,
  csvBatchFields,
  defaultCsvMapping,
  exportBatchResultsCSV,
  missingRequiredColumns,
  parseCSV,
  type TCsvBatchResultRow,
  type TCsvRecord,
} from '../utils/csvBatchTest';
import type { TTask } from '../types';

type TCSVBatchTestPanelProps = {
  task: TTask;
};

const INPUT_CLS =
  'w-full border border-solid border-primary-border rounded-md px-2 py-1.5 text-sm bg-primary-bg text-primary-text';

const CSVBatchTestPanel = ({ task }: TCSVBatchTestPanelProps) => {
  const fields = useMemo(() => csvBatchFields(task), [task]);
  const [file, setFile] = useState<File | null>(null);
  const [headers, setHeaders] = useState<string[]>([]);
  const [records, setRecords] = useState<TCsvRecord[]>([]);
  const [mapping, setMapping] = useState<Record<string, string>>({});
  const [maxRows, setMaxRows] = useState('10');
  const [running, setRunning] = useState(false);
  const [progress, setProgress] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [results, setResults] = useState<TCsvBatchResultRow[]>([]);

  useEffect(() => {
    let isCurrent = true;
    void Promise.resolve().then(async () => {
      setHeaders([]);
      setRecords([]);
      setMapping({});
      setResults([]);
      setError(null);
      if (!file) return;
      try {
        const parsed = parseCSV(await file.text());
        if (!isCurrent) return;
        setHeaders(parsed.headers);
        setRecords(parsed.records);
        setMapping(defaultCsvMapping(fields, parsed.headers));
      } catch (e) {
        if (isCurrent) setError(errorMessage(e));
      }
    });
    return () => {
      isCurrent = false;
    };
  }, [fields, file]);

  const missing = missingRequiredColumns(fields, mapping);
  const rowLimit = Number(maxRows);
  const rowsToRun =
    Number.isFinite(rowLimit) && rowLimit > 0 ? records.slice(0, rowLimit) : records;
  const canRun = fields.length > 0 && rowsToRun.length > 0 && missing.length === 0 && !running;

  const updateMapping = (field: string, column: string) => {
    setMapping((prev) => ({ ...prev, [field]: column }));
  };

  const runBatch = async () => {
    setRunning(true);
    setError(null);
    setResults([]);
    const nextRows: TCsvBatchResultRow[] = [];
    try {
      for (let i = 0; i < rowsToRun.length; i += 1) {
        const record = rowsToRun[i];
        setProgress(`Running ${i + 1}/${rowsToRun.length}`);
        const inputs = buildInputs(record, fields, mapping);
        try {
          const result = await api.testTaskForBatch(task.id, inputs, { version: task.prompt_version });
          nextRows.push({ rowNo: record.rowNo, inputs, result, error: '' });
        } catch (e) {
          nextRows.push({ rowNo: record.rowNo, inputs, result: null, error: errorMessage(e) });
        }
      }
      setResults(nextRows);
      downloadCSV(nextRows, `${task.id}-test-output.csv`);
    } finally {
      setRunning(false);
      setProgress('');
    }
  };

  if (fields.length === 0) {
    return (
      <div className="mt-4 rounded-md border border-solid border-primary-border p-3">
        <Typography variant="body" size="2" className="text-primary-text font-medium">
          CSV batch test
        </Typography>
        <Typography variant="body" size="2" className="text-tertiary-text mt-2">
          CSV batch testing requires an input schema.
        </Typography>
      </div>
    );
  }

  return (
    <div className="mt-4 rounded-md border border-solid border-primary-border p-3">
      <Typography variant="body" size="2" className="text-primary-text font-medium">
        CSV batch test
      </Typography>
      <Typography variant="body" size="1" className="text-tertiary-text mt-1">
        Upload a CSV, run this task as test traffic for each row, and download row-level outputs.
      </Typography>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-3 mt-3">
        <label className="block">
          <Typography variant="body" size="1" className="text-tertiary-text mb-1">
            CSV file
          </Typography>
          <input
            className={INPUT_CLS}
            type="file"
            accept=".csv,text/csv"
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
          />
        </label>
        <label className="block">
          <Typography variant="body" size="1" className="text-tertiary-text mb-1">
            Rows to run
          </Typography>
          <input className={INPUT_CLS} value={maxRows} onChange={(e) => setMaxRows(e.target.value)} />
        </label>
      </div>

      {headers.length > 0 && (
        <div className="mt-3 grid grid-cols-1 md:grid-cols-2 gap-2">
          {fields.map((field) => (
            <label key={field.name} className="flex items-center gap-2">
              <Typography variant="body" size="1" className="w-36 text-tertiary-text">
                {field.name}
                {field.required && ' *'}
              </Typography>
              <select
                className={INPUT_CLS}
                value={mapping[field.name] ?? ''}
                onChange={(e) => updateMapping(field.name, e.target.value)}
              >
                <option value="">Ignore</option>
                {headers.map((header) => (
                  <option key={header} value={header}>
                    {header}
                  </option>
                ))}
              </select>
            </label>
          ))}
        </div>
      )}

      {missing.length > 0 && (
        <Typography variant="body" size="1" className="text-error-text mt-2">
          Missing required CSV mappings: {missing.join(', ')}.
        </Typography>
      )}
      {error && (
        <Typography variant="body" size="1" className="text-error-text mt-2">
          {error}
        </Typography>
      )}

      <div className="mt-3 flex items-center gap-2 flex-wrap">
        <Button variant="primary" size="s" disabled={!canRun} onClick={runBatch}>
          {running ? progress || 'Running…' : 'Run and download CSV'}
        </Button>
        {results.length > 0 && (
          <Button variant="outline" size="s" onClick={() => downloadCSV(results, `${task.id}-test-output.csv`)}>
            Download last CSV
          </Button>
        )}
        {records.length > 0 && (
          <Typography variant="body" size="1" className="text-tertiary-text">
            {records.length} row{records.length === 1 ? '' : 's'} loaded.
          </Typography>
        )}
      </div>

      {results.length > 0 && <BatchPreview rows={results.slice(0, 5)} />}
    </div>
  );
};

const BatchPreview = ({ rows }: { rows: TCsvBatchResultRow[] }) => (
  <div className="mt-3 overflow-x-auto">
    <table className="w-full text-xs border-collapse">
      <thead>
        <tr className="text-left text-tertiary-text">
          <th className="border-b border-solid border-primary-border py-1 pr-3">Row</th>
          <th className="border-b border-solid border-primary-border py-1 pr-3">Status</th>
          <th className="border-b border-solid border-primary-border py-1 pr-3">Output / error</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <tr key={row.rowNo}>
            <td className="border-b border-solid border-tertiary-border py-1 pr-3">{row.rowNo}</td>
            <td className="border-b border-solid border-tertiary-border py-1 pr-3">
              {row.error || row.result?.error ? 'Error' : 'OK'}
            </td>
            <td className="border-b border-solid border-tertiary-border py-1 pr-3 font-mono">
              {row.error || row.result?.error || JSON.stringify(row.result?.output ?? row.result?.raw_response)}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  </div>
);

function downloadCSV(rows: TCsvBatchResultRow[], filename: string) {
  const blob = new Blob([exportBatchResultsCSV(rows)], { type: 'text/csv;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}

export default CSVBatchTestPanel;
