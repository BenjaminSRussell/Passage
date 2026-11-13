import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Alert, Badge, Button, isForbidden, loadedRows, statusTone, usePagedRows, type ListData, type RowPage } from "../../shell/ui";
import { describeProblem, ProcurementApiError, type RankingRule } from "./api/client";
import { formatOption, resolveOption, type LookupEntityKey } from "../../shell/lookup/entities";
import { Named } from "./Named";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export const dash = "—";
export { Named };

export const rankingLabels: Record<RankingRule, string> = {
  LOWEST_PRICE: "Lowest price, then lead time",
  LEAD_TIME: "Shortest lead time, then price",
  WEIGHTED_SCORE: "Highest weighted score, then price",
};

/** An instant as "2026-11-01 17:00 UTC", whatever offset the service used. */
export function formatInstant(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${d.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

export interface Failure {
  message: string;
  code: string;
  errors?: Record<string, string>;
}

export interface Resource<T> {
  data: T | null;
  loading: boolean;
  error: Failure | null;
  reload(): void;
}

/**
 * Loads one resource with abort on unmount or dependency change; a stale
 * response never overwrites a newer one.
 */
export function useResource<T>(load: (signal: AbortSignal) => Promise<T>, deps: readonly unknown[]): Resource<T> {
  const [state, setState] = useState<{ data: T | null; loading: boolean; error: Failure | null }>({ data: null, loading: true, error: null });
  const [tick, setTick] = useState(0);
  const latest = useRef(0);

  useEffect(() => {
    const token = ++latest.current;
    const controller = new AbortController();
    setState((s) => ({ data: s.data, loading: true, error: null }));
    load(controller.signal).then(
      (data) => {
        if (token === latest.current) setState({ data, loading: false, error: null });
      },
      (failure: unknown) => {
        if (token === latest.current && !controller.signal.aborted) {
          setState({ data: null, loading: false, error: describeProblem(failure) });
        }
      },
    );
    return () => controller.abort();
  }, [...deps, tick]);

  const reload = useCallback(() => setTick((n) => n + 1), []);
  return { ...state, reload };
}

/**
 * ListData for a cursor-paged procurement list, for `ListPage`'s `useRows`. A new `key` (the filters) starts from the
 * first page; a 403 shows ListPage's no-access message.
 */
export function useProcurementPages<T>(key: string, fetchPage: (cursor: string | undefined, signal: AbortSignal) => Promise<RowPage<T>>): ListData<T> {
  const data = usePagedRows(key, fetchPage);
  const denied = data.error instanceof ProcurementApiError && data.error.problem.status === 403;
  return denied ? { ...data, forbidden: true } : data;
}

/** ListData for a procurement list loaded in full with `useResource`; a 403 shows ListPage's no-access message. */
export function resourceRows<T>(resource: Resource<T[]>): ListData<T> {
  const error = resource.error ?? undefined;
  return loadedRows({ data: resource.data, loading: resource.loading, error, reload: resource.reload }, error?.code === "403" || isForbidden(error));
}

/** Sentence-case label for an upper-snake status, e.g. IN_REVIEW → In review. */
export function statusLabel(status: string): string {
  const spaced = status.replace(/_/g, " ").toLowerCase();
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

export function StatusBadge({ status }: { status: string }) {
  return <Badge tone={statusTone(status)}>{statusLabel(status)}</Badge>;
}

/** Danger alert for a load failure with the stable problem code and a retry. */
export function LoadError({ what, error, onRetry }: { what: string; error: Failure; onRetry?: () => void }) {
  return (
    <Alert
      tone="danger"
      live="alert"
      title={`Could not load ${what}`}
      action={
        onRetry ? (
          <Button size="sm" onClick={onRetry}>
            Try again
          </Button>
        ) : undefined
      }
    >
      <ProblemMessage problem={error} />
    </Alert>
  );
}

/** A definition list of label/value pairs in a two-column grid. */
export function Details({ items }: { items: { label: string; value: ReactNode; mono?: boolean }[] }) {
  return (
    <dl className={styles.details}>
      {items.map((item) => (
        <div key={item.label} className={styles.entry}>
          <dt className={styles.term}>{item.label}</dt>
          <dd className={item.mono ? styles.mono : styles.value}>{item.value === "" || item.value === undefined ? dash : item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** Amount with its currency, kept as the decimal string the service returned. */
export function money(value: string, currency: string): string {
  return `${value} ${currency}`;
}

/** Shortens a UUID for table cells; the full id stays in the title attribute. */
export function ShortId({ id }: { id?: string }) {
  if (!id) return <>{dash}</>;
  return (
    <span className={styles.mono} title={id}>
      {id.slice(0, 8)}
    </span>
  );
}

/** The display text of an entity for places that cannot hold a component (button labels, notices). */
export function useEntityText(entity: LookupEntityKey, id: string | undefined, fallback: string): string {
  const [text, setText] = useState(fallback);
  useEffect(() => {
    let live = true;
    setText(fallback);
    if (id) {
      void resolveOption(entity, id).then((found) => {
        if (live && found) setText(formatOption(found));
      });
    }
    return () => {
      live = false;
    };
  }, [entity, id, fallback]);
  return text;
}

/** Display text for several entities at once, for labels built as strings; unresolved ids read as the fallback. */
export function useEntityNames(entity: LookupEntityKey, ids: string[], fallback: string): (id: string) => string {
  const [names, setNames] = useState<Record<string, string>>({});
  const key = [...new Set(ids)].sort().join(",");
  useEffect(() => {
    let live = true;
    for (const id of key === "" ? [] : key.split(",")) {
      void resolveOption(entity, id).then((found) => {
        if (live && found) setNames((prev) => (prev[id] ? prev : { ...prev, [id]: formatOption(found) }));
      });
    }
    return () => {
      live = false;
    };
  }, [entity, key]);
  return (id) => names[id] ?? fallback;
}

export const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const decimalPattern = /^[0-9]+(\.[0-9]+)?$/;
export const currencyPattern = /^[A-Z]{3}$/;
export const datePattern = /^\d{4}-\d{2}-\d{2}$/;

/** The user's local calendar date as YYYY-MM-DD (not UTC, which can be tomorrow in the evening). */
export function todayIso(now = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}
