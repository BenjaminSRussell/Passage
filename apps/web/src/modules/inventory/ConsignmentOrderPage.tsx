import { useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getConsignment, receiveConsignment, type ConsignmentLine, type ConsignmentOrder, type Problem } from "./api/client";
import { DECIMAL, describeDimensions, formatDateTime, sentence, shortId, today } from "./format";
import { ALL_DIMENSIONS, Detail, ProblemAlert, stateTones } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** The line's own dimensions; site, warehouse and owner are on the order. */
const LINE_ONLY = ALL_DIMENSIONS.filter((name) => name !== "site" && name !== "warehouse" && name !== "owner");

function lineColumns(currency: string): DataTableColumn<ConsignmentLine>[] {
  return [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
    { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, LINE_ONLY) },
    { key: "qty", header: "Ordered", align: "end", mono: true, render: (row) => `${row.qty} ${row.unit}` },
    { key: "receivedQty", header: "Received", align: "end", mono: true },
    { key: "openQty", header: "Open", align: "end", mono: true },
    { key: "consignmentPrice", header: "Consignment price", align: "end", mono: true, render: (row) => `${row.consignmentPrice} ${currency}` },
  ];
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
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");

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
          title="The receipt was not booked"
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
        </dl>
      </Card>
      {order.status !== "RECEIVED" ? (
        <ReceiveForm
          key={order.version}
          order={order}
          onDone={(next) => {
            setFailure(null);
            setOrder(next);
            setNotice(next.status === "RECEIVED" ? `Consignment order ${next.number} fully received.` : "Receipt booked as vendor-owned stock.");
          }}
          onError={setFailure}
        />
      ) : null}
      <DataTable label="Consignment lines" columns={lineColumns(order.currency)} rows={order.lines} getRowKey={(row) => row.id} empty={{ title: "No lines" }} />
    </div>
  );
}
