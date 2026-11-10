import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Skeleton,
  StatGroup,
  StatTile,
  type DataTableColumn,
} from "../../shell/ui";
import {
  describeProblem,
  getBenefitPlan,
  simulateBenefitPlan,
  type BenefitOption,
  type BenefitPlan,
  type BenefitSimulation,
  type BenefitSimulationRow,
  type Problem,
} from "./api/client";
import { dash, DATE, employmentTypeLabel, formatRate, planTypeLabel, rateBasisLabel, todayIso } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

function Entry({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

const simulationColumns: DataTableColumn<BenefitSimulationRow>[] = [
  { key: "personnelNumber", header: "Worker", mono: true, render: (row) => <Link to={`/hr/workers/${row.workerId}`}>{row.personnelNumber}</Link> },
  { key: "legalName", header: "Name" },
  { key: "employmentType", header: "Employment", render: (row) => (row.employmentType ? employmentTypeLabel[row.employmentType] ?? row.employmentType : dash) },
  { key: "serviceDays", header: "Service days", mono: true, align: "end", render: (row) => row.serviceDays ?? dash },
  { key: "fte", header: "FTE", mono: true, align: "end", render: (row) => row.fte ?? dash },
  { key: "eligible", header: "Eligible", render: (row) => (row.eligible ? <Badge tone="success">Eligible</Badge> : <Badge tone="neutral">Not eligible</Badge>) },
  { key: "reason", header: "Why" },
];

/** Who is eligible on a date (BR-42.6, AC-42.5): the same waiting-period and rule check enrollment runs, over the whole workforce. */
function SimulationCard({ plan }: { plan: BenefitPlan }) {
  const [asOf, setAsOf] = useState(DATE.test(plan.enrollmentStart) && plan.enrollmentStart > todayIso() ? plan.enrollmentStart : todayIso());
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<BenefitSimulation | null>(null);
  const [eligibleOnly, setEligibleOnly] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (!DATE.test(asOf)) {
      setError("Enter the date to evaluate the rule on.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      setResult(await simulateBenefitPlan(plan.id, asOf));
    } catch (failed) {
      setFailure(describeProblem(failed));
    } finally {
      setBusy(false);
    }
  }

  const rows = result ? (eligibleOnly ? result.rows.filter((r) => r.eligible) : result.rows) : [];
  return (
    <Card title="Who is eligible">
      <div className={styles.stack}>
        <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="Simulate eligibility">
          <FormField id="simulate-asOf" label="As of" hint="Nothing is saved. Workers with a primary employment on this date are checked." error={error ?? undefined}>
            {(control) => <Input {...control} type="date" value={asOf} invalid={Boolean(error)} onChange={(event) => setAsOf(event.target.value)} />}
          </FormField>
          <Button type="submit" variant="primary" loading={busy}>
            Simulate
          </Button>
        </form>
        {failure ? (
          <Alert tone="danger" title="Could not simulate eligibility" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        {result ? (
          <>
            <StatGroup label={`Eligibility on ${result.asOf}`}>
              <StatTile label="Workers checked" value={result.total} />
              <StatTile label="Eligible" value={result.eligible} />
              <StatTile label="Not eligible" value={result.total - result.eligible} />
            </StatGroup>
            <div className={styles.check}>
              <Input id="simulate-eligibleOnly" type="checkbox" checked={eligibleOnly} onChange={(event) => setEligibleOnly(event.target.checked)} />
              <label htmlFor="simulate-eligibleOnly">Show eligible workers only</label>
            </div>
            <DataTable
              label={`Eligibility on ${result.asOf}`}
              columns={simulationColumns}
              rows={rows}
              getRowKey={(row) => row.workerId}
              empty={{ title: "No workers to show", description: "Nobody has a primary employment on this date, or no one is eligible." }}
            />
          </>
        ) : (
          <p className={styles.note}>Run the simulation to see each worker's result and the reason.</p>
        )}
      </div>
    </Card>
  );
}

/** One benefit plan: its terms, coverage options and an eligibility simulation. */
export function BenefitPlanDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getBenefitPlan(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Benefit plan" />
        {state.error.code === "HCM-BEN-PLAN-NOT-FOUND" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="We couldn't find this plan"
              description="It may belong to another tenant, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/hr/benefits/plans">
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
        <PageHeader title="Benefit plan" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  const plan = state.data;
  const optionColumns: DataTableColumn<BenefitOption>[] = [
    { key: "code", header: "Option", mono: true },
    { key: "coverageLevel", header: "Coverage" },
    { key: "employeeRate", header: "Employee pays", mono: true, align: "end", render: (row) => formatRate(row.employeeRate, plan.currency) },
    { key: "employerRate", header: "Employer pays", mono: true, align: "end", render: (row) => formatRate(row.employerRate, plan.currency) },
    { key: "rateBasis", header: "Per", render: (row) => rateBasisLabel[row.rateBasis] ?? row.rateBasis },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={plan.code}
        description={`${planTypeLabel[plan.planType] ?? plan.planType}, open enrollment ${plan.enrollmentStart} to ${plan.enrollmentEnd}`}
        meta={plan.preTax ? <Badge tone="info">Pre-tax</Badge> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/hr/benefits/plans">
            Back to plans
          </Link>
        }
      />
      <Card title="Terms">
        <dl className={styles.details}>
          <Entry term="Eligibility rule" mono>
            {plan.eligibilityRule}
          </Entry>
          <Entry term="Waiting period">{plan.waitingDays === 0 ? "None" : `${plan.waitingDays} days from hire`}</Entry>
          <Entry term="Currency" mono>
            {plan.currency}
          </Entry>
          <Entry term="Payroll deduction code" mono>
            {plan.payrollDeductionCode ?? dash}
          </Entry>
          <Entry term="Provider party" mono>
            {plan.providerPartyId ?? dash}
          </Entry>
          <Entry term="Life events">Open a 30-day enrollment window</Entry>
        </dl>
      </Card>
      <Card title="Coverage options">
        <DataTable
          label="Coverage options"
          columns={optionColumns}
          rows={plan.options}
          getRowKey={(row) => row.code}
          empty={{ title: "No options", description: "A plan always has at least one option." }}
        />
      </Card>
      <SimulationCard plan={plan} />
    </div>
  );
}
