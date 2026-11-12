import { useEffect, useRef, useState } from "react";
import { isForbidden, type ListData } from "./ListPage";

/** One page of a cursor-paged list route. */
export interface RowPage<T> {
  items: T[];
  nextCursor?: string | null;
}

interface Paged<T> {
  key: string;
  rows: T[];
  /** The cursor of the page being read; undefined for the first page. */
  cursor?: string;
  nextCursor?: string;
  ready: boolean;
  loading: boolean;
  error?: unknown;
}

/**
 * ListData for a cursor-paged list route, for `ListPage`'s `useRows`. `key` names the filters the route was asked
 * with: a new key starts again from the first page. `fetchPage(cursor, signal)` reads one page. A failed Load more
 * keeps the rows already loaded and retries that page; a 403 (status 403, AUTH-0003 or HTTP-403) shows no access.
 */
export function usePagedRows<T>(key: string, fetchPage: (cursor: string | undefined, signal: AbortSignal) => Promise<RowPage<T>>): ListData<T> {
  const [paged, setPaged] = useState<Paged<T>>({ key, rows: [], ready: false, loading: true });
  const [tick, setTick] = useState(0);
  const fetchRef = useRef(fetchPage);
  fetchRef.current = fetchPage;
  const current: Paged<T> = paged.key === key ? paged : { key, rows: [], ready: false, loading: true };
  const cursor = current.cursor;

  useEffect(() => {
    const controller = new AbortController();
    setPaged((s) => (s.key === key ? { ...s, loading: true, error: undefined } : { key, rows: [], ready: false, loading: true }));
    fetchRef.current(cursor, controller.signal).then(
      (page) => {
        if (controller.signal.aborted) return;
        setPaged((s) => {
          const base = s.key === key ? s : { key, rows: [], ready: false, loading: true };
          return { ...base, rows: cursor ? [...base.rows, ...page.items] : page.items, nextCursor: page.nextCursor || undefined, ready: true, loading: false, error: undefined };
        });
      },
      (error: unknown) => {
        if (controller.signal.aborted) return;
        setPaged((s) => (s.key === key ? { ...s, loading: false, error } : s));
      },
    );
    return () => controller.abort();
  }, [key, cursor, tick]);

  return {
    rows: current.error && cursor === undefined ? [] : current.rows,
    loading: cursor === undefined && current.loading,
    loadingMore: cursor !== undefined && current.loading,
    error: current.error,
    forbidden: isForbidden(current.error),
    hasMore: current.ready && Boolean(current.nextCursor) && !current.error,
    loadMore: () => setPaged((s) => (s.key === key && s.nextCursor ? { ...s, cursor: s.nextCursor } : s)),
    retry: () => setTick((n) => n + 1),
  };
}
