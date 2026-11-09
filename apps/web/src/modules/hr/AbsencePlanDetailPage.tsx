import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  closePlanYear,
  describeProblem,
  expireCarryOver,
  getAbsencePlan,
  runAccrual,
  type AbsencePlan,
  type AccrualPeriod,
  type PlanClose,
  type Problem,
} from "./api/client";
import { absenceUnitLabel, DATE, frequencyLabel, lastWholeMonth, planYearOf, shiftDays, todayIso } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const periodColumns: DataTableColumn<AccrualPeriod>[] = [
  { key: "periodStart", header: "From", mono: true },
  { key: "periodEnd", header: "To", mono: true },
  { key: "workers", header: "Workers", align: "end", mono: true },
  { key: "accrued", header: "Accrued", align: "end", mono: true },
  { key: "discarded", header: "Discarded at max", align: "end", mono: true },
];

const closeColumns: DataTableColumn<PlanClose>[] = [
  { key: "planYearStart", header: "Plan year from", mono: true },
  { key: "kind", header: "What", render: (row) => (row.kind === "YEAR_END" ? "Year end" : "Carry-over expiry") },
  { key: "workers", header: "Workers", align: "end", mono: true },
  { key: "carried", header: "Carried over", align: "end", mono: true },
  { key: "expired", header: "Expired", align: "end", mono: true },
];

function Entry({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** The period a run form starts with: last month for monthly plans, the last whole plan year for annual ones. */
function suggestedPeriod(plan: AbsencePlan, today: string): { start: string; end: string } {
  if (plan.accrualFrequency === "ANNUAL") {
    const current = planYearOf(plan.planYearStart, today);
    const start = `${Number(current.slice(0, 4)) - 1}${current.slice(4)}`;
    return { start, end: shiftDays(current, -1) };
  }
  if (plan.accrualFrequency === "BIWEEKLY") {
    return { start: shiftDays(today, -14), end: shiftDays(today, -1) };
  }
  return lastWholeMonth(today);
}

/** Run one accrual period (BR-42.3). The service accrues each worker once per period, so a retry is safe. */
function AccrualCard({ plan, onDone }: { plan: AbsencePlan; onDone(): void }) {
  const initial = suggestedPeriod(plan, todayIso());
  const [start, setStart] = useState(initial.start);
  const [end, setEnd] = useState(initial.end);
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const unit = absenceUnitLabel[plan.unit] ?? plan.unit;

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setNotice(null);
    if (!DATE.test(start) || !DATE.test(end) || end < start) {
      setError("Enter a period whose last day is on or after its first day.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      const run = await runAccrual(plan.id, start, end);
      setNotice(
        run.replayed
          ? `This period was already accrued for all ${run.alreadyAccrued} workers. Nothing changed.`
          : `Accrued ${run.accrued} ${unit} to ${run.credited} workers for the plan year from ${run.planYearStart}.` +
              (run.discarded !== "0.00" ? ` ${run.discarded} ${unit} were above the max balance and discarded.` : ""),
      );
      onDone();
    } catch (failed) {
      setFailure(describeProblem(failed));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Run accrual">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Run accrual">
        <FormField id="accrual-start" label="First day" error={error ?? undefined}>
          {(control) => <Input {...control} type="date" value={start} invalid={Boolean(error)} onChange={(event) => setStart(event.target.value)} />}
        </FormField>
        <FormField id="accrual-end" label="Last day" hint="One whole period of the plan, whose accrual day has come.">
          {(control) => <Input {...control} type="date" value={end} onChange={(event) => setEnd(event.target.value)} />}
        </FormField>
        <div className={styles.formActions}>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          {failure ? (
            <Alert tone="danger" title="Could not run the accrual" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          ) : null}
        </div>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Run accrual
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Close a plan year (carry-over and expiry, BR-42.3) and expire unused carry-over. */
function PlanYearCard({ plan, onDone }: { plan: AbsencePlan; onDone(): void }) {
  const current = planYearOf(plan.planYearStart, todayIso());
  const [year, setYear] = useState(`${Number(current.slice(0, 4)) - 1}${current.slice(4)}`);
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<"close" | "expire" | null>(null);
  const unit = absenceUnitLabel[plan.unit] ?? plan.unit;

  async function run(kind: "close" | "expire") {
    setFailure(null);
    setNotice(null);
    if (!DATE.test(year)) {
      setError("Enter the first day of the plan year.");
      return;
    }
    setError(null);
    setBusy(kind);
    try {
      const out = kind === "close" ? await closePlanYear(plan.id, year) : await expireCarryOver(plan.id, year);
      if (out.workers === 0) {
        setNotice(`Nothing left to do: ${out.alreadyClosed} workers were already done for the plan year from ${out.planYearStart}.`);
      } else if (kind === "close") {
        setNotice(`Closed the plan year from ${out.planYearStart} for ${out.workers} workers: ${out.carried} ${unit} carried over, ${out.expired} ${unit} expired.`);
      } else {
        setNotice(`Expired ${out.expired} ${unit} of unused carry-over for ${out.workers} workers.`);
      }
      onDone();
    } catch (failed) {
      setFailure({ title: kind === "close" ? "Could not close the plan year" : "Could not expire the carry-over", problem: describeProblem(failed) });
    } finally {
      setBusy(null);
    }
  }

  return (
    <Card title="Plan year">
      <form
        className={styles.form}
        onSubmit={(event) => {
          event.preventDefault();
          void run("close");
        }}
        noValidate
        aria-label="Plan year"
      >
        <FormField
          id="plan-year"
          label="Plan year starting"
          hint={`Close a year after it ends. Carry-over ${plan.carryOverExpiryMonths === 0 ? "lasts the whole year" : `expires ${plan.carryOverExpiryMonths} months into the year`}.`}
          error={error ?? undefined}
        >
          {(control) => <Input {...control} type="date" value={year} invalid={Boolean(error)} onChange={(event) => setYear(event.target.value)} />}
        </FormField>
        <div className={styles.formActions}>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          {failure ? (
            <Alert tone="danger" title={failure.title} live="alert">
              <ProblemMessage problem={failure.problem} />
            </Alert>
          ) : null}
        </div>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy === "close"} disabled={busy !== null}>
            Close plan year
          </Button>
          {plan.carryOverExpiryMonths > 0 ? (
            <Button onClick={() => void run("expire")} loading={busy === "expire"} disabled={busy !== null}>
              Expire carry-over
            </Button>
          ) : null}
        </div>
      </form>
    </Card>
  );
}

/** One absence plan: its rule, accrual runs and plan-year closes. */
export function AbsencePlanDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getAbsencePlan(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Absence plan" />
        {state.error.code === "HCM-ABS-PLAN-NOT-FOUND" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="We couldn't find this plan"
              description="It may belong to another tenant, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/hr/absence/plans">
                  Back to plans
                </Link>
              }
            />
          </Card>
        ) : (
          <Alert tone="danger" title="Could not load the plan" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        )}
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Absence plan" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  const plan = state.data;
  const unit = absenceUnitLabel[plan.unit] ?? plan.unit;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={plan.code}
        description={`${frequencyLabel[plan.accrualFrequency] ?? plan.accrualFrequency}, ${plan.accrualRate} ${unit} a period`}
        meta={plan.negativeBalanceAllowed ? <Badge tone="warning">Negative balance allowed</Badge> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/hr/absence/plans">
            Back to plans
          </Link>
        }
      />
      <Card title="Rule">
        <dl className={styles.details}>
          <Entry term="Rate per period" mono>
            {plan.accrualRate} {unit}
          </Entry>
          <Entry term="Accrual day">{plan.accrualDay === "FIRST_DAY" ? "First day" : plan.accrualDay === "PAYDATE" ? "Pay date" : "Last day"}</Entry>
          <Entry term="Proration">{plan.proration === "DAYS_IN_PERIOD" ? "By days employed" : "None"}</Entry>
          <Entry term="Max balance" mono>
            {plan.maxBalance}
          </Entry>
          <Entry term="Carry-over max" mono>
            {plan.carryOverMax}
          </Entry>
          <Entry term="Carry-over expires">{plan.carryOverExpiryMonths === 0 ? "End of the plan year" : `After ${plan.carryOverExpiryMonths} months`}</Entry>
          <Entry term="Plan year starts" mono>
            {plan.planYearStart}
          </Entry>
          <Entry term="Minimum service">{plan.minServiceDays} days</Entry>
        </dl>
      </Card>
      <AccrualCard plan={plan} onDone={state.reload} />
      <Card title="Accrual periods">
        <DataTable
          label="Accrual periods"
          columns={periodColumns}
          rows={plan.periods}
          getRowKey={(row) => row.periodStart}
          empty={{ title: "No accrual runs yet", description: "Run the first period above." }}
        />
      </Card>
      <PlanYearCard plan={plan} onDone={state.reload} />
      <Card title="Year ends and expiries">
        <DataTable
          label="Year ends and expiries"
          columns={closeColumns}
          rows={plan.closes}
          getRowKey={(row) => `${row.planYearStart}|${row.kind}`}
          empty={{ title: "No plan year closed yet", description: "Close a plan year after it ends to carry balances over." }}
        />
      </Card>
    </div>
  );
}
