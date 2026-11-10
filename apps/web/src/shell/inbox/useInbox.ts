import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError } from "../api/http";
import { fetchInbox, type InboxItem } from "./inbox";

export type InboxStatus = "off" | "loading" | "ready" | "error";

export interface InboxState {
  status: InboxStatus;
  items: InboxItem[];
  unavailable: string[];
  truncated: string[];
  hasMore: boolean;
  loadingMore: boolean;
  problem: ApiError | null;
  sourcesChanged: boolean;
  needsEntity: boolean;
  loadMore(): void;
  reload(): void;
  remove(item: InboxItem): void;
}

interface Data {
  items: InboxItem[];
  unavailable: string[];
  truncated: string[];
  nextCursor?: string;
}

const empty: Data = { items: [], unavailable: [], truncated: [] };
const union = (a: string[], b: string[]) => [...new Set([...a, ...b])];

export function useInbox(legalEntityId: string, enabled: boolean): InboxState {
  const [data, setData] = useState<Data>(empty);
  const [status, setStatus] = useState<InboxStatus>(enabled ? "loading" : "off");
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [sourcesChanged, setSourcesChanged] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const latest = useRef(0);
  const cursor = useRef<string | undefined>(undefined);
  const moreController = useRef<AbortController | null>(null);
  cursor.current = data.nextCursor;

  useEffect(() => {
    const token = ++latest.current;
    if (!enabled) {
      setStatus("off");
      return;
    }
    const controller = new AbortController();
    setStatus("loading");
    setProblem(null);
    setSourcesChanged(false);
    setLoadingMore(false);
    fetchInbox({ legalEntityId }, controller.signal).then(
      (page) => {
        if (token !== latest.current) return;
        setData({
          items: page.items,
          unavailable: page.unavailable.map((u) => u.source),
          truncated: page.truncated,
          nextCursor: page.nextCursor,
        });
        setStatus("ready");
      },
      (failure: unknown) => {
        if (token !== latest.current || controller.signal.aborted) return;
        setData(empty);
        setProblem(failure instanceof ApiError ? failure : new ApiError(0, "COM-0500", "The request failed"));
        setStatus("error");
      },
    );
    return () => {
      controller.abort();
      moreController.current?.abort();
    };
  }, [legalEntityId, enabled, attempt]);

  const loadMore = useCallback(() => {
    const next = cursor.current;
    if (!next) return;
    const token = latest.current;
    const controller = new AbortController();
    moreController.current = controller;
    setLoadingMore(true);
    setProblem(null);
    fetchInbox({ legalEntityId, cursor: next }, controller.signal).then(
      (page) => {
        if (token !== latest.current || controller.signal.aborted) return;
        setData((prev) => ({
          items: [...prev.items, ...page.items],
          unavailable: union(prev.unavailable, page.unavailable.map((u) => u.source)),
          truncated: union(prev.truncated, page.truncated),
          nextCursor: page.nextCursor,
        }));
        setLoadingMore(false);
      },
      (failure: unknown) => {
        if (token !== latest.current || controller.signal.aborted) return;
        setLoadingMore(false);
        if (failure instanceof ApiError && failure.status === 409) setSourcesChanged(true);
        else setProblem(failure instanceof ApiError ? failure : new ApiError(0, "COM-0500", "The request failed"));
      },
    );
  }, [legalEntityId]);

  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  const remove = useCallback(
    (item: InboxItem) => setData((prev) => ({ ...prev, items: prev.items.filter((i) => !(i.id === item.id && i.source === item.source)) })),
    [],
  );

  return {
    status,
    items: data.items,
    unavailable: data.unavailable,
    truncated: data.truncated,
    hasMore: data.nextCursor !== undefined,
    loadingMore,
    problem,
    sourcesChanged,
    needsEntity: status === "error" && problem?.status === 400 && legalEntityId === "",
    loadMore,
    reload,
    remove,
  };
}
