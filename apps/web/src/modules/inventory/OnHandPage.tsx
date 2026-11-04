import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchOnHand, ON_HAND_GROUPS, type OnHandGroup, type OnHandRow } from "./api/client";
import { onHandCsv, downloadText } from "./csvExport";
import { dash, isIsoDate, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const GROUP_LABELS: Record<OnHandGroup, string> = {
  product: "Product",
  legal_entity: "Legal entity",
  site: "Site",
  warehouse: "Warehouse",
  location: "Location",
  batch: "Batch",
  serial: "Serial",
  inventory_status: "Inventory status",
  owner: "Owner (vendor)",
  segment: "Segment",
};

interface Filters {
  asOf: string;
  product: string;
  site: string;
  warehouse: string;
  batch: string;
  owner: string;
  segment: string;
}

const noFilters: Filters = { asOf: "", product: "", site: "", warehouse: "", batch: "", owner: "", segment: "" };

const physicalColumn: DataTableColumn<OnHandRow> = { key: "physicalInvent", header: "Physical", align: "end", mono: true };
/** Availability and reservations are live figures: a date replay does not answer them. */
const availabilityColumns: DataTableColumn<OnHandRow>[] = [
  { key: "availPhysical", header: "Available physical", align: "end", mono: true, render: (row) => row.availPhysical ?? dash },
  { key: "availOrdered", header: "Available ordered", align: "end", mono: true, render: (row) => row.availOrdered ?? dash },
  { key: "reservedPhysical", header: "Reserved", align: "end", mono: true, render: (row) => row.reservedPhysical ?? dash },
  { key: "reservedOrdered", header: "Reserved on order", align: "end", mono: true, render: (row) => row.reservedOrdered ?? dash },
  { key: "heldPhysical", header: "Held", align: "end", mono: true, render: (row) => row.heldPhysical ?? dash },
];
const amountColumns: DataTableColumn<OnHandRow>[] = [
  { key: "blocked", header: "Blocked", align: "end", mono: true, render: (row) => row.blocked ?? "0" },
  { key: "postedValue", header: "Posted value", align: "end", mono: true, render: (row) => `${row.postedValue} ${row.currency}`.trim() },
  {
    key: "negative",
    header: "Stock",
    render: (row) => (row.negative ? <Badge tone="danger">Negative stock</Badge> : dash),
  },
];

/** The on-hand groups the transaction inquiry can be narrowed to, with the query parameter each one fills. */
const DRILL_PARAMS: Partial<Record<OnHandGroup, string>> = {
  product: "product",
  legal_entity: "legalEntity",
  site: "site",
  warehouse: "warehouse",
  location: "location",
  batch: "batch",
  serial: "serial",
  inventory_status: "inventoryStatus",
  owner: "owner",
  segment: "segment",
};

/** The transaction inquiry link for one on-hand row: every dimension the row is grouped by narrows it. */
function drillLink(row: OnHandRow): string | null {
  const params = new URLSearchParams();
  for (const [group, name] of Object.entries(DRILL_PARAMS)) {
    const value = row.group[group as OnHandGroup];
    if (value) params.set(name, value);
  }
  const qs = params.toString();
  return qs ? `/inventory/transactions?${qs}` : null;
}

/** On-hand inquiry (FR-16.3): group by any subset of dimensions, filter, flag negative stock in text. */
export function OnHandPage() {
  const [groupBy, setGroupBy] = useState<OnHandGroup[]>(["product", "site"]);
  const [draft, setDraft] = useState<Filters>(noFilters);
  const [filters, setFilters] = useState<Filters>(noFilters);
  const [productError, setProductError] = useState<string | undefined>();
  const [dateError, setDateError] = useState<string | undefined>();
  const state = useLoad((signal) => fetchOnHand({ groupBy, ...filters }, signal), `${groupBy.join(",")}|${JSON.stringify(filters)}`);

  function toggle(group: OnHandGroup) {
    setGroupBy((current) => (current.includes(group) ? current.filter((g) => g !== group) : [...current, group]));
  }

  function apply(event: FormEvent) {
    event.preventDefault();
    const product = draft.product.trim();
    const asOf = draft.asOf.trim();
    const badProduct = product !== "" && !UUID.test(product);
    const badDate = asOf !== "" && !isIsoDate(asOf);
    setProductError(badProduct ? "Enter the product id (a UUID)." : undefined);
    setDateError(badDate ? "Enter a date as YYYY-MM-DD." : undefined);
    if (badProduct || badDate) return;
    setFilters({ ...draft, product, asOf });
  }

  function clear() {
    setProductError(undefined);
    setDateError(undefined);
    setDraft(noFilters);
    setFilters(noFilters);
  }

  const field = (key: keyof Filters) => ({
    value: draft[key],
    onChange: (event: { target: { value: string } }) => setDraft({ ...draft, [key]: event.target.value }),
  });

  const shown = groupBy.length > 0 ? ON_HAND_GROUPS.filter((g) => groupBy.includes(g)) : (["product"] as OnHandGroup[]);
  const columns: DataTableColumn<OnHandRow>[] = [
    ...shown.map((group) => ({
      key: `group-${group}`,
      header: GROUP_LABELS[group],
      mono: group === "product" || group === "legal_entity",
      render: (row: OnHandRow) => row.group[group] || dash,
    })),
    ...(shown.some((g) => g in DRILL_PARAMS)
      ? [
          {
            key: "drill",
            header: "History",
            render: (row: OnHandRow) => {
              const to = drillLink(row);
              return to ? <Link to={to}>Transactions</Link> : dash;
            },
          } satisfies DataTableColumn<OnHandRow>,
        ]
      : []),
    physicalColumn,
    ...(filters.asOf ? [] : availabilityColumns),
    ...amountColumns,
  ];
  const rows = state.data?.items ?? [];
  const [exportNote, setExportNote] = useState("");
  function exportCsv() {
    const csv = onHandCsv(rows, groupBy, Boolean(filters.asOf), GROUP_LABELS);
    const done = downloadText(`on-hand-${filters.asOf || new Date().toISOString().slice(0, 10)}.csv`, csv);
    setExportNote(done ? `Exported ${rows.length} ${rows.length === 1 ? "row" : "rows"}.` : "This browser cannot save the file.");
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="On-hand inquiry"
        description="Physical and available stock by any combination of dimensions, now or on a past date, straight from the posting ledger."
        actions={
          <Button onClick={exportCsv} disabled={state.loading || rows.length === 0}>
            Export CSV
          </Button>
        }
      />
      <Alert tone="info" live="status">
        {exportNote}
      </Alert>
      <Card title="Filter and group">
        <form onSubmit={apply} noValidate className={styles.form}>
          <FormField id="onhand-asof" label="As of date" optional error={dateError} hint="Stock and value at the end of that day. Leave empty for now.">
            {(control) => <Input {...control} type="date" {...field("asOf")} />}
          </FormField>
          <FormField id="onhand-product" label="Product" optional error={productError}>
            {(control) => <Input {...control} {...field("product")} placeholder="Product id" />}
          </FormField>
          <FormField id="onhand-site" label="Site" optional>
            {(control) => <Input {...control} {...field("site")} />}
          </FormField>
          <FormField id="onhand-warehouse" label="Warehouse" optional>
            {(control) => <Input {...control} {...field("warehouse")} />}
          </FormField>
          <FormField id="onhand-batch" label="Batch" optional>
            {(control) => <Input {...control} {...field("batch")} />}
          </FormField>
          <FormField id="onhand-owner" label="Owner (vendor)" optional hint="Vendor-owned consignment stock only.">
            {(control) => <Input {...control} {...field("owner")} />}
          </FormField>
          <FormField id="onhand-segment" label="Segment" optional hint="SALES_ORDER:reference or PROJECT:reference.">
            {(control) => <Input {...control} {...field("segment")} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Apply filters
            </Button>
            <Button onClick={clear}>Clear filters</Button>
          </div>
        </form>
        <fieldset className={styles.groups}>
          <legend className={styles.groupsLegend}>Group by</legend>
          {ON_HAND_GROUPS.map((group) => (
            <span key={group} className={styles.check}>
              <Input type="checkbox" id={`onhand-group-${group}`} checked={groupBy.includes(group)} onChange={() => toggle(group)} />
              <label htmlFor={`onhand-group-${group}`}>{GROUP_LABELS[group]}</label>
            </span>
          ))}
        </fieldset>
      </Card>
      {state.error ? (
        <Alert
          tone="danger"
          title="Could not load on-hand"
          live="alert"
          action={<Button size="sm" onClick={state.reload}>Try again</Button>}
        >
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      {state.data?.asOf ? (
        <Alert tone="info" title={`Stock as of ${state.data.asOf}`}>
          Replayed from the ledger up to that date. Available, ordered and reserved quantities are not kept per date and are not shown.
        </Alert>
      ) : null}
      {state.data?.truncated ? (
        <Alert tone="warning" title="Showing the first 10,000 groups">
          Narrow the filters or group by fewer dimensions to see the rest.
        </Alert>
      ) : null}
      {state.error ? null : (
        <DataTable
          label="On-hand"
          columns={columns}
          rows={rows}
          getRowKey={(row, i) => `${shown.map((g) => row.group[g] ?? "").join("|")}|${row.currency}|${i}`}
          loading={state.loading}
          empty={{ title: "No stock matches", description: "Post an opening balance or movement journal, or widen the filters." }}
        />
      )}
    </div>
  );
}
