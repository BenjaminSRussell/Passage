import { useCallback, useEffect, useRef, useState } from "react";
import { describeProblem, type Problem } from "./api/client";

export interface LoadState<T> {
  data: T | null;
  loading: boolean;
  error: Problem | null;
  reload(): void;
}

interface Slot<T> {
  key: string;
  data: T | null;
  loading: boolean;
  error: Problem | null;
}

/**
 * Loads data for a page and reloads when key changes. A newer request wins:
 * an older response that arrives late is ignored, and the old fetch is aborted.
 * A new key never shows the previous key's data; reload() keeps it while refetching.
 */
export function useLoad<T>(load: (signal: AbortSignal) => Promise<T>, key: string): LoadState<T> {
  const [slot, setSlot] = useState<Slot<T>>({
    key,
    data: null,
    loading: true,
    error: null,
  });
  const [attempt, setAttempt] = useState(0);
  const latest = useRef(0);
  const loader = useRef(load);
  loader.current = load;

  useEffect(() => {
    const token = ++latest.current;
    const controller = new AbortController();
    setSlot((prev) => ({
      key,
      data: prev.key === key ? prev.data : null,
      loading: true,
      error: null,
    }));
    loader.current(controller.signal).then(
      (data) => {
        if (token === latest.current) setSlot({ key, data, loading: false, error: null });
      },
      (failure: unknown) => {
        if (token === latest.current && !controller.signal.aborted) {
          setSlot({
            key,
            data: null,
            loading: false,
            error: describeProblem(failure),
          });
        }
      },
    );
    return () => controller.abort();
  }, [key, attempt]);

  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  const current = slot.key === key;
  return {
    data: current ? slot.data : null,
    loading: current ? slot.loading : true,
    error: current ? slot.error : null,
    reload,
  };
}
