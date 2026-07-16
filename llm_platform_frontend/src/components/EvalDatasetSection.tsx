import { useCallback, useEffect, useMemo, useState } from 'react';
import { TextArea, Typography } from '@meesho/merlin-ui-tailwind';
import { api, ApiError, errorMessage } from '../api/client';
import { useToast } from '../toast/context';
import useEvalDatasetRuns from '../hooks/useEvalDatasetRuns';
import EvalDatasetActions from './EvalDatasetActions';
import EvalDatasetCheckPanel from './EvalDatasetCheckPanel';
import EvalDatasetList from './EvalDatasetList';
import EvalDatasetMappings from './EvalDatasetMappings';
import EvalRunControls from './EvalRunControls';
import EvalRecentRuns from './EvalRecentRuns';
import EvalValidationErrors from './EvalValidationErrors';
import EvalSourceTabs from './EvalSourceTabs';
import {
  defaultInputMapping,
  defaultOutputMapping,
  mappingWithExistingColumns,
  missingMappedColumns,
  parseCSVHeader,
  schemaFieldNames,
  schemaRequiredFieldNames,
} from '../utils/evalDatasets';
import type { TEvalDataset, TEvalRun, TEvalValidationError, TTask } from '../types';

type TEvalDatasetSectionProps = {
  task: TTask;
  canWrite: boolean;
};

const INPUT_CLS =
  'w-full border border-solid border-primary-border rounded-md px-2 py-1.5 text-sm bg-primary-bg text-primary-text';

const EvalDatasetSection = ({ task, canWrite }: TEvalDatasetSectionProps) => {
  const toast = useToast();
  const inputFields = useMemo(() => schemaFieldNames(task.input_schema), [task.input_schema]);
  const outputFields = useMemo(() => schemaFieldNames(task.output_schema), [task.output_schema]);
  const requiredInputFields = useMemo(
    () => schemaRequiredFieldNames(task.input_schema),
    [task.input_schema],
  );

  const [datasets, setDatasets] = useState<TEvalDataset[]>([]);
  const [runs, setRuns] = useState<TEvalRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [mode, setMode] = useState<'csv' | 'prism'>('csv');
  const [name, setName] = useState(`${task.id}-golden`);
  const [file, setFile] = useState<File | null>(null);
  const [sql, setSql] = useState('');
  const [csvHeaders, setCsvHeaders] = useState<string[] | null>(null);
  const [inputMapping, setInputMapping] = useState<Record<string, string>>(
    () => defaultInputMapping(inputFields),
  );
  const [outputMapping, setOutputMapping] = useState<Record<string, string>>(
    () => defaultOutputMapping(outputFields),
  );
  const [evalVersion, setEvalVersion] = useState(String(task.prompt_version));
  const [maxItems, setMaxItems] = useState('50');
  const [uploadBusy, setUploadBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [rowErrors, setRowErrors] = useState<TEvalValidationError[]>([]);
  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.listEvalDatasets(task.id);
      setDatasets(data.datasets);
      setRuns(data.runs);
      setError(null);
    } catch (e) {
      setError(
        e instanceof ApiError && e.status === 404 && !e.code
          ? 'Eval API route returned 404. Restart the Go backend so the new eval routes are loaded.'
          : errorMessage(e),
      );
    } finally {
      setLoading(false);
    }
  }, [task.id]);

  useEffect(() => {
    let isCurrent = true;
    void Promise.resolve().then(async () => {
      if (!isCurrent) return;
      await load();
    });
    return () => {
      isCurrent = false;
    };
  }, [load]);

  useEffect(() => {
    let isCurrent = true;
    void Promise.resolve().then(() => {
      if (!isCurrent) return;
      setInputMapping(defaultInputMapping(inputFields));
      setOutputMapping(defaultOutputMapping(outputFields));
      setEvalVersion(String(task.prompt_version));
    });
    return () => {
      isCurrent = false;
    };
  }, [inputFields, outputFields, task.prompt_version]);

  useEffect(() => {
    let isCurrent = true;
    void Promise.resolve().then(async () => {
      if (!isCurrent) return;
      if (!file || !file.name.toLowerCase().endsWith('.csv')) {
        setCsvHeaders(null);
        return;
      }
      const text = await file.text();
      if (!isCurrent) return;
      setCsvHeaders(parseCSVHeader(text));
    });
    return () => {
      isCurrent = false;
    };
  }, [file]);

  const evalActions = useEvalDatasetRuns({
    taskId: task.id,
    evalVersion,
    maxItems,
    toast,
    load,
  });
  const busy = uploadBusy ?? evalActions.busy;
  const hasInputSchema = inputFields.length > 0;
  const missingRequiredInputColumns = csvHeaders
    ? missingMappedColumns(inputMapping, csvHeaders, requiredInputFields)
    : [];
  const missingExpectedOutputColumns = csvHeaders
    ? missingMappedColumns(outputMapping, csvHeaders)
    : [];
  const hasMissingRequiredCSVColumns =
    missingRequiredInputColumns.length > 0 || missingExpectedOutputColumns.length > 0;

  const readyForUpload =
    canWrite && name.trim() !== '' && hasInputSchema && !!file && !hasMissingRequiredCSVColumns;
  const readyForPrism = canWrite && name.trim() !== '' && hasInputSchema && sql.trim() !== '';
  const changeInputMapping = (field: string, value: string) => {
    setInputMapping((prev) => ({ ...prev, [field]: value }));
  };

  const changeOutputMapping = (field: string, value: string) => {
    setOutputMapping((prev) => ({ ...prev, [field]: value }));
  };
  const upload = async () => {
    if (!file) return;
    setUploadBusy('upload');
    setRowErrors([]);
    try {
      const form = new FormData();
      form.set('name', name.trim());
      form.set('file', file);
      form.set(
        'input_mapping',
        JSON.stringify(mappingWithExistingColumns(inputMapping, csvHeaders, requiredInputFields)),
      );
      form.set('output_mapping', JSON.stringify(outputMapping));
      const result = await api.uploadEvalDataset(task.id, form);
      if (result.errors?.length) {
        setRowErrors(result.errors);
        toast.error('Dataset validation failed.');
        return;
      }
      const uploadedName = result.dataset?.name ?? name.trim();
      const uploadedVersion = result.dataset?.version ? ` v${result.dataset.version}` : '';
      toast.success(`Dataset ${uploadedName}${uploadedVersion} uploaded.`);
      setFile(null);
      await load();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setUploadBusy(null);
    }
  };

  const registerPrism = async () => {
    setUploadBusy('prism');
    try {
      const result = await api.createPrismEvalDataset(task.id, {
        name: name.trim(),
        sql,
        input_mapping: inputMapping,
        output_mapping: outputMapping,
      });
      toast.success(result.detail ?? 'Prism source registered.');
      await load();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setUploadBusy(null);
    }
  };

  return (
    <div className="flex flex-col gap-4">
      <EvalSourceTabs mode={mode} onChange={setMode} />

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
        <label className="block">
          <Typography variant="body" size="1" className="text-tertiary-text mb-1">
            Dataset name
          </Typography>
          <input className={INPUT_CLS} value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        {mode === 'csv' && (
          <label className="block">
            <Typography variant="body" size="1" className="text-tertiary-text mb-1">
              CSV or XLSX file
            </Typography>
            <input
              className={INPUT_CLS}
              type="file"
              accept=".csv,.xlsx,text/csv,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
              disabled={!canWrite}
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            />
          </label>
        )}
      </div>

      <EvalDatasetMappings
        inputFields={inputFields}
        outputFields={outputFields}
        inputMapping={inputMapping}
        outputMapping={outputMapping}
        disabled={!canWrite}
        onInputChange={changeInputMapping}
        onOutputChange={changeOutputMapping}
      />

      {mode === 'prism' && (
        <label className="block">
          <Typography variant="body" size="1" className="text-tertiary-text mb-1">
            SQL
          </Typography>
          <TextArea value={sql} onChange={({ value }) => setSql(value)} rows={4} />
        </label>
      )}

      <EvalValidationErrors errors={rowErrors} />

      {mode === 'csv' && !file && (
        <Typography variant="body" size="1" className="text-tertiary-text">
          Choose a CSV/XLSX file to enable upload.
        </Typography>
      )}
      {missingRequiredInputColumns.length > 0 && (
        <Typography variant="body" size="1" className="text-error-text">
          Missing required input columns in CSV: {missingRequiredInputColumns.join(', ')}.
        </Typography>
      )}
      {missingExpectedOutputColumns.length > 0 && (
        <Typography variant="body" size="1" className="text-error-text">
          Missing expected output columns in CSV: {missingExpectedOutputColumns.join(', ')}.
        </Typography>
      )}

      <EvalDatasetActions
        mode={mode}
        canWrite={canWrite}
        busy={busy}
        readyForUpload={readyForUpload}
        readyForPrism={readyForPrism}
        onUpload={upload}
        onRegisterPrism={registerPrism}
      />

      {!hasInputSchema && (
        <Typography variant="body" size="2" className="text-error-text">
          Add an input schema before uploading eval data.
        </Typography>
      )}

      <div className="border-t border-solid border-tertiary-border pt-3">
        <EvalRunControls
          version={evalVersion}
          maxItems={maxItems}
          onVersionChange={setEvalVersion}
          onMaxItemsChange={setMaxItems}
        />

        <Typography variant="body" size="1" className="text-tertiary-text mb-2">
          After upload, the dataset appears below. Use Download CSV on that row to run the prompt and export outputs.
        </Typography>

        <EvalDatasetList
          datasets={datasets}
          loading={loading}
          error={error}
          canWrite={canWrite}
          busy={busy}
          onCheck={evalActions.checkEval}
          onDownload={evalActions.downloadEvalCSV}
          onSaveRun={evalActions.saveEvalRun}
        />
      </div>

      <EvalDatasetCheckPanel run={evalActions.checkRun} hasInputSchema={hasInputSchema} />
      <EvalRecentRuns
        runs={runs}
        selectedRunId={evalActions.selectedRunId}
        onSelectRun={evalActions.selectRun}
      />
    </div>
  );
};

export default EvalDatasetSection;
