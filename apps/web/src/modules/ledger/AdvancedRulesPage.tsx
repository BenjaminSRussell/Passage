import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchAdvancedRules, fetchStructures, type AdvancedRule, type Structure } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

/** The structure version a rule belongs to, as `NAME vN`. */
export function structureLabel(structures: Structure[], id: string): string {
  const s = structures.find((x) => x.id === id);
  return s ? `${s.name} v${s.version}` : id;
}

/** The filter of a rule in words: account tokens and dimension conditions. */
export function filterText(r: AdvancedRule): string {
  const parts = [...(r.accounts.length > 0 ? [`accounts ${r.accounts.join(", ")}`] : []), ...Object.entries(r.dimensions).map(([k, v]) => `${k} = ${v}`)];
  return parts.join(" and ");
}

/** Advanced rule structures of a ledger (FR-11.16): extra segments that apply to lines matching a filter. */
export function AdvancedRulesPage() {
  const navigate = useNavigate();
  const [ledgerId, setLedgerId] = useLedgerParam();
  const rules = useQuery({ queryKey: ["ledger", "advanced-rules", ledgerId], queryFn: () => fetchAdvancedRules(ledgerId), enabled: ledgerId !== "" });
  const structures = useQuery({ queryKey: ["ledger", "structures", ledgerId], queryFn: () => fetchStructures(ledgerId), enabled: ledgerId !== "" });
  const query = ledgerQuery(ledgerId);
  const known = structures.data ?? [];

  const columns: DataTableColumn<AdvancedRule>[] = [
    { key: "name", header: "Name" },
    {
      key: "structureId",
      header: "Account structure",
      render: (r) => {
        const s = known.find((x) => x.id === r.structureId);
        return (
          <span>
            {structureLabel(known, r.structureId)} {s ? <Badge tone={s.status === "active" ? "success" : s.status === "draft" ? "warning" : "neutral"}>{s.status}</Badge> : null}
          </span>
        );
      },
    },
    { key: "priority", header: "Priority", align: "end", mono: true },
    { key: "filter", header: "When a line has", render: (r) => filterText(r) },
    { key: "segments", header: "Adds", render: (r) => r.segments.join(", ") },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Advanced rules"
        description="An advanced rule adds dimensions to the account structure for lines that match a filter, for example expense lines of department D100 also need a project. Rules belong to a draft structure version and apply once it is active."
        actions={
          ledgerId ? (
            <Link className={buttonClassName("primary")} to={`/ledger/advanced-rules/new${query}`}>
              New advanced rule
            </Link>
          ) : undefined
        }
      />
      <LedgerPicker id="ar-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Choose a ledger to see its advanced rules.</p>
      ) : rules.isError ? (
        <Alert tone="danger" title="Could not load the advanced rules" live="alert" action={<Button size="sm" onClick={() => void rules.refetch()}>Try again</Button>}>
          {errorText(rules.error)}
        </Alert>
      ) : (
        <DataTable
          label="Advanced rules"
          columns={columns}
          rows={rules.data ?? []}
          getRowKey={(r) => r.id}
          loading={rules.isPending}
          empty={{ title: "No advanced rules yet", description: "Add one to require extra dimensions on lines that match a filter." }}
          onRowOpen={(r) => navigate(`/ledger/advanced-rules/${encodeURIComponent(r.id)}${query}`)}
        />
      )}
    </div>
  );
}
