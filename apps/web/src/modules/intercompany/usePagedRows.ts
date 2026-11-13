import { useCallback, useEffect, useRef, useState } from "react";
import { isForbidden, type ListData } from "../../shell/ui";
import type { Page } from "./api/postings";

/**
 * ListData for a cursor-paged intercompany list: the first page loads when `key` changes, "Load more" asks for the next one
 * with the cursor the service answered.
 */
export function usePagedRows<T>(load: (after: string | undefined, signal?: AbortSignal) => Promise<Page<T>>, key: string): ListData<T> {
  const [state, setState] = useState<{ rows: T[]; next?: string; loading: boolean; loadingMore: boolean; error?: unknown }>({ rows: [], loading: true, loadingMore: false });
  const [attempt, setAttempt] = useState(0);
  const loader = useRef(load);
  loader.current = load;

  useEffect(() => {
    const ctl = new AbortController();
    setState({ rows: [], loading: true, loadingMore: false });
    loader
      .current(undefined, ctl.signal)
      .then((page) => setState({ rows: page.items, next: page.nextCursor, loading: false, loadingMore: false }))
      .catch((error: unknown) => {
        if (!ctl.signal.aborted) setState({ rows: [], loading: false, loadingMore: false, error });
      });
    return () => ctl.abort();
  }, [key, attempt]);

  const current = useRef(state);
  current.current = state;
  const loadMore = useCallback(() => {
    const { next, loadingMore } = current.current;
    if (!next || loadingMore) return;
    setState((cur) => ({ ...cur, loadingMore: true }));
    loader
      .current(next)
      .then((page) => setState((cur) => ({ ...cur, rows: [...cur.rows, ...page.items], next: page.nextCursor, loadingMore: false })))
      .catch((error: unknown) => setState((cur) => ({ ...cur, loadingMore: false, error })));
  }, []);

  return {
    rows: state.rows,
    loading: state.loading,
    loadingMore: state.loadingMore,
    error: state.error,
    forbidden: isForbidden(state.error),
    hasMore: Boolean(state.next),
    loadMore,
    retry: () => setAttempt((n) => n + 1),
  };
}
