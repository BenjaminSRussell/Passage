import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listBlockings, releaseBlocking, type Blocking, type BlockingState, type Problem } from "./api/client";
import { dash, describeDimensions, sentence, shortId } from "./format";
import { ALL_DIMENSIONS, ProblemAlert, RowAction, stateTones } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Blocking and quarantine inquiry (PRD 3.16.14) with release back to available stock. */
export function BlockingsPage() {
  const [state, setState] = useState<BlockingState | "">("ACTIVE");
  const [attempt, setAttempt] = useState(0);
  const load = useLoad((signal) => listBlockings(state, signal), `${state}|${attempt}`);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");

  const columns: DataTableColumn<Blocking>[] = [
    { key: "physicalDate", header: "Blocked on", mono: true },
    { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
    { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
    { key: "qty", header: "Quantity", align: "end", mono: true, render: (row) => `${row.qty} ${row.unit}` },
    { key: "targetStatus", header: "Status", render: (row) => <Badge tone={stateTones[row.targetStatus] ?? "neutral"}>{sentence(row.targetStatus)}</Badge> },
    { key: "reason", header: "Reason", render: (row) => (row.note ? `${sentence(row.reason)}: ${row.note}` : sentence(row.reason)) },
    {
      key: "state",
      header: "State",
      render: (row) => (row.state === "RELEASED" ? `Released${row.disposition ? ` (${sentence(row.disposition)})` : ""}` : "Active"),
    },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.state === "ACTIVE" ? (
          <RowAction
            label="Release"
            name={`blocking ${shortId(row.id)}`}
            run={() => releaseBlocking(row.id, row.version)}
            onDone={() => {
              setFailure(null);
              setNotice(`Released ${row.qty} ${row.unit} back to available stock.`);
              setAttempt((n) => n + 1);
            }}
            onError={setFailure}
          />
        ) : (
          dash
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Blocking and quarantine"
        description="Stock held out of availability for quality, damage, recall or legal reasons. It stays in physical stock and keeps its value."
        actions={
          <Link className={buttonClassName("primary")} to="/inventory/blockings/new">
            Block stock
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="blockings-state" label="State">
          {(control) => (
            <Select {...control} value={state} onChange={(event) => setState(event.target.value as BlockingState | "")}>
              <option value="ACTIVE">Active</option>
              <option value="RELEASED">Released</option>
              <option value="">All</option>
            </Select>
          )}
        </FormField>
      </div>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {load.error ? <ProblemAlert title="Could not load blockings" problem={load.error} onRetry={load.reload} /> : null}
      {failure ? <ProblemAlert title="The blocking was not released" problem={failure} /> : null}
      {load.error ? null : (
        <DataTable
          label="Blockings"
          columns={columns}
          rows={load.data ?? []}
          getRowKey={(row) => row.id}
          loading={load.loading}
          empty={{ title: "No blocked stock", description: "Block or quarantine a quantity, batch or serial to keep it out of availability." }}
        />
      )}
    </div>
  );
}
