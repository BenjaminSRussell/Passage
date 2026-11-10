import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listCommissionSettlementExports, SETTLEMENT_TARGETS, type SettlementExportRecord, type SettlementExportStatus, type SettlementTarget } from "./api/client";
import { TARGET_LABELS } from "./CommissionSettlementPage";
import styles from "./pages.module.css";
import { describeTotals, ExportStatusBadge } from "./settlementFile";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<SettlementExportRecord>[] = [
  { key: "period", header: "Invoice dates", mono: true, render: (r) => `${r.from} to ${r.to}` },
  { key: "target", header: "Settled through", render: (r) => TARGET_LABELS[r.target] },
  { key: "lineCount", header: "Rows", mono: true, align: "end" },
  { key: "totals", header: "Commission", mono: true, align: "end", render: describeTotals },
  { key: "exportedBy", header: "Recorded by", mono: true },
  { key: "status", header: "Status", render: (r) => <ExportStatusBadge status={r.status} /> },
];

/**
 * Recorded commission settlement exports (FR-26.18): what was handed to accounts payable or
 * payroll, per period. A period that is ISSUED cannot be exported again to the same target
 * until the export is voided.
 */
export function CommissionExportsPage() {
  const navigate = useNavigate();
  const [target, setTarget] = useState<SettlementTarget | "">("");
  const [status, setStatus] = useState<SettlementExportStatus | "">("");
  const state = useLoad((signal) => listCommissionSettlementExports({ target, status }, signal), `${target}|${status}`);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Commission exports"
        description="Each row is one hand-over of commission to accounts payable or payroll. The rows are frozen when it is recorded; a period already issued to a target is refused until that export is voided."
        actions={
          <Link className={buttonClassName("primary")} to="/pricing/commission/settlement">
            Prepare an export
          </Link>
        }
      />
      <div className={styles.form}>
        <FormField id="ce-target" label="Settled through">
          {(c) => (
            <Select {...c} value={target} onChange={(e) => setTarget(e.target.value as SettlementTarget | "")}>
              <option value="">Any</option>
              {SETTLEMENT_TARGETS.map((t) => (
                <option key={t} value={t}>
                  {TARGET_LABELS[t]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="ce-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as SettlementExportStatus | "")}>
              <option value="">Any</option>
              <option value="ISSUED">Issued</option>
              <option value="VOIDED">Voided</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the exports" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Commission exports"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={{ title: "No exports recorded", description: "Preview a period on the Commission settlement page, then record it to hand it to payroll or accounts payable." }}
          onRowOpen={(r) => navigate(`/pricing/commission/exports/${r.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the 100 newest exports.</p> : null}
    </div>
  );
}
