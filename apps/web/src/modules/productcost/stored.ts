import { useCallback, useEffect, useRef, useState } from "react";
import { describeProblem, newIdempotencyKey, type Problem } from "./api/client";

export interface Loaded<T> {
  data: T | null;
  loading: boolean;
  error: Problem | null;
  reload(): void;
}

/**
 * Loads stored records once and on reload(). A response that arrives after a
 * newer request started is dropped, and an unmounted page aborts its request.
 */
export function useLoad<T>(load: (signal: AbortSignal) => Promise<T>): Loaded<T> {
  const [state, setState] = useState<{ data: T | null; loading: boolean; error: Problem | null }>({ data: null, loading: true, error: null });
  const [generation, setGeneration] = useState(0);
  const latest = useRef(0);
  const loader = useRef(load);
  loader.current = load;

  useEffect(() => {
    const token = ++latest.current;
    const controller = new AbortController();
    setState((s) => ({ ...s, loading: true, error: null }));
    loader.current(controller.signal).then(
      (data) => {
        if (token === latest.current) setState({ data, loading: false, error: null });
      },
      (failure: unknown) => {
        if (token === latest.current && !controller.signal.aborted) setState({ data: null, loading: false, error: describeProblem(failure) });
      },
    );
    return () => controller.abort();
  }, [generation]);

  const reload = useCallback(() => setGeneration((g) => g + 1), []);
  return { ...state, reload };
}

/**
 * Idempotency-Key per create request: a retry of the same body (after a
 * network failure or a refusal) reuses its key, so the service replays rather
 * than creating twice; a changed body gets a new key, because the service
 * refuses a key reused with another body. forget() after a success.
 */
export function useIdempotencyKey() {
  const last = useRef<{ body: string; key: string } | null>(null);
  const keyFor = useCallback((body: unknown) => {
    const text = JSON.stringify(body);
    if (last.current?.body !== text) last.current = { body: text, key: newIdempotencyKey() };
    return last.current.key;
  }, []);
  const forget = useCallback(() => {
    last.current = null;
  }, []);
  return { keyFor, forget };
}

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Whether s is a UUID, the form of every stored id. */
export const isUuid = (s: string) => uuid.test(s.trim());

/** The code rule of cost components and cost groups (FR-39.1). */
export const structureCode = /^[A-Z0-9][A-Z0-9-]{0,19}$/;
