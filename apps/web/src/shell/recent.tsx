import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";

export interface RecentPage {
  to: string;
  label: string;
}

export const RECENT_LIMIT = 5;

const storageKey = (identity: string) => `passage.recent.${identity}`;

/** Newest first, without the page already at the top and without duplicates, at most RECENT_LIMIT long. */
export function withRecent(list: readonly RecentPage[], page: RecentPage): RecentPage[] {
  return [page, ...list.filter((p) => p.to !== page.to)].slice(0, RECENT_LIMIT);
}

function read(identity: string): RecentPage[] {
  if (identity === "") return [];
  try {
    const parsed: unknown = JSON.parse(window.localStorage.getItem(storageKey(identity)) ?? "[]");
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter((p): p is RecentPage => typeof p?.to === "string" && p.to.startsWith("/") && typeof p?.label === "string")
      .slice(0, RECENT_LIMIT);
  } catch {
    return [];
  }
}

function write(identity: string, list: RecentPage[]): void {
  if (identity === "") return;
  try {
    window.localStorage.setItem(storageKey(identity), JSON.stringify(list));
  } catch {
    return;
  }
}

interface RecentValue {
  pages: RecentPage[];
  record(page: RecentPage): void;
}

const RecentContext = createContext<RecentValue>({ pages: [], record: () => undefined });

export function useRecentPages(): RecentValue {
  return useContext(RecentContext);
}

/** The pages this person opened last, kept in this browser only, per signed-in identity. */
export function RecentProvider({ identity, children }: { identity: string; children: ReactNode }) {
  const [pages, setPages] = useState<RecentPage[]>(() => read(identity));
  const record = useCallback(
    (page: RecentPage) =>
      setPages((current) => {
        if (current[0]?.to === page.to && current[0].label === page.label) return current;
        const next = withRecent(current, page);
        write(identity, next);
        return next;
      }),
    [identity],
  );
  const value = useMemo(() => ({ pages, record }), [pages, record]);
  return <RecentContext.Provider value={value}>{children}</RecentContext.Provider>;
}
