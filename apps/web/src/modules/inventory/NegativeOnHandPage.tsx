import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listNegativeOnHand, type NegativeOnHandLine } from "./api/client";
import { dash, describeDimensions, UUID } from "./format";
import { ALL_DIMENSIONS, money, ProblemAlert } from "./parts";
import { Named } from "../purchasing/Named";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<NegativeOnHandLine>[] = [
  { key: "productId", header: "Product", render: (row) => <Named entity="product" id={row.productId} /> },
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
  { key: "physicalInvent", header: "Physical", align: "end", mono: true },
  { key: "shortfallQty", header: "Shortfall", align: "end", mono: true },
  { key: "postedValue", header: "Posted value", align: "end", mono: true, render: (row) => (row.currency ? money(row.postedValue, row.currency) : row.postedValue) },
  { key: "firstNegativeDate", header: "First negative", mono: true, render: (row) => row.firstNegativeDate ?? dash },
];

export interface NegativeFilters {
  legalEntityId: string;
  productId: string;
  siteId: string;
  warehouseId: string;
}

const noFilters: NegativeFilters = { legalEntityId: "", productId: "", siteId: "", warehouseId: "" };

/** Checks the filter form before it is sent. */
export function validateNegativeFilters(f: NegativeFilters): Partial<Record<keyof NegativeFilters, string>> {
  const errors: Partial<Record<keyof NegativeFilters, string>> = {};
  if (f.legalEntityId.trim() !== "" && !UUID.test(f.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (f.productId.trim() !== "" && !UUID.test(f.productId.trim())) errors.productId = "Enter the product id (a UUID).";
  return errors;
}

/** Negative on-hand list (3.16.14): rows whose physical stock is below zero, with the date they first went negative. */
export function NegativeOnHandPage() {
  const [draft, setDraft] = useState<NegativeFilters>(noFilters);
  const [filters, setFilters] = useState<NegativeFilters>(noFilters);
  const [errors, setErrors] = useState<Partial<Record<keyof NegativeFilters, string>>>({});
  const load = useLoad((signal) => listNegativeOnHand(filters, signal), JSON.stringify(filters));
  const rows = load.data?.items ?? [];

  function apply(event: FormEvent) {
    event.preventDefault();
    const found = validateNegativeFilters(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) setFilters(draft);
  }
  const set = (name: keyof NegativeFilters) => (event: { target: { value: string } }) => setDraft({ ...draft, [name]: event.target.value });

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Negative on-hand"
        description="Stock rows that have gone below zero, largest shortfall first. Post a receipt or an adjustment to correct them."
        actions={
          <Link className={buttonClassName("ghost")} to="/inventory/onhand">
            On-hand inquiry
          </Link>
        }
      />
      <Card title="Filter">
        <form onSubmit={apply} noValidate className={styles.form}>
          <FormField id="negative-site" label="Site" optional>
            {(control) => <Input {...control} value={draft.siteId} onChange={set("siteId")} />}
          </FormField>
          <FormField id="negative-warehouse" label="Warehouse" optional>
            {(control) => <Input {...control} value={draft.warehouseId} onChange={set("warehouseId")} />}
          </FormField>
          <FormField id="negative-product" label="Product" optional error={errors.productId}>
            {(control) => <Input {...control} value={draft.productId} onChange={set("productId")} placeholder="Product id" invalid={Boolean(errors.productId)} />}
          </FormField>
          <FormField id="negative-entity" label="Legal entity" optional error={errors.legalEntityId}>
            {(control) => <Input {...control} value={draft.legalEntityId} onChange={set("legalEntityId")} placeholder="Legal entity id" invalid={Boolean(errors.legalEntityId)} />}
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
      {load.data?.truncated ? (
        <Alert tone="info" title="Showing the first 1,000 rows">
          More rows are negative. Narrow the filters to see the rest.
        </Alert>
      ) : null}
      {load.error ? (
        <ProblemAlert title="Could not load the negative on-hand list" problem={load.error} onRetry={load.reload} />
      ) : (
        <DataTable
          label="Negative on-hand"
          columns={columns}
          rows={rows}
          getRowKey={(row) => `${row.legalEntityId}|${row.productId}|${JSON.stringify(row.dimensions)}`}
          loading={load.loading}
          empty={{ title: "No negative stock", description: "Every stock row is at or above zero." }}
        />
      )}
    </div>
  );
}
