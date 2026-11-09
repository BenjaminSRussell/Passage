import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, PageHeader, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { previewValidations, type TaskStatus, type UnitStatement, type ValidationPreview } from "./api/client";
import { dash, isCount, isDecimal, isSignedDecimal } from "./format";
import { RowGrid, type RowField } from "./RowGrid";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type UnitKey =
  | "unitId"
  | "totalAssets"
  | "totalLiabilities"
  | "totalEquity"
  | "openingRetained"
  | "netIncome"
  | "dividends"
  | "closingRetained"
  | "icItemsWithoutPartner";

const unitFields: RowField<UnitKey>[] = [
  { name: "unitId", label: "Unit", mono: true },
  { name: "totalAssets", label: "Total assets", decimal: true },
  { name: "totalLiabilities", label: "Total liabilities", decimal: true },
  { name: "totalEquity", label: "Total equity", decimal: true },
  { name: "openingRetained", label: "Opening retained earnings", decimal: true },
  { name: "netIncome", label: "Net income", decimal: true },
  { name: "dividends", label: "Dividends", decimal: true, optional: true },
  { name: "closingRetained", label: "Closing retained earnings", decimal: true },
  { name: "icItemsWithoutPartner", label: "IC items without partner", decimal: true, optional: true },
];

const emptyUnit = (): Record<UnitKey, string> => ({
  unitId: "",
  totalAssets: "",
  totalLiabilities: "",
  totalEquity: "",
  openingRetained: "",
  netIncome: "",
  dividends: "",
  closingRetained: "",
  icItemsWithoutPartner: "",
});

/** AC-40.15: unit S1's balance sheet is out by 12.00; S2 balances. */
export const ac4015: Record<UnitKey, string>[] = [
  {
    unitId: "S1",
    totalAssets: "1012.00",
    totalLiabilities: "400.00",
    totalEquity: "600.00",
    openingRetained: "200.00",
    netIncome: "150.00",
    dividends: "50.00",
    closingRetained: "300.00",
    icItemsWithoutPartner: "",
  },
  {
    unitId: "S2",
    totalAssets: "1000.00",
    totalLiabilities: "400.00",
    totalEquity: "600.00",
    openingRetained: "200.00",
    netIncome: "150.00",
    dividends: "50.00",
    closingRetained: "300.00",
    icItemsWithoutPartner: "",
  },
];

/** Checks that mirror the service, and the request the form sends. */
export function validateUnits(units: Record<UnitKey, string>[]): Checked<{ units: UnitStatement[] }> {
  const errors: Record<string, string> = {};
  const seen = new Set<string>();
  const out = units.map((u, i) => {
    const at = (k: UnitKey) => `units[${i}].${k}`;
    const id = u.unitId.trim();
    if (id === "") errors[at("unitId")] = "Enter the unit.";
    else if (seen.has(id)) errors[at("unitId")] = "This unit is already listed.";
    seen.add(id);
    for (const k of ["totalAssets", "totalLiabilities", "totalEquity", "openingRetained", "netIncome", "closingRetained"] as const) {
      if (!isSignedDecimal(u[k])) errors[at(k)] = "Enter an amount.";
    }
    if (u.dividends.trim() !== "" && !isDecimal(u.dividends)) errors[at("dividends")] = "Enter the dividends, 0 or more.";
    const ic = u.icItemsWithoutPartner.trim();
    if (ic !== "" && !isCount(ic)) errors[at("icItemsWithoutPartner")] = "Enter a whole number, or leave it empty.";
    return {
      unitId: id,
      totalAssets: u.totalAssets.trim(),
      totalLiabilities: u.totalLiabilities.trim(),
      totalEquity: u.totalEquity.trim(),
      openingRetained: u.openingRetained.trim(),
      netIncome: u.netIncome.trim(),
      dividends: u.dividends.trim() || undefined,
      closingRetained: u.closingRetained.trim(),
      icItemsWithoutPartner: ic === "" ? undefined : Number(ic),
    };
  });
  if (out.length === 0) errors.units = "Add at least one unit.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, request: { units: out } };
}

const statusTone: Record<TaskStatus, BadgeTone> = {
  NotStarted: "neutral",
  Running: "info",
  Completed: "success",
  CompletedWithWarnings: "warning",
  Error: "danger",
  Blocked: "danger",
};

const statusLabel: Record<TaskStatus, string> = {
  NotStarted: "Not started",
  Running: "Running",
  Completed: "Completed",
  CompletedWithWarnings: "Completed with warnings",
  Error: "Error",
  Blocked: "Blocked",
};

const ruleNames: Record<string, string> = {
  BS_BALANCES: "Total assets = liabilities + equity",
  RE_ROLL: "Retained earnings roll (opening + net income − dividends)",
  IC_PARTNER_PRESENT: "IC partner on every IC item",
};

type UnitRow = ValidationPreview["units"][number];
type RuleRow = UnitRow["rules"][number] & { unitId: string };

const unitColumns: DataTableColumn<UnitRow>[] = [
  { key: "unitId", header: "Unit", mono: true },
  { key: "status", header: "Validate task", render: (u) => <Badge tone={statusTone[u.status]}>{statusLabel[u.status]}</Badge> },
  { key: "failed", header: "Failed rules", align: "end", render: (u) => String(u.rules.filter((r) => r.failed).length) },
];

const ruleColumns: DataTableColumn<RuleRow>[] = [
  { key: "unitId", header: "Unit", mono: true },
  { key: "rule", header: "Rule", render: (r) => ruleNames[r.rule] ?? r.rule },
  { key: "severity", header: "Severity" },
  { key: "difference", header: "Difference", align: "end" },
  { key: "tolerance", header: "Tolerance", align: "end", render: (r) => (r.rule === "IC_PARTNER_PRESENT" ? dash : r.tolerance) },
  {
    key: "result",
    header: "Result",
    render: (r) => (r.failed ? <Badge tone={r.severity === "Error" ? "danger" : "warning"}>{r.code ?? "Failed"}</Badge> : <Badge tone="success">Passed</Badge>),
  },
];

/** Validate each unit's collected statements and gate the group translation task (FR-40.20, AC-40.15). */
export function ValidationPage() {
  const [units, setUnits] = useState(ac4015);
  const { errors, failure, busy, result, run } = usePreview(previewValidations);

  function submit(event: FormEvent) {
    event.preventDefault();
    void run(validateUnits(units));
  }

  const rules: RuleRow[] = result ? result.units.flatMap((u) => u.rules.map((r) => ({ ...r, unitId: u.unitId }))) : [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Unit validation"
        description="Each unit's statements are checked before translation: the balance sheet balances, retained earnings roll forward within 1.00, and every intercompany item names its partner. An Error blocks the group translation task; a Warning does not. The stored monitor with collect, release and task history arrives with consolidation runs."
      />
      <Card title="Unit statements" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <RowGrid
            idPrefix="val-unit"
            path="units"
            noun="Unit"
            fields={unitFields}
            rows={units}
            errors={errors}
            newRow={emptyUnit}
            onChange={setUnits}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The units were not validated" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Validate units
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Validate tasks" columns={unitColumns} rows={[]} getRowKey={(u) => u.unitId} loading />
      ) : result ? (
        <>
          {result.groupTranslation === "Blocked" ? (
            <Alert tone="danger" title="Group translation is blocked">
              A unit's validate task ended in Error. Correct its data and release it again.
            </Alert>
          ) : (
            <Alert tone="success" title="Group translation can start">
              No unit ended in Error.
            </Alert>
          )}
          <DataTable label="Validate tasks" columns={unitColumns} rows={result.units} getRowKey={(u) => u.unitId} />
          <DataTable label="Rule results" columns={ruleColumns} rows={rules} getRowKey={(r) => `${r.unitId}-${r.rule}`} />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="calendarCheck"
            headingLevel={3}
            title="No validation yet"
            description="The form is filled in with an example. Validate it, or enter your own units' totals."
          />
        </Card>
      )}
    </div>
  );
}
