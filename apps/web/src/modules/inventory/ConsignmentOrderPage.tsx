import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, ConfirmDialog, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, endConsignment, getConsignment, receiveConsignment, type ConsignmentLine, type ConsignmentOrder, type Problem } from "./api/client";
import { DECIMAL, describeDimensions, formatDateTime, sentence, today } from "./format";
import { ALL_DIMENSIONS, Detail, ProblemAlert, stateTones } from "./parts";
import { Named } from "../purchasing/Named";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** The line's own dimensions; site, warehouse and owner are on the order. */
const LINE_ONLY = ALL_DIMENSIONS.filter((name) => name !== "site" && name !== "warehouse" && name !== "owner");

function lineColumns(currency: string): DataTableColumn<ConsignmentLine>[] {
  return [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "productId", header: "Product", render: (row) => <Named entity="product" id={row.productId} /> },
    { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, LINE_ONLY) },
    { key: "qty", header: "Ordered", align: "end", mono: true, render: (row) => `${row.qty} ${row.unit}` },
    { key: "receivedQty", header: "Received", align: "end", mono: true },
    { key: "openQty", header: "Open", align: "end", mono: true },
    { key: "givenUpQty", header: "Given up", align: "end", mono: true },
    { key: "consignmentPrice", header: "Consignment price", align: "end", mono: true, render: (row) => `${row.consignmentPrice} ${currency}` },
  ];
}

export const DUTY_CONSIGNMENT_WRITE = "inventory.consignment.write";

/**
 * Ends the order: a partly received one is closed (the open quantity is given up, the stock received
 * stays), one nothing was received against is cancelled. Both ask for confirmation first.
 */
function EndCard({ order, onDone, onError }: { order: ConsignmentOrder; onDone(next: ConsignmentOrder, action: "close" | "cancel"): void; onError(p: Problem, action: "close" | "cancel"): void }) {
  const action = order.status === "CREATED" ? "cancel" : "close";
  const [reason, setReason] = useState("");
  const [invalid, setInvalid] = useState("");
  const [asking, setAsking] = useState(false);
  const [busy, setBusy] = useState(false);
  const sending = useRef(false);
  // Per line and unit, as the service sends it: lines can be in different units and summing decimal
  // strings as numbers would show float noise.
  const open = order.lines
    .filter((l) => Number(l.openQty) > 0)
    .map((l) => `${l.openQty} ${l.unit}`)
    .join(", ");

  async function confirm() {
    if (sending.current) return;
    sending.current = true;
    setBusy(true);
    try {
      onDone(await endConsignment(order.id, action, order.version, reason), action);
    } catch (error) {
      onError(describeProblem(error), action);
    } finally {
      sending.current = false;
      setBusy(false);
      setAsking(false);
    }
  }

  function ask(event: FormEvent) {
    event.preventDefault();
    if (reason.trim().length > 200) {
      setInvalid("Use at most 200 characters.");
      return;
    }
    setInvalid("");
    setAsking(true);
  }

  return (
    <Card title={action === "cancel" ? "Cancel order" : "Close order"}>
      <form onSubmit={ask} noValidate className={styles.form}>
        <FormField
          id="cro-end-reason"
          label="Reason"
          hint={action === "cancel" ? "Nothing was received, so cancelling moves no stock." : `The open quantity (${open}) is given up; the stock already received stays.`}
          error={invalid}
        >
          {(control) => <Input {...control} value={reason} onChange={(event) => setReason(event.target.value)} invalid={Boolean(invalid)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="secondary">
            {action === "cancel" ? "Cancel order" : "Close order"}
          </Button>
        </div>
      </form>
      {asking ? (
        <ConfirmDialog
          title={action === "cancel" ? "Cancel this consignment order?" : "Close this consignment order?"}
          description={
            action === "cancel"
              ? `Order ${order.number} will no longer expect any stock from vendor ${order.vendorId}.`
              : `Order ${order.number} will stop expecting the ${open} still open. Vendor-owned stock already received stays.`
          }
          confirmLabel={busy ? "Working" : action === "cancel" ? "Cancel order" : "Close order"}
          cancelLabel="Keep order"
          onCancel={() => (busy ? undefined : setAsking(false))}
          onConfirm={() => void confirm()}
        />
      ) : null}
    </Card>
  );
}

/** Receipt entry for the open lines; each receipt adds vendor-owned stock at no value. */
function ReceiveForm({ order, onDone, onError }: { order: ConsignmentOrder; onDone(next: ConsignmentOrder): void; onError(p: Problem): void }) {
  const open = order.lines.filter((l) => Number(l.openQty) > 0);
  const [qty, setQty] = useState<Record<string, string>>(() => Object.fromEntries(open.map((l) => [l.id, l.openQty])));
  const [date, setDate] = useState(today());
  const [invalid, setInvalid] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const errors: Record<string, string> = {};
    const lines = [];
    for (const line of open) {
      const value = (qty[line.id] ?? "").trim();
      if (value === "" || Number(value) === 0) continue;
      if (!DECIMAL.test(value) || Number(value) < 0) errors[line.id] = "Enter a quantity of zero or more.";
      else if (Number(value) > Number(line.openQty)) errors[line.id] = `At most ${line.openQty} ${line.unit} are still open.`;
      else lines.push({ lineId: line.id, qty: value });
    }
    if (Object.keys(errors).length === 0 && lines.length === 0) errors.form = "Enter a received quantity on at least one line.";
    setInvalid(errors);
    if (Object.keys(errors).length > 0) return;
    setBusy(true);
    try {
      onDone(await receiveConsignment(order.id, order.version, date, lines));
    } catch (error) {
      onError(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Receive">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="cro-receipt-date" label="Receipt date">
          {(control) => <Input {...control} type="date" value={date} onChange={(event) => setDate(event.target.value)} />}
        </FormField>
        {open.map((line) => (
          <FormField
            key={line.id}
            id={`cro-receive-${line.lineNo}`}
            label={`Line ${line.lineNo}: received ${line.unit}`}
            hint={`${line.openQty} ${line.unit} open`}
            error={invalid[line.id]}
          >
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                className={styles.narrow}
                value={qty[line.id] ?? ""}
                onChange={(event) => setQty({ ...qty, [line.id]: event.target.value })}
                invalid={Boolean(invalid[line.id])}
              />
            )}
          </FormField>
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

/** Consignment replenishment order (FR-16.17): vendor, destination, lines and receipts. */
export function ConsignmentOrderPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getConsignment(id, signal), id);
  const [order, setOrder] = useState<ConsignmentOrder | null>(null);
  // The refusal and what was refused: a receipt, a close or a cancel.
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [notice, setNotice] = useState("");
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_CONSIGNMENT_WRITE);

  useEffect(() => {
    setOrder(state.data);
  }, [state.data]);

  const back = (
    <Link className={buttonClassName("ghost")} to="/inventory/consignment-orders">
      Back to consignment orders
    </Link>
  );

  if (state.error) {
    if (state.error.code === "INV-CNS-001") {
      return <EmptyState icon="xCircle" headingLevel={2} title="Consignment order not found" description="It may belong to another legal entity, or the link is wrong." action={back} />;
    }
    return <ProblemAlert title="Could not load the consignment order" problem={state.error} onRetry={state.reload} />;
  }
  if (!order) {
    return (
      <div className={styles.stack} aria-busy="true">
        <span className="visually-hidden">Loading consignment order…</span>
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Consignment order ${order.number}`}
        meta={<Badge tone={stateTones[order.status] ?? "neutral"}>{sentence(order.status)}</Badge>}
        description={`Vendor ${order.vendorId} → ${order.warehouseId} (site ${order.siteId}) · ${order.lines.length} ${order.lines.length === 1 ? "line" : "lines"}`}
        actions={
          <>
            {back}
            <Link className={buttonClassName("secondary")} to={`/inventory/consignment/stock?vendor=${encodeURIComponent(order.vendorId)}`}>
              Vendor stock
            </Link>
          </>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert
          tone="danger"
          title={failure.title}
          live="alert"
          action={failure.problem.code === "conflict_version" ? <Button size="sm" onClick={state.reload}>Reload order</Button> : undefined}
        >
          <ProblemMessage problem={failure.problem} />
        </Alert>
      ) : null}
      <Card title="Details">
        <dl className={styles.details}>
          <Detail term="Legal entity" mono>
            {order.legalEntityId}
          </Detail>
          <Detail term="Vendor (owner)" mono>
            {order.vendorId}
          </Detail>
          <Detail term="Site" mono>
            {order.siteId}
          </Detail>
          <Detail term="Warehouse" mono>
            {order.warehouseId}
          </Detail>
          <Detail term="Requested date">{order.requestedDate}</Detail>
          <Detail term="Currency">{order.currency}</Detail>
          <Detail term="Created">{formatDateTime(order.createdAt)}</Detail>
          {order.closedAt ? (
            <Detail term={order.status === "CANCELLED" ? "Cancelled" : "Closed"}>
              {`${formatDateTime(order.closedAt)}${order.closedBy ? ` by ${order.closedBy}` : ""}`}
            </Detail>
          ) : null}
          {order.closeReason ? <Detail term="Reason">{order.closeReason}</Detail> : null}
        </dl>
      </Card>
      {order.status === "CREATED" || order.status === "PARTIALLY_RECEIVED" ? (
        <ReceiveForm
          key={order.version}
          order={order}
          onDone={(next) => {
            setFailure(null);
            setOrder(next);
            setNotice(next.status === "RECEIVED" ? `Consignment order ${next.number} fully received.` : "Receipt booked as vendor-owned stock.");
          }}
          onError={(problem) => setFailure({ title: "The receipt was not booked", problem })}
        />
      ) : null}
      {canWrite && (order.status === "CREATED" || order.status === "PARTIALLY_RECEIVED") ? (
        <EndCard
          key={`end-${order.version}`}
          order={order}
          onDone={(next, action) => {
            setFailure(null);
            setOrder(next);
            setNotice(action === "cancel" ? `Consignment order ${next.number} cancelled.` : `Consignment order ${next.number} closed; the open quantity is given up.`);
          }}
          onError={(problem, action) => setFailure({ title: action === "cancel" ? "The order was not cancelled" : "The order was not closed", problem })}
        />
      ) : null}
      <DataTable label="Consignment lines" columns={lineColumns(order.currency)} rows={order.lines} getRowKey={(row) => row.id} empty={{ title: "No lines" }} />
    </div>
  );
}
