import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { fetchRealignments, type RealignmentRun, type RealignmentStatus } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const TONE: Record<RealignmentStatus, BadgeTone> = { proposed: "info", posted: "success", reversed: "warning", cancelled: "neutral" };
const LABEL: Record<RealignmentStatus, string> = { proposed: "Proposed", posted: "Posted", reversed: "Reversed", cancelled: "Cancelled" };

export function RealignmentStatusBadge({ status }: { status: RealignmentStatus }) {
  return <Badge tone={TONE[status]}>{LABEL[status]}</Badge>;
}

/** "DEPT: C100 to C150" for every pair of a run's mapping. */
export function describeMapping(run: Pick<RealignmentRun, "mapping">): string {
  return Object.entries(run.mapping)
    .flatMap(([attr, values]) => Object.entries(values).map(([from, to]) => `${attr}: ${from} to ${to}`))
    .join(", ");
}

const columns: DataTableColumn<RealignmentRun>[] = [
  { key: "createdAt", header: "Created", mono: true, render: (r) => (r.createdAt ? r.createdAt.slice(0, 10) : "") },
  { key: "mapping", header: "Realignment", mono: true, render: describeMapping },
  { key: "periods", header: "Periods", mono: true, render: (r) => `${r.filter.periodFrom} to ${r.filter.periodTo}` },
  { key: "combinationsAffected", header: "Balances", align: "end", mono: true },
  { key: "amountMoved", header: "Amount moved", align: "end", mono: true, render: (r) => `${r.amountMoved} ${r.currency}` },
  { key: "status", header: "Status", render: (r) => <RealignmentStatusBadge status={r.status} /> },
];

/** Dimension realignment runs of a ledger (FR-11.24). */
export function RealignmentsPage() {
  const navigate = useNavigate();
  const [ledgerId, setLedgerId] = useLedgerParam();
  const result = useQuery({ queryKey: ["ledger", "realignments", ledgerId], queryFn: () => fetchRealignments(ledgerId), enabled: ledgerId !== "" });
  const query = ledgerQuery(ledgerId);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Dimension realignment"
        description="Move posted balances from an old dimension value to a new one with reclassification entries. Simulate first, then post; a posted run can be reversed as a unit. No posted line is changed."
        actions={
          ledgerId ? (
            <Link className={buttonClassName("primary")} to={`/ledger/realignments/new${query}`}>
              New realignment
            </Link>
          ) : undefined
        }
      />
      <LedgerPicker id="ra-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Enter a ledger id to see its realignment runs.</p>
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the realignment runs" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {errorText(result.error)}
        </Alert>
      ) : (
        <DataTable
          label="Realignment runs"
          columns={columns}
          rows={result.data ?? []}
          getRowKey={(r) => r.id}
          loading={result.isPending}
          empty={{ title: "No realignment runs yet", description: "Start one to move the balances of a cost center or other dimension value to its successor." }}
          onRowOpen={(r) => navigate(`/ledger/realignments/${encodeURIComponent(r.id)}${query}`)}
        />
      )}
    </div>
  );
}
