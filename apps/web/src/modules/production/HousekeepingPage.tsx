import { useState } from "react";
import { Alert, Badge, Button, Card, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  newIdempotencyKey,
  runHousekeeping,
  type BulkItem,
  type HousekeepingJob,
  type HousekeepingOrder,
  type HousekeepingResult,
  type Problem,
} from "./api/client";
import { dash, statusLabel } from "./labels";
import styles from "./pages.module.css";

const candidateColumns: DataTableColumn<HousekeepingOrder>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "siteId", header: "Site", mono: true },
  { key: "status", header: "Status", render: (row) => statusLabel[row.status] },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "qtyReportedFinished", header: "Reported finished", align: "end" },
  { key: "ageDays", header: "Age (days)", align: "end" },
];

function resultColumns(numbers: Record<string, string>): DataTableColumn<BulkItem>[] {
  return [
    { key: "orderId", header: "Order", mono: true, render: (row) => numbers[row.orderId] ?? row.orderId },
    { key: "ok", header: "Result", render: (row) => (row.ok ? <Badge tone="success">{row.replayed ? "Already done" : "Done"}</Badge> : <Badge tone="danger">Refused</Badge>) },
    { key: "status", header: "Status", render: (row) => (row.status ? statusLabel[row.status] : dash) },
    { key: "detail", header: "Reason", render: (row) => (row.ok ? dash : `${row.detail ?? "Failed"} (${row.code ?? "COM-0500"})`) },
  ];
}

interface JobProps {
  job: HousekeepingJob;
  title: string;
  description: string;
  /** Names the run button, with the count: "Cancel 3 orders". */
  verb: string;
  confirm: string;
  nothing: { title: string; description: string };
}

/**
 * One housekeeping job: find the orders it would act on (a dry run), review them, then run it. The run uses the
 * service's own `:cancel` or `:end` command per order, so a refusal is listed with its reason and nothing else is undone.
 */
function JobCard({ job, title, description, verb, confirm, nothing }: JobProps) {
  const [busy, setBusy] = useState<"find" | "run" | null>(null);
  const [found, setFound] = useState<HousekeepingResult | null>(null);
  const [outcome, setOutcome] = useState<{ result: HousekeepingResult; numbers: Record<string, string>; reviewed: number } | null>(null);
  const [error, setError] = useState<Problem | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [key, setKey] = useState(newIdempotencyKey);

  const find = async () => {
    setBusy("find");
    setError(null);
    setConfirming(false);
    setOutcome(null);
    try {
      setFound(await runHousekeeping(job, true, key));
    } catch (failure) {
      setFound(null);
      setError(describeProblem(failure));
    } finally {
      setBusy(null);
    }
  };

  const run = async () => {
    const reviewed = found?.candidates ?? [];
    const numbers = Object.fromEntries(reviewed.map((o) => [o.orderId, o.orderNumber]));
    setBusy("run");
    setError(null);
    try {
      // Only the reviewed orders: one that stopped qualifying since is left out by the service.
      const result = await runHousekeeping(
        job,
        false,
        key,
        reviewed.map((o) => o.orderId),
      );
      setOutcome({ result, numbers, reviewed: reviewed.length });
      setFound(null);
      setConfirming(false);
      setKey(newIdempotencyKey());
    } catch (failure) {
      setError(describeProblem(failure));
    } finally {
      setBusy(null);
    }
  };

  const count = found?.candidates.length ?? 0;
  return (
    <Card title={title}>
      <div className={styles.stack}>
        <p className={styles.hint}>{description}</p>
        <div className={styles.actions}>
          <Button variant="secondary" loading={busy === "find"} onClick={() => void find()}>
            Find orders
          </Button>
        </div>
        {error ? (
          <Alert tone={error.status === 403 ? "info" : "danger"} title="The job could not run" live="alert">
            {error.status === 403 ? "You need the duty of this command to use this job." : `${error.message} (${error.code})`}
          </Alert>
        ) : null}
        {found ? (
          <>
            <DataTable
              label={`${title} candidates`}
              columns={candidateColumns}
              rows={found.candidates}
              getRowKey={(row) => row.orderId}
              empty={nothing}
            />
            {found.truncated ? (
              <Alert tone="info">
                {found.total} orders qualify; one run takes the first {count}. Run it again for the rest.
              </Alert>
            ) : null}
            {count > 0 ? (
              <div className={styles.actions}>
                <Button variant="primary" onClick={() => setConfirming(true)}>
                  {verb} {count} {count === 1 ? "order" : "orders"}
                </Button>
              </div>
            ) : null}
            {confirming ? (
              <Alert
                tone="warning"
                title={`${verb} ${count} ${count === 1 ? "order" : "orders"}?`}
                action={
                  <div className={styles.actions}>
                    <Button variant="danger" loading={busy === "run"} onClick={() => void run()}>
                      Yes, {verb.toLowerCase()} them
                    </Button>
                    <Button variant="ghost" onClick={() => setConfirming(false)}>
                      Leave them as they are
                    </Button>
                  </div>
                }
              >
                {confirm}
              </Alert>
            ) : null}
          </>
        ) : null}
        {outcome ? (
          <>
            <Alert
              tone={outcome.result.failed > 0 ? "warning" : "success"}
              live="status"
              title={`${outcome.result.succeeded} succeeded, ${outcome.result.failed} failed`}
            >
              {outcome.result.failed > 0
                ? "The refused orders are unchanged; fix the reason listed and run the job again."
                : outcome.result.results.length === 0
                  ? "No order qualified."
                  : "Every order was updated."}
              {outcome.result.replayed ? " This run had already been done; its results are shown again." : ""}
              {!outcome.result.replayed && outcome.result.results.length < outcome.reviewed
                ? ` ${outcome.reviewed - outcome.result.results.length} of the reviewed orders no longer qualified and were left as they are.`
                : ""}
            </Alert>
            {outcome.result.results.length > 0 ? (
              <DataTable label={`${title} results`} columns={resultColumns(outcome.numbers)} rows={outcome.result.results} getRowKey={(row) => row.orderId} />
            ) : null}
          </>
        ) : null}
      </div>
    </Card>
  );
}

/**
 * Order housekeeping (PRD 4.21.16): cancel the orders left Created beyond their site's stale age, and end the orders
 * that are fully reported as finished at the sites that opted in. Both are set per site on the parameters page.
 */
export function HousekeepingPage() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Order housekeeping"
        description="Clean up the order book: stale orders that were never estimated, and fully reported orders waiting for End. Find the orders first, review them, then run the job."
      />
      <JobCard
        job="cancel-stale"
        title="Cancel stale orders"
        description="Orders still Created (never estimated) for longer than their site's stale-order days are cancelled. Orders that planning created are left to planning."
        verb="Cancel"
        confirm="Cancelled orders cannot be reopened. Any vendor purchase request of the order is withdrawn."
        nothing={{ title: "No stale orders", description: "No Created order is older than its site's stale-order days." }}
      />
      <JobCard
        job="auto-end"
        title="End fully reported orders"
        description="Orders reported as finished for their whole quantity, with no draft journal, at sites that turned auto-end on, are ended and settled."
        verb="End"
        confirm="Ending settles the order's cost and clears its WIP into variances. Ended orders cannot be reset."
        nothing={{ title: "No orders to end", description: "No fully reported order is waiting at a site with auto-end turned on." }}
      />
    </div>
  );
}
