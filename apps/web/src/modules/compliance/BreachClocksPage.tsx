import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import {
  BREACH_REGIMES,
  computeBreachClocks,
  describeProblem,
  type BreachClock,
  type BreachClocksInput,
  type BreachClocksResult,
  type BreachRegime,
  type ClockStatus,
  type Problem,
} from "./api/client";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";

export const regimeLabel: Record<BreachRegime, string> = {
  GDPR: "EU GDPR",
  UK_GDPR: "UK GDPR",
  US_STATE: "US state law",
  PIPEDA: "Canada PIPEDA",
  CERT_IN: "India CERT-In",
  LFPDPPP: "Mexico LFPDPPP",
};

const statusTone: Record<ClockStatus, BadgeTone> = { OVERDUE: "danger", AT_RISK: "warning", NOT_DUE: "info", NO_FIXED_DEADLINE: "neutral" };

const statusLabel: Record<ClockStatus, string> = { OVERDUE: "Overdue", AT_RISK: "Due within 12 h", NOT_DUE: "Not due", NO_FIXED_DEADLINE: "No fixed deadline" };

interface Values {
  awareAt: string;
  confirmed: boolean;
  regimes: BreachRegime[];
  stateDays: string;
}

type Errors = Partial<Record<"awareAt" | "regimes" | "stateDays", string>>;

/**
 * Checks the form and builds the request. awareAt is a datetime-local value
 * read as UTC, because the clocks are UTC instants (BR-48.18).
 */
export function toBreachInput(values: Values, now: Date = new Date()): { input?: BreachClocksInput; errors: Errors } {
  const errors: Errors = {};
  const aware = values.awareAt ? new Date(`${values.awareAt}Z`) : null;
  if (!aware || Number.isNaN(aware.getTime())) errors.awareAt = "Enter when you became aware of the breach.";
  else if (aware.getTime() > now.getTime()) errors.awareAt = "Awareness cannot be in the future.";
  if (values.regimes.length === 0) errors.regimes = "Choose at least one regime.";
  let days: number | undefined;
  if (values.regimes.includes("US_STATE") && values.stateDays.trim() !== "") {
    days = Number(values.stateDays.trim());
    if (!Number.isInteger(days) || days < 30 || days > 60) errors.stateDays = "Enter whole days from 30 to 60.";
  }
  if (Object.keys(errors).length > 0 || !aware) return { errors };
  const input: BreachClocksInput = { awareAt: aware.toISOString(), personalDataBreach: values.confirmed, regimes: values.regimes };
  if (days !== undefined) input.usStateWindowDays = days;
  return { input, errors };
}

const columns: DataTableColumn<BreachClock>[] = [
  { key: "regime", header: "Regime", render: (c) => regimeLabel[c.regime] ?? c.regime },
  { key: "recipient", header: "Notify", render: (c) => c.recipient.charAt(0).toUpperCase() + c.recipient.slice(1) },
  { key: "deadlineAt", header: "Deadline (UTC)", render: (c) => (c.deadlineAt ? formatDateTime(c.deadlineAt) : dash) },
  { key: "status", header: "Status", render: (c) => <Badge tone={statusTone[c.status]}>{statusLabel[c.status]}</Badge> },
  { key: "basis", header: "Basis", render: (c) => <span className={styles.muted}>{c.basis}</span> },
];

/**
 * Breach notification clocks (PRD §9.48.6 Security incidents, duty
 * SecurityIncidentManage): the deadlines per regime of a confirmed
 * personal-data breach, counted from awareness (FR-48.29, BR-48.18).
 */
export function BreachClocksPage() {
  const [values, setValues] = useState<Values>({ awareAt: "", confirmed: true, regimes: [], stateDays: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<BreachClocksResult | null>(null);

  function toggle(regime: BreachRegime, on: boolean) {
    setValues((v) => ({ ...v, regimes: on ? BREACH_REGIMES.filter((r) => r === regime || v.regimes.includes(r)) : v.regimes.filter((r) => r !== regime) }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { input, errors: found } = toBreachInput(values);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    setFailure(null);
    try {
      setResult(await computeBreachClocks(input));
    } catch (error) {
      setFailure(describeProblem(error));
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const overdue = result?.items.filter((c) => c.status === "OVERDUE").length ?? 0;
  const atRisk = result?.items.filter((c) => c.status === "AT_RISK").length ?? 0;
  const summary = result
    ? `${result.items.length} ${result.items.length === 1 ? "clock" : "clocks"} from awareness at ${formatDateTime(result.awareAt)}: ${overdue} overdue, ${atRisk} due within 12 hours.`
    : null;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Breach notification clocks"
        description="Work out who must be told about a confirmed personal-data breach, and by when. Clocks start when you became aware of the breach, not when it was detected. Nothing is saved yet."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Breach notification clocks">
          <FormField id="breach-aware" label="Aware at (UTC)" hint="When the organisation became aware of the breach." error={errors.awareAt}>
            {(control) => <Input {...control} type="datetime-local" value={values.awareAt} onChange={(e) => setValues({ ...values, awareAt: e.target.value })} invalid={Boolean(errors.awareAt)} />}
          </FormField>
          <FormField id="breach-state-days" label="US state window (days)" hint="30 to 60 days for the states involved; 30 when left empty." error={errors.stateDays} optional>
            {(control) => (
              <Input
                {...control}
                inputMode="numeric"
                value={values.stateDays}
                disabled={!values.regimes.includes("US_STATE")}
                onChange={(e) => setValues({ ...values, stateDays: e.target.value })}
                invalid={Boolean(errors.stateDays)}
              />
            )}
          </FormField>
          <fieldset className={`${styles.fieldset} ${styles.wide}`} aria-describedby={errors.regimes ? "breach-regimes-error" : undefined}>
            <legend className={styles.legend}>Regimes</legend>
            <div className={styles.checkList}>
              {BREACH_REGIMES.map((regime) => (
                <span key={regime} className={styles.check}>
                  <Input id={`breach-regime-${regime}`} type="checkbox" checked={values.regimes.includes(regime)} onChange={(e) => toggle(regime, e.target.checked)} />
                  <label htmlFor={`breach-regime-${regime}`}>{regimeLabel[regime]}</label>
                </span>
              ))}
            </div>
            {errors.regimes ? (
              <p id="breach-regimes-error" className={styles.fieldError}>
                {errors.regimes}
              </p>
            ) : null}
          </fieldset>
          <span className={`${styles.check} ${styles.wide}`}>
            <Input id="breach-confirmed" type="checkbox" checked={values.confirmed} onChange={(e) => setValues({ ...values, confirmed: e.target.checked })} />
            <label htmlFor="breach-confirmed">Personal data is confirmed to be affected</label>
          </span>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Compute clocks
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={failure ? "The clocks could not be computed" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone={overdue > 0 ? "warning" : "success"} live="status">
        {summary}
      </Alert>
      {result ? (
        <DataTable
          label="Breach clocks"
          columns={columns}
          rows={result.items}
          getRowKey={(c) => `${c.regime}-${c.recipient}`}
          empty={{ title: "No clocks for these regimes" }}
        />
      ) : (
        <Card>
          <EmptyState icon="info" title="No clocks yet" description="Enter when you became aware of the breach and choose the regimes that apply." compact />
        </Card>
      )}
    </div>
  );
}
