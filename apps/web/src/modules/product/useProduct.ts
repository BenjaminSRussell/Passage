import { useCallback, useEffect, useState } from "react";
import { getProduct, listItemGroups, ProductApiError, type ItemGroup, type Product } from "./api/client";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

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
    if (!UUID.test(id ?? "")) {
      setState({ data: null, error: new ProductApiError(404, "COM-0404", "product not found"), loading: false });
      return;
    }
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

export function useItemGroups() {
  const [state, setState] = useState<{ groups: ItemGroup[]; failed: boolean }>({ groups: [], failed: false });
  useEffect(() => {
    const controller = new AbortController();
    listItemGroups(controller.signal).then(
      (groups) => setState({ groups, failed: false }),
      () => {
        if (!controller.signal.aborted) setState({ groups: [], failed: true });
      },
    );
    return () => controller.abort();
  }, []);
  return state;
}
