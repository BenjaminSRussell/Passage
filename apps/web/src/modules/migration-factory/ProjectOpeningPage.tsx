import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, StatGroup, StatTile, StatusPill, type DataTableColumn } from "../../shell/ui";
import { getProjectOpening, type OpeningLine } from "./api/client";
import { formatKey, money, type ControlTotal } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const PAGE = 50;

const totalColumns: DataTableColumn<ControlTotal>[] = [
  { key: "category", header: "Category", render: (row) => row.key.category },
  { key: "name", header: "Measure", mono: true, render: (row) => row.name.replace("project_wip.", "") },
  { key: "source", header: "Legacy", align: "end", mono: true, render: (row) => money(row.source) },
  { key: "target", header: "Passage", align: "end", mono: true, render: (row) => money(row.target) },
  { key: "delta", header: "Delta", align: "end", mono: true, render: (row) => money(row.delta) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

const lineColumns: DataTableColumn<OpeningLine>[] = [
  { key: "legacyId", header: "Legacy id", mono: true },
  { key: "category", header: "Category" },
  { key: "transactionDate", header: "Date", mono: true },
  { key: "qty", header: "Qty", align: "end", mono: true, render: (row) => (row.qty ? money(row.qty) : "") },
  { key: "costAmount", header: "Cost", align: "end", mono: true, render: (row) => money(row.costAmount) },
  { key: "salesAmount", header: "Sales value", align: "end", mono: true, render: (row) => money(row.salesAmount) },
  { key: "currency", header: "Currency", mono: true },
  { key: "billingStatus", header: "Billing", render: (row) => <StatusPill status={row.billingStatus} /> },
  { key: "wipCost", header: "WIP cost", align: "end", mono: true, render: (row) => (row.wipCost ? money(row.wipCost) : "") },
  { key: "wipSales", header: "WIP sales", align: "end", mono: true, render: (row) => (row.wipSales ? money(row.wipSales) : "") },
  { key: "workerId", header: "Worker", mono: true },
];

/** One project's beginning balances: totals per category against legacy and its opening lines (FR-49.12, BR-49.12, 9.49.14). */
export function ProjectOpeningPage() {
  const { id = "", project = "" } = useParams();
  const [pages, setPages] = useState<string[]>([""]);
  const after = pages[pages.length - 1];
  const state = useLoad((signal) => getProjectOpening(id, project, { limit: PAGE, after: after || undefined }, signal), `${id}|${project}|${after}`);
  const opening = state.data;
  const last = opening?.lines.at(-1)?.legacyId;
  const mismatch = opening?.status === "OPENING_MISMATCH";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Project ${project}`}
        description={
          opening
            ? mismatch
              ? "The opening totals differ from the legacy project ledger. The project can't be invoiced or estimated until they match (BR-49.12)."
              : "Every category matches the legacy project ledger to 0.00."
            : "Opening balances carried in from the legacy project ledger."
        }
        meta={opening ? <StatusPill status={mismatch ? "Opening mismatch" : "Reconciled"} /> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to={`/migration-factory/runs/${id}`}>
            Back to run
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the project" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      {opening ? (
        <StatGroup label="Opening balance">
          <StatTile label="Opening lines" value={opening.lineCount} />
          <StatTile label="Totals not matching" value={opening.totals.filter((t) => t.status === "MISMATCH").length} tone="danger" />
        </StatGroup>
      ) : null}
      {!state.error ? (
        <>
          <DataTable
            label="Totals by category"
            columns={totalColumns}
            rows={opening?.totals ?? []}
            getRowKey={(row) => `${row.name}|${formatKey(row.key)}`}
            loading={state.loading}
            empty={{ title: "No totals", description: "The run has no totals for this project." }}
          />
          <DataTable
            label="Opening lines"
            columns={lineColumns}
            rows={opening?.lines ?? []}
            getRowKey={(row) => row.legacyId}
            loading={state.loading}
            empty={{ title: "No opening lines", description: "No line of this project has loaded. Lines that failed a rule are in the run's quarantine." }}
          />
          <div className={styles.formActions}>
            <Button size="sm" variant="ghost" disabled={pages.length === 1} onClick={() => setPages(pages.slice(0, -1))}>
              Previous lines
            </Button>
            <Button size="sm" variant="ghost" disabled={!last || (opening?.lines.length ?? 0) < PAGE} onClick={() => last && setPages([...pages, last])}>
              Next lines
            </Button>
          </div>
        </>
      ) : null}
    </div>
  );
}
