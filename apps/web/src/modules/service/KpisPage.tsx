import { useState, type FormEvent } from "react";
import { Alert, Button, Card, EmptyState, FormField, Input, PageHeader, Skeleton, StatGroup, StatTile } from "../../shell/ui";
import { getKpis, type Decimal, type KpiQuery } from "./api/client";
import { DATE, dash, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** The service accepts windows of at most 366 days (api/lifecycle.go maxKPIWindow). */
const MAX_DAYS = 366;
const DAY_MS = 24 * 60 * 60 * 1000;

export interface KpiValues {
  from: string;
  to: string;
  territory: string;
}

type Errors = Partial<Record<keyof KpiValues, string>>;

/**
 * Checks that mirror the service (api/lifecycle.go kpis). The form takes whole
 * UTC days with an inclusive end day; the request's `to` is the next midnight.
 */
export function validateKpis(values: KpiValues): { errors: Errors; query?: KpiQuery } {
  const errors: Errors = {};
  if (!DATE.test(values.from) || Number.isNaN(Date.parse(`${values.from}T00:00:00Z`))) errors.from = "Enter a date as YYYY-MM-DD.";
  if (!DATE.test(values.to) || Number.isNaN(Date.parse(`${values.to}T00:00:00Z`))) errors.to = "Enter a date as YYYY-MM-DD.";
  const territory = values.territory.trim();
  if (territory !== "" && !UUID.test(territory)) errors.territory = "Enter a territory UUID or leave it empty.";
  if (!errors.from && !errors.to) {
    const from = Date.parse(`${values.from}T00:00:00Z`);
    const end = Date.parse(`${values.to}T00:00:00Z`) + DAY_MS;
    if (end <= from) errors.to = "The last day must be on or after the first day.";
    else if (end - from > MAX_DAYS * DAY_MS) errors.to = `The window can be at most ${MAX_DAYS} days.`;
  }
  if (Object.keys(errors).length > 0) return { errors };
  const end = new Date(Date.parse(`${values.to}T00:00:00Z`) + DAY_MS).toISOString().replace(".000Z", "Z");
  return { errors, query: { from: `${values.from}T00:00:00Z`, to: end, territory: territory || undefined } };
}

/** The last 30 days up to and including today (UTC). */
export function defaultKpiValues(now: Date = new Date()): KpiValues {
  return { from: today(new Date(now.getTime() - 29 * DAY_MS)), to: today(now), territory: "" };
}

const pct = (value: Decimal | null) => (value === null ? dash : `${value} %`);
const minutes = (value: Decimal | null) => (value === null ? dash : `${value} min`);

/** Service KPI dashboard (PRD 8.45.6 "KPI dashboard", 8.45.14 "SLA attainment" and "First-time fix", FR-45.11). */
export function KpisPage() {
  const [values, setValues] = useState<KpiValues>(() => defaultKpiValues());
  const [errors, setErrors] = useState<Errors>({});
  const [query, setQuery] = useState<KpiQuery>(() => validateKpis(defaultKpiValues()).query as KpiQuery);
  const state = useLoad((signal) => getKpis(query, signal), `${query.from}|${query.to}|${query.territory ?? ""}`);

  function submit(event: FormEvent) {
    event.preventDefault();
    const result = validateKpis(values);
    setErrors(result.errors);
    if (result.query) setQuery(result.query);
  }

  const k = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Service KPIs"
        description="SLA attainment and mean times for cases opened in the window, and the first-time-fix rate of work orders completed in it."
      />
      <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="KPI window">
        <FormField id="kpi-from" label="First day" hint="UTC" error={errors.from}>
          {(control) => <Input {...control} type="date" value={values.from} onChange={(e) => setValues({ ...values, from: e.target.value })} invalid={Boolean(errors.from)} />}
        </FormField>
        <FormField id="kpi-to" label="Last day" hint="Included" error={errors.to}>
          {(control) => <Input {...control} type="date" value={values.to} onChange={(e) => setValues({ ...values, to: e.target.value })} invalid={Boolean(errors.to)} />}
        </FormField>
        <FormField id="kpi-territory" label="Territory" error={errors.territory} optional>
          {(control) => (
            <Input {...control} className={styles.mono} value={values.territory} onChange={(e) => setValues({ ...values, territory: e.target.value })} invalid={Boolean(errors.territory)} />
          )}
        </FormField>
        <Button type="submit" variant="primary" loading={state.loading}>
          Show KPIs
        </Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load the service KPIs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : !k ? (
        <div className={styles.loading} aria-busy="true">
          <Skeleton height="tile" />
          <Skeleton height="tile" />
        </div>
      ) : k.casesOpened === 0 && k.workOrdersCompleted === 0 ? (
        <EmptyState title="Nothing measured in this window" description="No case was opened and no work order was completed between these days. Widen the window or clear the territory." />
      ) : (
        <>
          <Card title="SLA attainment" headingLevel={3}>
            <StatGroup label="SLA attainment">
              <StatTile label="Cases opened" value={k.casesOpened} />
              <StatTile label="Response attainment" value={pct(k.responseAttainmentPct)} hint={`${k.responseMet} met, ${k.responseMissed} missed`} />
              <StatTile label="Resolution attainment" value={pct(k.resolutionAttainmentPct)} hint={`${k.resolutionMet} met, ${k.resolutionBreached} breached`} />
              <StatTile label="Resolutions breached" value={k.resolutionBreached} tone="danger" />
              <StatTile label="Mean time to respond" value={minutes(k.meanResponseMinutes)} />
              <StatTile label="Mean time to resolve" value={minutes(k.meanResolutionMinutes)} />
            </StatGroup>
            <p className={styles.note}>Open cases still within target are not counted yet. A breached clock stays a miss even when the case is resolved later. Means are wall-clock minutes.</p>
          </Card>
          <Card title="First-time fix" headingLevel={3}>
            <StatGroup label="First-time fix">
              <StatTile label="Work orders completed" value={k.workOrdersCompleted} />
              <StatTile label="Fixed on the first visit" value={k.firstTimeFix} />
              <StatTile label="First-time-fix rate" value={pct(k.firstTimeFixPct)} />
            </StatGroup>
            <p className={styles.note}>Follow-up visits are not counted as jobs of their own; opening one marks the original order as not fixed.</p>
          </Card>
        </>
      )}
    </div>
  );
}
