import { useEffect, useRef, useState } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  BULK_COMMANDS,
  MAX_BULK,
  REWORK,
  SPLIT,
  STATUSES,
  bulkStatus,
  canSplit,
  describeProblem,
  listOrders,
  mergeOrders,
  newIdempotencyKey,
  type BulkCommand,
  type BulkItem,
  type BulkResult,
  type OrderSummary,
  type Problem,
  type ProductionStatus,
} from "./api/client";
import { amount, bulkCommandLabel, dash, statusLabel, statusTone } from "./labels";
import styles from "./pages.module.css";

export interface ProductionOrdersPageProps {
  /** Opens an order's detail page. */
  onOpen?(order: OrderSummary): void;
  /** Opens the create form; the button is hidden without it. */
  onCreate?(): void;
}

interface ListState {
  rows: OrderSummary[];
  nextCursor?: string;
  loading: boolean;
  loadingMore: boolean;
  error: Problem | null;
}

/** The last bulk update with the order numbers it was applied to. */
interface BulkOutcome {
  result: BulkResult;
  numbers: Record<string, string>;
}

/** A bulk command or merge that cannot be undone waits for confirmation. */
type Pending = { kind: "bulk"; command: BulkCommand } | { kind: "merge" };

const irreversible: BulkCommand[] = ["cancel", "end"];

const baseColumns: DataTableColumn<OrderSummary>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "productId", header: "Product", mono: true },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "remaining", header: "Remaining", align: "end" },
  { key: "status", header: "Status", render: (row) => <Badge tone={statusTone[row.status]}>{statusLabel[row.status]}</Badge> },
  {
    key: "originType",
    header: "Origin",
    render: (row) => (row.originType === REWORK ? <Badge tone="warning">Rework</Badge> : row.originType ?? dash),
  },
  { key: "deliveryDate", header: "Delivery date", render: (row) => row.deliveryDate ?? dash },
  { key: "plannedCost", header: "Planned cost", align: "end", render: (row) => `${amount(row.plannedCost)} ${row.currency}` },
  { key: "actualCost", header: "Actual cost", align: "end", render: (row) => `${amount(row.actualCost ?? "0")} ${row.currency}` },
];

function resultColumns(numbers: Record<string, string>): DataTableColumn<BulkItem>[] {
  return [
    { key: "orderId", header: "Order", mono: true, render: (row) => numbers[row.orderId] ?? row.orderId },
    {
      key: "ok",
      header: "Result",
      render: (row) =>
        row.ok ? <Badge tone="success">{row.replayed ? "Already done" : "Done"}</Badge> : <Badge tone="danger">Failed</Badge>,
    },
    { key: "status", header: "Status", render: (row) => (row.status ? statusLabel[row.status] : dash) },
    { key: "detail", header: "Reason", render: (row) => (row.ok ? dash : `${row.detail ?? "Failed"} (${row.code ?? "COM-0500"})`) },
  ];
}

/**
 * Production orders list (PRD 4.21.6): newest first, filtered by status and
 * origin, paged by cursor; selected orders take a bulk status command
 * (FR-21.12) or are merged into the first one selected (FR-21.15).
 */
export function ProductionOrdersPage({ onOpen, onCreate }: ProductionOrdersPageProps) {
  const [status, setStatus] = useState<ProductionStatus | "">("");
  // FR-21.20: rework orders can be listed on their own.
  const [originType, setOriginType] = useState("");
  const [state, setState] = useState<ListState>({ rows: [], loading: true, loadingMore: false, error: null });
  const [reload, setReload] = useState(0);
  // Selection order matters: a merge keeps the first selected order.
  const [selected, setSelected] = useState<OrderSummary[]>([]);
  const [command, setCommand] = useState<BulkCommand>("release");
  const [busy, setBusy] = useState<"bulk" | "merge" | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const [actionError, setActionError] = useState<Problem | null>(null);
  const [outcome, setOutcome] = useState<BulkOutcome | null>(null);
  const [notice, setNotice] = useState("");
  const latest = useRef(0);
  // One Idempotency-Key per action and selection, so a retry after a lost response replays.
  const keys = useRef(new Map<string, string>());

  useEffect(() => {
    const token = ++latest.current;
    const controller = new AbortController();
    setState({ rows: [], loading: true, loadingMore: false, error: null });
    setSelected([]);
    listOrders({ status, originType, signal: controller.signal }).then(
      (page) => {
        if (token === latest.current) {
          setState({ rows: page.items ?? [], nextCursor: page.nextCursor, loading: false, loadingMore: false, error: null });
        }
      },
      (failure) => {
        if (token === latest.current) {
          setState({ rows: [], loading: false, loadingMore: false, error: describeProblem(failure) });
        }
      },
    );
    return () => controller.abort();
  }, [status, originType, reload]);

  const loadMore = async () => {
    const token = latest.current;
    setState((s) => ({ ...s, loadingMore: true }));
    try {
      const page = await listOrders({ status, originType, cursor: state.nextCursor });
      if (token === latest.current) {
        setState((s) => ({ ...s, rows: [...s.rows, ...(page.items ?? [])], nextCursor: page.nextCursor, loadingMore: false }));
      }
    } catch (failure) {
      if (token === latest.current) {
        setState((s) => ({ ...s, loadingMore: false, error: describeProblem(failure) }));
      }
    }
  };

  const isSelected = (id: string) => selected.some((o) => o.id === id);
  const toggle = (row: OrderSummary, on: boolean) =>
    setSelected((current) => (on ? [...current.filter((o) => o.id !== row.id), row] : current.filter((o) => o.id !== row.id)));

  const keyFor = (slot: string) => {
    const key = keys.current.get(slot) ?? newIdempotencyKey();
    keys.current.set(slot, key);
    return key;
  };

  const runBulk = async (cmd: BulkCommand) => {
    const ids = selected.map((o) => o.id);
    const numbers = Object.fromEntries(selected.map((o) => [o.id, o.orderNumber]));
    setBusy("bulk");
    setActionError(null);
    setNotice("");
    try {
      const result = await bulkStatus(ids, cmd, keyFor(`bulk:${cmd}:${ids.join(",")}`));
      setOutcome({ result, numbers });
      setPending(null);
      setReload((n) => n + 1);
    } catch (failure) {
      setActionError(describeProblem(failure));
    } finally {
      setBusy(null);
    }
  };

  const runMerge = async () => {
    const refs = selected.map((o) => ({ orderId: o.id, version: o.version }));
    setBusy("merge");
    setActionError(null);
    setNotice("");
    setOutcome(null);
    try {
      const result = await mergeOrders(refs, keyFor(`merge:${JSON.stringify(refs)}`));
      setNotice(
        `Merged ${result.merged.length} ${result.merged.length === 1 ? "order" : "orders"} into ${result.survivor.orderNumber}; quantity ${result.survivor.quantity}.`,
      );
      setPending(null);
      setReload((n) => n + 1);
    } catch (failure) {
      setActionError(describeProblem(failure));
    } finally {
      setBusy(null);
    }
  };

  const tooMany = selected.length > MAX_BULK;
  const mergeable = selected.length >= 2 && !tooMany && selected.every((o) => canSplit(o.status));
  const columns: DataTableColumn<OrderSummary>[] = [
    ...baseColumns,
    {
      key: "select",
      header: "Select",
      render: (row) => (
        <Input
          type="checkbox"
          aria-label={`Select ${row.orderNumber}`}
          checked={isSelected(row.id)}
          onClick={(event) => event.stopPropagation()}
          onChange={(event) => toggle(row, event.target.checked)}
        />
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Production orders"
        description="Create, estimate, release, start, report and end production orders; select orders to update them in bulk or merge them."
        actions={
          onCreate ? (
            <Button variant="primary" icon="box" onClick={onCreate}>
              New production order
            </Button>
          ) : undefined
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="production-status-filter" label="Status">
            {(control) => (
              <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as ProductionStatus | "")}>
                <option value="">All statuses</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
        <div className={styles.filter}>
          <FormField id="production-origin-filter" label="Origin">
            {(control) => (
              <Select {...control} value={originType} onChange={(event) => setOriginType(event.target.value)}>
                <option value="">All origins</option>
                <option value="Manual">Manual</option>
                <option value="PlannedOrder">Planned order</option>
                <option value="SalesOrderLine">Sales order line</option>
                <option value="Kanban">Kanban</option>
                <option value={REWORK}>Rework orders</option>
                <option value={SPLIT}>Split orders</option>
              </Select>
            )}
          </FormField>
        </div>
      </div>

      <Card title="Selected orders">
        <div className={styles.stack}>
          <p className={styles.hint} aria-live="polite">
            {selected.length === 0
              ? "Select orders in the list to apply a status command to all of them, or to merge them."
              : `${selected.length} selected. A merge keeps ${selected[0].orderNumber}, the first one selected, and cancels the others into it.`}
          </p>
          {tooMany ? (
            <Alert tone="warning">Select at most {MAX_BULK} orders for one bulk update or merge.</Alert>
          ) : null}
          <div className={styles.commandForm}>
            <FormField id="production-bulk-command" label="Bulk command">
              {(control) => (
                <Select {...control} value={command} onChange={(event) => setCommand(event.target.value as BulkCommand)}>
                  {BULK_COMMANDS.map((c) => (
                    <option key={c} value={c}>
                      {bulkCommandLabel[c]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <Button
              variant="primary"
              disabled={selected.length === 0 || tooMany}
              loading={busy === "bulk"}
              onClick={() => (irreversible.includes(command) ? setPending({ kind: "bulk", command }) : void runBulk(command))}
            >
              Apply to selected
            </Button>
            <Button disabled={!mergeable} loading={busy === "merge"} onClick={() => setPending({ kind: "merge" })}>
              Merge selected
            </Button>
            {selected.length > 0 ? (
              <Button variant="ghost" onClick={() => setSelected([])}>
                Clear selection
              </Button>
            ) : null}
          </div>
          {pending ? (
            <Alert
              tone="warning"
              title={
                pending.kind === "merge"
                  ? `Merge ${selected.length} orders into ${selected[0]?.orderNumber ?? ""}?`
                  : `${bulkCommandLabel[pending.command]} ${selected.length} orders?`
              }
              action={
                <div className={styles.actions}>
                  <Button
                    variant={pending.kind === "bulk" && pending.command === "cancel" ? "danger" : "primary"}
                    loading={busy !== null}
                    onClick={() => void (pending.kind === "merge" ? runMerge() : runBulk(pending.command))}
                  >
                    {pending.kind === "merge" ? "Yes, merge them" : `Yes, ${bulkCommandLabel[pending.command].toLowerCase()} them`}
                  </Button>
                  <Button variant="ghost" onClick={() => setPending(null)}>
                    Keep them as they are
                  </Button>
                </div>
              }
            >
              {pending.kind === "merge"
                ? "The other orders are cancelled and cannot be reopened."
                : pending.command === "cancel"
                  ? "Cancelled orders cannot be reopened."
                  : "Ended orders are settled and cannot be reset."}
            </Alert>
          ) : null}
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          {actionError ? (
            <Alert tone="danger" live="alert" title="The request was refused">
              {actionError.message} ({actionError.code})
            </Alert>
          ) : null}
        </div>
      </Card>

      {outcome ? (
        <Card title="Bulk update results" padded={false}>
          <Alert
            tone={outcome.result.failed > 0 ? "warning" : "success"}
            title={`${bulkCommandLabel[outcome.result.command] ?? outcome.result.command}: ${outcome.result.succeeded} succeeded, ${outcome.result.failed} failed`}
          >
            {outcome.result.failed > 0
              ? "Fix or skip the failed orders, then select them again to retry."
              : "Every selected order was updated."}
          </Alert>
          <DataTable
            label="Bulk update results"
            columns={resultColumns(outcome.numbers)}
            rows={outcome.result.results}
            getRowKey={(row) => row.orderId}
          />
        </Card>
      ) : null}

      {state.error ? (
        <Alert tone="danger" title="Could not load production orders" live="alert">
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      <DataTable
        label="Production orders"
        columns={columns}
        rows={state.rows}
        getRowKey={(row) => row.id}
        loading={state.loading}
        onRowOpen={onOpen}
        empty={{
          title: status
            ? `No ${statusLabel[status].toLowerCase()} orders`
            : originType === REWORK
              ? "No rework orders"
              : originType
                ? "No orders of this origin"
                : "No production orders yet",
          description:
            status || originType ? "Choose another filter to see more orders." : "Orders you create appear here, newest first.",
        }}
      />
      {state.nextCursor && !state.loading ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={state.loadingMore}>
            Load more orders
          </Button>
        </div>
      ) : null}
    </div>
  );
}
