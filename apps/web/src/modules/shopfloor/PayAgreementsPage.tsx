import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listPayAgreements, type PayAgreement } from "./api/client";
import { WEEKDAYS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<PayAgreement>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "dailyThreshold", header: "Daily overtime after", align: "end", render: (row) => `${row.dailyThreshold} h` },
  { key: "weeklyThreshold", header: "Weekly overtime after", align: "end", render: (row) => `${row.weeklyThreshold} h` },
  { key: "overtimeMultiplier", header: "Overtime", align: "end", render: (row) => `${row.overtimeMultiplier}x` },
  { key: "sundayMultiplier", header: "Sunday", align: "end", render: (row) => `${row.sundayMultiplier}x` },
  { key: "weekStart", header: "Pay week starts", render: (row) => WEEKDAYS[row.weekStart] ?? String(row.weekStart) },
  {
    key: "shiftPremiums",
    header: "Shift premiums",
    render: (row) => (row.shiftPremiums.length === 0 ? "None" : row.shiftPremiums.map((w) => `${w.name} ${w.from}-${w.to} +${w.premium}`).join("; ")),
  },
  { key: "active", header: "Status", render: (row) => (row.active ? <Badge tone="success">Active</Badge> : <Badge tone="neutral">Inactive</Badge>) },
];

/** Pay agreements: the overtime thresholds and multipliers workers are calculated with (FR-25.7). */
export function PayAgreementsPage() {
  const state = useLoad((signal) => listPayAgreements({}, signal), "pay-agreements");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Pay agreements"
        description="Overtime thresholds, multipliers, shift premium windows and the first day of the pay week. A worker without an agreement is paid 1.5x over 8 hours a day and 40 hours a week, and 2.0x on Sunday."
        actions={
          <Link className={buttonClassName("primary")} to="/shopfloor/pay-agreements/new">
            New pay agreement
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load pay agreements" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Pay agreements"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No pay agreements yet", description: "Create one, then assign it to workers from their page. Until then the default rules apply." }}
        />
      )}
    </div>
  );
}
