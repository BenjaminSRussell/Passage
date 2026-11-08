import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Alert, Badge, Button, type BadgeTone } from "../../shell/ui";
import { describeProblem } from "./api/client";
import styles from "./pages.module.css";

export const dash = "—";

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

const tones: Record<string, BadgeTone> = {
  DRAFT: "neutral",
  IN_REVIEW: "info",
  APPROVED: "success",
  CONFIRMED: "success",
  ACTIVE: "success",
  REJECTED: "danger",
  CANCELLED: "neutral",
  CLOSED: "neutral",
  FINALIZED: "success",
  EXPIRED: "warning",
  OPEN: "neutral",
  CONSOLIDATED: "success",
  EFFECTIVE: "success",
  ON_HOLD: "warning",
  SENT: "info",
  REPLIES_OPEN: "info",
  AWARDED: "success",
  INVITED: "neutral",
  REPLIED: "success",
  INVOICED: "success",
  STAGED: "info",
  COMMITTED: "success",
  ACCEPTABLE: "info",
  ERROR: "danger",
  ACCEPTED: "success",
  PENDING: "warning",
  APPLIED: "success",
  REFUSED: "danger",
  IGNORED: "neutral",
  COMPLETED: "success",
  PARTIAL: "warning",
  FAILED: "danger",
  EMPTY: "neutral",
};

/** Sentence-case label for an upper-snake status, e.g. IN_REVIEW → In review. */
export function statusLabel(status: string): string {
  const spaced = status.replace(/_/g, " ").toLowerCase();
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

export function StatusBadge({ status }: { status: string }) {
  return <Badge tone={tones[status] ?? "neutral"}>{statusLabel(status)}</Badge>;
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
      {error.message} ({error.code})
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

export const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const decimalPattern = /^[0-9]+(\.[0-9]+)?$/;
export const currencyPattern = /^[A-Z]{3}$/;
export const datePattern = /^\d{4}-\d{2}-\d{2}$/;

/** The user's local calendar date as YYYY-MM-DD (not UTC, which can be tomorrow in the evening). */
export function todayIso(now = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}
