import { useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import {
  getForecastLines,
  putForecastLines,
  ReductionPrinciple,
  type ForecastLine,
  type ForecastModel,
  type ForecastWrite,
} from "./api/client";
import { describeError, PLAN_CODE, useLoad, type LoadError } from "./hooks";
import styles from "./pages.module.css";

export const DEFAULT_FORECAST_MODEL = "BASE";
/** Rows the forecast endpoint returns at most (MaxPageSize in the service). */
const LINE_LIMIT = 500;

const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const QTY = /^(?:0|[1-9]\d{0,11})(?:\.\d{1,6})?$/;

export const principleLabel: Record<ReductionPrinciple, string> = {
  none: "None",
  percent: "Percent per period",
  transactions: "Transactions (forecast less sales in the period)",
  "transactions-dynamic": "Transactions, dynamic period",
};

const sourceTone: Record<ForecastLine["source"], BadgeTone> = { statistical: "info", manual: "neutral", consensus: "success" };

/** The setup of a model before its first write (the service default). */
const newModel = (code: string): ForecastModel => ({ code, version: 0, reductionPrinciple: "transactions", reductionPercent: "0" });

export interface CellDraft {
  itemId: string;
  siteId: string;
  periodStart: string;
  periodEnd: string;
  qty: string;
  salesQty: string;
  principle: ReductionPrinciple;
  percent: string;
}

type CellErrors = Partial<Record<keyof CellDraft, string>>;

const emptyDraft = (m: ForecastModel): CellDraft => ({
  itemId: "",
  siteId: "",
  periodStart: "",
  periodEnd: "",
  qty: "",
  salesQty: "",
  principle: m.reductionPrinciple,
  percent: m.reductionPrinciple === "percent" ? String(Number(m.reductionPercent)) : "",
});

/**
 * Checks one forecast cell and returns the PUT body: the cell, plus the
 * reduction setup when it differs from the model's (FR-22.8).
 */
export function validateCell(d: CellDraft, m: ForecastModel): { errors: CellErrors; body: ForecastWrite | null } {
  const errors: CellErrors = {};
  if (!UUID.test(d.itemId.trim())) errors.itemId = "Enter the item id as a UUID.";
  if (!UUID.test(d.siteId.trim())) errors.siteId = "Enter the site id as a UUID.";
  if (!DATE.test(d.periodStart)) errors.periodStart = "Enter the first day of the period.";
  if (!DATE.test(d.periodEnd)) errors.periodEnd = "Enter the last day of the period.";
  else if (!errors.periodStart && d.periodEnd < d.periodStart) errors.periodEnd = "The period cannot end before it starts.";
  if (!QTY.test(d.qty.trim())) errors.qty = "Enter 0 or more, with at most 6 decimals.";
  if (d.salesQty.trim() !== "" && !QTY.test(d.salesQty.trim())) errors.salesQty = "Enter 0 or more, with at most 6 decimals.";
  const percent = d.percent.trim();
  if (d.principle === "percent" && (!QTY.test(percent) || Number(percent) > 100)) errors.percent = "Enter a percentage from 0 to 100.";
  if (Object.keys(errors).length > 0) return { errors, body: null };
  const body: ForecastWrite = {
    lines: [
      {
        itemId: d.itemId.trim(),
        siteId: d.siteId.trim(),
        periodStart: d.periodStart,
        periodEnd: d.periodEnd,
        qty: d.qty.trim(),
        ...(d.salesQty.trim() !== "" ? { salesQty: d.salesQty.trim() } : {}),
      },
    ],
  };
  const changed =
    d.principle !== m.reductionPrinciple || (d.principle === "percent" && Number(percent) !== Number(m.reductionPercent));
  if (changed) {
    body.reductionPrinciple = d.principle;
    if (d.principle === "percent") body.reductionPercent = percent;
  }
  return { errors, body };
}

/** Picks the forecast model in view; the choice lives in `?model=`. */
function ModelPicker({ model, onPick }: { model: string; onPick: (code: string) => void }) {
  const [draft, setDraft] = useState(model);
  const [error, setError] = useState<string | undefined>();
  function submit(e: FormEvent) {
    e.preventDefault();
    const code = draft.trim().toUpperCase();
    if (!PLAN_CODE.test(code)) {
      setError("Use 2 to 20 capital letters, digits, _ or -.");
      return;
    }
    setError(undefined);
    setDraft(code);
    onPick(code);
  }
  return (
    <form className={styles.toolbar} onSubmit={submit} aria-label="Choose forecast model">
      <div className={styles.toolbarField}>
        <FormField id="forecast-model" label="Forecast model" error={error}>
          {(a11y) => <Input {...a11y} value={draft} maxLength={20} onChange={(e) => setDraft(e.target.value)} />}
        </FormField>
      </div>
      <Button type="submit">Show model</Button>
    </form>
  );
}

function CellForm({ model, draft, setDraft, onSaved }: {
  model: ForecastModel;
  draft: CellDraft;
  setDraft: (d: CellDraft) => void;
  onSaved: (m: ForecastModel) => void;
}) {
  const [errors, setErrors] = useState<CellErrors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [busy, setBusy] = useState(false);
  const set = (field: keyof CellDraft) => (value: string) => setDraft({ ...draft, [field]: value });

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, body } = validateCell(draft, model);
    setErrors(found);
    if (!body) return;
    setBusy(true);
    setFailure(null);
    try {
      onSaved(await putForecastLines(model.code, model.version, body));
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Forecast cell">
      <p className={styles.summary}>
        A cell replaces the forecast of its item, site and period. Choose a row in the table to change it.
      </p>
      <div className={styles.stack}>
        <Alert
          tone="danger"
          title={failure ? (failure.status === 412 ? "Someone else changed this forecast; reload it and try again" : "The cell was not saved") : undefined}
          live="alert"
        >
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Forecast cell">
          <FormField id="cell-item" label="Item" hint="Item id (UUID)." error={errors.itemId}>
            {(a11y) => <Input {...a11y} value={draft.itemId} onChange={(e) => set("itemId")(e.target.value)} />}
          </FormField>
          <FormField id="cell-site" label="Site" hint="Site id (UUID)." error={errors.siteId}>
            {(a11y) => <Input {...a11y} value={draft.siteId} onChange={(e) => set("siteId")(e.target.value)} />}
          </FormField>
          <FormField id="cell-start" label="Period start" error={errors.periodStart}>
            {(a11y) => <Input {...a11y} type="date" value={draft.periodStart} onChange={(e) => set("periodStart")(e.target.value)} />}
          </FormField>
          <FormField id="cell-end" label="Period end" error={errors.periodEnd}>
            {(a11y) => <Input {...a11y} type="date" value={draft.periodEnd} onChange={(e) => set("periodEnd")(e.target.value)} />}
          </FormField>
          <FormField id="cell-qty" label="Forecast quantity" error={errors.qty}>
            {(a11y) => <Input {...a11y} inputMode="decimal" value={draft.qty} onChange={(e) => set("qty")(e.target.value)} />}
          </FormField>
          <FormField id="cell-sales" label="Open sales quantity" optional hint="Sales already ordered in the period; reduces the forecast." error={errors.salesQty}>
            {(a11y) => <Input {...a11y} inputMode="decimal" value={draft.salesQty} onChange={(e) => set("salesQty")(e.target.value)} />}
          </FormField>
          <FormField id="cell-principle" label="Reduction principle" hint="Applies to every cell of the model." error={errors.principle}>
            {(a11y) => (
              <Select {...a11y} value={draft.principle} onChange={(e) => set("principle")(e.target.value)}>
                {ReductionPrinciple.options.map((p) => (
                  <option key={p} value={p}>
                    {principleLabel[p]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {draft.principle === "percent" ? (
            <FormField id="cell-percent" label="Reduction percent" error={errors.percent}>
              {(a11y) => <Input {...a11y} inputMode="decimal" value={draft.percent} onChange={(e) => set("percent")(e.target.value)} />}
            </FormField>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save cell
            </Button>
            <Button variant="ghost" onClick={() => setDraft(emptyDraft(model))}>
              Clear
            </Button>
          </div>
        </form>
      </div>
    </Card>
  );
}

/**
 * Forecast (FR-22.8, FR-22.9 manual override): the cells of a forecast model
 * with their reduced quantities, and a form to add or change a cell and the
 * model's reduction principle.
 */
export function ForecastPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("model") ?? DEFAULT_FORECAST_MODEL;
  const model = PLAN_CODE.test(raw) ? raw : DEFAULT_FORECAST_MODEL;
  const [reload, setReload] = useState(0);
  const [saved, setSaved] = useState<string | null>(null);
  const state = useLoad((signal) => getForecastLines(model, { limit: LINE_LIMIT }, { signal }), [model, reload]);
  const missing = state.error?.status === 404;
  const current = state.data?.model ?? (missing ? newModel(model) : null);
  const [draft, setDraft] = useState<CellDraft | null>(null);
  const form = draft ?? (current ? emptyDraft(current) : null);

  const pick = (code: string) => {
    const next = new URLSearchParams(params);
    next.set("model", code);
    setParams(next);
    setDraft(null);
    setSaved(null);
  };
  const edit = (l: ForecastLine) => {
    if (!current) return;
    setDraft({ ...emptyDraft(current), itemId: l.itemId, siteId: l.siteId, periodStart: l.periodStart, periodEnd: l.periodEnd, qty: l.qty, salesQty: l.salesQty });
  };
  const onSaved = (m: ForecastModel) => {
    setSaved(`Cell saved. Forecast model ${m.code} is at version ${m.version}.`);
    setDraft(null);
    setReload((n) => n + 1);
  };

  const columns: DataTableColumn<ForecastLine>[] = [
    { key: "itemId", header: "Item", mono: true },
    { key: "siteId", header: "Site", mono: true },
    { key: "periodStart", header: "Period start" },
    { key: "periodEnd", header: "Period end" },
    { key: "source", header: "Source", render: (l) => <Badge tone={sourceTone[l.source]}>{l.source.charAt(0).toUpperCase() + l.source.slice(1)}</Badge> },
    { key: "qty", header: "Forecast", align: "end" },
    { key: "salesQty", header: "Open sales", align: "end" },
    { key: "reducedQty", header: "Reduced forecast", align: "end" },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Forecast"
        description="Forecast cells per item, site and period. The reduced forecast is the demand a plan run nets when it names this model."
      />
      <ModelPicker model={model} onPick={pick} />
      <Alert tone="success" live="status">
        {saved}
      </Alert>
      {state.error && !missing ? (
        <Alert tone="danger" title={`Could not load forecast model ${model}`} live="alert">
          {`${state.error.message} (${state.error.code})`}
        </Alert>
      ) : null}
      <Card title="Model">
        {state.loading ? (
          <div aria-busy="true">
            <Skeleton width="3/4" />
            <span className="visually-hidden">Loading…</span>
          </div>
        ) : current ? (
          <dl className={styles.details}>
            {[
              ["Model", current.code],
              ["Version", String(current.version)],
              ["Reduction principle", principleLabel[current.reductionPrinciple]],
              ...(current.reductionPrinciple === "percent" ? [["Reduction percent", `${Number(current.reductionPercent)}%`]] : []),
            ].map(([term, value]) => (
              <div key={term} className={styles.entry}>
                <dt className={styles.term}>{term}</dt>
                <dd className={styles.value}>{value}</dd>
              </div>
            ))}
          </dl>
        ) : null}
      </Card>
      {missing && !state.loading ? (
        <Card>
          <EmptyState
            compact
            icon="grid"
            title={`Forecast model ${model} has no cells yet`}
            description="Save the first cell below to create the model."
          />
        </Card>
      ) : null}
      {state.data || state.loading ? (
        <DataTable
          label="Forecast cells"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(l) => `${l.itemId}-${l.siteId}-${l.periodStart}`}
          loading={state.loading}
          empty={{ title: "No forecast cells", description: "Save a cell below to add the first one." }}
          onRowOpen={edit}
        />
      ) : null}
      {current && form && !state.loading ? <CellForm key={`${current.code}-${current.version}`} model={current} draft={form} setDraft={setDraft} onSaved={onSaved} /> : null}
    </div>
  );
}
