import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listParallelRuns, type ParallelRun, type ParallelState } from "./api/client";
import { formatDateTime, money, PARALLEL_STATES, SOURCE_LABELS } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<ParallelRun>[] = [
  { key: "period", header: "Period", mono: true },
  { key: "sourceSystem", header: "Source", render: (row) => SOURCE_LABELS[row.sourceSystem] ?? row.sourceSystem },
  { key: "documentsReplayed", header: "Documents", align: "end" },
  { key: "state", header: "State", render: (row) => <StatusPill status={row.state} /> },
  { key: "unexplainedCount", header: "Unexplained", align: "end" },
  { key: "unexplainedAmount", header: "Unexplained amount", align: "end", mono: true, render: (row) => money(row.unexplainedAmount) },
  { key: "explainedAmount", header: "Explained amount", align: "end", mono: true, render: (row) => money(row.explainedAmount) },
  { key: "createdAt", header: "Started", render: (row) => formatDateTime(row.createdAt) },
];

/** Parallel runs: replayed legacy periods and their variance review state (FR-49.5, 9.49.14). */
export function ParallelRunsPage() {
  const navigate = useNavigate();
  const [state, setState] = useState<ParallelState | "">("");
  const load = useLoad((signal) => listParallelRuns({ state }, signal), state);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Parallel runs"
        description="A legacy period replayed into Passage and diffed. A run is signed off only with no unexplained variance and explained amounts within the revenue limit."
        actions={
          <Link className={buttonClassName("primary")} to="/migration-factory/parallel-runs/new">
            Start parallel run
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="parallel-state" label="State">
          {(control) => (
            <Select {...control} value={state} onChange={(event) => setState(event.target.value as ParallelState | "")}>
              <option value="">All states</option>
              {PARALLEL_STATES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {load.error ? (
        <Alert tone="danger" title="Could not load parallel runs" live="alert" action={<Button size="sm" onClick={load.reload}>Try again</Button>}>
          <ProblemMessage problem={load.error} />
        </Alert>
      ) : (
        <DataTable
          label="Parallel runs"
          columns={columns}
          rows={load.data ?? []}
          getRowKey={(row) => row.parallelRunId}
          loading={load.loading}
          empty={{ title: "No parallel runs yet", description: "Start a parallel run with a replayed period to review its variances." }}
          onRowOpen={(row) => navigate(`/migration-factory/parallel-runs/${row.parallelRunId}`)}
        />
      )}
    </div>
  );
}
