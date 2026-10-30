import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listTransferLines, TRANSFER_STATUSES, type TransferLineItem, type TransferStatus } from "./api/client";
import { dash, describeDimensions, sentence, shortId, UUID } from "./format";
import { ALL_DIMENSIONS, ProblemAlert, stateTones } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

const columns: DataTableColumn<TransferLineItem>[] = [
  {
    key: "number",
    header: "Transfer order",
    mono: true,
    render: (row) => <Link to={`/inventory/transfer-orders/${row.orderId}`}>{row.number}</Link>,
  },
  { key: "lineNo", header: "Line", align: "end", render: (row) => String(row.lineNo) },
  { key: "orderStatus", header: "Status", render: (row) => <Badge tone={stateTones[row.orderStatus] ?? "neutral"}>{sentence(row.orderStatus)}</Badge> },
  { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
  { key: "route", header: "Route", mono: true, render: (row) => `${row.fromWarehouseId} → ${row.toWarehouseId}` },
  { key: "shipDate", header: "Ship date", mono: true },
  { key: "qty", header: "Ordered", align: "end", mono: true, render: (row) => `${row.qty} ${row.unit}` },
  { key: "shippedQty", header: "Shipped", align: "end", mono: true },
  { key: "receivedQty", header: "Received", align: "end", mono: true },
  { key: "remainingQty", header: "Remaining", align: "end", mono: true },
  { key: "inTransit", header: "In transit", align: "end", mono: true },
  { key: "daysInTransit", header: "Days in transit", align: "end", mono: true, render: (row) => (row.daysInTransit > 0 ? String(row.daysInTransit) : dash) },
  {
    key: "late",
    header: "Timing",
    render: (row) =>
      row.late === "LATE_SHIPMENT" ? (
        <Badge tone="danger">{`Late shipment, ${plural(row.daysLate, "day", "days")} overdue`}</Badge>
      ) : row.late === "LATE_RECEIPT" ? (
        <Badge tone="warning">{`Late receipt, ${plural(row.daysLate, "day", "days")} overdue`}</Badge>
      ) : row.closed && Number(row.inTransit) > 0 ? (
        <Badge tone="neutral">Closed short, investigate</Badge>
      ) : (
        dash
      ),
  },
];

export interface TransferLineFilters {
  legalEntityId: string;
  status: TransferStatus | "";
  productId: string;
  fromWarehouseId: string;
  toWarehouseId: string;
  from: string;
  to: string;
  lateOnly: boolean;
}

const noFilters: TransferLineFilters = { legalEntityId: "", status: "", productId: "", fromWarehouseId: "", toWarehouseId: "", from: "", to: "", lateOnly: false };

type FilterErrors = Partial<Record<keyof TransferLineFilters, string>>;

/** Checks the filter form before it is sent; the keys are the fields in error. */
export function validateTransferLineFilters(f: TransferLineFilters): FilterErrors {
  const errors: FilterErrors = {};
  if (f.legalEntityId.trim() !== "" && !UUID.test(f.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (f.productId.trim() !== "" && !UUID.test(f.productId.trim())) errors.productId = "Enter the product id (a UUID).";
  if (f.from && f.to && f.to < f.from) errors.to = "The end date is before the start date.";
  return errors;
}

/** Transfer order line inquiry and late overview (FR-16.15): every line across orders, with late shipments and receipts flagged. */
export function TransferLinesPage() {
  const [draft, setDraft] = useState<TransferLineFilters>(noFilters);
  const [filters, setFilters] = useState<TransferLineFilters>(noFilters);
  const [errors, setErrors] = useState<FilterErrors>({});
  const load = useLoad((signal) => listTransferLines(filters, signal), JSON.stringify(filters));
  const rows = load.data?.items ?? [];
  const shipments = rows.filter((row) => row.late === "LATE_SHIPMENT").length;
  const receipts = rows.filter((row) => row.late === "LATE_RECEIPT").length;

  function apply(event: FormEvent) {
    event.preventDefault();
    const found = validateTransferLineFilters(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) setFilters(draft);
  }

  const set = (name: Exclude<keyof TransferLineFilters, "lateOnly">) => (event: { target: { value: string } }) =>
    setDraft({ ...draft, [name]: event.target.value });

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Transfer order lines"
        description="Every transfer line across orders. A line is late when its order has not shipped by the planned ship date, or when its shipment has been in transit more than seven days."
        actions={
          <Link className={buttonClassName("ghost")} to="/inventory/transfer-orders">
            Transfer orders
          </Link>
        }
      />
      <Card title="Filter">
        <form onSubmit={apply} noValidate className={styles.form}>
          <FormField id="transfer-lines-status" label="Order status" optional>
            {(control) => (
              <Select {...control} value={draft.status} onChange={set("status")}>
                <option value="">Any status</option>
                {TRANSFER_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {sentence(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="transfer-lines-late" label="Show">
            {(control) => (
              <Select {...control} value={draft.lateOnly ? "late" : "all"} onChange={(event) => setDraft({ ...draft, lateOnly: event.target.value === "late" })}>
                <option value="all">All lines</option>
                <option value="late">Late lines only</option>
              </Select>
            )}
          </FormField>
          <FormField id="transfer-lines-from-warehouse" label="Shipping warehouse" optional>
            {(control) => <Input {...control} value={draft.fromWarehouseId} onChange={set("fromWarehouseId")} />}
          </FormField>
          <FormField id="transfer-lines-to-warehouse" label="Receiving warehouse" optional>
            {(control) => <Input {...control} value={draft.toWarehouseId} onChange={set("toWarehouseId")} />}
          </FormField>
          <FormField id="transfer-lines-product" label="Product" optional error={errors.productId}>
            {(control) => <Input {...control} value={draft.productId} onChange={set("productId")} placeholder="Product id" invalid={Boolean(errors.productId)} />}
          </FormField>
          <FormField id="transfer-lines-legal-entity" label="Legal entity" optional error={errors.legalEntityId}>
            {(control) => (
              <Input {...control} value={draft.legalEntityId} onChange={set("legalEntityId")} placeholder="Legal entity id" invalid={Boolean(errors.legalEntityId)} />
            )}
          </FormField>
          <FormField id="transfer-lines-from" label="Ship date from" optional>
            {(control) => <Input {...control} type="date" value={draft.from} onChange={set("from")} />}
          </FormField>
          <FormField id="transfer-lines-to" label="Ship date to" optional error={errors.to}>
            {(control) => <Input {...control} type="date" value={draft.to} onChange={set("to")} invalid={Boolean(errors.to)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Apply filters
            </Button>
            <Button
              onClick={() => {
                setErrors({});
                setDraft(noFilters);
                setFilters(noFilters);
              }}
            >
              Clear filters
            </Button>
          </div>
        </form>
      </Card>
      {shipments + receipts > 0 ? (
        <Alert tone="warning" title="Late transfers">
          {`${plural(shipments, "late shipment", "late shipments")} and ${plural(receipts, "late receipt", "late receipts")} in this list.`}
        </Alert>
      ) : null}
      {load.data?.truncated ? (
        <Alert tone="info" title="Showing the first 1,000 lines">
          More lines match. Narrow the filters to see the rest.
        </Alert>
      ) : null}
      {load.error ? (
        <ProblemAlert title="Could not load transfer order lines" problem={load.error} onRetry={load.reload} />
      ) : (
        <DataTable
          label="Transfer order lines"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.lineId}
          loading={load.loading}
          empty={{
            title: filters.lateOnly ? "No late transfer lines" : "No transfer order lines",
            description: filters.lateOnly
              ? "Every transfer that matches the filters is on schedule."
              : "Create a transfer order to move stock from one warehouse to another, or widen the filters.",
          }}
        />
      )}
    </div>
  );
}
