import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getPromotion, recordPromotionActuals, type Problem, type Promotion, type PromotionActuals } from "./api/client";
import { DECIMAL } from "./format";
import { describeScope, signedPct } from "./fundFormat";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

export interface ActualsDraft {
  units: string;
  weeks: string;
  spend: string;
}

/** Client checks that mirror the service (FR-26.20); the service has the last word. */
export function validateActuals(v: ActualsDraft): Errors {
  const errors: Errors = {};
  if (!DECIMAL.test(v.units.trim())) errors.units = "Enter the units invoiced in the window.";
  if (!/^\d+$/.test(v.weeks.trim()) || Number(v.weeks) < 1 || Number(v.weeks) > 260) errors.weeks = "Enter the number of weeks, from 1 to 260.";
  if (v.spend.trim() !== "" && !DECIMAL.test(v.spend.trim())) errors.spend = "Enter the spend as a number.";
  return errors;
}

export function toActuals(v: ActualsDraft): PromotionActuals {
  return { units: v.units.trim(), weeks: Number(v.weeks), spend: v.spend.trim() === "" ? "0" : v.spend.trim() };
}

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

interface Week {
  week: string;
  units: string;
}

const weekColumns: DataTableColumn<Week>[] = [
  { key: "week", header: "Week before the promotion", mono: true },
  { key: "units", header: "Units", mono: true, align: "end" },
];

/** One promotion plan: baseline, forecast, planned spend, and actuals with their effectiveness (FR-26.20). */
export function PromotionDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getPromotion(id, signal), id);
  const [updated, setUpdated] = useState<Promotion | null>(null);
  const [draft, setDraft] = useState<ActualsDraft>({ units: "", weeks: "", spend: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState(false);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Promotion" />
        <Alert tone="danger" title="Could not load the promotion" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  const plan = updated && updated.id === id ? updated : state.data;
  if (!plan) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Promotion" />
        <span className="visually-hidden">Loading the promotion…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  async function record(event: FormEvent, p: Promotion) {
    event.preventDefault();
    const found = validateActuals(draft);
    setErrors(found);
    setFailure(null);
    setDone("");
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const next = await recordPromotionActuals(p.id, toActuals(draft));
      setUpdated(next);
      setDone(next.effectivenessPct ? `Actuals recorded: ${signedPct(next.effectivenessPct)} against the baseline.` : "Actuals recorded. Effectiveness needs a baseline above zero.");
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const weeks: Week[] = plan.baselineWeeklyUnits.map((units, i) => ({ week: `-${plan.baselineWeeklyUnits.length - i}`, units }));
  const event = [plan.eventCategory, plan.eventType].filter(Boolean).join(" / ");
  return (
    <div className={styles.stack}>
      <PageHeader
        title={plan.name}
        description={`${plan.productId} · ${plan.startDate} – ${plan.endDate}`}
        actions={
          <div className={styles.actions}>
            {plan.fundId ? (
              <Link className={buttonClassName("secondary")} to={`/pricing/funds/${plan.fundId}`}>
                Fund
              </Link>
            ) : null}
            {plan.campaignId ? (
              <Link className={buttonClassName("secondary")} to={`/pricing/campaigns/${plan.campaignId}`}>
                Campaign
              </Link>
            ) : null}
            <Link className={buttonClassName("ghost")} to="/pricing/promotions">
              All promotions
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="Could not record the actuals" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <Card title="Plan">
        <dl className={styles.details}>
          <Detail term="Merchandising event">{event || "Not classified"}</Detail>
          <Detail term="Customers">{describeScope(plan.customerScope)}</Detail>
          <Detail term="Baseline per week" mono>
            {plan.baseline}
          </Detail>
          <Detail term="Forecast lift" mono>
            {signedPct(plan.liftPct)}
          </Detail>
          <Detail term="Forecast units" mono>
            {plan.forecastUnits}
          </Detail>
          <Detail term="Planned spend" mono>{`${plan.plannedSpend} ${plan.currency}`}</Detail>
          <Detail term="Fund" mono>
            {plan.fundId || "None"}
          </Detail>
          <Detail term="Campaign" mono>
            {plan.campaignId || "None"}
          </Detail>
          <Detail term="Legal entity" mono>
            {plan.legalEntityId}
          </Detail>
        </dl>
      </Card>
      <Card title="Actuals">
        {plan.actuals ? (
          <dl className={styles.details}>
            <Detail term="Units" mono>
              {plan.actuals.units}
            </Detail>
            <Detail term="Weeks" mono>
              {String(plan.actuals.weeks)}
            </Detail>
            <Detail term="Spend" mono>{`${plan.actuals.spend} ${plan.currency}`}</Detail>
            <Detail term="Effectiveness" mono>
              {plan.effectivenessPct ? signedPct(plan.effectivenessPct) : "Needs a baseline above zero"}
            </Detail>
          </dl>
        ) : (
          <p className={styles.note}>No actuals yet. The nightly job will fill them from posted invoices; until then record them below.</p>
        )}
      </Card>
      <Card title="Record actuals">
        <form className={styles.form} onSubmit={(e) => record(e, plan)} noValidate>
          <FormField id="pa-units" label="Units invoiced" error={errors.units}>
            {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={draft.units} onChange={(e) => setDraft({ ...draft, units: e.target.value })} invalid={Boolean(errors.units)} />}
          </FormField>
          <FormField id="pa-weeks" label="Weeks" hint="Weeks of the window the units cover." error={errors.weeks}>
            {(c) => <Input {...c} inputMode="numeric" className={styles.mono} value={draft.weeks} onChange={(e) => setDraft({ ...draft, weeks: e.target.value })} invalid={Boolean(errors.weeks)} />}
          </FormField>
          <FormField id="pa-spend" label={`Spend (${plan.currency})`} optional error={errors.spend}>
            {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={draft.spend} onChange={(e) => setDraft({ ...draft, spend: e.target.value })} invalid={Boolean(errors.spend)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record actuals
            </Button>
            <span className={styles.note}>Recording again replaces the previous actuals.</span>
          </div>
        </form>
      </Card>
      <Card title="Baseline weeks">
        <DataTable label="Baseline weeks" columns={weekColumns} rows={weeks} getRowKey={(w) => w.week} />
      </Card>
    </div>
  );
}
