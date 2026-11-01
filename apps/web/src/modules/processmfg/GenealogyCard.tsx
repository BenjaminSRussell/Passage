import { Link } from "react-router-dom";
import { Alert, Button, Card, DataTable, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getBatchGenealogy, type GenealogyLink } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<GenealogyLink>[] = [
  { key: "batchNumber", header: "Batch", mono: true, render: (l) => <Link to={`/processmfg/batches/${l.batchId}`}>{l.batchNumber}</Link> },
  { key: "relation", header: "Relation", render: () => "Merge" },
  { key: "qty", header: "Quantity", align: "end" },
  { key: "unitCost", header: "Cost per unit", align: "end" },
];

/** FR-24.16: the batches this batch was merged from and the batches it went into. */
export function GenealogyCard({ batchId }: { batchId: string }) {
  const state = useLoad((signal) => getBatchGenealogy(batchId, signal), `genealogy|${batchId}`);
  if (state.error) {
    return (
      <Card title="Genealogy">
        <Alert tone="danger" title="Could not load the genealogy" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </Card>
    );
  }
  if (!state.data) {
    return (
      <Card title="Genealogy">
        <span className="visually-hidden">Loading the genealogy…</span>
        <Skeleton height="tile" />
      </Card>
    );
  }
  return (
    <Card title="Genealogy">
      <div className={styles.stack}>
        <DataTable
          label="Merged from"
          columns={columns}
          rows={state.data.parents}
          getRowKey={(l) => l.batchId}
          empty={{ title: "Not a merged batch", description: "Batches this one was merged from are listed here." }}
        />
        <DataTable
          label="Merged into"
          columns={columns}
          rows={state.data.children}
          getRowKey={(l) => l.batchId}
          empty={{ title: "Not merged into another batch", description: "Batches made from this one are listed here." }}
        />
      </div>
    </Card>
  );
}
