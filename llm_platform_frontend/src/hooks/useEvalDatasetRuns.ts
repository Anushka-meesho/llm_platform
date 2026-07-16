import { useState } from 'react';
import { api, errorMessage } from '../api/client';
import type { ToastApi } from '../toast/context';
import type { TEvalDataset, TEvalRun } from '../types';

type TUseEvalDatasetRunsArgs = {
  taskId: string;
  evalVersion: string;
  maxItems: string;
  toast: ToastApi;
  load: () => Promise<void>;
};

const formatPct = (value: number) => `${Math.round(value * 1000) / 10}%`;

const downloadBlob = (blob: Blob, filename: string) => {
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
};

const useEvalDatasetRuns = ({
  taskId,
  evalVersion,
  maxItems,
  toast,
  load,
}: TUseEvalDatasetRunsArgs) => {
  const [busy, setBusy] = useState<string | null>(null);
  const [selectedRunId, setSelectedRunId] = useState<number | null>(null);
  const [checkRun, setCheckRun] = useState<TEvalRun | null>(null);

  const payloadForDataset = (dataset: TEvalDataset) => {
    const limit = Number(maxItems);
    return {
      dataset_id: dataset.id,
      max_items: Number.isFinite(limit) && limit > 0 ? limit : undefined,
    };
  };

  const checkEval = async (dataset: TEvalDataset) => {
    setBusy(`check-${dataset.id}`);
    try {
      const run = await api.checkEvalDataset(taskId, Number(evalVersion), payloadForDataset(dataset));
      setCheckRun(run);
      setSelectedRunId(null);
      toast.success(`Check complete: ${formatPct(run.match_rate)} match rate.`);
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };

  const downloadEvalCSV = async (dataset: TEvalDataset) => {
    setBusy(`csv-${dataset.id}`);
    try {
      const blob = await api.downloadEvalCSV(taskId, Number(evalVersion), payloadForDataset(dataset));
      downloadBlob(blob, `${dataset.name}-v${dataset.version}-eval-check.csv`);
      toast.success('CSV downloaded.');
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };

  const saveEvalRun = async (dataset: TEvalDataset) => {
    setBusy(`run-${dataset.id}`);
    try {
      const run = await api.runEval(taskId, Number(evalVersion), payloadForDataset(dataset));
      toast.success(`Eval complete: ${formatPct(run.match_rate)} match rate.`);
      setCheckRun(null);
      setSelectedRunId(run.id);
      await load();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };

  const selectRun = (nextRun: TEvalRun) => {
    setCheckRun(null);
    setSelectedRunId((prev) => (prev === nextRun.id ? null : nextRun.id));
  };

  return {
    busy,
    checkRun,
    selectedRunId,
    setBusy,
    setSelectedRunId,
    checkEval,
    downloadEvalCSV,
    saveEvalRun,
    selectRun,
  };
};

export default useEvalDatasetRuns;
