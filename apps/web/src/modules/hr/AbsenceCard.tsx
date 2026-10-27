import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  cancelAbsenceRequest,
  createAbsenceRequest,
  describeProblem,
  getAbsenceBalances,
  listAbsencePlans,
  listAbsenceRequests,
  newKey,
  type AbsenceBalance,
  type AbsencePlan,
  type AbsenceRequest,
  type AbsenceRequestResult,
  type NewAbsenceRequest,
  type Problem,
} from "./api/client";
import { RequestStatus } from "./AbsenceRequestsPage";
import { absenceUnitLabel, addFixed2, DATE, todayIso, validDecimal } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const balanceColumns: DataTableColumn<AbsenceBalance>[] = [
  { key: "planCode", header: "Plan", mono: true },
  { key: "planYearStart", header: "Plan year from", mono: true },
  { key: "carryOver", header: "Carry-over", mono: true, align: "end", render: (row) => row.carryOver.balance },
  { key: "accrued", header: "Accrued", mono: true, align: "end", render: (row) => row.current.accrued },
  { key: "used", header: "Used", mono: true, align: "end", render: (row) => addFixed2(row.current.used, row.carryOver.used) },
  { key: "total", header: "Available", mono: true, align: "end", render: (row) => `${row.total} ${absenceUnitLabel[row.unit] ?? row.unit}` },
];

interface Values {
  planId: string;
  from: string;
  to: string;
  quantity: string;
  reason: string;
}
type Field = keyof Values;
type Errors = Partial<Record<Field, string>>;
const FIELDS: Field[] = ["planId", "from", "to", "quantity", "reason"];

/** The service's request checks that need no balance (FR-42.7), and the request the form sends. */
export function validateAbsenceRequest(workerId: string, values: Values, unit?: string): { errors: Errors; input?: NewAbsenceRequest } {
  const errors: Errors = {};
  if (!values.planId) errors.planId = "Choose a plan.";
  if (!DATE.test(values.from)) errors.from = "Enter the first day of the absence.";
  if (!DATE.test(values.to)) errors.to = "Enter the last day of the absence.";
  else if (DATE.test(values.from) && values.to < values.from) errors.to = "The last day can't be before the first.";
  const quantity = values.quantity.trim();
  if (!validDecimal(quantity, 2, false)) errors.quantity = "Enter a positive amount with at most 2 decimals, for example 1.5.";
  else if (DATE.test(values.from) && DATE.test(values.to) && values.to >= values.from) {
    const days = Math.round((Date.parse(`${values.to}T00:00:00Z`) - Date.parse(`${values.from}T00:00:00Z`)) / 86_400_000) + 1;
    const max = unit === "HOURS" ? days * 24 : days;
    if (Number(quantity) > max) errors.quantity = `These dates allow at most ${max} ${absenceUnitLabel[unit ?? "DAYS"] ?? "days"}.`;
  }
  if (values.reason.length > 500) errors.reason = "Keep the reason to 500 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewAbsenceRequest = { workerId, planId: values.planId, from: values.from, to: values.to, quantity };
  if (values.reason.trim()) input.reason = values.reason.trim();
  return { errors, input };
}

/** Request absence for one worker; the service checks the balance and overlaps. */
export function AbsenceRequestForm({
  workerId,
  plans,
  onCreated,
  onCancel,
}: {
  workerId: string;
  plans: AbsencePlan[];
  onCreated(result: AbsenceRequestResult): void;
  onCancel(): void;
}) {
  const today = todayIso();
  const [values, setValues] = useState<Values>({ planId: plans[0]?.id ?? "", from: today, to: today, quantity: "", reason: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const unit = plans.find((p) => p.id === values.planId)?.unit;

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAbsenceRequest(workerId, values, unit);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      onCreated(await createAbsenceRequest(input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "HCM-ABS-002") fieldErrors.quantity = problem.message;
      if (problem.code === "HCM-ABS-001" || problem.code === "HCM-ABS-YEAR-SPAN") fieldErrors.to = problem.message;
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Request absence">
      <FormField id="absence-planId" label="Plan" error={errors.planId}>
        {(control) => (
          <Select {...control} value={values.planId} invalid={Boolean(errors.planId)} onChange={(event) => update({ planId: event.target.value })}>
            {plans.map((p) => (
              <option key={p.id} value={p.id}>
                {p.code} ({absenceUnitLabel[p.unit] ?? p.unit})
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="absence-from" label="First day" error={errors.from}>
        {(control) => <Input {...control} type="date" value={values.from} invalid={Boolean(errors.from)} onChange={(event) => update({ from: event.target.value })} />}
      </FormField>
      <FormField id="absence-to" label="Last day" error={errors.to}>
        {(control) => <Input {...control} type="date" value={values.to} invalid={Boolean(errors.to)} onChange={(event) => update({ to: event.target.value })} />}
      </FormField>
      <FormField id="absence-quantity" label={`Quantity (${absenceUnitLabel[unit ?? "DAYS"] ?? "days"})`} hint="Half days are fine, for example 0.5." error={errors.quantity}>
        {(control) => (
          <Input {...control} inputMode="decimal" value={values.quantity} invalid={Boolean(errors.quantity)} onChange={(event) => update({ quantity: event.target.value })} />
        )}
      </FormField>
      <FormField id="absence-reason" label="Reason" optional error={errors.reason}>
        {(control) => <Textarea {...control} rows={2} maxLength={500} value={values.reason} onChange={(event) => update({ reason: event.target.value })} />}
      </FormField>
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="Could not submit the request" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Submit request
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

interface AbsenceData {
  balances: AbsenceBalance[];
  requests: AbsenceRequest[];
  plans: AbsencePlan[];
}

/** A worker's absence: balances for the current plan year, requests, and the request form (ESS, FR-42.6/42.7). */
export function AbsenceCard({ workerId }: { workerId: string }) {
  const [adding, setAdding] = useState(false);
  const [notice, setNotice] = useState<{ text: string; warnings: string[] } | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [canceling, setCanceling] = useState<string | null>(null);
  const state = useLoad<AbsenceData>(async (signal) => {
    const [balances, requests, plans] = await Promise.all([
      getAbsenceBalances(workerId, todayIso(), signal),
      listAbsenceRequests({ workerId }, signal),
      listAbsencePlans({}, signal),
    ]);
    return { balances: balances.balances, requests: requests.items, plans: plans.items };
  }, `absence|${workerId}`);

  async function cancel(row: AbsenceRequest) {
    setCanceling(row.id);
    setFailure(null);
    try {
      await cancelAbsenceRequest(row.id);
      setNotice({ text: `The request from ${row.from} is cancelled.`, warnings: [] });
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setCanceling(null);
    }
  }

  const requestColumns: DataTableColumn<AbsenceRequest>[] = [
    { key: "planCode", header: "Plan", mono: true },
    { key: "from", header: "From", mono: true },
    { key: "to", header: "To", mono: true },
    { key: "quantity", header: "Quantity", mono: true, align: "end", render: (row) => `${row.quantity} ${absenceUnitLabel[row.unit] ?? row.unit}` },
    { key: "status", header: "Status", render: (row) => <RequestStatus status={row.status} /> },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.status === "SUBMITTED" ? (
          <Button size="sm" variant="ghost" loading={canceling === row.id} aria-label={`Cancel the request from ${row.from}`} onClick={() => void cancel(row)}>
            Cancel
          </Button>
        ) : null,
    },
  ];

  const plans = state.data?.plans ?? [];
  return (
    <Card
      title="Absence"
      actions={
        adding || !state.data ? null : (
          <Button size="sm" onClick={() => setAdding(true)} disabled={plans.length === 0}>
            Request absence
          </Button>
        )
      }
    >
      <div className={styles.stack}>
        <Alert tone="success" live="status">
          {notice ? (
            <>
              {notice.text}
              {notice.warnings.length > 0 ? (
                <ul className={styles.warnings}>
                  {notice.warnings.map((w) => (
                    <li key={w}>{w}</li>
                  ))}
                </ul>
              ) : null}
            </>
          ) : null}
        </Alert>
        {failure ? (
          <Alert tone="danger" title="That didn't work" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        {state.error ? (
          <Alert tone="danger" title="Could not load absence" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : !state.data ? (
          <div aria-busy="true">
            <Skeleton width="3/4" />
            <Skeleton width="1/2" />
          </div>
        ) : (
          <>
            {plans.length === 0 ? <p className={styles.note}>There are no absence plans yet, so absence can't be requested.</p> : null}
            {adding ? (
              <AbsenceRequestForm
                workerId={workerId}
                plans={plans}
                onCancel={() => setAdding(false)}
                onCreated={(r) => {
                  setAdding(false);
                  setNotice({ text: `Requested ${r.quantity} ${absenceUnitLabel[r.unit] ?? r.unit} from ${r.from} to ${r.to}.`, warnings: r.warnings.map((w) => `${w.detail} (${w.code})`) });
                  state.reload();
                }}
              />
            ) : null}
            <DataTable
              label="Absence balances"
              columns={balanceColumns}
              rows={state.data.balances}
              getRowKey={(row) => row.planId}
              empty={{ title: "No balances yet", description: "Balances appear after the first accrual run." }}
            />
            <DataTable
              label="Absence requests"
              columns={requestColumns}
              rows={state.data.requests}
              getRowKey={(row) => row.id}
              empty={{ title: "No absence requested", description: "Requests you submit appear here until they're decided." }}
            />
          </>
        )}
      </div>
    </Card>
  );
}
