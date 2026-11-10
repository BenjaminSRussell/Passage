import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listConsignmentStock, type ConsignmentStockItem } from "./api/client";
import { dash, describeDimensions } from "./format";
import { ALL_DIMENSIONS, ProblemAlert } from "./parts";
import { Named } from "../purchasing/Named";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const STOCK_DIMENSIONS = ALL_DIMENSIONS.filter((name) => name !== "owner");

const columns: DataTableColumn<ConsignmentStockItem>[] = [
  { key: "vendorId", header: "Vendor", mono: true },
  { key: "productId", header: "Product", render: (row) => <Named entity="product" id={row.productId} /> },
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, STOCK_DIMENSIONS) },
  { key: "qty", header: "Vendor-owned", align: "end", mono: true },
  {
    key: "consignmentPrice",
    header: "Consignment price",
    align: "end",
    mono: true,
    render: (row) => (row.consignmentPrice === null ? dash : `${row.consignmentPrice} ${row.currency ?? ""}`.trim()),
  },
  {
    key: "statisticalValue",
    header: "Statistical value",
    align: "end",
    mono: true,
    render: (row) => (row.statisticalValue === null ? dash : `${row.statisticalValue} ${row.currency ?? ""}`.trim()),
  },
  { key: "oldestReceipt", header: "First received", mono: true, render: (row) => row.oldestReceipt ?? dash },
  { key: "ageDays", header: "Age (days)", align: "end", mono: true, render: (row) => (row.ageDays === null ? dash : String(row.ageDays)) },
];

/**
 * Consignment stock by vendor (PRD 3.16.14, SAP MB54): vendor-owned quantity
 * with its age. The value is a statistic; the stock is valued only when an
 * ownership change takes it into company ownership.
 */
export function ConsignmentStockPage() {
  const [params, setParams] = useSearchParams();
  const vendor = params.get("vendor") ?? "";
  const [draft, setDraft] = useState(vendor);
  const load = useLoad((signal) => listConsignmentStock(vendor, signal), vendor);

  function apply(event: FormEvent) {
    event.preventDefault();
    const next = draft.trim();
    setParams(next ? { vendor: next } : {});
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Consignment stock"
        description="Stock on hand that vendors still own. Take it into company ownership with an ownership change journal; it is valued at the consignment price then."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/inventory/consignment-orders">
              Consignment orders
            </Link>
            <Link className={buttonClassName("primary")} to="/inventory/journals/new?type=OWNERSHIP_CHANGE">
              New ownership change
            </Link>
          </>
        }
      />
      <Card title="Filter">
        <form onSubmit={apply} noValidate className={styles.toolbar}>
          <FormField id="consignment-vendor" label="Vendor" optional>
            {(control) => <Input {...control} value={draft} maxLength={60} onChange={(event) => setDraft(event.target.value)} />}
          </FormField>
          <Button type="submit" variant="primary">
            Apply
          </Button>
        </form>
      </Card>
      {load.error ? (
        <ProblemAlert title="Could not load consignment stock" problem={load.error} onRetry={load.reload} />
      ) : (
        <DataTable
          label="Consignment stock"
          columns={columns}
          rows={load.data ?? []}
          getRowKey={(row) => `${row.legalEntityId}|${row.vendorId}|${row.productId}|${JSON.stringify(row.dimensions)}`}
          loading={load.loading}
          empty={{
            title: vendor ? `No stock owned by ${vendor}` : "No vendor-owned stock",
            description: "Receive a consignment order to put vendor-owned stock on hand.",
          }}
        />
      )}
    </div>
  );
}
