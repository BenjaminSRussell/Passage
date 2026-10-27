import type { ReactNode } from "react";
import { Alert, Badge, Button } from "../../shell/ui";
import { describeError } from "./api/client";
import { resultLabel, resultTone, type CheckLineResult } from "./logic";
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
      {describeError(error, "The budgeting service could not be reached.")}
    </Alert>
  );
}

export function ResultBadge({ result }: { result: CheckLineResult }) {
  return <Badge tone={resultTone(result)}>{resultLabel(result)}</Badge>;
}

/** Formats an ISO timestamp as a short local date and time, or a dash. */
export function formatDateTime(iso: string | undefined): string {
  if (!iso) return dash;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

/** "Open" / "PartiallyRelieved" / "Relieved" as sentence-case labels. */
export function statusLabel(status: string): string {
  return status === "PartiallyRelieved" ? "Partially relieved" : status;
}
