import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  BREACH_REGIMES,
  checkIncidentStep,
  describeProblem,
  INCIDENT_SEVERITIES,
  INCIDENT_SOURCES,
  INCIDENT_STEPS,
  type BreachRegime,
  type IncidentClock,
  type IncidentSeverity,
  type IncidentSource,
  type IncidentStatus,
  type IncidentStepInput,
  type IncidentStepResult,
  type Problem,
} from "./api/client";
import { regimeLabel } from "./BreachClocksPage";
import { formatDateTime, sentence } from "./format";
import styles from "./pages.module.css";

export interface StepValues {
  severity: IncidentSeverity;
  source: IncidentSource;
  from: IncidentStatus | "";
  breach: boolean;
  awareAt: string;
  regimes: BreachRegime[];
  notified: Partial<Record<BreachRegime, string>>;
  justification: string;
}

type Errors = Partial<Record<"awareAt" | "regimes" | "notified", string>>;

/** The step after from: TRIAGE when the incident is new. */
export function nextStatus(from: IncidentStatus | ""): IncidentStatus | null {
  if (!from) return "TRIAGE";
  const i = INCIDENT_STEPS.indexOf(from);
  return INCIDENT_STEPS[i + 1] ?? null;
}

const utc = (local: string) => new Date(`${local}Z`);

/** Checks the form and builds the request; datetime-local values are UTC. */
export function toStepInput(v: StepValues, now: Date = new Date()): { input?: IncidentStepInput; errors: Errors } {
  const errors: Errors = {};
  const to = nextStatus(v.from);
  const input: IncidentStepInput = { severity: v.severity, source: v.source, to: to ?? "CLOSED", personalDataBreach: v.breach };
  if (v.from) input.from = v.from;
  if (v.breach) {
    const aware = v.awareAt ? utc(v.awareAt) : null;
    if (!aware || Number.isNaN(aware.getTime())) errors.awareAt = "Enter when you became aware of the breach.";
    else if (aware.getTime() > now.getTime()) errors.awareAt = "Awareness cannot be in the future.";
    else input.awareAt = aware.toISOString();
    if (v.regimes.length === 0) errors.regimes = "Choose at least one regime.";
    input.regimes = v.regimes;
    const notifiedAt: Partial<Record<BreachRegime, string>> = {};
    for (const r of v.regimes) {
      const raw = v.notified[r];
      if (!raw) continue;
      const at = utc(raw);
      if (Number.isNaN(at.getTime()) || at.getTime() > now.getTime()) errors.notified = "A notification time must be a past date and time.";
      else notifiedAt[r] = at.toISOString();
    }
    if (Object.keys(notifiedAt).length > 0) input.notifiedAt = notifiedAt;
  }
  if (v.justification.trim()) input.justification = v.justification.trim();
  return Object.keys(errors).length > 0 ? { errors } : { input, errors };
}

const columns: DataTableColumn<IncidentClock>[] = [
  { key: "regime", header: "Regime", render: (c) => regimeLabel[c.regime] ?? c.regime },
  { key: "recipient", header: "Notify", render: (c) => sentence(c.recipient) },
  { key: "deadlineAt", header: "Deadline (UTC)", render: (c) => formatDateTime(c.deadlineAt) },
  { key: "notifiedAt", header: "Sent", render: (c) => formatDateTime(c.notifiedAt) },
  {
    key: "result",
    header: "Result",
    render: (c) => (!c.deadlineAt ? <Badge tone="neutral">No fixed deadline</Badge> : c.met ? <Badge tone="success">Met</Badge> : c.late ? <Badge tone="danger">Late</Badge> : <Badge tone="info">Not yet sent</Badge>),
  },
];

/**
 * Security incident step (PRD §9.48.6 Security incidents, duty
 * SecurityIncidentManage): check the next lifecycle step of an incident and,
 * for a personal-data breach, whether its notification clocks allow it
 * (FR-48.29, BR-48.18). Incidents are not recorded yet.
 */
export function IncidentStepPage() {
  const [values, setValues] = useState<StepValues>({ severity: "SEV2", source: "ALERT", from: "", breach: false, awareAt: "", regimes: [], notified: {}, justification: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<IncidentStepResult | null>(null);
  const [busy, setBusy] = useState(false);
  const to = nextStatus(values.from);

  function toggle(regime: BreachRegime, on: boolean) {
    setValues((v) => ({ ...v, regimes: on ? BREACH_REGIMES.filter((r) => r === regime || v.regimes.includes(r)) : v.regimes.filter((r) => r !== regime) }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { input, errors: found } = toStepInput(values);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    setFailure(null);
    try {
      setResult(await checkIncidentStep(input));
    } catch (error) {
      setFailure(describeProblem(error));
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Incident step check"
        description="Incidents move from triage to contained, eradicated, recovered and closed, one step at a time. A personal-data breach cannot be closed while a notification went out late, or not at all, without a late-notification justification."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Incident step">
          <FormField id="inc-severity" label="Severity">
            {(control) => (
              <Select {...control} value={values.severity} onChange={(e) => setValues({ ...values, severity: e.target.value as IncidentSeverity })}>
                {INCIDENT_SEVERITIES.map((s) => (
                  <option key={s} value={s}>
                    {s.replace("SEV", "Sev-")}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="inc-source" label="Source">
            {(control) => (
              <Select {...control} value={values.source} onChange={(e) => setValues({ ...values, source: e.target.value as IncidentSource })}>
                {INCIDENT_SOURCES.map((s) => (
                  <option key={s} value={s}>
                    {sentence(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="inc-from" label="Current status" hint={to ? `Checks the move to ${sentence(to).toLowerCase()}.` : "A closed incident has no next step."}>
            {(control) => (
              <Select {...control} value={values.from} onChange={(e) => setValues({ ...values, from: e.target.value as IncidentStatus | "" })}>
                <option value="">New incident</option>
                {INCIDENT_STEPS.map((s) => (
                  <option key={s} value={s}>
                    {sentence(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <span className={`${styles.check} ${styles.wide}`}>
            <Input id="inc-breach" type="checkbox" checked={values.breach} onChange={(e) => setValues({ ...values, breach: e.target.checked })} />
            <label htmlFor="inc-breach">Confirmed personal-data breach</label>
          </span>
          {values.breach ? (
            <>
              <FormField id="inc-aware" label="Aware at (UTC)" error={errors.awareAt}>
                {(control) => <Input {...control} type="datetime-local" value={values.awareAt} onChange={(e) => setValues({ ...values, awareAt: e.target.value })} invalid={Boolean(errors.awareAt)} />}
              </FormField>
              <fieldset className={`${styles.fieldset} ${styles.wide}`} aria-describedby={errors.regimes || errors.notified ? "inc-regimes-error" : undefined}>
                <legend className={styles.legend}>Regimes and when each was notified (UTC)</legend>
                <div className={styles.checkList}>
                  {BREACH_REGIMES.map((regime) => (
                    <span key={regime} className={styles.check}>
                      <Input id={`inc-regime-${regime}`} type="checkbox" checked={values.regimes.includes(regime)} onChange={(e) => toggle(regime, e.target.checked)} />
                      <label htmlFor={`inc-regime-${regime}`}>{regimeLabel[regime]}</label>
                      {values.regimes.includes(regime) ? (
                        <Input
                          type="datetime-local"
                          aria-label={`${regimeLabel[regime]} notified at`}
                          value={values.notified[regime] ?? ""}
                          onChange={(e) => setValues({ ...values, notified: { ...values.notified, [regime]: e.target.value } })}
                        />
                      ) : null}
                    </span>
                  ))}
                </div>
                {errors.regimes || errors.notified ? (
                  <p id="inc-regimes-error" className={styles.fieldError}>
                    {errors.regimes ?? errors.notified}
                  </p>
                ) : null}
              </fieldset>
            </>
          ) : null}
          {to === "CLOSED" ? (
            <div className={styles.wide}>
              <FormField id="inc-justification" label="Late-notification justification" hint="Needed when a notification went out after its deadline." optional>
                {(control) => <Textarea {...control} rows={3} value={values.justification} onChange={(e) => setValues({ ...values, justification: e.target.value })} />}
              </FormField>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy} disabled={!to}>
              Check step
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={failure ? "The step is not allowed" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone="success" live="status">
        {result ? `The incident can move to ${sentence(result.status).toLowerCase()}.${result.pageOnCall ? " Sev-1: page the on-call responder." : ""}` : null}
      </Alert>
      {result && result.clocks.length > 0 ? (
        <DataTable label="Notification clocks" columns={columns} rows={result.clocks} getRowKey={(c) => `${c.regime}-${c.recipient}`} empty={{ title: "No clocks" }} />
      ) : (
        <Card>
          <EmptyState icon="info" title={result ? "No breach clocks" : "No step checked"} description={result ? "The incident is not a confirmed personal-data breach." : "Choose the incident's status to check its next step."} compact />
        </Card>
      )}
    </div>
  );
}
