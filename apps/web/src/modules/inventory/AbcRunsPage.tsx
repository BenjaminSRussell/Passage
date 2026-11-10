import { Link } from "react-router-dom";
import { buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listAbcRuns, type AbcRun } from "./api/client";
import { formatDateTime } from "./format";
import { ProblemAlert } from "./parts";
import { Named } from "../purchasing/Named";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const count = (run: AbcRun, cls: "A" | "B" | "C" | "N") => String(run.classCounts[cls] ?? 0);

const columns: DataTableColumn<AbcRun>[] = [
  {
    key: "createdAt",
    header: "Run",
    render: (row) => <Link to={`/inventory/abc-runs/${row.id}`}>{formatDateTime(row.createdAt)}</Link>,
  },
  { key: "legalEntityId", header: "Legal entity", render: (row) => <Named entity="legalEntity" id={row.legalEntityId} /> },
  { key: "thresholds", header: "Thresholds A / B / C", mono: true, render: (row) => `${row.thresholds.a} / ${row.thresholds.b} / ${row.thresholds.c}` },
  { key: "a", header: "A", align: "end", mono: true, render: (row) => count(row, "A") },
  { key: "b", header: "B", align: "end", mono: true, render: (row) => count(row, "B") },
  { key: "c", header: "C", align: "end", mono: true, render: (row) => count(row, "C") },
  { key: "n", header: "Not classified", align: "end", mono: true, render: (row) => count(row, "N") },
];

/** ABC classification runs (FR-16.19), newest first. */
export function AbcRunsPage() {
  const load = useLoad((signal) => listAbcRuns(signal), "abc-runs");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="ABC classification"
        description="Products ranked per site by on-hand value: A items hold most of the value and are counted most often."
        actions={
          <Link className={buttonClassName("primary")} to="/inventory/abc-runs/new">
            Run classification
          </Link>
        }
      />
      {load.error ? (
        <ProblemAlert title="Could not load ABC runs" problem={load.error} onRetry={load.reload} />
      ) : (
        <DataTable
          label="ABC runs"
          columns={columns}
          rows={load.data ?? []}
          getRowKey={(row) => row.id}
          loading={load.loading}
          empty={{ title: "No classification yet", description: "Run a classification to rank products by on-hand value." }}
        />
      )}
    </div>
  );
}
