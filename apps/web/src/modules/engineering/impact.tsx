import { useEffect, useState } from "react";
import { Alert, Button, Card, DataTable, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { getEcoImpact, type EcoImpact, type EcoImpactProduct, type EcoLineChange } from "./api/client";
import styles from "./pages.module.css";

const productColumns: DataTableColumn<EcoImpactProduct>[] = [
  { key: "productId", header: "Product", mono: true },
  { key: "reason", header: "Why affected", render: (row) => (row.reason === "replaced" ? "Replaced" : `Uses it (level ${row.level})`) },
  { key: "topLevel", header: "Top level", render: (row) => (row.topLevel ? "Yes" : "No") },
  { key: "siteIds", header: "Sites", render: (row) => (row.siteIds.length > 0 ? row.siteIds.join(", ") : "All sites") },
  { key: "versionIds", header: "Active versions", align: "end", render: (row) => row.versionIds.length },
];

const changeColumns: DataTableColumn<EcoLineChange>[] = [
  { key: "componentProductId", header: "Component", mono: true },
  { key: "change", header: "Change", render: (row) => <StatusPill status={row.change} /> },
  { key: "oldQuantity", header: "Old quantity", align: "end", render: (row) => row.oldQuantity ?? "None" },
  { key: "newQuantity", header: "New quantity", align: "end", render: (row) => row.newQuantity ?? "None" },
];

function plural(n: number, one: string, many: string) {
  return `${n} ${n === 1 ? one : many}`;
}

// EcoImpactPanel loads the FR-9.8 impact report on demand: the service walks the where-used tree of each replaced product.
// Render it with key={id} so another ECO starts from the collapsed state.
export function EcoImpactPanel({ id }: { id: string }) {
  const [requested, setRequested] = useState(false);
  const [impact, setImpact] = useState<EcoImpact | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (!requested) return;
    let current = true;
    setImpact(null);
    setError("");
    getEcoImpact(id)
      .then((loaded) => current && setImpact(loaded))
      .catch((e: unknown) => current && setError(e instanceof Error ? e.message : "Request failed"));
    return () => {
      current = false;
    };
  }, [id, requested, attempt]);

  return (
    <Card
      title="Impact report"
      actions={
        !requested ? (
          <Button size="sm" onClick={() => setRequested(true)}>Show impact</Button>
        ) : error ? (
          <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry impact</Button>
        ) : undefined
      }
    >
      {!requested ? <p>Products and sites that use the versions this change order replaces.</p> : null}
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {error || null}
        </Alert>
      </div>
      {requested && !impact && !error ? <Skeleton /> : null}
      {impact ? (
        <>
          <p>
            As of {impact.asOf}: {plural(impact.products.length, "affected product", "affected products")} (
            {plural(impact.products.filter((p) => p.topLevel).length, "top-level product", "top-level products")}){" "}
            {impact.siteIds.length > 0 ? `across ${plural(impact.siteIds.length, "site", "sites")}` : "on every site"}. Open production orders{" "}
            {impact.openProductionOrders}, open planned orders {impact.openPlannedOrders}.
          </p>
          <div className={styles.results}>
            <DataTable
              label="Affected products"
              columns={productColumns}
              rows={impact.products}
              getRowKey={(row) => row.productId}
              empty={{ title: "No products are affected" }}
            />
          </div>
          {impact.replacements.map((r) => (
            <div key={r.newVersionId} className={styles.results}>
              <DataTable
                label={`Line changes for ${r.productId || r.newVersionId}`}
                columns={changeColumns}
                rows={r.changes}
                getRowKey={(row) => row.componentProductId}
                empty={{ title: "No component differences" }}
              />
            </div>
          ))}
        </>
      ) : null}
    </Card>
  );
}
