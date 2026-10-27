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

export interface PagedState<T> extends Omit<LoadState<unknown>, "data"> {
  rows: T[];
  hasMore: boolean;
  loadingMore: boolean;
  moreError: Problem | null;
  loadMore(): void;
}

/** A newest-first list with "load more" over the service's cursor pages. A new key starts again from the first page. */
export function usePaged<T>(load: (cursor: string | undefined, signal?: AbortSignal) => Promise<Page<T>>, key: string): PagedState<T> {
  const first = useLoad((signal) => load(undefined, signal), key);
  const [more, setMore] = useState<{ key: string; items: T[]; cursor?: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const extra = more && more.key === key ? more : null;
  const cursor = extra ? extra.cursor : first.data?.nextCursor;

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const next = await load(cursor);
      setMore({ key, items: [...(extra?.items ?? []), ...next.items], cursor: next.nextCursor });
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return {
    rows: [...(first.data?.items ?? []), ...(extra?.items ?? [])],
    loading: first.loading,
    error: first.error,
    reload: first.reload,
    hasMore: Boolean(cursor) && !first.loading && !first.error,
    loadingMore,
    moreError,
    loadMore,
  };
}
