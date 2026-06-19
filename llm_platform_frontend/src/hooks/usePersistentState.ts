import { useEffect, useState, type Dispatch, type SetStateAction } from 'react';

// usePersistentState is a drop-in replacement for useState that mirrors its value
// to localStorage, so a page "continues where it was left" — including filled-in
// inputs — across reloads and new sessions, not just across in-app tab switches
// (which the kept-alive mounts already handle). It is best-effort: storage that
// is full, disabled (private mode), or holds non-serializable/legacy data never
// crashes the app — it just falls back to the initial value.
//
// Keys should be stable for the lifetime of a mount. For per-entity state, fold
// the entity id into the key (e.g. `tasks.draft.${task.id}`) and mount the owning
// component with key={id} so the key doesn't change underneath a live mount.

function read<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    if (raw === null) return fallback;
    return JSON.parse(raw) as T;
  } catch {
    return fallback;
  }
}

export function usePersistentState<T>(
  key: string,
  initial: T | (() => T),
): [T, Dispatch<SetStateAction<T>>] {
  const [state, setState] = useState<T>(() =>
    read(key, typeof initial === 'function' ? (initial as () => T)() : initial),
  );

  useEffect(() => {
    try {
      localStorage.setItem(key, JSON.stringify(state));
    } catch {
      // Quota exceeded, storage disabled, or a non-serializable value — persistence
      // is a convenience, never a correctness requirement, so swallow it.
    }
  }, [key, state]);

  return [state, setState];
}

// clearPersisted removes every persisted key under a prefix — used to discard a
// draft once it has been committed (e.g. the create-task form after the task is
// created), so a stale draft doesn't resurface next time.
export function clearPersisted(prefix: string): void {
  try {
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const k = localStorage.key(i);
      if (k && k.startsWith(prefix)) localStorage.removeItem(k);
    }
  } catch {
    // ignore — see usePersistentState
  }
}
