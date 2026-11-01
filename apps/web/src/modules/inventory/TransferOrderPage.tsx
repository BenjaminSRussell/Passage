import { useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  cancelTransfer,
  describeProblem,
  getTransfer,
  receiveTransfer,
  shipTransfer,
  type Problem,
  type TransferLine,
  type TransferOrder,
} from "./api/client";
import { dash, DECIMAL, describeDimensions, formatDateTime, sentence, shortId, today } from "./format";
import { ALL_DIMENSIONS, Detail, ProblemAlert, stateTones } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const lineColumns: DataTableColumn<TransferLine>[] = [
  { key: "lineNo", header: "Line", align: "end" },
  { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
  { key: "qty", header: "Ordered", align: "end", mono: true, render: (row) => `${row.qty} ${row.unit}` },
  { key: "shippedQty", header: "Shipped", align: "end", mono: true },
  { key: "unshippedQty", header: "Left to ship", align: "end", mono: true, render: (row) => (Number(unshipped(row)) === 0 ? dash : unshipped(row)) },
  { key: "receivedQty", header: "Received", align: "end", mono: true },
  { key: "inTransit", header: "In transit", align: "end", mono: true, render: (row) => (Number(row.inTransit) === 0 ? dash : row.inTransit) },
  { key: "tolerance", header: "Under / over %", align: "end", mono: true, render: (row) => `${row.underdeliveryPct} / ${row.overdeliveryPct}` },
  {
    key: "closed",
    header: "Line",
    render: (row) =>
      row.closed ? (
        <Badge tone={Number(row.inTransit) > 0 ? "danger" : "success"}>{Number(row.inTransit) > 0 ? "Closed short" : "Closed"}</Badge>
      ) : (
        <Badge tone="info">Open</Badge>
      ),
  },
];

/** Quantity of the line not shipped yet; older service responses lack the field. */
function unshipped(line: TransferLine): string {
  if (line.unshippedQty !== undefined) return line.unshippedQty;
  if (line.closed) return "0";
  const left = Number(line.qty) - Number(line.shippedQty);
  return String(left > 0 ? left : 0);
}

interface ReceiptRow {
  qty: string;
  closeShort: boolean;
}

/**
 * Shipment entry: one quantity per line with something left to ship, defaulting to all of it. A
 * shipment of everything left is sent without lines; anything less is a partial shipment that
 * names its lines, and the order stays open for the rest.
 */
function ShipForm({ order, onDone, onError }: { order: TransferOrder; onDone(next: TransferOrder, partial: boolean): void; onError(p: Problem): void }) {
  const open = order.lines.filter((l) => Number(unshipped(l)) > 0);
  const [rows, setRows] = useState<Record<string, string>>(() => Object.fromEntries(open.map((l) => [l.id, unshipped(l)])));
  const [date, setDate] = useState(today());
  const [invalid, setInvalid] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const errors: Record<string, string> = {};
    const lines: { lineId: string; qty: string }[] = [];
    let all = true;
    for (const line of open) {
      const qty = (rows[line.id] ?? "").trim();
      if (qty === "" || Number(qty) === 0) {
        all = false;
        continue;
      }
      if (!DECIMAL.test(qty) || Number(qty) < 0) {
        errors[line.id] = "Enter a quantity of zero or more.";
        continue;
      }
      if (Number(qty) > Number(unshipped(line))) {
        errors[line.id] = `Only ${unshipped(line)} ${line.unit} is left to ship.`;
        continue;
      }
      if (Number(qty) !== Number(unshipped(line))) all = false;
      lines.push({ lineId: line.id, qty });
    }
    if (Object.keys(errors).length === 0 && lines.length === 0) errors.form = "Enter a quantity to ship on at least one line.";
    setInvalid(errors);
    if (Object.keys(errors).length > 0) return;
    setBusy(true);
    try {
      onDone(await shipTransfer(order.id, order.version, date, all ? undefined : lines), !all);
    } catch (error) {
      onError(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Ship">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="ship-date" label="Ship date" hint="Shipping takes the quantities below out of the shipping warehouse. Ship less than the full quantity to ship the rest later.">
          {(control) => <Input {...control} type="date" value={date} onChange={(event) => setDate(event.target.value)} />}
        </FormField>
        {open.map((line) => (
          <FormField
            key={line.id}
            id={`ship-${line.lineNo}`}
            label={`Line ${line.lineNo}: ship ${line.unit}`}
            hint={`${unshipped(line)} ${line.unit} left to ship`}
            error={invalid[line.id]}
          >
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                className={styles.narrow}
                value={rows[line.id] ?? ""}
                onChange={(event) => setRows({ ...rows, [line.id]: event.target.value })}
                invalid={Boolean(invalid[line.id])}
              />
            )}
          </FormField>
        ))}
        {invalid.form ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Nothing to ship" live="alert">
              {invalid.form}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Ship order
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Receipt entry for the open lines of a shipped order that have shipped quantity. */
function ReceiveForm({ order, onDone, onError }: { order: TransferOrder; onDone(next: TransferOrder): void; onError(p: Problem): void }) {
  const open = order.lines.filter((l) => !l.closed && Number(l.shippedQty) > 0);
  const [rows, setRows] = useState<Record<string, ReceiptRow>>(() => Object.fromEntries(open.map((l) => [l.id, { qty: l.inTransit, closeShort: false }])));
  const [date, setDate] = useState(today());
  const [invalid, setInvalid] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const errors: Record<string, string> = {};
    const lines = [];
    for (const line of open) {
      const row = rows[line.id] ?? { qty: "", closeShort: false };
      const qty = row.qty.trim();
      if (qty === "" || Number(qty) === 0) continue;
      if (!DECIMAL.test(qty) || Number(qty) < 0) {
        errors[line.id] = "Enter a quantity of zero or more.";
        continue;
      }
      lines.push({ lineId: line.id, qty, closeShort: row.closeShort });
    }
    if (Object.keys(errors).length === 0 && lines.length === 0) errors.form = "Enter a received quantity on at least one line.";
    setInvalid(errors);
    if (Object.keys(errors).length > 0) return;
    setBusy(true);
    try {
      onDone(await receiveTransfer(order.id, order.version, date, lines));
    } catch (error) {
      onError(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Receive">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="receipt-date" label="Receipt date">
          {(control) => <Input {...control} type="date" value={date} onChange={(event) => setDate(event.target.value)} />}
        </FormField>
        {open.map((line) => (
          <div key={line.id} className={styles.formActions}>
            <FormField
              id={`receive-${line.lineNo}`}
              label={`Line ${line.lineNo}: received ${line.unit}`}
              hint={`${line.inTransit} ${line.unit} in transit · up to ${line.overdeliveryPct}% over`}
              error={invalid[line.id]}
            >
              {(control) => (
                <Input
                  {...control}
                  inputMode="decimal"
                  className={styles.narrow}
                  value={rows[line.id]?.qty ?? ""}
                  onChange={(event) => setRows({ ...rows, [line.id]: { ...(rows[line.id] ?? { closeShort: false }), qty: event.target.value } })}
                  invalid={Boolean(invalid[line.id])}
                />
              )}
            </FormField>
            <span className={styles.check}>
              <Input
                type="checkbox"
                id={`close-short-${line.lineNo}`}
                checked={rows[line.id]?.closeShort ?? false}
                onChange={(event) => setRows({ ...rows, [line.id]: { ...(rows[line.id] ?? { qty: "" }), closeShort: event.target.checked } })}
              />
              <label htmlFor={`close-short-${line.lineNo}`}>
                {`Close line ${line.lineNo} short (within ${line.underdeliveryPct}%)`}
                {Number(unshipped(line)) > 0 ? ` and give up the ${unshipped(line)} ${line.unit} not shipped` : ""}
              </label>
            </span>
          </div>
        ))}
        {invalid.form ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Nothing to receive" live="alert">
              {invalid.form}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Book receipt
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Transfer order (FR-16.15): route, lines, and ship / receive / cancel by status. */
export function TransferOrderPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getTransfer(id, signal), id);
  const [order, setOrder] = useState<TransferOrder | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setOrder(state.data);
  }, [state.data]);

  async function cancel() {
    if (!order) return;
    setBusy(true);
    setFailure(null);
    setNotice("");
    try {
      const next = await cancelTransfer(order.id, order.version);
      setOrder(next);
      setNotice(`Transfer order ${next.number} cancelled.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  function shipped(next: TransferOrder, partial: boolean) {
    setFailure(null);
    setOrder(next);
    if (partial) setNotice(`Partial shipment booked. ${next.lines.filter((l) => Number(unshipped(l)) > 0).length} line(s) still have quantity left to ship.`);
    else if (next.useTransit) setNotice(`Shipped: ${next.lines.length} ${next.lines.length === 1 ? "line is" : "lines are"} now in transit at ${next.transitWarehouseId}.`);
    else setNotice(`Shipped and received at ${next.toWarehouseId} in one step.`);
  }

  const back = (
    <Link className={buttonClassName("ghost")} to="/inventory/transfer-orders">
      Back to transfer orders
    </Link>
  );

  if (state.error) {
    if (state.error.code === "INV-TRF-004") {
      return <EmptyState icon="xCircle" headingLevel={2} title="Transfer order not found" description="It may belong to another legal entity, or the link is wrong." action={back} />;
    }
    return <ProblemAlert title="Could not load the transfer order" problem={state.error} onRetry={state.reload} />;
  }
  if (!order) {
    return (
      <div className={styles.stack} aria-busy="true">
        <span className="visually-hidden">Loading transfer order…</span>
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  const created = order.status === "CREATED";
  const receivable = order.status === "SHIPPED" || order.status === "PARTIALLY_RECEIVED";
  const shippable = created || (receivable && order.lines.some((l) => Number(unshipped(l)) > 0));
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Transfer order ${order.number}`}
        meta={<Badge tone={stateTones[order.status] ?? "neutral"}>{sentence(order.status)}</Badge>}
        description={`${order.fromWarehouseId} → ${order.useTransit ? `${order.transitWarehouseId} → ` : ""}${order.toWarehouseId} · ${order.lines.length} ${order.lines.length === 1 ? "line" : "lines"}`}
        actions={
          <>
            {back}
            {created ? (
              <Button variant="danger" loading={busy} onClick={cancel}>
                Cancel order
              </Button>
            ) : null}
          </>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert
          tone="danger"
          title="The transfer order was not updated"
          live="alert"
          action={failure.code === "conflict_version" ? <Button size="sm" onClick={state.reload}>Reload order</Button> : undefined}
        >
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <Card title="Details">
        <dl className={styles.details}>
          <Detail term="Legal entity" mono>
            {order.legalEntityId}
          </Detail>
          <Detail term="Shipping warehouse" mono>
            {order.fromWarehouseId}
          </Detail>
          <Detail term="Transit warehouse" mono>
            {order.useTransit ? order.transitWarehouseId ?? dash : "None (one-step)"}
          </Detail>
          <Detail term="Receiving warehouse" mono>
            {order.toSiteId ? `${order.toWarehouseId} (site ${order.toSiteId})` : order.toWarehouseId}
          </Detail>
          <Detail term={created ? "Planned ship date" : "First shipped on"}>{order.shipDate}</Detail>
          <Detail term="Planned receipt date">{order.plannedReceiptDate ?? dash}</Detail>
          <Detail term="Received on">{order.receiptDate ?? dash}</Detail>
          <Detail term="Created">{formatDateTime(order.createdAt)}</Detail>
        </dl>
      </Card>
      {shippable ? <ShipForm key={order.version} order={order} onDone={shipped} onError={setFailure} /> : null}
      {receivable ? (
        <ReceiveForm
          key={order.version}
          order={order}
          onDone={(next) => {
            setFailure(null);
            setOrder(next);
            setNotice(next.status === "RECEIVED" ? `Transfer order ${next.number} fully received.` : "Receipt booked.");
          }}
          onError={setFailure}
        />
      ) : null}
      <DataTable label="Transfer lines" columns={lineColumns} rows={order.lines} getRowKey={(row) => row.id} empty={{ title: "No lines" }} />
    </div>
  );
}
