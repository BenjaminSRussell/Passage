import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchToleranceGroups, type ToleranceGroup } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const columns: DataTableColumn<ToleranceGroup>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "debitLimit", header: "Debit limit", align: "end", mono: true },
  { key: "creditLimit", header: "Credit limit", align: "end", mono: true },
  { key: "percentLimit", header: "Percent limit", align: "end", mono: true, render: (g) => (Number(g.percentLimit) > 0 ? `${g.percentLimit} %` : "None") },
  { key: "differenceAccount", header: "Difference account", mono: true },
  { key: "active", header: "Status", render: (g) => <Badge tone={g.active ? "success" : "neutral"}>{g.active ? "Active" : "Inactive"}</Badge> },
];

/** Tolerance groups of a ledger (FR-11.20): how much a clearing may write off. */
export function ToleranceGroupsPage() {
  const navigate = useNavigate();
  const [ledgerId, setLedgerId] = useLedgerParam();
  const result = useQuery({ queryKey: ["ledger", "tolerance-groups", ledgerId], queryFn: () => fetchToleranceGroups(ledgerId), enabled: ledgerId !== "" });
  const query = ledgerQuery(ledgerId);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Tolerance groups"
        description="The largest residual a manual clearing may write off, and the account that receives it. A residual above the limit is refused or carried as a new open item."
        actions={
          ledgerId ? (
            <Link className={buttonClassName("primary")} to={`/ledger/tolerance-groups/new${query}`}>
              New tolerance group
            </Link>
          ) : undefined
        }
      />
      <LedgerPicker id="tg-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Choose a ledger to see its tolerance groups.</p>
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the tolerance groups" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {errorText(result.error)}
        </Alert>
      ) : (
        <DataTable
          label="Tolerance groups"
          columns={columns}
          rows={result.data ?? []}
          getRowKey={(g) => g.code}
          loading={result.isPending}
          empty={{ title: "No tolerance groups yet", description: "Without one, a clearing must net to zero or carry the residual as a new item." }}
          onRowOpen={(g) => navigate(`/ledger/tolerance-groups/${encodeURIComponent(g.code)}${query}`)}
        />
      )}
    </div>
  );
}
