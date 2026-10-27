import { useState } from "react";
import { Alert, Button, type BadgeTone } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { dash } from "./format";
import styles from "./pages.module.css";

/** Dimensions a slice 2 form can name (owner and license_plate are reserved). */
export const FORM_DIMENSIONS = ["site", "warehouse", "location", "batch", "serial"] as const;
export type FormDimension = (typeof FORM_DIMENSIONS)[number];

export const emptyDimensions = (): Record<FormDimension, string> => ({ site: "", warehouse: "", location: "", batch: "", serial: "" });

/** All dimensions in canonical order, for describing stored dimension sets. */
export const ALL_DIMENSIONS = ["site", "warehouse", "location", "batch", "serial", "inventory_status", "config", "size", "color", "style", "owner", "segment"] as const;

export function Detail({ term, children, mono = false }: { term: string; children: string; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

export function ProblemAlert({ title, problem, onRetry }: { title: string; problem: Problem; onRetry?: () => void }) {
  return (
    <Alert tone="danger" title={title} live="alert" action={onRetry ? <Button size="sm" onClick={onRetry}>Try again</Button> : undefined}>
      {problem.message} ({problem.code})
    </Alert>
  );
}

/** Maps a problem's field errors onto a form's own field names. */
export function fieldErrorsOf<K extends string>(problem: Problem, names: readonly K[]): Partial<Record<K, string>> {
  const out: Partial<Record<K, string>> = {};
  for (const [name, message] of Object.entries(problem.fields)) {
    const field = name.startsWith("dimensions") ? "dimensions" : name;
    if ((names as readonly string[]).includes(field)) out[field as K] = message;
  }
  return out;
}

export const stateTones: Record<string, BadgeTone> = {
  ACTIVE: "warning",
  RELEASED: "neutral",
  CONSUMED: "success",
  OPEN: "info",
  POSTED: "success",
  BLOCKED: "danger",
  QUARANTINE: "warning",
  CREATED: "info",
  SHIPPED: "warning",
  PARTIALLY_RECEIVED: "warning",
  RECEIVED: "success",
  CANCELLED: "neutral",
};

export function money(amount: string | undefined, currency: string): string {
  return amount === undefined ? dash : `${amount} ${currency}`.trim();
}

/**
 * A row action that runs once, shows its own busy state and reports failure
 * through onError, so a table can hold one per row.
 */
export function RowAction({ label, name, run, onDone, onError }: {
  label: string;
  name: string;
  run(): Promise<unknown>;
  onDone(): void;
  onError(problem: Problem): void;
}) {
  const [busy, setBusy] = useState(false);
  async function click() {
    setBusy(true);
    try {
      await run();
      onDone();
    } catch (error) {
      onError(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Button size="sm" loading={busy} onClick={click} aria-label={`${label} ${name}`}>
      {label}
    </Button>
  );
}
