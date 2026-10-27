import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchOnHand, ON_HAND_GROUPS, type OnHandGroup, type OnHandRow } from "./api/client";
import { dash, UUID } from "./format";
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
};

interface Filters {
  product: string;
  site: string;
  warehouse: string;
  batch: string;
}

const noFilters: Filters = { product: "", site: "", warehouse: "", batch: "" };

const amountColumns: DataTableColumn<OnHandRow>[] = [
  { key: "physicalInvent", header: "Physical", align: "end", mono: true },
  { key: "availPhysical", header: "Available physical", align: "end", mono: true },
  { key: "availOrdered", header: "Available ordered", align: "end", mono: true },
  { key: "reservedPhysical", header: "Reserved", align: "end", mono: true },
  { key: "blocked", header: "Blocked", align: "end", mono: true, render: (row) => row.blocked ?? "0" },
  { key: "postedValue", header: "Posted value", align: "end", mono: true, render: (row) => `${row.postedValue} ${row.currency}`.trim() },
  {
    key: "negative",
    header: "Stock",
    render: (row) => (row.negative ? <Badge tone="danger">Negative stock</Badge> : dash),
  },
];

/** On-hand inquiry (FR-16.3): group by any subset of dimensions, filter, flag negative stock in text. */
export function OnHandPage() {
  const [groupBy, setGroupBy] = useState<OnHandGroup[]>(["product", "site"]);
  const [draft, setDraft] = useState<Filters>(noFilters);
  const [filters, setFilters] = useState<Filters>(noFilters);
  const [productError, setProductError] = useState<string | undefined>();
  const state = useLoad((signal) => fetchOnHand({ groupBy, ...filters }, signal), `${groupBy.join(",")}|${JSON.stringify(filters)}`);

  function toggle(group: OnHandGroup) {
    setGroupBy((current) => (current.includes(group) ? current.filter((g) => g !== group) : [...current, group]));
  }

  function apply(event: FormEvent) {
    event.preventDefault();
    const product = draft.product.trim();
    if (product !== "" && !UUID.test(product)) {
      setProductError("Enter the product id (a UUID).");
      return;
    }
    setProductError(undefined);
    setFilters({ ...draft, product });
  }

  function clear() {
    setProductError(undefined);
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
    ...amountColumns,
  ];
  const rows = state.data?.items ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="On-hand inquiry"
        description="Physical and available stock by any combination of dimensions, straight from the posting ledger."
      />
      <Card title="Filter and group">
        <form onSubmit={apply} noValidate className={styles.form}>
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
