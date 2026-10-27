import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listWaves, type WaveSummary } from "./api/client";
import { formatDateTime, GROUP_LABELS, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<WaveSummary>[] = [
  { key: "waveNumber", header: "Wave", mono: true },
  { key: "groupBy", header: "Grouped by", render: (row) => GROUP_LABELS[row.groupBy] ?? row.groupBy },
  { key: "groupKey", header: "Group", render: (row) => (row.groupKey === "UNASSIGNED" ? "Unassigned" : row.groupKey) },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "orderCount", header: "Orders", align: "end" },
  { key: "warehouseId", header: "Warehouse", mono: true, render: (row) => shortId(row.warehouseId) },
  { key: "createdAt", header: "Created", render: (row) => formatDateTime(row.createdAt) },
];

/** Pick waves list (section 5.28.14 "Pick work / waves"), newest first. */
export function WavesPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listWaves({ limit: 200 }, signal), "waves");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Pick waves"
        description="Waves created by the picking workbench. Open one to see its shipments and pick them."
        actions={
          <Link className={buttonClassName("primary")} to="/fulfillment/workbench">
            Picking workbench
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the waves" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Pick waves"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          onRowOpen={(row) => navigate(`/fulfillment/waves/${row.id}`)}
          empty={{ title: "No waves yet", description: "Group released shipments into waves on the picking workbench." }}
        />
      )}
    </div>
  );
}
