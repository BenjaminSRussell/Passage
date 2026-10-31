import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchAutoClearingRules, type AutoClearingRule } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const columns: DataTableColumn<AutoClearingRule>[] = [
  { key: "sequence", header: "Order", align: "end", mono: true },
  { key: "name", header: "Name" },
  { key: "range", header: "Accounts", mono: true, render: (r) => `${r.accountFrom} to ${r.accountTo}` },
  { key: "criteria", header: "Grouped by", mono: true, render: (r) => r.criteria.join(", ") },
  { key: "tolerance", header: "Tolerance group", mono: true, render: (r) => (r.allowTolerance ? r.toleranceGroup : "None") },
  { key: "active", header: "Status", render: (r) => <Badge tone={r.active ? "success" : "neutral"}>{r.active ? "Active" : "Inactive"}</Badge> },
];

/** Automatic clearing rules of a ledger (FR-11.20): which open items group and clear on their own. */
export function AutoClearingRulesPage() {
  const navigate = useNavigate();
  const [ledgerId, setLedgerId] = useLedgerParam();
  const result = useQuery({ queryKey: ["ledger", "auto-clearing-rules", ledgerId], queryFn: () => fetchAutoClearingRules(ledgerId), enabled: ledgerId !== "" });
  const query = ledgerQuery(ledgerId);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Automatic clearing rules"
        description="Each rule names an account range and the fields that group its open items. A group that nets to zero, or within the rule's tolerance group, clears as one document. Rules run in the order shown."
        actions={
          ledgerId ? (
            <>
              <Link className={buttonClassName("secondary")} to={`/ledger/auto-clearing${query}`}>
                Run automatic clearing
              </Link>
              <Link className={buttonClassName("primary")} to={`/ledger/auto-clearing-rules/new${query}`}>
                New rule
              </Link>
            </>
          ) : undefined
        }
      />
      <LedgerPicker id="acr-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Enter a ledger id to see its automatic clearing rules.</p>
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the rules" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {errorText(result.error)}
        </Alert>
      ) : (
        <DataTable
          label="Automatic clearing rules"
          columns={columns}
          rows={result.data ?? []}
          getRowKey={(r) => r.id}
          loading={result.isPending}
          empty={{ title: "No automatic clearing rules yet", description: "Without a rule, open items clear only by hand." }}
          onRowOpen={(r) => navigate(`/ledger/auto-clearing-rules/${encodeURIComponent(r.id)}${query}`)}
        />
      )}
    </div>
  );
}
