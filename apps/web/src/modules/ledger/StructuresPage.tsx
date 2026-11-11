import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { fetchStructures, type Structure } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import styles from "./pages.module.css";

const columns: DataTableColumn<Structure>[] = [
  { key: "name", header: "Name", mono: true },
  { key: "version", header: "Version", align: "end" },
  { key: "status", header: "Status", render: (s) => <StatusPill status={s.status} /> },
  { key: "effectiveFrom", header: "Effective from", mono: true },
  { key: "priority", header: "Priority", align: "end" },
  { key: "accountFilter", header: "Accounts", mono: true, render: (s) => (s.accountFilter.length ? s.accountFilter.join(", ") : "All") },
  { key: "segments", header: "Segments", mono: true, render: (s) => ["Main account", ...s.segments].join(" - ") },
  { key: "constraints", header: "Rules", align: "end", render: (s) => s.constraints.length },
];

/** Account structure versions of a ledger (FR-11.3). */
export function StructuresPage() {
  const navigate = useNavigate();
  const [ledgerId, setLedgerId] = useLedgerParam();
  const result = useQuery({
    queryKey: ["ledger", "structures", ledgerId],
    queryFn: () => fetchStructures(ledgerId),
    enabled: ledgerId !== "",
  });
  const query = ledgerQuery(ledgerId);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Account structures"
        description="Which dimensions each main account takes and which values are allowed. A posting uses, per structure, the active version in effect on its date; structures are tried by priority, then name."
        actions={
          ledgerId ? (
            <Link className={buttonClassName("primary")} to={`/ledger/structures/new${query}`}>
              New structure
            </Link>
          ) : undefined
        }
      />
      <LedgerPicker id="st-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Choose a ledger to see its account structures.</p>
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the account structures" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : (
        <>
          <DataTable
            label="Account structures"
            columns={columns}
            rows={result.data ?? []}
            getRowKey={(s) => s.id}
            loading={result.isPending}
            empty={{
              title: "No account structures yet",
              description: "Without an active structure the ledger accepts any defined dimension. Create one to make dimensions required or limit their values.",
            }}
            onRowOpen={(s) => navigate(`/ledger/structures/${encodeURIComponent(s.id)}${query}`)}
          />
          <p className={styles.note}>
            Drafts can be edited. Active versions are locked; to change one, save a new version with a later effective date, or retire it.{" "}
            <Link to={`/ledger/dimension-values${query}`}>Dimension values</Link>
          </p>
        </>
      )}
    </div>
  );
}
