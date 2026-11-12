import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchValidationRules, type ValidationRule } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const columns: DataTableColumn<ValidationRule>[] = [
  { key: "name", header: "Name" },
  { key: "sequence", header: "Order", align: "end", mono: true },
  { key: "level", header: "Applies to", render: (r) => (r.level === "line" ? "Each line" : "Whole entry") },
  { key: "severity", header: "When it fails", render: (r) => <Badge tone={r.severity === "error" ? "danger" : "warning"}>{r.severity === "error" ? "Refuses" : "Warns"}</Badge> },
  { key: "active", header: "Status", render: (r) => <Badge tone={r.active ? "success" : "neutral"}>{r.active ? "Active" : "Off"}</Badge> },
  { key: "message", header: "Message" },
];

/** Validation rules of a ledger (FR-11.21): a prerequisite and a check with an error or warning message. */
export function ValidationRulesPage() {
  const navigate = useNavigate();
  const [ledgerId, setLedgerId] = useLedgerParam();
  const result = useQuery({ queryKey: ["ledger", "validation-rules", ledgerId], queryFn: () => fetchValidationRules(ledgerId), enabled: ledgerId !== "" });
  const query = ledgerQuery(ledgerId);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Validation rules"
        description="Rules every posting on this ledger is checked against after the account structure, for example expense lines above 1,000 need a department. A rule either refuses the entry or only warns."
        actions={
          ledgerId ? (
            <Link className={buttonClassName("primary")} to={`/ledger/validation-rules/new${query}`}>
              New validation rule
            </Link>
          ) : undefined
        }
      />
      <LedgerPicker id="vr-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Choose a ledger to see its validation rules.</p>
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the validation rules" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {errorText(result.error)}
        </Alert>
      ) : (
        <DataTable
          label="Validation rules"
          columns={columns}
          rows={result.data ?? []}
          getRowKey={(r) => r.id}
          loading={result.isPending}
          empty={{ title: "No validation rules yet", description: "Add one to refuse or flag postings that break a company policy." }}
          onRowOpen={(r) => navigate(`/ledger/validation-rules/${encodeURIComponent(r.id)}${query}`)}
        />
      )}
    </div>
  );
}
