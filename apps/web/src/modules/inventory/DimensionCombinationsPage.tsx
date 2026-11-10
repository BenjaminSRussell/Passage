import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listDimensionCombinations, type DimensionCombination } from "./api/client";
import { describeDimensions, UUID } from "./format";
import { ALL_DIMENSIONS, ProblemAlert } from "./parts";
import { Named } from "../purchasing/Named";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

const columns: DataTableColumn<DimensionCombination>[] = [
  { key: "productId", header: "Product", render: (row) => <Named entity="product" id={row.productId} /> },
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
  { key: "activeDimensions", header: "Active", render: (row) => row.activeDimensions.join(", ") },
  { key: "firstUse", header: "First use", mono: true },
  { key: "lastUse", header: "Last use", mono: true },
  { key: "transactionCount", header: "Transactions", align: "end", render: (row) => String(row.transactionCount) },
];

/** Checks the filter form before it is sent. */
export function validateCombinationFilters(productId: string, legalEntityId: string): { productId?: string; legalEntityId?: string } {
  const errors: { productId?: string; legalEntityId?: string } = {};
  if (productId.trim() !== "" && !UUID.test(productId.trim())) errors.productId = "Enter the product id (a UUID).";
  if (legalEntityId.trim() !== "" && !UUID.test(legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  return errors;
}

/** Dimension combinations inquiry (3.16.14): every dimension set a product has used, with first and last use. */
export function DimensionCombinationsPage() {
  const [draft, setDraft] = useState({ productId: "", legalEntityId: "" });
  const [filters, setFilters] = useState({ productId: "", legalEntityId: "" });
  const [errors, setErrors] = useState<{ productId?: string; legalEntityId?: string }>({});
  const load = useLoad((signal) => listDimensionCombinations(filters, signal), JSON.stringify(filters));

  function apply(event: FormEvent) {
    event.preventDefault();
    const found = validateCombinationFilters(draft.productId, draft.legalEntityId);
    setErrors(found);
    if (Object.keys(found).length === 0) setFilters(draft);
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Dimension combinations" description="The site, warehouse, location, batch and other dimension sets each product has had stock movements in." />
      <Card title="Filter">
        <form onSubmit={apply} noValidate className={styles.form}>
          <LookupFormField id="combinations-product" label="Product" entity="product" optional prefill={false} value={draft.productId} error={errors.productId} onChange={(productId) => setDraft({ ...draft, productId })} />
          <LookupFormField id="combinations-entity" label="Legal entity" entity="legalEntity" optional prefill={false} value={draft.legalEntityId} error={errors.legalEntityId} onChange={(legalEntityId) => setDraft({ ...draft, legalEntityId })} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Apply filters
            </Button>
          </div>
        </form>
      </Card>
      {load.data?.truncated ? (
        <Alert tone="info" title="Showing the first 1,000 combinations">
          More combinations match. Filter by product to see the rest.
        </Alert>
      ) : null}
      {load.error ? (
        <ProblemAlert title="Could not load dimension combinations" problem={load.error} onRetry={load.reload} />
      ) : (
        <DataTable
          label="Dimension combinations"
          columns={columns}
          rows={load.data?.items ?? []}
          getRowKey={(row) => `${row.legalEntityId}|${row.productId}|${row.dimensionSetHash}`}
          loading={load.loading}
          empty={{ title: "No dimension combinations", description: "Combinations appear once stock has moved." }}
        />
      )}
    </div>
  );
}
