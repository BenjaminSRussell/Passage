import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { useSearchParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  createForecastEntry,
  describeProblem,
  getCashForecast,
  LAYOUTS,
  newKey,
  SOURCE_TYPES,
  type ForecastBucket,
  type ForecastEntryInput,
  type Layout,
  type Problem,
  type SourceType,
} from "./api/client";
import { CURRENCY, DAY, money, SIGNED_DECIMAL, UUID } from "./format";
import { CommandFailure, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

const LAYOUT_LABELS: Record<Layout, string> = {
  daily: "Daily, 30 days",
  weekly: "Weekly, 13 weeks",
  monthly: "Monthly, 12 months",
};

const SOURCE_NOTES: Record<string, Record<string, (n: number) => string>> = {
  AP: {
    ok: (n) => `Includes ${n} open vendor invoices from payables, on their due dates.`,
    truncated: (n) => `Includes ${n} open vendor invoices from payables, but there are more; the outflows are understated.`,
    unavailable: () => "Payables did not answer, so open vendor invoices are not included beyond the entries you added.",
  },
  AR: {
    ok: (n) => `Includes ${n} open customer invoices from receivables, on their due dates.`,
    truncated: (n) => `Includes ${n} open customer invoices from receivables, but there are more; the inflows are understated.`,
    unavailable: () => "Receivables did not answer, so open customer invoices are not included beyond the entries you added.",
  },
};

const bucketColumns: DataTableColumn<ForecastBucket>[] = [
  { key: "key", header: "Bucket starts", mono: true },
  { key: "currency", header: "Currency", mono: true },
  { key: "inflow", header: "Inflow", mono: true, align: "end", render: (row) => money(row.inflow, row.currency) },
  { key: "outflow", header: "Outflow", mono: true, align: "end", render: (row) => money(row.outflow, row.currency) },
  { key: "net", header: "Net", mono: true, align: "end", render: (row) => money(row.net, row.currency) },
  { key: "cumulative", header: "Cumulative", mono: true, align: "end", render: (row) => money(row.cumulative, row.currency) },
];

interface Filters {
  legalEntityId: string;
  layout: Layout;
  asOf: string;
  currency: string;
}

function readFilters(params: URLSearchParams): Filters {
  const layout = params.get("layout");
  return {
    legalEntityId: params.get("legalEntityId") ?? "",
    layout: (LAYOUTS as readonly string[]).includes(layout ?? "") ? (layout as Layout) : "daily",
    asOf: params.get("asOf") ?? "",
    currency: params.get("currency") ?? "",
  };
}

export function validateFilters(f: Filters): Errors {
  const errors: Errors = {};
  if (!UUID.test(f.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (f.asOf !== "" && !DAY.test(f.asOf)) errors.asOf = "Enter a date as YYYY-MM-DD.";
  if (f.currency.trim() !== "" && !CURRENCY.test(f.currency.trim())) errors.currency = "Enter an ISO 4217 code such as EUR.";
  return errors;
}

export interface EntryDraft {
  sourceType: SourceType;
  sourceId: string;
  expectedDate: string;
  amount: string;
  currency: string;
  confidence: string;
}

export function validateEntry(v: EntryDraft): Errors {
  const errors: Errors = {};
  if (!DAY.test(v.expectedDate)) errors.expectedDate = "Enter the expected date.";
  if (!SIGNED_DECIMAL.test(v.amount.trim())) errors.amount = "Enter an amount such as 1250.00; use a minus sign for an outflow.";
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter an ISO 4217 code such as EUR.";
  if (v.confidence.trim() !== "") {
    const c = v.confidence.trim();
    if (!/^(0(\.\d{1,6})?|1(\.0{1,6})?)$/.test(c)) errors.confidence = "Enter a number from 0 to 1, for example 0.8.";
  }
  if (v.sourceId.length > 64) errors.sourceId = "Use at most 64 characters.";
  return errors;
}

export function toEntryInput(legalEntityId: string, v: EntryDraft): ForecastEntryInput {
  const input: ForecastEntryInput = {
    legalEntityId,
    sourceType: v.sourceType,
    expectedDate: v.expectedDate,
    amount: v.amount.trim(),
    currency: v.currency.trim(),
  };
  if (v.sourceId.trim() !== "") input.sourceId = v.sourceId.trim();
  if (v.confidence.trim() !== "") input.confidence = v.confidence.trim();
  return input;
}

const emptyEntry: EntryDraft = { sourceType: "Manual", sourceId: "", expectedDate: "", amount: "", currency: "", confidence: "" };

/** Cash flow forecast (FR-36.9): buckets per currency, each the sum of its entries, with manual entries added here. */
export function CashForecastPage() {
  const [params, setParams] = useSearchParams();
  const applied = readFilters(params);
  const [filters, setFilters] = useState<Filters>(applied);
  const [filterErrors, setFilterErrors] = useState<Errors>({});
  const ready = Object.keys(validateFilters(applied)).length === 0;
  const forecast = useLoad(
    (signal) =>
      ready
        ? getCashForecast({ legalEntityId: applied.legalEntityId, layout: applied.layout, asOf: applied.asOf || undefined, currency: applied.currency || undefined }, signal)
        : Promise.resolve(null),
    params.toString(),
  );

  const [entry, setEntry] = useState<EntryDraft>(emptyEntry);
  const [entryErrors, setEntryErrors] = useState<Errors>({});
  const [entryFailure, setEntryFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState("");
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function apply(event: FormEvent) {
    event.preventDefault();
    const found = validateFilters(filters);
    setFilterErrors(found);
    if (Object.keys(found).length > 0) return;
    const next: Record<string, string> = { legalEntityId: filters.legalEntityId.trim(), layout: filters.layout };
    if (filters.asOf) next.asOf = filters.asOf;
    if (filters.currency.trim()) next.currency = filters.currency.trim();
    setParams(next);
  }

  async function addEntry(event: FormEvent) {
    event.preventDefault();
    const found = validateEntry(entry);
    setEntryErrors(found);
    setEntryFailure(null);
    setSaved("");
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createForecastEntry(toEntryInput(applied.legalEntityId, entry), key.current);
      key.current = newKey();
      setEntry(emptyEntry);
      setSaved(`Added ${money(created.amount, created.currency)} expected on ${created.expectedDate}.`);
      forecast.reload();
    } catch (error) {
      if (!keepKeyAfter(error)) key.current = newKey();
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setEntryErrors(problem.fields);
      else setEntryFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const data = forecast.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cash forecast"
        description="Expected inflows and outflows per currency. Entries dated before the as-of date fall into the first bucket; each bucket's net is exactly the sum of its entries."
      />
      <Card>
        <form className={styles.toolbar} onSubmit={apply} noValidate>
          <FormField id="cf-entity" label="Legal entity" error={filterErrors.legalEntityId}>
            {(c) => (
              <EntityLookup entity="legalEntity"
                {...c}
                value={filters.legalEntityId}
                onChange={(e) => setFilters((p) => ({ ...p, legalEntityId: e.target.value }))}
                invalid={Boolean(filterErrors.legalEntityId)}
              />
            )}
          </FormField>
          <FormField id="cf-layout" label="Buckets">
            {(c) => (
              <Select {...c} value={filters.layout} onChange={(e) => setFilters((p) => ({ ...p, layout: e.target.value as Layout }))}>
                {LAYOUTS.map((l) => (
                  <option key={l} value={l}>
                    {LAYOUT_LABELS[l]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="cf-asof" label="As of" optional error={filterErrors.asOf}>
            {(c) => <Input {...c} type="date" value={filters.asOf} onChange={(e) => setFilters((p) => ({ ...p, asOf: e.target.value }))} invalid={Boolean(filterErrors.asOf)} />}
          </FormField>
          <FormField id="cf-currency" label="Currency" optional error={filterErrors.currency}>
            {(c) => (
              <Input
                {...c}
                className={styles.mono}
                value={filters.currency}
                onChange={(e) => setFilters((p) => ({ ...p, currency: e.target.value }))}
                invalid={Boolean(filterErrors.currency)}
              />
            )}
          </FormField>
          <Button type="submit">Show forecast</Button>
        </form>
      </Card>
      {!ready ? (
        <Card>
          <EmptyState icon="calendarCheck" title="Choose a legal entity" description="The forecast is computed per legal entity. Enter its id to see the buckets." />
        </Card>
      ) : (
        <>
          {forecast.error ? (
            <LoadFailure title="Could not load the cash forecast" problem={forecast.error} retry={forecast.reload} />
          ) : (
            <Card title={data ? `Buckets from ${data.asOf}` : "Buckets"} headingLevel={3}>
              <div className={styles.stack}>
                <DataTable
                  label="Forecast buckets"
                  columns={bucketColumns}
                  rows={data?.buckets ?? []}
                  getRowKey={(row) => `${row.key}-${row.currency}`}
                  loading={forecast.loading}
                  empty={{ title: "No expected cash flows", description: "Add a forecast entry below, or widen the horizon." }}
                />
                {data?.sources?.map((s) => (
                  <p key={s.type} className={styles.note} role={s.status === "ok" ? undefined : "status"}>
                    {SOURCE_NOTES[s.type]?.[s.status]?.(s.count) ?? `${s.type}: ${s.status}`}
                  </p>
                ))}
                {data && data.outOfHorizon > 0 ? (
                  <p className={styles.note} role="status">
                    {`${data.outOfHorizon} entries fall after the last bucket and are not shown.`}
                  </p>
                ) : null}
              </div>
            </Card>
          )}
          <Card title="Add forecast entry" headingLevel={3}>
            <form className={styles.form} onSubmit={addEntry} noValidate>
              <FormField id="fe-source" label="Source">
                {(c) => (
                  <Select {...c} value={entry.sourceType} onChange={(e) => setEntry((p) => ({ ...p, sourceType: e.target.value as SourceType }))}>
                    {SOURCE_TYPES.map((t) => (
                      <option key={t} value={t}>
                        {t}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="fe-date" label="Expected date" error={entryErrors.expectedDate}>
                {(c) => <Input {...c} type="date" value={entry.expectedDate} onChange={(e) => setEntry((p) => ({ ...p, expectedDate: e.target.value }))} invalid={Boolean(entryErrors.expectedDate)} />}
              </FormField>
              <FormField id="fe-amount" label="Amount" hint="Positive for an inflow, negative for an outflow." error={entryErrors.amount}>
                {(c) => (
                  <Input
                    {...c}
                    className={styles.mono}
                    inputMode="decimal"
                    value={entry.amount}
                    onChange={(e) => setEntry((p) => ({ ...p, amount: e.target.value }))}
                    invalid={Boolean(entryErrors.amount)}
                  />
                )}
              </FormField>
              <FormField id="fe-currency" label="Entry currency" error={entryErrors.currency}>
                {(c) => <Input {...c} className={styles.mono} value={entry.currency} onChange={(e) => setEntry((p) => ({ ...p, currency: e.target.value }))} invalid={Boolean(entryErrors.currency)} />}
              </FormField>
              <FormField id="fe-confidence" label="Confidence" optional hint="0 to 1; defaults to 1." error={entryErrors.confidence}>
                {(c) => <Input {...c} className={styles.mono} value={entry.confidence} onChange={(e) => setEntry((p) => ({ ...p, confidence: e.target.value }))} invalid={Boolean(entryErrors.confidence)} />}
              </FormField>
              <FormField id="fe-sourceid" label="Source reference" optional error={entryErrors.sourceId}>
                {(c) => <Input {...c} className={styles.mono} value={entry.sourceId} onChange={(e) => setEntry((p) => ({ ...p, sourceId: e.target.value }))} invalid={Boolean(entryErrors.sourceId)} />}
              </FormField>
              {entryFailure ? (
                <div className={styles.formActions}>
                  <CommandFailure title="Could not add the forecast entry" problem={entryFailure} />
                </div>
              ) : null}
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy}>
                  Add entry
                </Button>
              </div>
            </form>
          </Card>
          <Alert tone="success" live="status">
            {saved}
          </Alert>
        </>
      )}
    </div>
  );
}
