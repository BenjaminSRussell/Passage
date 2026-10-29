import { useCallback, useEffect, useRef, useState } from "react";

export interface LoadState<T> {
  data: T | null;
  loading: boolean;
  error: string | null;
  reload(): void;
}

/** Loads page data; a newer request wins and the older fetch is aborted. */
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
          setState({ data: null, loading: false, error: failure instanceof Error ? failure.message : "Something went wrong." });
        }
      },
    );
    return () => controller.abort();
  }, [key, attempt]);

  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  return { ...state, reload };
}
