import { useCallback, useEffect, useRef, useState } from "react";
import { loadedRows, type ListData } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";

export interface LoadState<T> {
  data: T | null;
  loading: boolean;
  error: Problem | null;
  reload(): void;
}

/**
 * Loads data for a page and reloads when key changes. A newer request wins:
 * an older response that arrives late is ignored, and the old fetch is aborted.
 */
export function useLoad<T>(load: (signal: AbortSignal) => Promise<T>, key: string): LoadState<T> {
  const [state, setState] = useState<Omit<LoadState<T>, "reload">>({ data: null, loading: true, error: null });
  const [attempt, setAttempt] = useState(0);
  const latest = useRef(0);
  const loader = useRef(load);
  loader.current = load;

  useEffect(() => {
    const token = ++latest.current;
    const controller = new AbortController();
    setState((prev) => ({ data: prev.data, loading: true, error: null }));
    loader.current(controller.signal).then(
      (data) => {
        if (token === latest.current) setState({ data, loading: false, error: null });
      },
      (failure: unknown) => {
        if (token === latest.current && !controller.signal.aborted) {
          setState({ data: null, loading: false, error: describeProblem(failure) });
        }
      },
    );
    return () => controller.abort();
  }, [key, attempt]);

  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  return { ...state, reload };
}

/**
 * ListData for a `ListPage` whose rows come from one capped call (the receivables list routes take a `limit` and no
 * cursor). `onCapped` hears whether the cap was reached, so the page can say that more rows exist than it shows.
 */
export function useCappedRows<T>(load: (signal: AbortSignal) => Promise<T[]>, key: string, cap: number, onCapped: (capped: boolean) => void): ListData<T> {
  const state = useLoad(load, key);
  const capped = (state.data?.length ?? 0) >= cap;
  useEffect(() => onCapped(capped), [capped, onCapped]);
  return loadedRows(state);
}
