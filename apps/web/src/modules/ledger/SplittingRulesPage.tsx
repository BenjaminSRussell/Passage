import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, Badge, type DataTableColumn } from "../../shell/ui";
import { fetchSplittingRules, type SplittingRule } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const columns: DataTableColumn<SplittingRule>[] = [
  { key: "sourceType", header: "Source type", mono: true },
  { key: "splittingDimensions", header: "Splitting dimensions", mono: true, render: (r) => r.splittingDimensions.join(", ") },
  { key: "zeroBalanceClearingAccount", header: "Clearing account", mono: true, render: (r) => r.zeroBalanceClearingAccount || "none" },
  { key: "active", header: "Status", render: (r) => <Badge tone={r.active ? "success" : "neutral"}>{r.active ? "Active" : "Inactive"}</Badge> },
  { key: "version", header: "Version", align: "end", mono: true },
];

/** Document splitting rules of a ledger (FR-11.23): which dimensions lines inherit, per source type. */
export function SplittingRulesPage() {
  const navigate = useNavigate();
  const [ledgerId, setLedgerId] = useLedgerParam();
  const result = useQuery({ queryKey: ["ledger", "splitting-rules", ledgerId], queryFn: () => fetchSplittingRules(ledgerId), enabled: ledgerId !== "" });
  const query = ledgerQuery(ledgerId);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Document splitting"
        description="Rules that give lines without a profit center, segment or other dimension the values of the lines that carry them, in proportion to their amounts, so every entry balances per dimension value."
        actions={
          ledgerId ? (
            <Link className={buttonClassName("primary")} to={`/ledger/splitting-rules/new${query}`}>
              New splitting rule
            </Link>
          ) : undefined
        }
      />
      <LedgerPicker id="sp-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Enter a ledger id to see its splitting rules.</p>
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the splitting rules" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {errorText(result.error)}
        </Alert>
      ) : (
        <DataTable
          label="Splitting rules"
          columns={columns}
          rows={result.data ?? []}
          getRowKey={(r) => r.id}
          loading={result.isPending}
          empty={{ title: "No splitting rules yet", description: "Add one for a source type such as vendor_invoice to split payable and tax lines by profit center." }}
          onRowOpen={(r) => navigate(`/ledger/splitting-rules/${encodeURIComponent(r.id)}${query}`)}
        />
      )}
    </div>
  );
}
