import { useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input } from "../../shell/ui";
import { applyFiniteDates, canApplyFiniteDates, describeProblem, type FiniteWindow, type Problem, type ProductionOrder } from "./api/client";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface FiniteDatesPanelProps {
  order: ProductionOrder;
  /** Called with the order the service answered, so the page shows its new dates and version. */
  onApplied(order: ProductionOrder): void;
}

interface Row {
  start: string;
  end: string;
}

/** A stored instant as the `datetime-local` value of its UTC minute. */
export const toField = (iso: string | undefined): string => (iso ? iso.slice(0, 16) : "");

/** The instant a field holds, read as UTC; undefined when it is empty or not a time. */
const fromField = (value: string): string | undefined => {
  const t = new Date(`${value}:00Z`);
  return value !== "" && !Number.isNaN(t.getTime()) ? t.toISOString() : undefined;
};

/**
 * Checks the rows as the service does and returns the windows to send: only operations whose start or end
 * changed. The windows, sent or kept, must follow the route (an operation may overlap the one before it but
 * not start or end before it). An error names the operation; a request with no change is refused here.
 */
export function buildWindows(
  order: ProductionOrder,
  rows: Record<number, Row>,
): { windows: FiniteWindow[]; error?: string } {
  const windows: FiniteWindow[] = [];
  for (const op of order.operations) {
    const row = rows[op.operNumber];
    if (!row || (row.start === toField(op.plannedStart) && row.end === toField(op.plannedEnd))) {
      continue;
    }
    const start = fromField(row.start);
    const end = fromField(row.end);
    if (!start || !end) {
      return { windows, error: `Enter a start and an end for operation ${op.operNumber}.` };
    }
    if (end < start) {
      return { windows, error: `Operation ${op.operNumber} ends before it starts.` };
    }
    windows.push({ operNumber: op.operNumber, start, end });
  }
  if (windows.length === 0) {
    return { windows, error: "Change at least one operation window." };
  }
  let previous: { operNumber: number; start: number; end: number } | undefined;
  for (const op of [...order.operations].sort((a, b) => a.operNumber - b.operNumber)) {
    const sent = windows.find((w) => w.operNumber === op.operNumber);
    const start = sent?.start ?? op.plannedStart;
    const end = sent?.end ?? op.plannedEnd;
    if (!start || !end) {
      continue;
    }
    const current = { operNumber: op.operNumber, start: Date.parse(start), end: Date.parse(end) };
    if (previous && (current.start < previous.start || current.end < previous.end)) {
      return {
        windows,
        error: `Operation ${current.operNumber} cannot start or end before operation ${previous.operNumber}: the windows must follow the route.`,
      };
    }
    previous = current;
  }
  return { windows };
}

/**
 * Finite-capacity dates (FR-23.10): the windows scheduling found for the operations replace the order's
 * infinite-capacity dates; the jobs follow inside them. An operation with reported feedback cannot move.
 */
export function FiniteDatesPanel({ order, onApplied }: FiniteDatesPanelProps) {
  const scheduled = order.operations.filter((op) => op.plannedStart && op.plannedEnd);
  const [rows, setRows] = useState<Record<number, Row>>(() =>
    Object.fromEntries(scheduled.map((op) => [op.operNumber, { start: toField(op.plannedStart), end: toField(op.plannedEnd) }])),
  );
  const [runId, setRunId] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  if (!canApplyFiniteDates(order.status) || scheduled.length === 0) {
    return null;
  }
  const locked = (status: string | undefined) => status !== undefined && status !== "open";

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const { windows, error: problem } = buildWindows(order, rows);
    setError(problem);
    setFailure(null);
    if (problem || busy) {
      return;
    }
    setBusy(true);
    try {
      const updated = await applyFiniteDates(order.id, order.version, windows, runId.trim() || undefined);
      onApplied(updated);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  };

  const set = (oper: number, patch: Partial<Row>) => setRows({ ...rows, [oper]: { ...rows[oper], ...patch } });
  return (
    <Card title="Finite-capacity dates">
      <form className={styles.stack} onSubmit={submit} noValidate aria-label="Apply finite dates">
        <p className={styles.hint}>
          Enter the windows scheduling found for the operations (UTC). The jobs of each operation share its window in proportion to their hours.
          Each operation may overlap the one before it but not start or end before it. An operation with reported feedback cannot move.
        </p>
        {scheduled.map((op) => (
          <div key={op.operNumber} className={styles.commandForm}>
            <FormField id={`finite-start-${op.operNumber}`} label={`Operation ${op.operNumber} start`}>
              {(control) => (
                <Input
                  {...control}
                  type="datetime-local"
                  value={rows[op.operNumber]?.start ?? ""}
                  disabled={locked(op.status)}
                  onChange={(e) => set(op.operNumber, { start: e.target.value })}
                />
              )}
            </FormField>
            <FormField id={`finite-end-${op.operNumber}`} label={`Operation ${op.operNumber} end`}>
              {(control) => (
                <Input
                  {...control}
                  type="datetime-local"
                  value={rows[op.operNumber]?.end ?? ""}
                  disabled={locked(op.status)}
                  onChange={(e) => set(op.operNumber, { end: e.target.value })}
                />
              )}
            </FormField>
          </div>
        ))}
        <FormField id="finite-run" label="Scheduling run" optional error={error}>
          {(control) => <Input {...control} value={runId} maxLength={64} onChange={(e) => setRunId(e.target.value)} />}
        </FormField>
        {failure ? (
          <Alert tone="danger" live="alert" title="The dates were refused">
            {<ProblemMessage problem={failure} />}
          </Alert>
        ) : null}
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Apply finite dates
          </Button>
        </div>
      </form>
    </Card>
  );
}
