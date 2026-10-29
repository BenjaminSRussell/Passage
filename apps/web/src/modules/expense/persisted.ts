// Shared state for the persisted list and detail pages of slice 5 (cash advances,
// travel requisitions, card transactions).
import { useCallback, useEffect, useRef, useState } from "react";
import { describeProblem, newIdempotencyKey, type Page, type Problem } from "./api/client";

export type Failure = Pick<Problem, "code" | "message">;

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * One list page with "load more": `load` is called with no cursor whenever `key`
 * (the serialised filters) changes or the user retries, and with the next cursor on
 * `more`.
 */
export function usePaged<T>(load: (cursor?: string) => Promise<Page<T>>, key: string) {
  const [items, setItems] = useState<T[]>([]);
  const [next, setNext] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [attempt, setAttempt] = useState(0);
  const loader = useRef(load);
  loader.current = load;

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setFailure(null);
    loader.current()
      .then((page) => {
        if (!alive) return;
        setItems(page.items);
        setNext(page.nextCursor);
      })
      .catch((err: unknown) => {
        if (!alive) return;
        setItems([]);
        setNext("");
        setFailure(describeProblem(err));
      })
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [key, attempt]);

  async function more() {
    setLoadingMore(true);
    try {
      const page = await loader.current(next);
      setItems((prev) => [...prev, ...page.items]);
      setNext(page.nextCursor);
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setLoadingMore(false);
    }
  }

  return { items, next, loading, loadingMore, failure, retry: () => setAttempt((n) => n + 1), more };
}

/**
 * Idempotency-Keys for the actions of a detail page: the same action on the same
 * version with the same input reuses its key, so a retry replays; anything else is a
 * new request with a new key.
 */
export function useActionKeys(): (action: string, version: number, input?: string) => string {
  const keys = useRef<Record<string, string>>({});
  return useCallback((action: string, version: number, input = "") => {
    const id = `${action}@${version}#${input}`;
    keys.current[id] ??= newIdempotencyKey();
    return keys.current[id];
  }, []);
}

/** One record loaded by id, with a reload. */
export function useRecord<T>(load: () => Promise<T>, key: string) {
  const [record, setRecord] = useState<T | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const loader = useRef(load);
  loader.current = load;
  const reload = useCallback(() => {
    let alive = true;
    setFailure(null);
    loader.current()
      .then((r) => alive && setRecord(r))
      .catch((err: unknown) => alive && setFailure(describeProblem(err)));
    return () => {
      alive = false;
    };
  }, []);
  useEffect(() => reload(), [reload, key]);
  return { record, setRecord, failure, reload };
}

export function amount(currency: string, value: string): string {
  return `${value} ${currency}`;
}
