import type { ReactNode } from "react";
import { Alert, Button } from "../../shell/ui";
import { describeError } from "./api/client";
import styles from "./pages.module.css";

export const dash = "—";

export function Detail({ term, children, mono = false }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

export function LoadError({ title, error, onRetry }: { title: string; error: unknown; onRetry(): void }) {
  return (
    <Alert
      tone="danger"
      title={title}
      live="alert"
      action={
        <Button size="sm" onClick={onRetry}>
          Try again
        </Button>
      }
    >
      {describeError(error, "The cost accounting service could not be reached.")}
    </Alert>
  );
}

export function ErrorList({ errors, failure }: { errors: string[]; failure: string | null }) {
  return (
    <Alert tone="danger" live="alert">
      {errors.length > 0 ? (
        <ul className={styles.errorList}>
          {errors.map((msg) => (
            <li key={msg}>{msg}</li>
          ))}
        </ul>
      ) : (
        failure
      )}
    </Alert>
  );
}

/** Formats an ISO timestamp as a short local date and time, or a dash. */
export function formatDateTime(iso: string | undefined): string {
  if (!iso) return dash;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

/** "CostCenter" → "Cost center". */
export function kindLabel(kind: string): string {
  const spaced = kind.replace(/([a-z])([A-Z])/g, "$1 $2").toLowerCase();
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

/** The current month as YYYY-MM. */
export function thisPeriod(): string {
  return new Date().toISOString().slice(0, 7);
}
