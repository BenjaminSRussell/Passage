import { useEffect, useRef, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import {
  describeProblem,
  exportPeriods,
  getFlow,
  listFlows,
  listPeriods,
  newKey,
  openPeriod,
  previewBackflush,
  settlePeriod,
  type BackflushPreview,
  type ConversionLine,
  type CostingPeriod,
  type Problem,
} from "./api/client";
import { AMOUNT, CURRENCY, dash, formatDateTime, variance } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

function amount(value: string | null, currency: string): string {
  return value === null ? dash : `${value} ${currency}`;
}

const columns: DataTableColumn<CostingPeriod>[] = [
  { key: "periodStart", header: "Period", render: (p) => `${p.periodStart} to ${p.periodEnd}` },
  { key: "status", header: "Status", render: (p) => <StatusPill status={p.status} /> },
  { key: "materialCost", header: "Material", align: "end", render: (p) => amount(p.materialCost, p.currency) },
  { key: "conversionCost", header: "Conversion", align: "end", render: (p) => amount(p.conversionCost, p.currency) },
  { key: "outputValue", header: "Output", align: "end", render: (p) => amount(p.outputValue, p.currency) },
  { key: "variance", header: "Variance", align: "end", render: (p) => amount(p.variance, p.currency) },
  { key: "kanbans", header: "Cards", align: "end", render: (p) => p.kanbans ?? dash },
];

type OpenErrors = Partial<Record<"periodStart" | "periodEnd" | "currency", string>>;

/** Checks that mirror the service before a period is opened. */
export function validatePeriod(values: { periodStart: string; periodEnd: string; currency: string }): OpenErrors {
  const errors: OpenErrors = {};
  const date = /^\d{4}-\d{2}-\d{2}$/;
  if (!date.test(values.periodStart)) errors.periodStart = "Enter the first day of the period.";
  if (!date.test(values.periodEnd)) errors.periodEnd = "Enter the last day of the period.";
  else if (date.test(values.periodStart) && values.periodEnd < values.periodStart) errors.periodEnd = "The last day cannot be before the first day.";
  if (!CURRENCY.test(values.currency.trim())) errors.currency = "Enter a three-letter currency code, such as USD.";
  return errors;
}

function OpenPeriodForm({ flowId, onOpened }: { flowId: string; onOpened(p: CostingPeriod): void }) {
  const [values, setValues] = useState({ periodStart: "", periodEnd: "", currency: "USD" });
  const [errors, setErrors] = useState<OpenErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<typeof values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validatePeriod(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const period = await openPeriod(flowId, { ...values, currency: values.currency.trim() }, key.current);
      key.current = newKey();
      setValues({ periodStart: "", periodEnd: "", currency: values.currency });
      onOpened(period);
    } catch (error) {
      setFailure(describeProblem(error));
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Open a period">
      <form className={styles.stack} onSubmit={submit} noValidate>
        <div className={styles.form}>
          <FormField id="period-start" label="First day" error={errors.periodStart}>
            {(c) => <Input {...c} type="date" value={values.periodStart} onChange={(e) => update({ periodStart: e.target.value })} invalid={Boolean(errors.periodStart)} />}
          </FormField>
          <FormField id="period-end" label="Last day" error={errors.periodEnd}>
            {(c) => <Input {...c} type="date" value={values.periodEnd} onChange={(e) => update({ periodEnd: e.target.value })} invalid={Boolean(errors.periodEnd)} />}
          </FormField>
          <FormField id="period-currency" label="Currency" error={errors.currency}>
            {(c) => <Input {...c} maxLength={3} value={values.currency} onChange={(e) => update({ currency: e.target.value.toUpperCase() })} invalid={Boolean(errors.currency)} />}
          </FormField>
        </div>
        {failure ? (
          <Alert tone="danger" title="Could not open the period" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Open period
          </Button>
        </div>
      </form>
    </Card>
  );
}

type SettleField = "materialCost" | "conversionCost" | "outputValue";
const SETTLE_FIELDS: { field: SettleField; label: string }[] = [
  { field: "materialCost", label: "Material cost" },
  { field: "conversionCost", label: "Conversion cost" },
  { field: "outputValue", label: "Output value" },
];

function SettleForm({
  flowId,
  period,
  conversion,
  onSettled,
}: {
  flowId: string;
  period: CostingPeriod;
  /** A backflushed conversion cost carried into the form. */
  conversion?: string;
  onSettled(p: CostingPeriod): void;
}) {
  const [values, setValues] = useState<Record<SettleField, string>>({ materialCost: "", conversionCost: "", outputValue: "" });
  const [errors, setErrors] = useState<Partial<Record<SettleField, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const preview = variance(values.materialCost, values.conversionCost, values.outputValue);

  useEffect(() => {
    if (conversion === undefined) return;
    setValues((v) => ({ ...v, conversionCost: conversion }));
    key.current = newKey();
  }, [conversion]);

  function update(field: SettleField, value: string) {
    setValues({ ...values, [field]: value.trim() });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Partial<Record<SettleField, string>> = {};
    for (const { field } of SETTLE_FIELDS) {
      if (!AMOUNT.test(values[field])) found[field] = `Enter an amount of zero or more in ${period.currency}.`;
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      onSettled(await settlePeriod(flowId, period.id, { version: period.version, ...values }, key.current));
    } catch (error) {
      setFailure(describeProblem(error));
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const failureTitle =
    failure?.code === "LEAN-0003"
      ? "Cards with jobs in this period are still open"
      : failure?.code === "LEAN-0112"
        ? "The period has not ended yet"
        : "Could not settle the period";

  return (
    <Card title={`Settle ${period.periodStart} to ${period.periodEnd}`}>
      <form className={styles.stack} onSubmit={submit} noValidate>
        <p className={styles.note}>
          The period settles once its last day has passed and every card with jobs in it is completed or emptied. The variance is material plus conversion minus
          output.
        </p>
        <div className={styles.form}>
          {SETTLE_FIELDS.map(({ field, label }) => (
            <FormField key={field} id={`settle-${field}`} label={label} hint={period.currency} error={errors[field]}>
              {(c) => <Input {...c} inputMode="decimal" value={values[field]} onChange={(e) => update(field, e.target.value)} invalid={Boolean(errors[field])} />}
            </FormField>
          ))}
        </div>
        <p className={styles.muted}>{`Variance ${preview === null ? dash : `${preview} ${period.currency}`}`}</p>
        {failure ? (
          <Alert tone="danger" title={failureTitle} live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Settle period
          </Button>
        </div>
      </form>
    </Card>
  );
}

function lineCost(l: ConversionLine, currency: string): string {
  if (l.cost === null) return "From the purchase agreement";
  return `${l.cost} ${currency}`;
}

const conversionColumns = (currency: string): DataTableColumn<ConversionLine>[] => [
  { key: "name", header: "Activity", render: (l) => `${l.sequence}. ${l.name}` },
  { key: "kind", header: "Kind" },
  { key: "jobs", header: "Jobs", align: "end" },
  { key: "processedQty", header: "Quantity", align: "end" },
  { key: "hours", header: "Hours", align: "end" },
  { key: "ratePerHour", header: "Rate per hour", align: "end", render: (l) => (l.ratePerHour === null ? dash : `${l.ratePerHour} ${currency}`) },
  { key: "cost", header: "Conversion", align: "end", render: (l) => lineCost(l, currency) },
];

/**
 * Backflush preview (FR-24.12): the conversion cost of a period from the jobs
 * completed in it, each activity's cycle time and an hourly rate per activity.
 * Nothing is stored; an open period can carry the total into its settlement.
 */
function BackflushCard({ flowId, period, onUse }: { flowId: string; period: CostingPeriod; onUse?(conversion: string): void }) {
  const flow = useLoad((signal) => getFlow(flowId, signal), `flow|${flowId}`);
  const [rates, setRates] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<BackflushPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const rated = (flow.data?.activities ?? []).filter((a) => a.kind !== "service");

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    for (const a of rated) {
      const v = (rates[a.id] ?? "").trim();
      if (v !== "" && !AMOUNT.test(v)) found[a.id] = `Enter a rate of zero or more in ${period.currency}, with at most six decimals.`;
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const given = rated.filter((a) => (rates[a.id] ?? "").trim() !== "").map((a) => ({ activityId: a.id, ratePerHour: rates[a.id].trim() }));
      setResult(await previewBackflush(flowId, period.id, given));
    } catch (error) {
      setResult(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title={`Backflush conversion ${period.periodStart} to ${period.periodEnd}`}>
      <div className={styles.stack}>
        <p className={styles.note}>
          Conversion cost is each activity&apos;s cycle time per unit times the quantity its completed jobs processed in the period, at the activity&apos;s hourly
          rate. Every activity with completed jobs needs a rate. Service activities are priced by their purchase agreement.
        </p>
        {flow.error ? (
          <Alert tone="danger" title="Could not load the flow's activities" live="alert" action={<Button size="sm" onClick={flow.reload}>Try again</Button>}>
            {flow.error.message} ({flow.error.code})
          </Alert>
        ) : !flow.data ? (
          <div aria-busy="true">
            <span className="visually-hidden">Loading the activities…</span>
            <Skeleton height="control" />
          </div>
        ) : rated.length === 0 ? (
          <EmptyState title="No activities to rate" description="This flow has no process or transfer activities." />
        ) : (
          <form className={styles.stack} onSubmit={submit} noValidate>
            <div className={styles.form}>
              {rated.map((a) => (
                <FormField key={a.id} id={`rate-${a.id}`} label={`${a.sequence}. ${a.name}`} hint={`${period.currency} per hour · cycle ${a.cycleTimeS} s`} error={errors[a.id]}>
                  {(c) => (
                    <Input {...c} inputMode="decimal" value={rates[a.id] ?? ""} onChange={(e) => setRates({ ...rates, [a.id]: e.target.value })} invalid={Boolean(errors[a.id])} />
                  )}
                </FormField>
              ))}
            </div>
            <div className={styles.formActions}>
              <Button type="submit" variant="secondary" loading={busy}>
                Preview backflush
              </Button>
            </div>
          </form>
        )}
        {failure ? (
          <Alert tone="danger" title="Could not preview the backflush" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        {result ? (
          <>
            <DataTable
              label="Conversion by activity"
              columns={conversionColumns(result.currency)}
              rows={result.activities}
              getRowKey={(l) => l.activityId}
              empty={{ title: "No activities", description: "The flow has no activities." }}
            />
            <div className={styles.toolbar}>
              <p className={styles.muted} role="status">{`Conversion cost ${result.conversionCost} ${result.currency}`}</p>
              {onUse ? (
                <Button variant="secondary" onClick={() => onUse(result.conversionCost)}>
                  Use as conversion cost
                </Button>
              ) : null}
            </div>
          </>
        ) : null}
      </div>
    </Card>
  );
}

/** Fetches the flow costing period report as CSV and offers it as a download. */
function ExportPeriods({ flowId }: { flowId: string }) {
  const [csv, setCsv] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function prepare() {
    setBusy(true);
    setFailure(null);
    try {
      setCsv(await exportPeriods(flowId));
    } catch (error) {
      setCsv(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <div className={styles.toolbar}>
        <Button variant="secondary" loading={busy} onClick={prepare}>
          Export periods
        </Button>
        {csv !== null ? (
          <a className={buttonClassName("primary")} href={`data:text/csv;charset=utf-8,${encodeURIComponent(csv)}`} download="flow-costing-periods.csv">
            Download CSV
          </a>
        ) : null}
      </div>
      {failure ? (
        <Alert tone="danger" title="Could not export the periods" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
    </div>
  );
}

function SettledFacts({ period }: { period: CostingPeriod }) {
  return (
    <Card title={`Settled ${period.periodStart} to ${period.periodEnd}`}>
      <div className={styles.stack}>
        <StatGroup label="Settlement">
          <StatTile label="Material" value={amount(period.materialCost, period.currency)} />
          <StatTile label="Conversion" value={amount(period.conversionCost, period.currency)} />
          <StatTile label="Output" value={amount(period.outputValue, period.currency)} />
          <StatTile label="Variance" value={amount(period.variance, period.currency)} />
          <StatTile label="Cards" value={period.kanbans ?? dash} />
        </StatGroup>
        <p className={styles.muted}>
          {`Settled ${formatDateTime(period.settledAt)} by ${period.settledBy ?? dash} · journal entry ${period.journalEntryId ?? "pending the ledger integration"}`}
        </p>
      </div>
    </Card>
  );
}

/** Flow costing (FR-24.12, BR-24.9): a flow's periods, opening one and settling it. */
export function CostingPage() {
  const [params, setParams] = useSearchParams();
  const flowId = params.get("flowId") ?? "";
  const flows = useLoad((signal) => listFlows("", signal), "flows");
  const periods = useLoad((signal) => (flowId ? listPeriods(flowId, signal) : Promise.resolve([])), `periods|${flowId}`);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [carried, setCarried] = useState<{ periodId: string; conversion: string } | null>(null);
  const selected = periods.data?.find((p) => p.id === selectedId) ?? null;

  function chooseFlow(id: string) {
    setSelectedId(null);
    setNotice(null);
    setCarried(null);
    setParams(id ? { flowId: id } : {});
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Flow costing"
        description="Periodic backflush costing of a production flow. Open a period, preview its conversion cost by activity, then settle it after its last day with material, conversion and output amounts."
      />
      <div className={styles.toolbar}>
        <FormField id="costing-flow" label="Production flow">
          {(c) => (
            <Select {...c} value={flowId} onChange={(e) => chooseFlow(e.target.value)}>
              <option value="">{flows.loading ? "Loading flows…" : "Choose a flow"}</option>
              {(flows.data ?? []).map((f) => (
                <option key={f.id} value={f.id}>
                  {f.name}
                </option>
              ))}
              {flowId && flows.data && !flows.data.some((f) => f.id === flowId) ? <option value={flowId}>{flowId}</option> : null}
            </Select>
          )}
        </FormField>
      </div>
      {flows.error ? (
        <Alert tone="danger" title="Could not load production flows" live="alert" action={<Button size="sm" onClick={flows.reload}>Try again</Button>}>
          {flows.error.message} ({flows.error.code})
        </Alert>
      ) : null}
      {notice ? <Alert tone="success" title={notice} live="status" /> : null}
      {!flowId ? (
        <EmptyState title="Choose a production flow" description="Its costing periods, open and settled, appear here." />
      ) : periods.error ? (
        <Alert tone="danger" title="Could not load costing periods" live="alert" action={<Button size="sm" onClick={periods.reload}>Try again</Button>}>
          {periods.error.message} ({periods.error.code})
        </Alert>
      ) : (
        <>
          <ExportPeriods key={flowId} flowId={flowId} />
          <DataTable
            label="Costing periods"
            columns={columns}
            rows={periods.data ?? []}
            getRowKey={(p) => p.id}
            loading={periods.loading}
            empty={{ title: "No costing periods yet", description: "Open the first period of this flow below." }}
            onRowOpen={(p) => {
              setSelectedId(p.id);
              setNotice(null);
            }}
          />
          {selected?.status === "open" ? (
            <SettleForm
              key={selected.id}
              flowId={flowId}
              period={selected}
              conversion={carried?.periodId === selected.id ? carried.conversion : undefined}
              onSettled={(p) => {
                setNotice(`Settled ${p.periodStart} to ${p.periodEnd} with a variance of ${p.variance ?? dash} ${p.currency}.`);
                periods.reload();
              }}
            />
          ) : null}
          {selected?.status === "settled" ? <SettledFacts period={selected} /> : null}
          {selected ? (
            <BackflushCard
              key={`backflush-${selected.id}`}
              flowId={flowId}
              period={selected}
              onUse={selected.status === "open" ? (conversion) => setCarried({ periodId: selected.id, conversion }) : undefined}
            />
          ) : null}
          <OpenPeriodForm
            flowId={flowId}
            onOpened={(p) => {
              setNotice(`Opened ${p.periodStart} to ${p.periodEnd}.`);
              periods.reload();
            }}
          />
        </>
      )}
    </div>
  );
}
