import { useCallback, useEffect, useRef, useState } from "react";
import { describeProblem, type Problem } from "./api/client";

export interface LoadState<T> {
  data: T | null;
  loading: boolean;
  error: Problem | null;
  reload(): void;
  /** Replaces the data with a command's response without a refetch. */
  set(data: T): void;
}

/**
 * Loads data for a page and reloads when key changes. A newer request wins: an older response
 * that arrives late is ignored, and the old fetch is aborted.
 */
export function useLoad<T>(load: (signal: AbortSignal) => Promise<T>, key: string): LoadState<T> {
  const [state, setState] = useState<{ data: T | null; loading: boolean; error: Problem | null }>({ data: null, loading: true, error: null });
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
  const set = useCallback((data: T) => setState({ data, loading: false, error: null }), []);
  return { ...state, reload, set };
}
