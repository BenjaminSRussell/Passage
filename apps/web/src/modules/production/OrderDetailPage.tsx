import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  type DataTableColumn,
} from "../../shell/ui";
import {
  allowedCommands,
  describeProblem,
  getCosts,
  getOrder,
  getStatusLog,
  newIdempotencyKey,
  nextCommand,
  resetTargets,
  runCommand,
  type CommandBody,
  type CostGroup,
  type CostRow,
  type CostView,
  type Operation,
  type OrderLine,
  type Problem,
  type ProductionCommand,
  type ProductionOrder,
  type ProductionStatus,
  type StatusLogEntry,
} from "./api/client";
import { amount, commandLabel, costGroupLabel, dash, statusLabel, statusTone } from "./labels";
import styles from "./pages.module.css";

const quantityPattern = /^[0-9]{1,12}(\.[0-9]{1,6})?$/;

export interface ProductionOrderDetailProps {
  orderId: string;
  /** Returns to the order list. */
  onBack?(): void;
}

const lineColumns: DataTableColumn<OrderLine>[] = [
  { key: "lineNo", header: "Line", mono: true },
  { key: "itemId", header: "Item", mono: true },
  { key: "qtyPer", header: "Per unit", align: "end" },
  { key: "scrapPct", header: "Scrap %", align: "end" },
  { key: "qtyRequired", header: "Required", align: "end" },
  { key: "unitCost", header: "Unit cost", align: "end" },
];

const operationColumns: DataTableColumn<Operation>[] = [
  { key: "operNumber", header: "Operation", align: "end" },
  { key: "resourceId", header: "Resource", mono: true },
  { key: "setupTimeH", header: "Setup h", align: "end" },
  { key: "runTimeHPer", header: "Run h per unit", align: "end" },
  { key: "processQty", header: "Process qty", align: "end" },
  { key: "scrapPct", header: "Scrap %", align: "end" },
  { key: "subcontract", header: "Subcontract", render: (row) => (row.subcontract ? "Yes" : "No") },
];

const costColumns: DataTableColumn<CostRow>[] = [
  { key: "group", header: "Cost group", render: (row) => costGroupLabel[row.group] },
  { key: "key", header: "Item or operation", mono: true },
  { key: "qty", header: "Planned qty", align: "end" },
  { key: "amount", header: "Planned amount", align: "end" },
  { key: "fixed", header: "Fixed (setup)", align: "end" },
];

const logColumns: DataTableColumn<StatusLogEntry>[] = [
  { key: "occurredAt", header: "When", mono: true, render: (row) => <time dateTime={row.occurredAt}>{row.occurredAt}</time> },
  { key: "command", header: "Command", render: (row) => commandLabel[row.command] ?? row.command },
  { key: "from", header: "From", render: (row) => statusLabel[row.from] ?? row.from },
  { key: "to", header: "To", render: (row) => statusLabel[row.to] ?? row.to },
  { key: "actor", header: "Actor" },
  { key: "traceId", header: "Trace", mono: true },
];

const costGroups: CostGroup[] = ["material", "labor", "overhead", "subcontract"];

/** Commands that cannot be undone (BR-21.1: no reset from Ended or Cancelled) ask for confirmation. */
const irreversible: ProductionCommand[] = ["cancel", "end"];

function Detail({ term, children, mono = false }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

interface Loaded<T> {
  value: T | null;
  loading: boolean;
  error: Problem | null;
}

const idle = <T,>(): Loaded<T> => ({ value: null, loading: true, error: null });

/** Production order detail (PRD 4.21.6): header, status actions, BOM lines, route, planned cost and status log. */
export function ProductionOrderDetail({ orderId, onBack }: ProductionOrderDetailProps) {
  const [order, setOrder] = useState<Loaded<ProductionOrder>>(idle);
  const [log, setLog] = useState<Loaded<StatusLogEntry[]>>(idle);
  const [costs, setCosts] = useState<Loaded<CostView>>(idle);
  const [busy, setBusy] = useState<ProductionCommand | null>(null);
  const [commandError, setCommandError] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [raf, setRaf] = useState({ quantity: "", errorQuantity: "" });
  const [rafErrors, setRafErrors] = useState<{ quantity?: string; errorQuantity?: string }>({});
  const [resetTo, setResetTo] = useState<ProductionStatus | "">("");
  const [confirming, setConfirming] = useState<ProductionCommand | null>(null);
  // One Idempotency-Key per command and version, so a retry after a lost response replays.
  const keys = useRef(new Map<string, string>());

  const loadSide = useCallback(
    (signal?: AbortSignal) => {
      getStatusLog(orderId, signal).then(
        (value) => setLog({ value, loading: false, error: null }),
        (failure) => !signal?.aborted && setLog({ value: null, loading: false, error: describeProblem(failure) }),
      );
      getCosts(orderId, signal).then(
        (value) => setCosts({ value, loading: false, error: null }),
        (failure) => !signal?.aborted && setCosts({ value: null, loading: false, error: describeProblem(failure) }),
      );
    },
    [orderId],
  );

  useEffect(() => {
    const controller = new AbortController();
    setOrder(idle);
    setLog(idle);
    setCosts(idle);
    getOrder(orderId, controller.signal).then(
      (value) => setOrder({ value, loading: false, error: null }),
      (failure) => !controller.signal.aborted && setOrder({ value: null, loading: false, error: describeProblem(failure) }),
    );
    loadSide(controller.signal);
    return () => controller.abort();
  }, [orderId, loadSide]);

  const current = order.value;

  const run = async (command: ProductionCommand, body: CommandBody = {}) => {
    if (!current || busy) {
      return;
    }
    const slot = `${command}@${current.version}:${JSON.stringify(body)}`;
    const key = keys.current.get(slot) ?? newIdempotencyKey();
    keys.current.set(slot, key);
    setBusy(command);
    setCommandError(null);
    setNotice("");
    try {
      const updated = await runCommand(current.id, command, current.version, key, body);
      setOrder({ value: updated, loading: false, error: null });
      setNotice(`${commandLabel[command]} applied. Status: ${statusLabel[updated.status]}.`);
      setRaf({ quantity: "", errorQuantity: "" });
      setResetTo("");
      setConfirming(null);
      loadSide();
    } catch (failure) {
      setCommandError(describeProblem(failure));
    } finally {
      setBusy(null);
    }
  };

  const submitRaf = (event: FormEvent) => {
    event.preventDefault();
    const errors: typeof rafErrors = {};
    if (!quantityPattern.test(raf.quantity) || !/[1-9]/.test(raf.quantity)) {
      errors.quantity = "Enter a positive quantity with up to 6 decimals.";
    }
    if (raf.errorQuantity !== "" && !quantityPattern.test(raf.errorQuantity)) {
      errors.errorQuantity = "Enter a quantity with up to 6 decimals, or leave it empty.";
    }
    setRafErrors(errors);
    if (Object.keys(errors).length > 0) {
      return;
    }
    const body: CommandBody = { quantity: raf.quantity };
    if (raf.errorQuantity !== "") {
      body.errorQuantity = raf.errorQuantity;
    }
    void run("report-finished", body);
  };

  if (order.loading) {
    return (
      <div className={styles.stack} aria-busy="true">
        <Skeleton width="1/4" height="control" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading production order…</span>
      </div>
    );
  }
  if (!current) {
    const missing = order.error?.status === 404;
    return (
      <Card>
        {missing ? (
          <EmptyState
            headingLevel={2}
            icon="box"
            title="We couldn't find that production order"
            description="It may belong to another legal entity, or the link is out of date."
            action={onBack ? <Button onClick={onBack}>Back to production orders</Button> : undefined}
          />
        ) : (
          <Alert tone="danger" title="Could not load the production order" live="alert">
            {order.error?.message} ({order.error?.code})
          </Alert>
        )}
      </Card>
    );
  }

  const forward = nextCommand(current.status);
  const commands = allowedCommands(current.status).filter((c) => c !== "report-finished" && c !== "reset-status");
  const canReport = current.status === "Started" || current.status === "ReportedAsFinished";
  const targets = resetTargets(current.status);
  const unit = (value: string) => `${value} ${current.currency}`;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Production order ${current.orderNumber}`}
        description={`Version ${current.version}${current.createdAt ? ` · created ${current.createdAt.slice(0, 10)}` : ""}`}
        meta={<Badge tone={statusTone[current.status]}>{statusLabel[current.status]}</Badge>}
        actions={onBack ? <Button variant="ghost" onClick={onBack}>Back to production orders</Button> : undefined}
      />

      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Alert tone="danger" live="alert" title={commandError ? "The command was refused" : undefined}>
        {commandError ? `${commandError.message} (${commandError.code})` : null}
      </Alert>

      <Card title="Status actions">
        {commands.length === 0 && !canReport && targets.length === 0 ? (
          <p className={styles.hint}>This order is {statusLabel[current.status].toLowerCase()}; no further actions apply.</p>
        ) : (
          <div className={styles.stack}>
            {commands.length > 0 ? (
              <div className={styles.actions}>
                {commands.map((command) => (
                  <Button
                    key={command}
                    variant={command === forward ? "primary" : command === "cancel" ? "danger" : "secondary"}
                    loading={busy === command}
                    onClick={() => (irreversible.includes(command) ? setConfirming(command) : void run(command))}
                  >
                    {command === "estimate" && current.status !== "Created" ? "Re-estimate" : commandLabel[command]}
                  </Button>
                ))}
              </div>
            ) : null}
            {confirming ? (
              <Alert
                tone="warning"
                title={confirming === "cancel" ? `Cancel ${current.orderNumber}?` : `End ${current.orderNumber}?`}
                action={
                  <div className={styles.actions}>
                    <Button variant={confirming === "cancel" ? "danger" : "primary"} loading={busy === confirming} onClick={() => void run(confirming)}>
                      {confirming === "cancel" ? "Yes, cancel the order" : "Yes, end the order"}
                    </Button>
                    <Button variant="ghost" onClick={() => setConfirming(null)}>
                      Keep it open
                    </Button>
                  </div>
                }
              >
                {confirming === "cancel"
                  ? "A cancelled order cannot be reopened."
                  : "An ended order is settled and cannot be reset or reported on again."}
              </Alert>
            ) : null}
            {canReport ? (
              <form className={styles.commandForm} onSubmit={submitRaf} noValidate aria-label="Report as finished">
                <FormField id="raf-quantity" label="Good quantity" error={rafErrors.quantity}>
                  {(control) => (
                    <Input
                      {...control}
                      inputMode="decimal"
                      value={raf.quantity}
                      onChange={(event) => setRaf({ ...raf, quantity: event.target.value })}
                    />
                  )}
                </FormField>
                <FormField id="raf-error-quantity" label="Error quantity" optional error={rafErrors.errorQuantity}>
                  {(control) => (
                    <Input
                      {...control}
                      inputMode="decimal"
                      value={raf.errorQuantity}
                      onChange={(event) => setRaf({ ...raf, errorQuantity: event.target.value })}
                    />
                  )}
                </FormField>
                <Button type="submit" variant={forward === "report-finished" ? "primary" : "secondary"} loading={busy === "report-finished"}>
                  Report as finished
                </Button>
              </form>
            ) : null}
            {targets.length > 0 ? (
              <div className={styles.commandForm}>
                <FormField id="reset-target" label="Reset to">
                  {(control) => (
                    <Select {...control} value={resetTo} onChange={(event) => setResetTo(event.target.value as ProductionStatus | "")}>
                      <option value="">Previous status ({statusLabel[targets[0]]})</option>
                      {targets.slice(1).map((s) => (
                        <option key={s} value={s}>
                          {statusLabel[s]}
                        </option>
                      ))}
                    </Select>
                  )}
                </FormField>
                <Button loading={busy === "reset-status"} onClick={() => void run("reset-status", resetTo ? { status: resetTo } : {})}>
                  Reset status
                </Button>
              </div>
            ) : null}
          </div>
        )}
      </Card>

      <Card title="Order">
        <dl className={styles.details}>
          <Detail term="Product" mono>
            {current.productId}
          </Detail>
          <Detail term="Quantity">{current.quantity}</Detail>
          <Detail term="Reported as finished">{current.qtyRaf}</Detail>
          <Detail term="Error quantity">{current.qtyError}</Detail>
          <Detail term="Delivery date">{current.deliveryDate ?? dash}</Detail>
          <Detail term="Site" mono>
            {current.siteId}
          </Detail>
          <Detail term="Warehouse" mono>
            {current.warehouseId}
          </Detail>
          <Detail term="Origin">{current.originType}</Detail>
        </dl>
      </Card>

      <Card title="Components" padded={false}>
        <DataTable
          label="Order components"
          columns={lineColumns}
          rows={current.lines}
          getRowKey={(row) => row.lineNo}
          empty={{ title: "No components", description: "This order consumes no material, for example a rework order." }}
        />
      </Card>

      <Card title="Route" padded={false}>
        <DataTable label="Order operations" columns={operationColumns} rows={current.operations} getRowKey={(row) => String(row.operNumber)} />
      </Card>

      <Card title="Planned cost">
        {costs.error ? (
          <Alert tone={costs.error.status === 403 ? "info" : "danger"} title="Planned cost is not available">
            {costs.error.status === 403 ? "You need the View production costs duty to see costs." : `${costs.error.message} (${costs.error.code})`}
          </Alert>
        ) : costs.loading ? (
          <Skeleton height="tile" />
        ) : costs.value && costs.value.rows.length > 0 ? (
          <>
            <ul className={styles.totals} aria-label="Planned cost by group">
              {costGroups
                .filter((g) => costs.value?.totals[g] !== undefined)
                .map((g) => (
                  <li key={g} className={styles.total}>
                    <span className={styles.term}>{costGroupLabel[g]}</span>{" "}
                    <span className={styles.totalValue}>{unit(amount(costs.value?.totals[g]))}</span>
                  </li>
                ))}
              <li className={styles.total}>
                <span className={styles.term}>Total</span>{" "}
                <span className={styles.totalValue}>{unit(amount(costs.value.total))}</span>
              </li>
            </ul>
            <DataTable label="Planned cost rows" columns={costColumns} rows={costs.value.rows} getRowKey={(row) => `${row.group}/${row.key}`} />
          </>
        ) : (
          <EmptyState compact title="Not estimated yet" description="Estimate the order to compute planned material, labor and overhead." />
        )}
      </Card>

      <Card title="Status log" padded={false}>
        {log.error ? (
          <Alert tone="danger" title="Could not load the status log">
            {log.error.message} ({log.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Status log"
            columns={logColumns}
            rows={log.value ?? []}
            loading={log.loading}
            getRowKey={(row, i) => `${row.occurredAt}-${i}`}
            empty={{ title: "No status changes yet", description: "Each command on this order is listed with who ran it and when." }}
          />
        )}
      </Card>
    </div>
  );
}
