import { useEffect, useRef, useState } from "react";
import { describeProblem, type Page, type Problem } from "./api/client";
import { useLoad } from "./useLoad";

export interface PagedState<T> {
  /** The first page followed by every page loaded with loadMore. */
  rows: T[];
  loading: boolean;
  error: Problem | null;
  reload(): void;
  /** True while the service reports more rows after the last loaded page. */
  hasMore: boolean;
  loadMore(): void;
  loadingMore: boolean;
  moreError: Problem | null;
}

/**
 * A list over the services' keyset cursor: the first page loads (and reloads)
 * when key changes, loadMore appends the next page. A page that arrives after
 * the filters changed is dropped, so rows of two filters never mix.
 */
export function usePaged<T>(load: (cursor: string | undefined, signal?: AbortSignal) => Promise<Page<T>>, key: string): PagedState<T> {
  const first = useLoad((signal) => load(undefined, signal), key);
  const [more, setMore] = useState<T[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const generation = useRef(0);

  useEffect(() => {
    generation.current += 1;
    setMore([]);
    setMoreError(null);
    setLoadingMore(false);
    setCursor(first.data?.nextCursor);
  }, [first.data]);

  async function loadMore() {
    if (!cursor || loadingMore) return;
    const at = generation.current;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await load(cursor);
      if (at !== generation.current) return;
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      if (at === generation.current) setMoreError(describeProblem(error));
    } finally {
      if (at === generation.current) setLoadingMore(false);
    }
  }

  return {
    rows: [...(first.data?.items ?? []), ...more],
    loading: first.loading,
    error: first.error,
    reload: first.reload,
    hasMore: Boolean(cursor) && !first.error && !first.loading,
    loadMore: () => void loadMore(),
    loadingMore,
    moreError,
  };
}
