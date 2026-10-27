import { useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import {
  Alert,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Skeleton,
  type DataTableColumn,
} from "../../shell/ui";
import {
  describeProblem,
  listMetrics,
  MAX_DIMENSIONS,
  MAX_MEASURES,
  MAX_SPAN_YEARS,
  planQuery,
  type Filter,
  type Metric,
  type Plan,
  type Problem,
  type QueryInput,
} from "./api/client";
import { DATE, dimensionLabel, grainLabel, monthStart, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Partial<Record<"measures" | "dimensions" | "from" | "to", string>>;

/** Dimensions every selected measure allows (the service rejects any other). */
export function allowedDimensions(catalog: Metric[], measures: string[]): string[] {
  const selected = catalog.filter((m) => measures.includes(m.metricId));
  if (selected.length === 0) return [];
  return selected[0].allowedDimensions.filter((d) => selected.every((m) => m.allowedDimensions.includes(d)));
}

function addYears(date: string, years: number): string {
  return `${Number(date.slice(0, 4)) + years}${date.slice(4)}`;
}

/** Checks that mirror domain.ValidateShape and BuildPlan in services/analytics. */
export function validateQuery(input: QueryInput, catalog: Metric[]): Errors {
  const errors: Errors = {};
  if (input.measures.length === 0) errors.measures = "Choose at least one metric.";
  else if (input.measures.length > MAX_MEASURES) errors.measures = `Choose at most ${MAX_MEASURES} metrics.`;
  else {
    const grains = new Set(catalog.filter((m) => input.measures.includes(m.metricId)).map((m) => m.grain));
    if (grains.size > 1) errors.measures = "Choose metrics of one grain; journal-line, sales-line and resource-shift metrics cannot be combined.";
  }
  if (input.dimensions.length > MAX_DIMENSIONS) errors.dimensions = `Choose at most ${MAX_DIMENSIONS} dimensions.`;
  if (!DATE.test(input.from)) errors.from = "Choose the first day.";
  if (!DATE.test(input.to)) errors.to = "Choose the last day.";
  else if (DATE.test(input.from) && input.to < input.from) errors.to = "The last day must not be before the first day.";
  else if (DATE.test(input.from) && input.to > addYears(input.from, MAX_SPAN_YEARS))
    errors.to = `The range must not exceed ${MAX_SPAN_YEARS} years.`;
  return errors;
}

const filterColumns: DataTableColumn<Filter>[] = [
  { key: "member", header: "Member", mono: true },
  { key: "operator", header: "Operator", mono: true },
  { key: "values", header: "Values", mono: true, render: (f) => f.values.join(", ") },
];

function PlanResult({ plan }: { plan: Plan }) {
  if (plan.reason === "NO_SCOPE") {
    return (
      <Card>
        <EmptyState
          icon="lock"
          title="You have no data scope for this model"
          description="Your access grants no legal entity, so this query returns no rows. Ask your workspace administrator for legal-entity access."
        />
      </Card>
    );
  }
  return (
    <Card title="Query plan">
      <dl className={styles.details}>
        <div className={styles.entry}>
          <dt className={styles.term}>Model</dt>
          <dd className={`${styles.value} ${styles.mono}`}>{plan.model}</dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Measures</dt>
          <dd className={`${styles.value} ${styles.mono}`}>{plan.measures.join(", ")}</dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Dimensions</dt>
          <dd className={styles.value}>{plan.dimensions.length > 0 ? plan.dimensions.map(dimensionLabel).join(", ") : "None (one total)"}</dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Range</dt>
          <dd className={`${styles.value} ${styles.mono}`}>
            {plan.from} to {plan.to}
          </dd>
        </div>
      </dl>
      <DataTable
        label="Scope filters"
        columns={filterColumns}
        rows={plan.filters}
        getRowKey={(f) => f.member}
        empty={{ title: "No scope filter", description: "You hold every legal entity in this tenant, so no legal-entity filter is added." }}
      />
    </Card>
  );
}

/**
 * Self-service explorer, slice 1 (FR-46.9, FR-46.7): compose metrics by
 * dimensions over a date range and see the Cube plan the service builds,
 * including the legal-entity filter mirrored from your access. Running the
 * plan and exporting rows are later slices.
 */
export function ExplorerPage() {
  const [params] = useSearchParams();
  const preset = params.get("measure");
  const catalog = useLoad((signal) => listMetrics(undefined, signal), "metrics");
  const [input, setInput] = useState<QueryInput>(() => ({
    measures: preset ? [preset] : [],
    dimensions: [],
    from: monthStart(today()),
    to: today(),
  }));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [plan, setPlan] = useState<Plan | null>(null);
  const [busy, setBusy] = useState(false);

  const metrics = catalog.data ?? [];
  const dimensions = allowedDimensions(metrics, input.measures);

  function toggle(list: "measures" | "dimensions", value: string, on: boolean) {
    setInput((current) => {
      const next = on ? [...current[list], value] : current[list].filter((v) => v !== value);
      const updated = { ...current, [list]: next };
      // Drop dimensions the new measure set no longer allows.
      if (list === "measures") updated.dimensions = current.dimensions.filter((d) => allowedDimensions(metrics, next).includes(d));
      return updated;
    });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateQuery(input, metrics);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setPlan(await planQuery(input));
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as Errors);
      setFailure(problem);
      setPlan(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Explorer"
        description="Choose governed metrics, the dimensions to group by and a date range. The plan shows exactly what runs, including the legal-entity filter from your access."
      />
      {catalog.error ? (
        <Alert tone="danger" title="Could not load the metric catalog" live="alert" action={<Button size="sm" onClick={catalog.reload}>Try again</Button>}>
          {catalog.error.message} ({catalog.error.code})
        </Alert>
      ) : catalog.loading ? (
        <Card>
          <div className={styles.loading} aria-busy="true">
            <Skeleton width="1/2" height="control" />
            <Skeleton width="3/4" height="control" />
            <Skeleton width="1/4" height="control" />
          </div>
          <p role="status" className={styles.muted}>
            Loading governed metrics…
          </p>
        </Card>
      ) : metrics.length === 0 ? (
        <Card>
          <EmptyState icon="database" title="No published metrics yet" description="The explorer lists metrics once a version is published." />
        </Card>
      ) : (
        <Card>
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Explorer query">
            <fieldset className={styles.choices} aria-describedby={errors.measures ? "explorer-measures-error" : undefined}>
              <legend className={styles.choicesLegend}>Metrics</legend>
              {metrics.map((m) => (
                <span key={m.metricId} className={styles.check}>
                  <Input
                    id={`measure-${m.metricId}`}
                    type="checkbox"
                    checked={input.measures.includes(m.metricId)}
                    onChange={(e) => toggle("measures", m.metricId, e.target.checked)}
                  />
                  <label htmlFor={`measure-${m.metricId}`} title={m.formula}>
                    {m.name}
                  </label>
                  <span className={styles.muted}>{grainLabel(m.grain)}</span>
                </span>
              ))}
              {errors.measures ? (
                <p id="explorer-measures-error" role="alert" className={styles.fieldError}>
                  {errors.measures}
                </p>
              ) : null}
            </fieldset>
            <fieldset className={styles.choices} aria-describedby={errors.dimensions ? "explorer-dimensions-error" : undefined}>
              <legend className={styles.choicesLegend}>Group by</legend>
              {dimensions.length === 0 ? (
                <p className={styles.muted}>Choose a metric to see the dimensions it can be grouped by.</p>
              ) : (
                dimensions.map((d) => (
                  <span key={d} className={styles.check}>
                    <Input id={`dimension-${d}`} type="checkbox" checked={input.dimensions.includes(d)} onChange={(e) => toggle("dimensions", d, e.target.checked)} />
                    <label htmlFor={`dimension-${d}`}>{dimensionLabel(d)}</label>
                  </span>
                ))
              )}
              {errors.dimensions ? (
                <p id="explorer-dimensions-error" role="alert" className={styles.fieldError}>
                  {errors.dimensions}
                </p>
              ) : null}
            </fieldset>
            <FormField id="explorer-from" label="From" error={errors.from}>
              {(control) => (
                <Input {...control} type="date" value={input.from} onChange={(e) => setInput((v) => ({ ...v, from: e.target.value }))} invalid={Boolean(errors.from)} />
              )}
            </FormField>
            <FormField id="explorer-to" label="To" error={errors.to}>
              {(control) => (
                <Input {...control} type="date" value={input.to} onChange={(e) => setInput((v) => ({ ...v, to: e.target.value }))} invalid={Boolean(errors.to)} />
              )}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="The query was not accepted" live="alert">
                  {failure.message} ({failure.code})
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Build plan
              </Button>
            </div>
          </form>
        </Card>
      )}
      {plan ? <PlanResult plan={plan} /> : null}
    </div>
  );
}
