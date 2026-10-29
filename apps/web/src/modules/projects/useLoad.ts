import { useCallback, useEffect, useRef, useState } from "react";
import { describeProblem, type Page, type Problem } from "./api/client";

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

export interface MoreState<T> {
  /** The first page's rows followed by every page loaded since. */
  rows: T[];
  /** True while another page follows. */
  hasMore: boolean;
  loading: boolean;
  error: Problem | null;
  loadMore(): Promise<void>;
}

/**
 * "Load more" over a keyset-paged list: starts from the first page useLoad
 * fetched and appends the pages after its nextCursor. A new first page (a
 * reload or a filter change) starts over.
 */
export function useMore<T>(first: Page<T> | null, fetchPage: (cursor: string) => Promise<Page<T>>): MoreState<T> {
  const [more, setMore] = useState<T[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<Problem | null>(null);
  const fetcher = useRef(fetchPage);
  fetcher.current = fetchPage;

  useEffect(() => {
    setMore([]);
    setError(null);
    setCursor(first?.nextCursor);
  }, [first]);

  const loadMore = useCallback(async () => {
    if (!cursor) return;
    setLoading(true);
    setError(null);
    try {
      const page = await fetcher.current(cursor);
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (failure) {
      setError(describeProblem(failure));
    } finally {
      setLoading(false);
    }
  }, [cursor]);

  return { rows: [...(first?.items ?? []), ...more], hasMore: Boolean(cursor), loading, error, loadMore };
}
