import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listAbsenceCodes, type AbsenceCode } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<AbsenceCode>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "paid", header: "Pay", render: (row) => (row.paid ? <Badge tone="success">Paid</Badge> : <Badge tone="neutral">Unpaid</Badge>) },
  { key: "payMultiplier", header: "Share of rate", align: "end", render: (row) => (row.paid ? `${row.payMultiplier}x` : "none") },
  { key: "active", header: "Status", render: (row) => (row.active ? <Badge tone="success">Active</Badge> : <Badge tone="neutral">Inactive</Badge>) },
];

/** Absence codes: the paid and unpaid reasons a worker can be absent (FR-25.2). */
export function AbsenceCodesPage() {
  const state = useLoad((signal) => listAbsenceCodes({}, signal), "absence-codes");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Absence codes"
        description="A paid code pays the absence hours at its share of the worker's rate; an unpaid code records the hours with no pay. Absence hours never count toward overtime."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/shopfloor/absences/new">
              Record absence
            </Link>
            <Link className={buttonClassName("primary")} to="/shopfloor/absence-codes/new">
              New absence code
            </Link>
          </>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load absence codes" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Absence codes"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No absence codes yet", description: "Create one, such as SICK or HOLIDAY, before recording an absence." }}
        />
      )}
    </div>
  );
}
