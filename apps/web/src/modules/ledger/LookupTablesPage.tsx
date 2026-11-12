import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchLookupTables, type LookupTable } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const columns: DataTableColumn<LookupTable>[] = [
  { key: "name", header: "Name" },
  { key: "sourceFields", header: "Reads", mono: true, render: (t) => t.sourceFields.join(", ") },
  { key: "target", header: "Fills", mono: true },
  { key: "rowCount", header: "Rows", align: "end", mono: true },
  { key: "version", header: "Version", align: "end", mono: true },
];

/** Lookup tables of a ledger (FR-11.22): source values to a dimension value, used by derivation steps. */
export function LookupTablesPage() {
  const navigate = useNavigate();
  const [ledgerId, setLedgerId] = useLedgerParam();
  const result = useQuery({ queryKey: ["ledger", "lookup-tables", ledgerId], queryFn: () => fetchLookupTables(ledgerId), enabled: ledgerId !== "" });
  const query = ledgerQuery(ledgerId);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Lookup tables"
        description="Tables that map account, account type or dimension values to another dimension value, for example account and cost center to functional area. Derivation strategies use them."
        actions={
          ledgerId ? (
            <Link className={buttonClassName("primary")} to={`/ledger/lookup-tables/new${query}`}>
              New lookup table
            </Link>
          ) : undefined
        }
      />
      <LedgerPicker id="lt-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Choose a ledger to see its lookup tables.</p>
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the lookup tables" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {errorText(result.error)}
        </Alert>
      ) : (
        <DataTable
          label="Lookup tables"
          columns={columns}
          rows={result.data ?? []}
          getRowKey={(t) => t.id}
          loading={result.isPending}
          empty={{ title: "No lookup tables yet", description: "Add one to let a derivation strategy fill a dimension from other values on the line." }}
          onRowOpen={(t) => navigate(`/ledger/lookup-tables/${encodeURIComponent(t.id)}${query}`)}
        />
      )}
    </div>
  );
}
