import { useState } from "react";
import { Link } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, DataTable, FormField, Input, PageHeader, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { autoCloseOrders, describeProblem, type AutoCloseItem, type AutoCloseRequest, type AutoCloseResultKind, type AutoCloseRun, type Problem } from "./api/client";
import { formatDateTime, sentence, UUID } from "./format";
import styles from "./pages.module.css";

/** Running the close or its preview needs this duty (SalesOrderMaintain); the service enforces it. */
const DUTY_ORDER_WRITE = "sales.order.write";

export const DEFAULT_GRACE_DAYS = 30;
export const DEFAULT_LIMIT = 100;

export interface AutoCloseDraft {
  legalEntity: string;
  graceDays: string;
  limit: string;
}

/** Turns the form into the request, or the field errors that stop it. */
export function autoCloseRequest(draft: AutoCloseDraft, dryRun: boolean): { request: AutoCloseRequest } | { errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  const le = draft.legalEntity.trim();
  if (le && !UUID.test(le)) errors.legalEntity = "Enter a UUID.";
  const grace = draft.graceDays.trim();
  if (!/^\d{1,3}$/.test(grace) || Number(grace) > 365) errors.graceDays = "Enter whole days from 0 to 365.";
  const limit = draft.limit.trim();
  if (!/^\d{1,3}$/.test(limit) || Number(limit) < 1 || Number(limit) > 500) errors.limit = "Enter a number from 1 to 500.";
  if (Object.keys(errors).length > 0) return { errors };
  return { request: { legalEntityId: le || undefined, graceDays: Number(grace), limit: Number(limit), dryRun } };
}

/** Sentence for the outcome of a run. */
export function autoCloseText(run: AutoCloseRun, dryRun: boolean): string {
  const more = run.more ? " More are left than the limit; run it again." : "";
  if (dryRun) {
    if (run.items.length === 0) return "No order is ready to close.";
    return `${run.items.length} ${run.items.length === 1 ? "order is" : "orders are"} ready to close.${more}`;
  }
  if (run.closed === 0 && run.skipped === 0) return "No order was ready to close.";
  const skipped = run.skipped > 0 ? ` ${run.skipped} changed in the meantime and ${run.skipped === 1 ? "was" : "were"} left open.` : "";
  return `${run.closed} ${run.closed === 1 ? "order" : "orders"} closed.${skipped}${more}`;
}

const tone: Record<AutoCloseResultKind, BadgeTone> = { CLOSED: "success", CANDIDATE: "info", SKIPPED: "warning" };

const columns: DataTableColumn<AutoCloseItem>[] = [
  { key: "orderNumber", header: "Order", mono: true, render: (i) => <Link to={`/sales/orders/${i.orderId}`}>{i.orderNumber}</Link> },
  { key: "lastChangedAt", header: "Last changed", render: (i) => formatDateTime(i.lastChangedAt) },
  { key: "version", header: "Version", align: "end", render: (i) => String(i.version) },
  { key: "result", header: "Result", render: (i) => <Badge tone={tone[i.result]}>{sentence(i.result)}</Badge> },
];

/**
 * Close invoiced orders (PRD §5.27.16, BR-27.5): the nightly auto-close, run by
 * hand. Preview lists the INVOICED orders that nothing has changed for the
 * grace period and that carry no active hold; Close moves them to CLOSED.
 */
export function OrderAutoClosePage() {
  const canClose = holdsDuty(usePrincipalDuties(), DUTY_ORDER_WRITE);
  const [draft, setDraft] = useState<AutoCloseDraft>({ legalEntity: "", graceDays: String(DEFAULT_GRACE_DAYS), limit: String(DEFAULT_LIMIT) });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [running, setRunning] = useState<"preview" | "close" | null>(null);
  const [run, setRun] = useState<{ result: AutoCloseRun; dryRun: boolean } | null>(null);
  const [problem, setProblem] = useState<Problem | null>(null);

  async function start(dryRun: boolean) {
    const outcome = autoCloseRequest(draft, dryRun);
    if ("errors" in outcome) {
      setErrors(outcome.errors);
      return;
    }
    setErrors({});
    setProblem(null);
    setRun(null);
    setRunning(dryRun ? "preview" : "close");
    try {
      setRun({ result: await autoCloseOrders(outcome.request), dryRun });
    } catch (failure) {
      setProblem(describeProblem(failure));
    } finally {
      setRunning(null);
    }
  }

  const set = (key: keyof AutoCloseDraft, value: string) => setDraft((d) => ({ ...d, [key]: value }));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Close invoiced orders"
        description="An order that is fully invoiced and has not changed for the grace period, with no active hold, moves to Closed and takes no further delivery, invoice, change or cancel. The same run happens nightly."
      />
      <form className={styles.toolbar} onSubmit={(e) => e.preventDefault()} noValidate aria-label="Close invoiced orders">
        <FormField id="ac-le" label="Legal entity" error={errors.legalEntity} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntity} onChange={(e) => set("legalEntity", e.target.value)} invalid={Boolean(errors.legalEntity)} />}
        </FormField>
        <FormField id="ac-grace" label="Unchanged for (days)" error={errors.graceDays}>
          {(control) => <Input {...control} inputMode="numeric" value={draft.graceDays} onChange={(e) => set("graceDays", e.target.value)} invalid={Boolean(errors.graceDays)} />}
        </FormField>
        <FormField id="ac-limit" label="Most orders" error={errors.limit}>
          {(control) => <Input {...control} inputMode="numeric" value={draft.limit} onChange={(e) => set("limit", e.target.value)} invalid={Boolean(errors.limit)} />}
        </FormField>
        {/* The preview is the same POST as the close (dryRun=true), so it needs the write duty too. */}
        {canClose ? (
          <>
            <Button variant="secondary" loading={running === "preview"} disabled={running !== null} onClick={() => void start(true)}>
              Preview
            </Button>
            <Button loading={running === "close"} disabled={running !== null} onClick={() => void start(false)}>
              Close orders
            </Button>
          </>
        ) : (
          <span className={styles.muted}>Previewing and closing orders needs the {DUTY_ORDER_WRITE} duty.</span>
        )}
      </form>
      {problem ? (
        <Alert tone="danger" title="The run could not finish" live="alert">
          {problem.message} ({problem.code})
        </Alert>
      ) : null}
      <Alert tone={run && run.result.skipped > 0 ? "warning" : "success"} live="status">
        {run ? autoCloseText(run.result, run.dryRun) : null}
      </Alert>
      {run ? (
        <DataTable
          label="Orders in this run"
          columns={columns}
          rows={run.result.items}
          getRowKey={(i) => i.orderId}
          empty={{ title: "No orders", description: "No invoiced order has been unchanged for that long without an active hold." }}
        />
      ) : (
        <p className={styles.muted}>Preview lists the orders that would close; nothing changes until Close orders.</p>
      )}
    </div>
  );
}
