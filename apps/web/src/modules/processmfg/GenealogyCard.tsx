import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getBatchGenealogy, type GenealogyLink, type ReworkOrder } from "./api/client";
import { dash } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<GenealogyLink>[] = [
  { key: "batchNumber", header: "Batch", mono: true, render: (l) => <Link to={`/processmfg/batches/${l.batchId}`}>{l.batchNumber}</Link> },
  { key: "relation", header: "Relation", render: (l) => (l.relation === "rework" ? "Rework" : "Merge") },
  { key: "qty", header: "Quantity", align: "end" },
  { key: "unitCost", header: "Cost per unit", align: "end" },
];

const reworkColumns: DataTableColumn<ReworkOrder>[] = [
  { key: "orderNumber", header: "Rework order", mono: true, render: (r) => <Link to={`/processmfg/rework-orders/${r.id}`}>{r.orderNumber}</Link> },
  { key: "sourceBatchNumber", header: "Reworked batch", mono: true },
  { key: "outputBatchNumber", header: "Output batch", mono: true, render: (r) => r.outputBatchNumber ?? dash },
  { key: "keepBatchNumber", header: "Number", render: (r) => (r.keepBatchNumber ? "Kept" : "New") },
  { key: "receivedQty", header: "Received", align: "end", render: (r) => r.receivedQty ?? dash },
  { key: "status", header: "Status", render: (r) => (r.status === "received" ? <Badge tone="success">Received</Badge> : <Badge tone="warning">Planned</Badge>) },
];

/** FR-24.16, FR-24.18: the batches this batch was made from, the batches it went into and its rework orders. */
export function GenealogyCard({ batchId }: { batchId: string }) {
  const state = useLoad((signal) => getBatchGenealogy(batchId, signal), `genealogy|${batchId}`);
  if (state.error) {
    return (
      <Card title="Genealogy">
        <Alert tone="danger" title="Could not load the genealogy" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
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
          label="Made from"
          columns={columns}
          rows={state.data.parents}
          getRowKey={(l) => l.batchId}
          empty={{ title: "Not made from another batch", description: "Batches this one was merged or reworked from are listed here." }}
        />
        <DataTable
          label="Went into"
          columns={columns}
          rows={state.data.children}
          getRowKey={(l) => l.batchId}
          empty={{ title: "Did not go into another batch", description: "Batches made from this one are listed here." }}
        />
        <DataTable
          label="Rework orders"
          columns={reworkColumns}
          rows={state.data.reworks ?? []}
          getRowKey={(r) => r.id}
          empty={{ title: "Never reworked", description: "Rework orders of this batch, including those that kept its number, are listed here." }}
        />
      </div>
    </Card>
  );
}
