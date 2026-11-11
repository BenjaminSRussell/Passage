import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchDerivationStrategies, type DerivationStrategy } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const TONES = { draft: "neutral", active: "success", retired: "warning" } as const;
const LABELS = { draft: "Draft", active: "Active", retired: "Retired" } as const;

const columns: DataTableColumn<DerivationStrategy>[] = [
  { key: "name", header: "Name" },
  { key: "version", header: "Version", align: "end", mono: true },
  { key: "status", header: "Status", render: (s) => <Badge tone={TONES[s.status]}>{LABELS[s.status]}</Badge> },
  { key: "steps", header: "Steps", align: "end", mono: true, render: (s) => String(s.steps.length) },
  { key: "targets", header: "Fills", mono: true, render: (s) => [...new Set(s.steps.map((st) => st.target))].join(", ") },
  { key: "activatedAt", header: "Activated", render: (s) => (s.activatedAt ? s.activatedAt.slice(0, 10) : "Not yet") },
];

/** Derivation strategy versions of a ledger (FR-11.22): the steps that fill dimensions before a posting is validated. */
export function DerivationStrategiesPage() {
  const navigate = useNavigate();
  const [ledgerId, setLedgerId] = useLedgerParam();
  const result = useQuery({ queryKey: ["ledger", "derivation-strategies", ledgerId], queryFn: () => fetchDerivationStrategies(ledgerId), enabled: ledgerId !== "" });
  const query = ledgerQuery(ledgerId);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Derivation strategies"
        description="Ordered steps that fill, copy, clear or set dimension values on every posted line before its account structure is checked. One version is active per ledger."
        actions={
          ledgerId ? (
            <Link className={buttonClassName("primary")} to={`/ledger/derivation-strategies/new${query}`}>
              New version
            </Link>
          ) : undefined
        }
      />
      <LedgerPicker id="ds-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Choose a ledger to see its derivation strategies.</p>
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the derivation strategies" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {errorText(result.error)}
        </Alert>
      ) : (
        <DataTable
          label="Derivation strategies"
          columns={columns}
          rows={result.data ?? []}
          getRowKey={(s) => String(s.version)}
          loading={result.isPending}
          empty={{ title: "No derivation strategy yet", description: "Without an active strategy, a posting keeps exactly the dimensions it arrived with." }}
          onRowOpen={(s) => navigate(`/ledger/derivation-strategies/${s.version}${query}`)}
        />
      )}
    </div>
  );
}
