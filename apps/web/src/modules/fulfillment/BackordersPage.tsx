import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listBackorders, type BackorderRow } from "./api/client";
import { ShipmentStatusBadge } from "./badges";
import { dash, formatDateTime, shortId, SHORT_PICK_REASONS, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** Rows per page of the report. */
export const BACKORDER_PAGE = 100;

const columns: DataTableColumn<BackorderRow>[] = [
  {
    key: "shipmentNumber",
    header: "Shipment",
    mono: true,
    render: (row) => <Link to={`/fulfillment/shipments/${row.shipmentId}`}>{row.shipmentNumber}</Link>,
  },
  { key: "status", header: "Shipment status", render: (row) => <ShipmentStatusBadge status={row.status} /> },
  { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
  { key: "customerId", header: "Customer", mono: true, render: (row) => shortId(row.customerId) },
  { key: "qtyPlanned", header: "Planned", align: "end", render: (row) => `${row.qtyPlanned} ${row.unit}` },
  { key: "qtyPicked", header: "Picked", align: "end", render: (row) => `${row.qtyPicked} ${row.unit}` },
  { key: "qtyRemainder", header: "Backorder", align: "end", render: (row) => `${row.qtyRemainder} ${row.unit}` },
  { key: "qtyClosed", header: "Qty closed", align: "end", render: (row) => `${row.qtyClosed} ${row.unit}` },
  {
    key: "open",
    header: "Backorder state",
    render: (row) => (row.open ? <Badge tone="warning">Open</Badge> : <Badge tone="neutral">Closed</Badge>),
  },
  { key: "reason", header: "Reason", render: (row) => (row.reason ? (SHORT_PICK_REASONS[row.reason] ?? row.reason) : dash) },
  { key: "shortPickedAt", header: "Short-picked", render: (row) => formatDateTime(row.shortPickedAt) },
];

interface Filter {
  warehouseId: string;
  status: "open" | "all";
  from: string;
  to: string;
}

const blank: Filter = { warehouseId: "", status: "open", from: "", to: "" };
const DATE = /^\d{4}-\d{2}-\d{2}$/;

function validate(values: Filter): Record<string, string> {
  const found: Record<string, string> = {};
  if (values.warehouseId !== "" && !UUID.test(values.warehouseId)) found.warehouseId = "Enter the warehouse id (a UUID), or leave it empty.";
  if (values.from !== "" && !DATE.test(values.from)) found.from = "Enter a date.";
  if (values.to !== "" && !DATE.test(values.to)) found.to = "Enter a date.";
  if (!found.from && !found.to && values.from !== "" && values.to !== "" && values.to < values.from) found.to = "The last date cannot be before the first.";
  return found;
}

/**
 * Short picks and backorders (FR-28.2, section 5.28.14): shipment lines picked
 * below plan, newest short pick first. The remainder stays a backorder until
 * it is re-picked on the shipment detail, its remainder is closed there, or the
 * shipment is cancelled.
 */
export function BackordersPage() {
  const [values, setValues] = useState<Filter>(blank);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [filter, setFilter] = useState<Filter>(blank);
  // Cursors of the pages before the current one; the last entry is the current page's cursor.
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.length > 0 ? cursors[cursors.length - 1] : undefined;
  const state = useLoad(
    (signal) =>
      listBackorders(
        {
          warehouseId: filter.warehouseId || undefined,
          status: filter.status === "all" ? "all" : undefined,
          from: filter.from || undefined,
          to: filter.to || undefined,
          cursor,
          limit: BACKORDER_PAGE,
        },
        signal,
      ),
    `${filter.warehouseId}/${filter.status}/${filter.from}/${filter.to}/${cursor ?? ""}`,
  );

  function apply(event: FormEvent) {
    event.preventDefault();
    const trimmed = { ...values, warehouseId: values.warehouseId.trim() };
    const found = validate(trimmed);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setFilter(trimmed);
    setCursors([]);
  }

  function clear() {
    setValues(blank);
    setErrors({});
    setFilter(blank);
    setCursors([]);
  }

  const filtered = filter.warehouseId !== "" || filter.status !== "open" || filter.from !== "" || filter.to !== "";
  const next = state.data?.nextCursor;
  const field = (key: "warehouseId" | "from" | "to") => ({
    value: values[key],
    invalid: Boolean(errors[key]),
    onChange: (event: { target: { value: string } }) => setValues({ ...values, [key]: event.target.value }),
  });

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Short picks and backorders"
        description="Lines picked below their planned quantity. The remainder is a backorder until it is re-picked on the shipment or the shipment is cancelled."
      />
      <Card title="Filter">
        <form className={styles.form} onSubmit={apply} noValidate>
          <FormField id="bo-warehouse" label="Warehouse id" error={errors.warehouseId} optional>
            {(control) => <Input {...control} {...field("warehouseId")} />}
          </FormField>
          <FormField id="bo-status" label="Show">
            {(control) => (
              <Select {...control} value={values.status} onChange={(event) => setValues({ ...values, status: event.target.value as Filter["status"] })}>
                <option value="open">Open backorders</option>
                <option value="all">Open and closed</option>
              </Select>
            )}
          </FormField>
          <FormField id="bo-from" label="Short-picked from" error={errors.from} optional>
            {(control) => <Input {...control} {...field("from")} type="date" />}
          </FormField>
          <FormField id="bo-to" label="Short-picked to" error={errors.to} optional>
            {(control) => <Input {...control} {...field("to")} type="date" />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Apply filter
            </Button>
            {filtered ? (
              <Button type="button" onClick={clear}>
                Clear filter
              </Button>
            ) : null}
          </div>
        </form>
      </Card>
      {state.error ? (
        <Alert tone="danger" title="Could not load the backorders" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <>
          <DataTable
            label="Short picks and backorders"
            columns={columns}
            rows={state.data?.items ?? []}
            getRowKey={(row) => row.shipmentLineId}
            loading={state.loading}
            empty={
              filtered
                ? { title: "No short picks match the filter", description: "Change the warehouse, dates or what to show, or clear the filter." }
                : { title: "No open backorders", description: "A pick confirmed below the planned quantity shows here until its remainder is picked." }
            }
          />
          {cursors.length > 0 || next ? (
            <div className={styles.actions}>
              <Button size="sm" disabled={cursors.length === 0 || state.loading} onClick={() => setCursors(cursors.slice(0, -1))}>
                Previous page
              </Button>
              <Button size="sm" disabled={!next || state.loading} onClick={() => next && setCursors([...cursors, next])}>
                Next page
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
