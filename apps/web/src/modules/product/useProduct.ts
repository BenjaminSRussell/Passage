import { useCallback, useEffect, useState } from "react";
import { getProduct, type Product } from "./api/client";

interface State {
  data: Product | null;
  error: unknown;
  loading: boolean;
}

export function useProduct(id: string | undefined) {
  const [state, setState] = useState<State>({ data: null, error: null, loading: true });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setState({ data: null, error: null, loading: true });
    getProduct(id ?? "", controller.signal).then(
      (data) => setState({ data, error: null, loading: false }),
      (error: unknown) => {
        if (!controller.signal.aborted) setState({ data: null, error, loading: false });
      },
    );
    return () => controller.abort();
  }, [id, attempt]);

  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  return { ...state, reload };
}
