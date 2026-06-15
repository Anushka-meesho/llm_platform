import type { ReactNode } from 'react';

// Small shared building blocks — plain Tailwind, no design-system dependency.

export const Section = ({ title, children }: { title: string; children: ReactNode }) => (
  <div className="border border-neutral-200 bg-white rounded-lg p-4">
    <div className="text-sm font-semibold text-neutral-900 mb-3">{title}</div>
    {children}
  </div>
);

export const Stat = ({ label, value }: { label: string; value: string }) => (
  <div className="bg-white border border-neutral-200 rounded-lg px-4 py-2">
    <div className="text-[11px] text-neutral-500 uppercase tracking-wider">{label}</div>
    <div className="text-lg font-semibold text-neutral-900">{value}</div>
  </div>
);

export const Badge = ({
  tone,
  children,
}: {
  tone: 'ok' | 'warn' | 'error' | 'neutral';
  children: ReactNode;
}) => {
  const tones = {
    ok: 'bg-emerald-50 text-emerald-700 border-emerald-200',
    warn: 'bg-amber-50 text-amber-700 border-amber-200',
    error: 'bg-red-50 text-red-700 border-red-200',
    neutral: 'bg-neutral-100 text-neutral-600 border-neutral-200',
  } as const;
  return (
    <span
      className={`text-[10px] font-semibold uppercase tracking-wider px-2 py-0.5 rounded-full border ${tones[tone]}`}
    >
      {children}
    </span>
  );
};

export const Spinner = () => (
  <div className="h-6 w-6 rounded-full border-2 border-neutral-300 border-t-neutral-700 animate-spin" />
);
