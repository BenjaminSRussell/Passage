import { useCallback, useEffect, useState } from "react";

export interface FlowState<T> {
  data?: T;
  error?: unknown;
  loading: boolean;
  reload: () => void;
}

/**
 * Loads a document flow without needing a query client, so the panel can sit on any page. It loads
 * again when `key` changes (pass something that changes when the page's own document does, such as its
 * status or version) and when `reload` is called.
 */
export function useFlow<T>(load: () => Promise<T>, key: string): FlowState<T> {
  const [state, setState] = useState<{ data?: T; error?: unknown; loading: boolean }>({ loading: true });
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let live = true;
    setState((s) => ({ data: s.data, loading: true }));
    load().then(
      (data) => live && setState({ data, loading: false }),
      (error: unknown) => live && setState({ error, loading: false }),
    );
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, tick]);
  const reload = useCallback(() => setTick((n) => n + 1), []);
  return { ...state, reload };
}
