import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listRuns, type PackRun } from "./api/client";
import { formatDateTime, PACKS, RUN_STATUSES, SOURCE_LABELS, type Pack, type RunStatus } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<PackRun>[] = [
  { key: "createdAt", header: "Started", render: (row) => formatDateTime(row.createdAt) },
  { key: "pack", header: "Pack", mono: true },
  { key: "sourceSystem", header: "Source", render: (row) => `${SOURCE_LABELS[row.sourceSystem] ?? row.sourceSystem} ${row.sourceCompany}` },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "recordsIn", header: "Records", align: "end" },
  { key: "recordsLoaded", header: "Loaded", align: "end" },
  { key: "alreadyPresent", header: "Already present", align: "end" },
  { key: "recordsQuarantined", header: "Quarantined", align: "end" },
];

/** Migration factory workspace: pack runs with their status and record counts (9.49.6, 9.49.14). */
export function PackRunsPage() {
  const navigate = useNavigate();
  const [pack, setPack] = useState<Pack | "">("");
  const [status, setStatus] = useState<RunStatus | "">("");
  const state = useLoad((signal) => listRuns({ pack, status }, signal), `${pack}|${status}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Migration factory"
        description="Pack runs that validate, load and reconcile a legacy extract. A run reconciles only when every control total matches."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/migration-factory/packs">
              Pack catalog
            </Link>
            <Link className={buttonClassName("secondary")} to="/migration-factory/runs/new-gl">
              Load GL balances
            </Link>
            <Link className={buttonClassName("secondary")} to="/migration-factory/runs/new-onhand">
              Load on-hand stock
            </Link>
            <Link className={buttonClassName("secondary")} to="/migration-factory/runs/new-orders">
              Load open orders
            </Link>
            <Link className={buttonClassName("secondary")} to="/migration-factory/runs/new-bank">
              Load bank balances
            </Link>
            <Link className={buttonClassName("secondary")} to="/migration-factory/runs/new-project">
              Load project balances
            </Link>
            <Link className={buttonClassName("primary")} to="/migration-factory/runs/new">
              Start pack run
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="runs-pack" label="Pack">
          {(control) => (
            <Select {...control} value={pack} onChange={(event) => setPack(event.target.value as Pack | "")}>
              <option value="">All packs</option>
              {PACKS.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="runs-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as RunStatus | "")}>
              <option value="">All statuses</option>
              {RUN_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load pack runs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Pack runs"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.runId}
          loading={state.loading}
          empty={{ title: "No pack runs yet", description: "Start a pack run with a legacy extract to see its control totals." }}
          onRowOpen={(row) => navigate(`/migration-factory/runs/${row.runId}`)}
        />
      )}
    </div>
  );
}
