import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Badge, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listCountSheets, type CountSheetStatus, type CountSheetSummary } from "./api/client";
import { formatDateTime, sentence } from "./format";
import { ProblemAlert, stateTones } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<CountSheetSummary>[] = [
  { key: "number", header: "Sheet", mono: true },
  { key: "planName", header: "Plan" },
  { key: "status", header: "Status", render: (row) => <Badge tone={stateTones[row.status] ?? "neutral"}>{sentence(row.status)}</Badge> },
  { key: "counted", header: "Counted", align: "end", render: (row) => `${row.countedCount} of ${row.lineCount}` },
  { key: "recount", header: "Recounts", align: "end", render: (row) => (row.recountLines > 0 ? String(row.recountLines) : "None") },
  { key: "createdAt", header: "Generated", render: (row) => formatDateTime(row.createdAt) },
  { key: "postedAt", header: "Posted", render: (row) => formatDateTime(row.postedAt) },
];

/** Count sheets, newest first; open one to enter counts and post it. */
export function CountSheetsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<CountSheetStatus | "">("OPEN");
  const state = useLoad((signal) => listCountSheets({ status }, signal), status);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Count sheets"
        description="Generated counts. Enter the counted quantities, then post the variances as a count journal."
        actions={
          <Link className={buttonClassName("ghost")} to="/inventory/count-plans">
            Count plans
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="sheets-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as CountSheetStatus | "")}>
              <option value="OPEN">Open</option>
              <option value="COUNTED">Counted</option>
              <option value="APPROVED">Approved</option>
              <option value="POSTED">Posted</option>
              <option value="CANCELLED">Cancelled</option>
              <option value="">All</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? <ProblemAlert title="Could not load count sheets" problem={state.error} onRetry={state.reload} /> : null}
      {state.error ? null : (
        <DataTable
          label="Count sheets"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No count sheets", description: "Generate a sheet from a count plan." }}
          onRowOpen={(row) => navigate(`/inventory/count-sheets/${row.id}`)}
        />
      )}
    </div>
  );
}
