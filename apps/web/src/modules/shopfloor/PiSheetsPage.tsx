import { useState } from "react";
import { Link } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { ProblemMessage } from "../../shell/errors";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { DUTY_PI_SHEET_MAINTAIN, listPISheets, type PISheet, type PISheetStatus } from "./api/client";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const PI_STATUS: Record<PISheetStatus, { label: string; tone: BadgeTone }> = {
  draft: { label: "Draft", tone: "warning" },
  released: { label: "Released", tone: "success" },
  obsolete: { label: "Obsolete", tone: "neutral" },
};

export function PiStatusBadge({ status }: { status: PISheetStatus }) {
  const known = PI_STATUS[status];
  return <Badge tone={known?.tone ?? "neutral"}>{known?.label ?? status}</Badge>;
}

const columns: DataTableColumn<PISheet>[] = [
  { key: "code", header: "Code", mono: true, render: (row) => <Link to={`/shopfloor/pi-sheets/${row.id}`}>{row.code}</Link> },
  { key: "name", header: "Name" },
  { key: "version", header: "Version", align: "end", mono: true },
  { key: "status", header: "Status", render: (row) => <PiStatusBadge status={row.status} /> },
  { key: "updatedAt", header: "Last changed", mono: true, render: (row) => formatDateTime(row.updatedAt) },
  { key: "updatedBy", header: "By" },
];

/** PI sheets (FR-25.16): the versioned templates the kiosk renders as the steps of an order. */
export function PiSheetsPage() {
  const [status, setStatus] = useState<PISheetStatus | "">("");
  const canDesign = holdsDuty(usePrincipalDuties(), DUTY_PI_SHEET_MAINTAIN);
  const state = useLoad((signal) => listPISheets({ status: status || undefined }, signal), `pi-sheets:${status}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="PI sheets"
        description="The steps an operator works through at the kiosk. Design a sheet, release it, and make an order's control recipe from it."
        actions={
          canDesign ? (
            <Link className={buttonClassName("primary")} to="/shopfloor/pi-sheets/new">
              New PI sheet
            </Link>
          ) : undefined
        }
      />
      <div className={styles.toolbar}>
        <FormField id="pi-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as PISheetStatus | "")}>
              <option value="">Any status</option>
              {Object.entries(PI_STATUS).map(([value, { label }]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the PI sheets" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="PI sheets"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading && !state.data}
          empty={{ title: "No PI sheets", description: "Design the first sheet to give a kiosk steps to run." }}
        />
      )}
    </div>
  );
}
