import { useState, type FormEvent } from "react";
import {
  Alert,
  Badge,
  Button,
  DataTable,
  FormField,
  Input,
  PageHeader,
  StatGroup,
  StatTile,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import { getReconciliation, type Reconciliation } from "./api/client";
import { DATE, dayBefore, shortId, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const METRIC_ID = /^[a-z][a-z0-9_]{1,62}$/;

const columns: DataTableColumn<Reconciliation>[] = [
  { key: "metricId", header: "Metric", mono: true },
  {
    key: "legalEntityId",
    header: "Legal entity",
    mono: true,
    render: (r) => <span title={r.legalEntityId}>{shortId(r.legalEntityId)}</span>,
  },
  { key: "currency", header: "Currency", mono: true },
  { key: "semanticValue", header: "Semantic layer", align: "end" },
  { key: "sourceValue", header: "Ledger", align: "end" },
  { key: "delta", header: "Delta", align: "end" },
  { key: "tolerance", header: "Tolerance", align: "end" },
  { key: "status", header: "Status", render: (r) => <StatusPill status={r.status} /> },
  {
    key: "trusted",
    header: "Trust",
    render: (r) =>
      r.trusted ? <Badge tone="success">Trusted</Badge> : <span className={styles.muted}>{r.consecutiveOkDays} of 30 days</span>,
  },
];

/**
 * Daily metric reconciliation (FR-46.13, BR-46.10): semantic-layer values
 * against the ledger per metric and legal entity, with the failure banner the
 * dashboards show while any metric is outside tolerance.
 */
export function ReconciliationPage() {
  const [draft, setDraft] = useState({ date: dayBefore(today()), metric: "" });
  const [filter, setFilter] = useState(draft);
  const [errors, setErrors] = useState<{ date?: string; metric?: string }>({});
  const state = useLoad((signal) => getReconciliation(filter.date, filter.metric, undefined, signal), `${filter.date}/${filter.metric}`);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: { date?: string; metric?: string } = {};
    if (!DATE.test(draft.date)) found.date = "Choose a day.";
    const metric = draft.metric.trim();
    if (metric !== "" && !METRIC_ID.test(metric)) found.metric = "Use a metric id such as revenue or gross_margin.";
    setErrors(found);
    if (Object.keys(found).length === 0) setFilter({ date: draft.date, metric });
  }

  const day = state.data;
  const failed = day?.items.filter((r) => r.status === "FAILED") ?? [];
  const blocked = day?.items.filter((r) => r.status === "BLOCKED") ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Metric reconciliation"
        description="Each night at 02:00 the semantic layer is compared with the ledger. A metric outside its tolerance puts a banner on every dashboard until it is green again."
      />
      <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="Reconciliation filter">
        <FormField id="recon-date" label="Day" error={errors.date}>
          {(control) => (
            <Input {...control} type="date" value={draft.date} onChange={(e) => setDraft((d) => ({ ...d, date: e.target.value }))} invalid={Boolean(errors.date)} />
          )}
        </FormField>
        <FormField id="recon-metric" label="Metric" hint="Leave empty for every metric." error={errors.metric} optional>
          {(control) => (
            <Input {...control} className={styles.mono} value={draft.metric} onChange={(e) => setDraft((d) => ({ ...d, metric: e.target.value }))} invalid={Boolean(errors.metric)} />
          )}
        </FormField>
        <Button type="submit">Show results</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load reconciliation results" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <>
          {day && day.failedCount > 0 ? (
            <Alert tone="warning" title={`${day.failedCount} of ${day.items.length} results need attention on ${day.date}`}>
              {failed.map((r) => `Metric ${r.metricId} failed reconciliation on ${r.date}: delta ${r.delta} ${r.currency}.`).join(" ")}
              {blocked.length > 0 ? ` ${blocked.length} blocked by a failed upstream data test.` : ""}
            </Alert>
          ) : null}
          {day ? (
            <StatGroup label="Reconciliation summary">
              <StatTile label="Results" value={day.items.length} />
              <StatTile label="Outside tolerance" value={failed.length} tone="danger" />
              <StatTile label="Blocked" value={blocked.length} tone="warning" />
              <StatTile label="Trusted" value={day.items.filter((r) => r.trusted).length} />
            </StatGroup>
          ) : null}
          <DataTable
            label="Reconciliation results"
            columns={columns}
            rows={day?.items ?? []}
            getRowKey={(r) => `${r.metricId}/${r.legalEntityId}`}
            loading={state.loading}
            empty={{
              title: "No results for this day",
              description: "The nightly job has not recorded a reconciliation for this day yet, or no metric matches the filter.",
            }}
          />
        </>
      )}
    </div>
  );
}
