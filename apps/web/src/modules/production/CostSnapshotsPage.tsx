import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listCostSnapshots, runCostSnapshot, type CostSnapshot, type Problem } from "./api/client";
import { statusLabel, statusTone } from "./labels";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface CostSnapshotsPageProps {
  /** Opens a production order from its row. */
  onOpenOrder?(orderId: string): void;
}

interface Filter {
  orderId: string;
  from: string;
  to: string;
}

/** Checks the filter as the service does: the order is an id and the period is in order. */
export function validateSnapshotFilter(f: Filter): Record<string, string> {
  const errors: Record<string, string> = {};
  if (f.orderId.trim() !== "" && !uuidPattern.test(f.orderId.trim())) {
    errors.orderId = "Enter the order's id (a UUID), or leave it empty.";
  }
  if (f.from !== "" && f.to !== "" && f.from > f.to) {
    errors.to = "The end of the period must not be before its start.";
  }
  return errors;
}

const columns: DataTableColumn<CostSnapshot>[] = [
  { key: "snapshotDate", header: "Day", mono: true },
  { key: "orderNumber", header: "Order", mono: true },
  { key: "status", header: "Status", render: (row) => <Badge tone={statusTone[row.status]}>{statusLabel[row.status] ?? row.status}</Badge> },
  { key: "qtyReported", header: "Reported / ordered", align: "end", render: (row) => `${row.qtyReported} / ${row.qtyOrdered}` },
  { key: "plannedCost", header: "Planned cost", align: "end", render: (row) => `${row.plannedCost} ${row.currency}` },
  { key: "actualCost", header: "Actual cost", align: "end", render: (row) => `${row.actualCost} ${row.currency}` },
  { key: "costVariance", header: "Actual less planned", align: "end", render: (row) => `${row.costVariance} ${row.currency}` },
  { key: "wipBalance", header: "WIP balance", align: "end", render: (row) => `${row.wipBalance} ${row.currency}` },
];

interface State {
  rows: CostSnapshot[];
  nextCursor?: string;
  loading: boolean;
  loadingMore: boolean;
  error: Problem | null;
}

const initial: State = { rows: [], loading: true, loadingMore: false, error: null };

/**
 * Order cost snapshots (PRD 4.21.16): the planned and actual cost and the WIP balance of every open order, one row
 * per order and day, for the trend of the order KPIs. Taking the snapshot for a day replaces that day's rows.
 */
export function CostSnapshotsPage({ onOpenOrder }: CostSnapshotsPageProps) {
  const [draft, setDraft] = useState<Filter>({ orderId: "", from: "", to: "" });
  const [filter, setFilter] = useState<Filter>(draft);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [state, setState] = useState<State>(initial);
  const [reload, setReload] = useState(0);
  const [runDate, setRunDate] = useState("");
  const [running, setRunning] = useState(false);
  const [notice, setNotice] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const latest = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const token = ++latest.current;
    setState(initial);
    listCostSnapshots({ orderId: filter.orderId.trim() || undefined, from: filter.from || undefined, to: filter.to || undefined }, controller.signal).then(
      (page) => token === latest.current && setState({ rows: page.items, nextCursor: page.nextCursor, loading: false, loadingMore: false, error: null }),
      (error) => !controller.signal.aborted && setState({ ...initial, loading: false, error: describeProblem(error) }),
    );
    return () => controller.abort();
  }, [filter, reload]);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    const problems = validateSnapshotFilter(draft);
    setErrors(problems);
    if (Object.keys(problems).length === 0) {
      setFilter({ ...draft });
    }
  };

  const loadMore = async () => {
    if (!state.nextCursor) {
      return;
    }
    setState((s) => ({ ...s, loadingMore: true }));
    try {
      const page = await listCostSnapshots({
        orderId: filter.orderId.trim() || undefined,
        from: filter.from || undefined,
        to: filter.to || undefined,
        cursor: state.nextCursor,
      });
      setState((s) => ({ ...s, rows: [...s.rows, ...page.items], nextCursor: page.nextCursor, loadingMore: false }));
    } catch (error) {
      setState((s) => ({ ...s, loadingMore: false, error: describeProblem(error) }));
    }
  };

  const run = async (event: FormEvent) => {
    event.preventDefault();
    setNotice("");
    setFailure(null);
    setRunning(true);
    try {
      const result = await runCostSnapshot(runDate || undefined);
      setNotice(`Snapshot for ${result.snapshotDate} taken: ${result.orders} open ${result.orders === 1 ? "order" : "orders"}.`);
      setReload((n) => n + 1);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setRunning(false);
    }
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Order cost snapshots"
        description="Planned cost, actual cost and WIP balance of the open orders, one row per order and day: the trend behind the production KPIs."
      />
      <Card>
        <form className={styles.commandForm} onSubmit={run} aria-label="Take a snapshot">
          <FormField id="snap-run-date" label="Snapshot day" optional hint="Empty is today. Taking a day again overwrites its rows; actual cost and WIP count the postings dated up to the day.">
            {(control) => <Input {...control} type="date" value={runDate} onChange={(e) => setRunDate(e.target.value)} />}
          </FormField>
          <Button type="submit" variant="primary" loading={running}>
            Take snapshot
          </Button>
        </form>
        {notice ? <Alert tone="success" title="Snapshot taken" live="status">{notice}</Alert> : null}
        {failure ? (
          <Alert tone={failure.status === 403 ? "info" : "danger"} title="The snapshot was not taken" live="alert">
            {failure.status === 403 ? "You need the View production costs duty to take a snapshot." : <ProblemMessage problem={failure} />}
          </Alert>
        ) : null}
      </Card>
      <Card>
        <form className={styles.commandForm} onSubmit={apply} noValidate aria-label="Filter the snapshots">
          <FormField id="snap-order" label="Order" optional error={errors.orderId}>
            {(control) => <Input {...control} value={draft.orderId} onChange={(e) => setDraft({ ...draft, orderId: e.target.value })} />}
          </FormField>
          <FormField id="snap-from" label="From" optional>
            {(control) => <Input {...control} type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />}
          </FormField>
          <FormField id="snap-to" label="To" optional error={errors.to}>
            {(control) => <Input {...control} type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />}
          </FormField>
          <Button type="submit" variant="primary" loading={state.loading}>
            Show
          </Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="The cost snapshots are not available" live="alert">
          {state.error.status === 403 ? "You need the View production costs duty to see the snapshots." : <ProblemMessage problem={state.error} />}
        </Alert>
      ) : (
        <>
          <DataTable
            label="Order cost snapshots"
            columns={columns}
            rows={state.rows}
            loading={state.loading}
            getRowKey={(row) => `${row.snapshotDate}:${row.orderId}`}
            onRowOpen={onOpenOrder ? (row) => onOpenOrder(row.orderId) : undefined}
            empty={{ title: "No snapshots yet", description: "Take a snapshot to store today's planned and actual cost of the open orders." }}
          />
          {state.nextCursor && !state.loading ? (
            <div className={styles.more}>
              <Button onClick={loadMore} loading={state.loadingMore}>
                Load more snapshots
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
