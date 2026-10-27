import { useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createBenefitEnrollment,
  describeProblem,
  endBenefitEnrollment,
  getWorkerBenefits,
  listBenefitPlans,
  newKey,
  recordLifeEvent,
  type BenefitEnrollment,
  type BenefitPlan,
  type EnrollmentEndReason,
  type LifeEvent,
  type LifeEventKind,
  type NewBenefitEnrollment,
  type Problem,
} from "./api/client";
import { DATE, dayOf, endReasonLabel, enrollmentStatusLabel, formatRate, lastDay, lifeEventLabel, rateBasisLabel, todayIso } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface EnrollValues {
  planId: string;
  optionCode: string;
  effectiveDate: string;
  lifeEventId: string;
  dependentCount: string;
}
type EnrollField = keyof EnrollValues;
type EnrollErrors = Partial<Record<EnrollField, string>>;
const ENROLL_FIELDS: EnrollField[] = ["planId", "optionCode", "effectiveDate", "lifeEventId", "dependentCount"];

/** The enrollment checks that need no database (8.42.5), and the request the form sends. */
export function validateEnrollment(workerId: string, values: EnrollValues, plan?: BenefitPlan): { errors: EnrollErrors; input?: NewBenefitEnrollment } {
  const errors: EnrollErrors = {};
  if (!plan) errors.planId = "Choose a plan.";
  else if (!plan.options.some((o) => o.code === values.optionCode)) errors.optionCode = "Choose one of the plan's options.";
  if (!DATE.test(values.effectiveDate)) errors.effectiveDate = "Enter the day coverage starts.";
  else if (plan && !values.lifeEventId && (values.effectiveDate < plan.enrollmentStart || values.effectiveDate > plan.enrollmentEnd))
    errors.effectiveDate = `Open enrollment runs ${plan.enrollmentStart} to ${plan.enrollmentEnd}. Outside it, choose a life event.`;
  const dependents = values.dependentCount.trim();
  if (!/^\d{1,2}$/.test(dependents) || Number(dependents) > 20) errors.dependentCount = "Enter 0 to 20 dependents.";
  if (Object.keys(errors).length > 0 || !plan) return { errors };
  const input: NewBenefitEnrollment = {
    workerId,
    planId: plan.id,
    optionCode: values.optionCode,
    effectiveDate: values.effectiveDate,
    dependentCount: Number(dependents),
  };
  if (values.lifeEventId) input.lifeEventId = values.lifeEventId;
  return { errors, input };
}

/** The day a new enrollment suggests: today inside the window, otherwise the window's first day. */
function suggestedStart(plan: BenefitPlan | undefined, today: string): string {
  if (!plan) return today;
  return today >= plan.enrollmentStart && today <= plan.enrollmentEnd ? today : plan.enrollmentStart;
}

/** Enroll the worker in one option of a plan, with the per-period cost shown before confirming (8.42.6 wizard). */
function EnrollForm({
  workerId,
  plans,
  lifeEvents,
  onDone,
  onCancel,
}: {
  workerId: string;
  plans: BenefitPlan[];
  lifeEvents: LifeEvent[];
  onDone(enrollment: BenefitEnrollment): void;
  onCancel(): void;
}) {
  const today = todayIso();
  const first = plans[0];
  const [values, setValues] = useState<EnrollValues>({
    planId: first?.id ?? "",
    optionCode: first?.options[0]?.code ?? "",
    effectiveDate: suggestedStart(first, today),
    lifeEventId: "",
    dependentCount: "0",
  });
  const [errors, setErrors] = useState<EnrollErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const plan = plans.find((p) => p.id === values.planId);
  const option = plan?.options.find((o) => o.code === values.optionCode);

  function update(patch: Partial<EnrollValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function choosePlan(planId: string) {
    const next = plans.find((p) => p.id === planId);
    update({ planId, optionCode: next?.options[0]?.code ?? "", effectiveDate: suggestedStart(next, today) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateEnrollment(workerId, values, plan);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      onDone(await createBenefitEnrollment(input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: EnrollErrors = {};
      for (const name of ENROLL_FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "HCM-BEN-001" || problem.code === "HCM-BEN-004") fieldErrors.effectiveDate = problem.message;
      if (problem.code === "HCM-BEN-003") fieldErrors.optionCode = problem.message;
      if (problem.code === "HCM-BEN-LE-NOT-FOUND") fieldErrors.lifeEventId = problem.message;
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Enroll in a benefit plan">
      <FormField id="enroll-planId" label="Plan" error={errors.planId}>
        {(control) => (
          <Select {...control} value={values.planId} invalid={Boolean(errors.planId)} onChange={(event) => choosePlan(event.target.value)}>
            {plans.map((p) => (
              <option key={p.id} value={p.id}>
                {p.code}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="enroll-optionCode" label="Coverage" error={errors.optionCode}>
        {(control) => (
          <Select {...control} value={values.optionCode} invalid={Boolean(errors.optionCode)} onChange={(event) => update({ optionCode: event.target.value })}>
            {(plan?.options ?? []).map((o) => (
              <option key={o.code} value={o.code}>
                {o.coverageLevel} ({o.code})
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="enroll-effectiveDate" label="Coverage starts" error={errors.effectiveDate}>
        {(control) => (
          <Input {...control} type="date" value={values.effectiveDate} invalid={Boolean(errors.effectiveDate)} onChange={(event) => update({ effectiveDate: event.target.value })} />
        )}
      </FormField>
      <FormField id="enroll-lifeEventId" label="Life event" hint="Needed outside open enrollment: coverage may start up to 30 days after the event." error={errors.lifeEventId}>
        {(control) => (
          <Select {...control} value={values.lifeEventId} invalid={Boolean(errors.lifeEventId)} onChange={(event) => update({ lifeEventId: event.target.value })}>
            <option value="">None (open enrollment)</option>
            {lifeEvents.map((e) => (
              <option key={e.id} value={e.id}>
                {lifeEventLabel[e.kind] ?? e.kind} on {e.eventDate} (until {e.windowEnd})
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="enroll-dependentCount" label="Dependents covered" error={errors.dependentCount}>
        {(control) => (
          <Input {...control} inputMode="numeric" value={values.dependentCount} invalid={Boolean(errors.dependentCount)} onChange={(event) => update({ dependentCount: event.target.value })} />
        )}
      </FormField>
      {plan && option ? (
        <div className={styles.formActions}>
          <p className={styles.note} aria-live="polite">
            The employee pays {formatRate(option.employeeRate, plan.currency)} {rateBasisLabel[option.rateBasis] ?? option.rateBasis}
            {plan.preTax ? " before tax" : ""}; the employer pays {formatRate(option.employerRate, plan.currency)}.
          </p>
        </div>
      ) : null}
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title={failure.code === "HCM-BEN-002" ? "Not eligible for this plan" : "Could not enroll"} live="alert">
            {failure.message} ({failure.code})
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Enroll
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

/** Record a life event; it opens a 30-day enrollment window from its date. */
function LifeEventForm({ workerId, onDone, onCancel }: { workerId: string; onDone(event: LifeEvent): void; onCancel(): void }) {
  const [kind, setKind] = useState<LifeEventKind>("MARRIAGE");
  const [eventDate, setEventDate] = useState(todayIso());
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (!DATE.test(eventDate)) {
      setError("Enter the day of the event.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      onDone(await recordLifeEvent({ workerId, kind, eventDate }, key.current));
    } catch (failed) {
      const problem = describeProblem(failed);
      if (problem.fields.eventDate) setError(problem.fields.eventDate);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Record a life event">
      <FormField id="life-kind" label="Event">
        {(control) => (
          <Select
            {...control}
            value={kind}
            onChange={(event) => {
              setKind(event.target.value as LifeEventKind);
              key.current = newKey();
            }}
          >
            {Object.entries(lifeEventLabel).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="life-eventDate" label="Date of the event" error={error ?? undefined}>
        {(control) => (
          <Input
            {...control}
            type="date"
            value={eventDate}
            invalid={Boolean(error)}
            onChange={(event) => {
              setEventDate(event.target.value);
              key.current = newKey();
            }}
          />
        )}
      </FormField>
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="Could not record the event" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Record event
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

/** End one enrollment after its last covered day; a termination keeps coverage to the month end (BR-42.6). */
function EndForm({ enrollment, onDone, onCancel }: { enrollment: BenefitEnrollment; onDone(e: BenefitEnrollment): void; onCancel(): void }) {
  const [day, setDay] = useState(todayIso());
  const [reason, setReason] = useState<EnrollmentEndReason>("LIFE_EVENT");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (!DATE.test(day)) {
      setError("Enter the last covered day.");
      return;
    }
    if (day < dayOf(enrollment.validFrom)) {
      setError(`Coverage started on ${dayOf(enrollment.validFrom)}; the last day can't be before it.`);
      return;
    }
    setError(null);
    setBusy(true);
    try {
      onDone(await endBenefitEnrollment(enrollment.id, day, reason));
    } catch (failed) {
      const problem = describeProblem(failed);
      if (problem.code === "HCM-BEN-005" || problem.fields.lastDay) setError(problem.fields.lastDay ?? problem.message);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label={`End ${enrollment.planCode} coverage`}>
      <FormField id="end-reason" label="Reason">
        {(control) => (
          <Select {...control} value={reason} onChange={(event) => setReason(event.target.value as EnrollmentEndReason)}>
            {Object.entries(endReasonLabel).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField
        id="end-lastDay"
        label={reason === "TERMINATION" ? "Termination date" : "Last covered day"}
        hint={reason === "TERMINATION" ? "Coverage continues to the end of this month." : undefined}
        error={error ?? undefined}
      >
        {(control) => <Input {...control} type="date" value={day} invalid={Boolean(error)} onChange={(event) => setDay(event.target.value)} />}
      </FormField>
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="Could not end the coverage" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="danger" loading={busy}>
          End coverage
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Keep coverage
        </Button>
      </div>
    </form>
  );
}

const lifeEventColumns: DataTableColumn<LifeEvent>[] = [
  { key: "kind", header: "Event", render: (row) => lifeEventLabel[row.kind] ?? row.kind },
  { key: "eventDate", header: "Date", mono: true },
  { key: "windowEnd", header: "Enroll until", mono: true },
];

interface BenefitsData {
  enrollments: BenefitEnrollment[];
  lifeEvents: LifeEvent[];
  plans: BenefitPlan[];
}

type Mode = { kind: "enroll" } | { kind: "life" } | { kind: "end"; enrollment: BenefitEnrollment } | null;

/** A worker's benefits: enrollments with their cost, life events, and the enroll, record and end forms (FR-42.8, FR-42.9). */
export function BenefitsCard({ workerId }: { workerId: string }) {
  const [mode, setMode] = useState<Mode>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const state = useLoad<BenefitsData>(async (signal) => {
    const [benefits, plans] = await Promise.all([getWorkerBenefits(workerId, signal), listBenefitPlans({}, signal)]);
    return { enrollments: benefits.enrollments, lifeEvents: benefits.lifeEvents, plans: plans.items };
  }, `benefits|${workerId}`);

  const enrollmentColumns: DataTableColumn<BenefitEnrollment>[] = [
    { key: "planCode", header: "Plan", mono: true },
    { key: "coverageLevel", header: "Coverage" },
    { key: "validFrom", header: "From", mono: true, render: (row) => dayOf(row.validFrom) },
    { key: "validTo", header: "Last day", mono: true, render: (row) => lastDay(row.validTo) },
    {
      key: "status",
      header: "Status",
      render: (row) => {
        const s = enrollmentStatusLabel[row.status] ?? { label: row.status, tone: "neutral" as const };
        return (
          <>
            <Badge tone={s.tone}>{s.label}</Badge>
            {row.endReason ? ` ${endReasonLabel[row.endReason] ?? row.endReason}` : null}
          </>
        );
      },
    },
    {
      key: "employeeRate",
      header: "Employee cost",
      mono: true,
      align: "end",
      render: (row) => `${formatRate(row.employeeRate, row.currency)} ${rateBasisLabel[row.rateBasis] ?? row.rateBasis}`,
    },
    { key: "dependentCount", header: "Dependents", mono: true, align: "end" },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.validTo ? null : (
          <Button size="sm" variant="ghost" aria-label={`End ${row.planCode} coverage`} onClick={() => setMode({ kind: "end", enrollment: row })}>
            End
          </Button>
        ),
    },
  ];

  const plans = state.data?.plans ?? [];
  return (
    <Card
      title="Benefits"
      actions={
        mode || !state.data ? null : (
          <div className={styles.rowActions}>
            <Button size="sm" onClick={() => setMode({ kind: "life" })}>
              Record life event
            </Button>
            <Button size="sm" onClick={() => setMode({ kind: "enroll" })} disabled={plans.length === 0}>
              Enroll
            </Button>
          </div>
        )
      }
    >
      <div className={styles.stack}>
        <Alert tone="success" live="status">
          {notice}
        </Alert>
        {state.error ? (
          <Alert tone="danger" title="Could not load benefits" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : !state.data ? (
          <div aria-busy="true">
            <Skeleton width="3/4" />
            <Skeleton width="1/2" />
          </div>
        ) : (
          <>
            {plans.length === 0 ? <p className={styles.note}>There are no benefit plans yet, so nobody can enroll.</p> : null}
            {mode?.kind === "enroll" ? (
              <EnrollForm
                workerId={workerId}
                plans={plans}
                lifeEvents={state.data.lifeEvents}
                onCancel={() => setMode(null)}
                onDone={(e) => {
                  setMode(null);
                  setNotice(`Enrolled in ${e.planCode} (${e.coverageLevel}) from ${dayOf(e.validFrom)}.`);
                  state.reload();
                }}
              />
            ) : null}
            {mode?.kind === "life" ? (
              <LifeEventForm
                workerId={workerId}
                onCancel={() => setMode(null)}
                onDone={(e) => {
                  setMode(null);
                  setNotice(`Recorded ${(lifeEventLabel[e.kind] ?? e.kind).toLowerCase()} on ${e.eventDate}. Enrollment is open until ${e.windowEnd}.`);
                  state.reload();
                }}
              />
            ) : null}
            {mode?.kind === "end" ? (
              <EndForm
                enrollment={mode.enrollment}
                onCancel={() => setMode(null)}
                onDone={(e) => {
                  setMode(null);
                  setNotice(`${e.planCode} coverage ends after ${lastDay(e.validTo)}.`);
                  state.reload();
                }}
              />
            ) : null}
            <DataTable
              label="Benefit enrollments"
              columns={enrollmentColumns}
              rows={state.data.enrollments}
              getRowKey={(row) => row.id}
              empty={{ title: "Not enrolled in any plan", description: "Enroll during open enrollment, or record a life event first." }}
            />
            <DataTable
              label="Life events"
              columns={lifeEventColumns}
              rows={state.data.lifeEvents}
              getRowKey={(row) => row.id}
              empty={{ title: "No life events", description: "A marriage, birth or loss of other coverage opens a 30-day enrollment window." }}
            />
          </>
        )}
      </div>
    </Card>
  );
}
