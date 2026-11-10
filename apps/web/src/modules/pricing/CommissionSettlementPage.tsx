import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  exportCommissionSettlement,
  newKey,
  PricingApiError,
  recordCommissionSettlementExport,
  SETTLEMENT_TARGETS,
  type Problem,
  type SettlementExport,
  type SettlementRow,
  type SettlementTarget,
} from "./api/client";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { saveCsv } from "./settlementFile";
import { addDays, DAY, sumDecimals, today, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export const TARGET_LABELS: Record<SettlementTarget, string> = { AP: "Accounts payable", PAYROLL: "Payroll" };

export interface SettlementDraft {
  legalEntityId: string;
  from: string;
  to: string;
  target: SettlementTarget;
}

type Errors = Partial<Record<keyof SettlementDraft, string>>;

/** The previous calendar month, the usual settlement period. */
export function previousMonth(day: string = today()): { from: string; to: string } {
  const to = addDays(`${day.slice(0, 7)}-01`, -1);
  return { from: `${to.slice(0, 7)}-01`, to };
}

export function validateSettlement(v: SettlementDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!DAY.test(v.from)) errors.from = "Enter the first invoice date.";
  if (!DAY.test(v.to)) errors.to = "Enter the last invoice date.";
  else if (DAY.test(v.from) && v.to < v.from) errors.to = "The last date must not be before the first.";
  else if (DAY.test(v.from) && v.to >= addDays(v.from, 366)) errors.to = "An export covers at most 366 days.";
  return errors;
}

const columns: DataTableColumn<SettlementRow>[] = [
  { key: "repId", header: "Rep", mono: true },
  { key: "currency", header: "Currency", mono: true },
  { key: "transactions", header: "Transactions", mono: true, align: "end" },
  { key: "amount", header: "Commission", mono: true, align: "end", render: (r) => `${r.amount} ${r.currency}` },
];

/**
 * Commission settlement export (FR-26.18, §5.26.15): what each rep is owed for a period, credit
 * notes netted, as the CSV payroll (w43) or accounts payable (w19) imports.
 */
export function CommissionSettlementPage() {
  const [values, setValues] = useState<SettlementDraft>({ legalEntityId: "", ...previousMonth(), target: "AP" });
  const [errors, setErrors] = useState<Errors>({});
  const [result, setResult] = useState<SettlementExport | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const request = useRef<AbortController | null>(null);
  const recordKey = useRef(newKey());
  const navigate = useNavigate();
  const canRecord = holdsDuty(usePrincipalDuties(), "pricing.commission.maintain");
  const [recording, setRecording] = useState(false);

  function update(patch: Partial<SettlementDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    setResult(null);
    recordKey.current = newKey();
  }

  async function preview(event: FormEvent) {
    event.preventDefault();
    const found = validateSettlement(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    try {
      const out = await exportCommissionSettlement({ ...values, legalEntityId: values.legalEntityId.trim() }, controller.signal);
      if (!controller.signal.aborted) setResult(out);
    } catch (error) {
      if (controller.signal.aborted) return;
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["legalEntityId", "from", "to", "target"] as const) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      if (request.current === controller) setBusy(false);
    }
  }

  /** Records the previewed period as handed over: its rows are frozen and the period is blocked for this target. */
  async function record() {
    setFailure(null);
    setRecording(true);
    try {
      const saved = await recordCommissionSettlementExport({ ...values, legalEntityId: values.legalEntityId.trim() }, recordKey.current);
      navigate(`/pricing/commission/exports/${saved.id}`);
    } catch (error) {
      setFailure(describeProblem(error));
      // A refused command is final; only a network or server failure may be retried with the same key.
      if (!(error instanceof PricingApiError) || error.status < 500) recordKey.current = newKey();
    } finally {
      setRecording(false);
    }
  }

  const rows = result?.rows ?? [];
  const currencies = [...new Set(rows.map((r) => r.currency))];
  const total = currencies.length === 1 ? `${sumDecimals(rows.map((r) => r.amount))} ${currencies[0]}` : currencies.length === 0 ? "0" : "Several currencies";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Commission settlement"
        description="Commission per rep and currency for invoices dated in the period, credit notes netted, as the file payroll or accounts payable imports. Only rep ids are exported."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/pricing/commission/exports">
              Recorded exports
            </Link>
            <Link className={buttonClassName("ghost")} to="/pricing/commission/transactions">
              Commission transactions
            </Link>
          </>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={preview} noValidate>
          <FormField id="cs-legal-entity" label="Legal entity" error={errors.legalEntityId}>
            {(c) => (
              <EntityLookup entity="legalEntity" {...c} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />
            )}
          </FormField>
          <FormField id="cs-from" label="First invoice date" error={errors.from}>
            {(c) => <Input {...c} type="date" value={values.from} onChange={(e) => update({ from: e.target.value })} invalid={Boolean(errors.from)} />}
          </FormField>
          <FormField id="cs-to" label="Last invoice date" error={errors.to}>
            {(c) => <Input {...c} type="date" value={values.to} onChange={(e) => update({ to: e.target.value })} invalid={Boolean(errors.to)} />}
          </FormField>
          <FormField id="cs-target" label="Settle through" error={errors.target}>
            {(c) => (
              <Select {...c} value={values.target} onChange={(e) => update({ target: e.target.value as SettlementTarget })}>
                {SETTLEMENT_TARGETS.map((t) => (
                  <option key={t} value={t}>
                    {TARGET_LABELS[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not complete the settlement" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Preview settlement
            </Button>
            {result && rows.length > 0 ? (
              <Button icon="receipt" onClick={() => saveCsv(result.csv, result.filename)}>
                Download CSV
              </Button>
            ) : null}
            {result && rows.length > 0 && canRecord ? (
              <Button variant="secondary" loading={recording} onClick={record}>
                Record export
              </Button>
            ) : null}
          </div>
        </form>
      </Card>
      {result ? (
        <>
          <StatGroup label="Settlement totals">
            <StatTile label="Reps" value={new Set(rows.map((r) => r.repId)).size} />
            <StatTile label="Commission" value={total} />
          </StatGroup>
          <DataTable
            label="Commission settlement"
            columns={columns}
            rows={rows}
            getRowKey={(r) => `${r.repId}|${r.currency}`}
            empty={{ title: "No commission in this period", description: "Commission appears once invoices dated in the period have posted commission transactions." }}
          />
        </>
      ) : null}
    </div>
  );
}
