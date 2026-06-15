import { useMemo, useState, type ReactNode } from 'react';
import { Typography, Button, Checkbox, TextArea, Input } from '@meesho/merlin-ui-tailwind';
import { DEFAULT_COMPARE_MODELS, MODEL_GROUPS } from '../types';
import { countTokens, estimateCost, formatCost } from '../utils/tokens';

type TMode = 'single' | 'batch';

// Splits batch input into individual prompts: blank line OR a line of '---'.
const splitPrompts = (raw: string): string[] =>
  raw
    .split(/\n\s*---+\s*\n|\n\s*\n/)
    .map((p) => p.trim())
    .filter(Boolean);

const EstimatePage = () => {
  const [mode, setMode] = useState<TMode>('single');
  const [text, setText] = useState('');
  const [systemPrompt, setSystemPrompt] = useState('');
  const [expectedOutput, setExpectedOutput] = useState(500);
  const [models, setModels] = useState<string[]>([...DEFAULT_COMPARE_MODELS]);

  const prompts = useMemo(
    () => (mode === 'single' ? (text.trim() ? [text.trim()] : []) : splitPrompts(text)),
    [mode, text],
  );

  const systemTokens = useMemo(() => countTokens(systemPrompt), [systemPrompt]);

  // Per-prompt input token counts (prompt + shared system prompt).
  const promptTokens = useMemo(
    () => prompts.map((p) => countTokens(p) + systemTokens),
    [prompts, systemTokens],
  );

  const totalInputTokens = promptTokens.reduce((a, b) => a + b, 0);

  // Per-model totals across all prompts.
  const perModel = useMemo(
    () =>
      models.map((model) => {
        const outPer = Math.max(0, expectedOutput);
        const cost = promptTokens.reduce(
          (acc, inTok) => acc + estimateCost(model, inTok, outPer),
          0,
        );
        return {
          model,
          inputTokens: totalInputTokens,
          outputTokens: outPer * prompts.length,
          cost,
        };
      }),
    [models, promptTokens, totalInputTokens, expectedOutput, prompts.length],
  );

  const grandTotal = perModel.reduce((a, m) => a + m.cost, 0);

  const toggleModel = (model: string, checked: boolean) =>
    setModels((prev) => (checked ? [...prev, model] : prev.filter((m) => m !== model)));

  return (
    <div className="flex-1 overflow-y-auto bg-primary-bg p-6">
      <div className="mx-auto max-w-4xl flex flex-col gap-6">
        <div>
          <Typography variant="heading" size="6" className="text-primary-text">
            Estimate cost &amp; tokens
          </Typography>
          <Typography variant="body" size="3" className="text-tertiary-text">
            Count tokens and project cost for a prompt or a batch — before spending anything.
          </Typography>
        </div>

        {/* Mode toggle */}
        <div className="flex gap-2">
          <Button
            variant={mode === 'single' ? 'primary' : 'outline'}
            size="s"
            onClick={() => setMode('single')}
          >
            Single prompt
          </Button>
          <Button
            variant={mode === 'batch' ? 'primary' : 'outline'}
            size="s"
            onClick={() => setMode('batch')}
          >
            Batch
          </Button>
        </div>

        {/* Inputs */}
        <div className="flex flex-col gap-3">
          <div>
            <Typography variant="body" size="2" className="text-primary-text mb-1 font-medium">
              System prompt (optional, shared)
            </Typography>
            <TextArea
              value={systemPrompt}
              onChange={({ value }) => setSystemPrompt(value)}
              placeholder="You are a helpful assistant…"
              rows={2}
            />
          </div>
          <div>
            <Typography variant="body" size="2" className="text-primary-text mb-1 font-medium">
              {mode === 'single' ? 'Prompt' : 'Prompts (separate with a blank line or ---)'}
            </Typography>
            <TextArea
              value={text}
              onChange={({ value }) => setText(value)}
              placeholder={
                mode === 'single'
                  ? 'Enter your prompt…'
                  : 'First prompt…\n---\nSecond prompt…\n\nThird prompt…'
              }
              rows={mode === 'single' ? 4 : 8}
            />
          </div>

          <div className="flex flex-wrap items-end gap-6">
            <div>
              <Typography variant="body" size="2" className="text-primary-text mb-1 font-medium">
                Expected output tokens / prompt
              </Typography>
              <Input
                type="number"
                value={String(expectedOutput)}
                onChange={({ value }) => setExpectedOutput(Number(value) || 0)}
                wrapperClassName="w-40"
              />
            </div>
            <div>
              <Typography variant="body" size="2" className="text-primary-text mb-1 font-medium">
                Models
              </Typography>
              <div className="flex flex-wrap gap-6">
                {MODEL_GROUPS.map((group) => (
                  <div key={group.provider}>
                    <Typography
                      variant="body"
                      size="1"
                      className="text-tertiary-text font-semi-bold uppercase tracking-wider mb-1"
                    >
                      {group.provider}
                    </Typography>
                    <div className="flex flex-col gap-1">
                      {group.models.map((m) => (
                        <Checkbox
                          key={m}
                          checked={models.includes(m)}
                          onChange={({ checked }) => toggleModel(m, checked)}
                          label={
                            <Typography variant="body" size="2" className="text-primary-text">
                              {m}
                            </Typography>
                          }
                        />
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>

        {/* Summary */}
        <div className="flex flex-wrap gap-4">
          <SummaryCard label="Prompts" value={String(prompts.length)} />
          <SummaryCard label="Total input tokens" value={totalInputTokens.toLocaleString()} />
          <SummaryCard label="Est. total cost" value={formatCost(grandTotal)} accent />
        </div>

        {/* Per-model table */}
        {prompts.length > 0 && models.length > 0 && (
          <div className="border border-solid border-primary-border rounded-lg overflow-hidden">
            <table className="w-full text-left">
              <thead className="bg-secondary-bg">
                <tr>
                  <Th>Model</Th>
                  <Th right>Input tok</Th>
                  <Th right>Output tok</Th>
                  <Th right>Est. cost</Th>
                </tr>
              </thead>
              <tbody>
                {perModel.map((row) => (
                  <tr key={row.model} className="border-t border-solid border-tertiary-border">
                    <Td>{row.model}</Td>
                    <Td right>{row.inputTokens.toLocaleString()}</Td>
                    <Td right>{row.outputTokens.toLocaleString()}</Td>
                    <Td right>{formatCost(row.cost)}</Td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        <Typography variant="body" size="1" className="text-tertiary-text">
          Token counts use the cl100k_base tokenizer — an approximation for non-OpenAI models.
          Rates come from the backend pricing table; actual usage may vary.
        </Typography>
      </div>
    </div>
  );
};

const SummaryCard = ({
  label,
  value,
  accent,
}: {
  label: string;
  value: string;
  accent?: boolean;
}) => (
  <div className="flex-1 min-w-[160px] bg-secondary-bg border border-solid border-primary-border rounded-lg px-4 py-3">
    <Typography variant="body" size="1" className="text-tertiary-text uppercase tracking-wider">
      {label}
    </Typography>
    <Typography
      variant="heading"
      size="5"
      className={accent ? 'text-accent' : 'text-primary-text'}
    >
      {value}
    </Typography>
  </div>
);

const Th = ({ children, right }: { children: ReactNode; right?: boolean }) => (
  <th className={`px-4 py-2 ${right ? 'text-right' : 'text-left'}`}>
    <Typography variant="body" size="1" className="text-tertiary-text uppercase tracking-wider">
      {children}
    </Typography>
  </th>
);

const Td = ({ children, right }: { children: ReactNode; right?: boolean }) => (
  <td className={`px-4 py-2.5 ${right ? 'text-right tabular-nums' : ''}`}>
    <Typography variant="body" size="3" className="text-primary-text">
      {children}
    </Typography>
  </td>
);

export default EstimatePage;
