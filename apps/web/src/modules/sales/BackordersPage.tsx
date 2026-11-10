import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listBackorders, type BackorderFilter, type BackorderItem } from "./api/client";
import { dash, sentence, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const DATE = /^\d{4}-\d{2}-\d{2}$/;

export interface BackorderDraft {
  legalEntityId: string;
  productId: string;
  customerId: string;
  promisedTo: string;
}

/** Checks that mirror the service (api/schedule.go). Keys are the filter's field names. */
export function validateBackorderFilter(draft: BackorderDraft): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const key of ["legalEntityId", "productId", "customerId"] as const) {
    const value = draft[key].trim();
    if (value && !UUID.test(value)) errors[key] = "Enter a UUID.";
  }
  if (draft.promisedTo && !DATE.test(draft.promisedTo)) errors.promisedTo = "Choose a date.";
  return errors;
}

/** "3 days late", "on time" or the dash when no date is promised. */
export function lateness(item: BackorderItem): string {
  if (item.daysLate === undefined) return dash;
  return item.daysLate <= 0 ? "On time" : `${item.daysLate} ${item.daysLate === 1 ? "day" : "days"} late`;
}

const columns: DataTableColumn<BackorderItem>[] = [
  { key: "orderNumber", header: "Order", mono: true, render: (b) => <Link to={`/sales/orders/${b.orderId}`}>{b.orderNumber}</Link> },
  { key: "lineNo", header: "Line", align: "end" },
  { key: "customerId", header: "Customer", mono: true, render: (b) => shortId(b.customerId) },
  { key: "productId", header: "Product", mono: true, render: (b) => shortId(b.productId) },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "requestedShipDate", header: "Requested", mono: true },
  { key: "promisedDate", header: "Promised", mono: true, render: (b) => b.promisedDate ?? "Not promised" },
  { key: "daysLate", header: "Late by", render: lateness },
  { key: "status", header: "Status", render: (b) => <Badge tone={b.status === "UNPROMISED" ? "danger" : "warning"}>{sentence(b.status)}</Badge> },
  {
    key: "scheduleLineId",
    header: "Action",
    render: (b) => (
      <Link to={`/sales/orders/${b.orderId}/promise?line=${b.lineNo}`} aria-label={`Re-promise ${b.orderNumber} line ${b.lineNo}`}>
        Re-promise
      </Link>
    ),
  },
];

/**
 * Backorder list (FR-27.6, AC-27.8): schedule lines promised after the requested
 * date or not at all, earliest promised date first, undated lines last.
 */
export function BackordersPage() {
  const empty: BackorderDraft = { legalEntityId: "", productId: "", customerId: "", promisedTo: "" };
  const [draft, setDraft] = useState<BackorderDraft>(empty);
  const [filter, setFilter] = useState<BackorderFilter>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const state = useLoad((signal) => listBackorders(filter, signal), JSON.stringify(filter));
  const items = state.data ?? [];

  function apply(event: React.FormEvent) {
    event.preventDefault();
    const found = validateBackorderFilter(draft);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setFilter({
      legalEntityId: draft.legalEntityId.trim(),
      productId: draft.productId.trim(),
      customerId: draft.customerId.trim(),
      promisedTo: draft.promisedTo,
    });
  }

  const field = (key: keyof BackorderDraft, label: string, type?: string) => (
    <FormField id={`bo-${key}`} label={label} error={errors[key]} optional>
      {(control) => (
        <Input {...control} type={type} className={type ? undefined : styles.mono} value={draft[key]} onChange={(e) => setDraft({ ...draft, [key]: e.target.value })} invalid={Boolean(errors[key])} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Backorders"
        description="Order lines promised after the requested date or not at all. Promise a line against ATP from its order to split it into schedule lines."
      />
      <form className={styles.toolbar} onSubmit={apply} noValidate aria-label="Filter backorders">
        {field("legalEntityId", "Legal entity")}
        {field("productId", "Product")}
        {field("customerId", "Customer")}
        {field("promisedTo", "Promised by", "date")}
        <Button type="submit">Filter</Button>
        <Button
          variant="ghost"
          onClick={() => {
            setDraft(empty);
            setErrors({});
            setFilter({});
          }}
        >
          Clear
        </Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load the backorders" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <>
          <p role="status" className={styles.muted}>
            {state.loading ? "" : `${items.length} ${items.length === 1 ? "line" : "lines"} on backorder.`}
          </p>
          <DataTable
            label="Backorder lines"
            columns={columns}
            rows={items}
            getRowKey={(b) => b.scheduleLineId}
            loading={state.loading}
            empty={{ title: "No backorders", description: "Lines promised on their requested date are not listed. Partial promises and unpromised quantity appear here." }}
          />
        </>
      )}
    </div>
  );
}

