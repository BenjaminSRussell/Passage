import { Alert, Badge, Button, Card, DataTable, type DataTableColumn } from "../../shell/ui";
import { listContractAccruals, type ContractAccrual, type ContractKind } from "./api/client";
import { ACCRUAL_KIND_LABELS } from "./contractFormat";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<ContractAccrual>[] = [
  { key: "periodId", header: "Period", mono: true },
  { key: "kind", header: "Posting", render: (a) => <Badge tone={a.kind === "ACCRUAL" ? "neutral" : "info"}>{ACCRUAL_KIND_LABELS[a.kind]}</Badge> },
  { key: "lineKey", header: "Invoice line", mono: true },
  { key: "invoiceDate", header: "Date", mono: true },
  { key: "basisValue", header: "Net", mono: true, align: "end" },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (a) => `${a.amount} ${a.currency}` },
];

/** The accrual rows of a broker contract or royalty agreement, oldest first (the claim drill-down). */
export function ContractAccrualsCard({ kind, contractId }: { kind: ContractKind; contractId: string }) {
  const state = useLoad((signal) => listContractAccruals(kind, contractId, "", signal), `${kind}-${contractId}`);
  return (
    <Card title="Accruals">
      {state.error ? (
        <Alert tone="danger" title="Could not load the accruals" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Accruals of this contract"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(a) => a.id}
          loading={state.loading}
          empty={{ title: "Nothing accrued yet", description: "Posted invoice lines in the contract's scope accrue here, period by period." }}
        />
      )}
    </Card>
  );
}
