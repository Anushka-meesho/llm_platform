import { createContext, useContext } from 'react';

export type ToastTone = 'error' | 'success' | 'info';

export interface Toast {
  id: number;
  tone: ToastTone;
  message: string;
}

// ToastApi is the imperative surface components use to raise transient
// notifications — primarily `error` for failed actions (e.g. a save/delete that
// the backend rejected), carrying the backend message + request_id ref.
export interface ToastApi {
  error: (message: string) => void;
  success: (message: string) => void;
  info: (message: string) => void;
}

export const ToastContext = createContext<ToastApi | null>(null);

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error('useToast must be used within <ToastProvider>');
  return ctx;
}
