import { useState } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link } from "react-router-dom";
import { Button, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listOpenOrderLines, type OpenOrderLine, type OpenSupplyFilters } from "./api/client";
import { LoadError, ShortId, datePattern, useResource, uuidPattern } from "./parts";
import styles from "./pages.module.css";

interface Draft {
  warehouseId: string;
  siteId: string;
  productId: string;
  dueFrom: string;
  dueTo: string;
}

const emptyDraft: Draft = { warehouseId: "", siteId: "", productId: "", dueFrom: "", dueTo: "" };

type Errors = Partial<Record<keyof Draft, string>>;

/** Checks the filters before the round trip: ids are UUIDs, dates are dates and the range is not backwards. */
export function validateOpenSupply(d: Draft): Errors {
  const errors: Errors = {};
  for (const [key, label] of [
    ["warehouseId", "Warehouse id"],
    ["siteId", "Site id"],
    ["productId", "Product id"],
  ] as const) {
    if (d[key].trim() !== "" && !uuidPattern.test(d[key].trim())) errors[key] = `${label} must be a UUID.`;
  }
  for (const key of ["dueFrom", "dueTo"] as const) {
    if (d[key] !== "" && !datePattern.test(d[key])) errors[key] = "Enter a date.";
  }
  if (!errors.dueFrom && !errors.dueTo && d.dueFrom !== "" && d.dueTo !== "" && d.dueTo < d.dueFrom) errors.dueTo = "The end date is before the start date.";
  return errors;
}

const toFilters = (d: Draft): OpenSupplyFilters => {
  const opt = (v: string) => (v.trim() === "" ? undefined : v.trim());
  return { warehouseId: opt(d.warehouseId), siteId: opt(d.siteId), productId: opt(d.productId), dueFrom: opt(d.dueFrom), dueTo: opt(d.dueTo) };
};

/**
 * Open supply (FR-17.14): the lines of confirmed purchase orders that are still to be received, with the
 * warehouse each is delivered to. This is the view master planning nets against a warehouse's demand.
 */
export function OpenSupplyPage() {
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [applied, setApplied] = useState<Draft>(emptyDraft);
  const [errors, setErrors] = useState<Errors>({});
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<OpenOrderLine[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listOpenOrderLines(toFilters(applied), { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [applied, cursor],
  );

  const apply = () => {
    const found = validateOpenSupply(draft);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setCursor(undefined);
    setApplied(draft);
  };
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));

  const columns: DataTableColumn<OpenOrderLine>[] = [
    {
      key: "po",
      header: "Purchase order",
      render: (l) => (
        <Link className={styles.link} to={`/purchasing/purchase-orders/${l.purchaseOrderId}`}>
          {l.number}
        </Link>
      ),
    },
    { key: "lineNo", header: "Line", align: "end" },
    { key: "item", header: "Item", render: (l) => l.description || <ShortId id={l.productId} /> },
    { key: "warehouseId", header: "Warehouse", render: (l) => <ShortId id={l.warehouseId} /> },
    { key: "siteId", header: "Site", render: (l) => <ShortId id={l.siteId} /> },
    { key: "openQty", header: "Open quantity", align: "end", render: (l) => `${l.openQty} ${l.unit}` },
    { key: "qty", header: "Ordered", align: "end", render: (l) => `${l.qty} ${l.unit}` },
    { key: "deliveryDate", header: "Due", mono: true },
    { key: "confirmedDate", header: "Vendor confirmed", mono: true, render: (l) => l.confirmedDate ?? "—" },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Open supply"
        description="Lines of confirmed purchase orders still to be received, by delivery warehouse and due date. Master planning reads the same list."
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          apply();
        }}
      >
        <div className={styles.filter}>
          <FormField id="supply-warehouse" label="Warehouse" optional error={errors.warehouseId}>
            {(c) => <EntityLookup entity="warehouse" {...c} value={draft.warehouseId} onChange={(e) => set({ warehouseId: e.target.value })} />}
          </FormField>
        </div>
        <div className={styles.filter}>
          <FormField id="supply-site" label="Site" optional error={errors.siteId}>
            {(c) => <EntityLookup entity="site" prefill={false} {...c} value={draft.siteId} onChange={(e) => set({ siteId: e.target.value })} />}
          </FormField>
        </div>
        <div className={styles.filter}>
          <FormField id="supply-product" label="Product" optional error={errors.productId}>
            {(c) => <EntityLookup entity="product" {...c} value={draft.productId} onChange={(e) => set({ productId: e.target.value })} />}
          </FormField>
        </div>
        <div className={styles.filter}>
          <FormField id="supply-from" label="Due from" optional error={errors.dueFrom}>
            {(c) => <Input {...c} type="date" value={draft.dueFrom} onChange={(e) => set({ dueFrom: e.target.value })} />}
          </FormField>
        </div>
        <div className={styles.filter}>
          <FormField id="supply-to" label="Due to" optional error={errors.dueTo}>
            {(c) => <Input {...c} type="date" value={draft.dueTo} onChange={(e) => set({ dueTo: e.target.value })} />}
          </FormField>
        </div>
        <div className={styles.filter}>
          <Button type="submit">Show supply</Button>
        </div>
      </form>
      {page.error ? <LoadError what="open supply" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Open order lines"
        columns={columns}
        rows={page.error ? [] : rows}
        getRowKey={(l) => `${l.purchaseOrderId}:${l.lineNo}`}
        loading={page.loading && rows.length === 0}
        empty={{
          title: "No open supply",
          description: "Confirmed purchase order lines appear here until they are received in full.",
        }}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button loading={page.loading} onClick={() => setCursor(page.data?.nextCursor)}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}
