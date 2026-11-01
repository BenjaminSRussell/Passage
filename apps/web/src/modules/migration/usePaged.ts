import { useEffect, useRef, useState } from "react";
import { describeProblem, type Page, type Problem } from "./api/client";
import type { LoadState } from "./useLoad";

export interface Paged<T> {
  /** The first page followed by every page loaded after it. */
  rows: T[];
  hasMore: boolean;
  loadingMore: boolean;
  moreError: Problem | null;
  loadMore(): void;
}

/**
 * Appends later pages to a first page loaded with useLoad. A new first page (a new filter or a reload)
 * replaces everything fetched after the old one, and a page requested from an older first page is dropped.
 * Load more is offered only once the first page for the current key has settled, so an old cursor is never
 * sent with new filters.
 */
export function usePaged<T>(first: LoadState<Page<T>>, key: string, fetchMore: (cursor: string) => Promise<Page<T>>): Paged<T> {
  const [more, setMore] = useState<T[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const keyRef = useRef(key);
  keyRef.current = key;
  const firstRef = useRef(first.data);
  firstRef.current = first.data;

  useEffect(() => {
    setMore([]);
    setCursor(first.data?.nextCursor);
    setMoreError(null);
  }, [first.data]);

  async function loadMore() {
    if (!cursor) return;
    const requested = key;
    const from = first.data;
    const current = () => keyRef.current === requested && firstRef.current === from;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const next = await fetchMore(cursor);
      if (!current()) return;
      setMore((prev) => [...prev, ...next.items]);
      setCursor(next.nextCursor);
    } catch (e) {
      if (current()) setMoreError(describeProblem(e));
    } finally {
      setLoadingMore(false);
    }
  }

  return { rows: [...(first.data?.items ?? []), ...more], hasMore: Boolean(cursor) && !first.loading && !first.error, loadingMore, moreError, loadMore };
}
