import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import { generateStatisticalForecast, type ForecastModel, type GenerateForecastResult, type GenerateForecastWrite, type GenerateModel } from "./api/client";
import { describeError, type LoadError } from "./hooks";
import styles from "./pages.module.css";

const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const QTY = /^(?:0|[1-9]\d{0,11})(?:\.\d{1,6})?$/;
const MONTH_START = /^\d{4}-\d{2}-01$/;

export interface GenerateDraft {
  itemId: string;
  siteId: string;
  history: string;
  firstPeriodStart: string;
  horizonPeriods: string;
  model: GenerateModel;
  autoSeason: boolean;
}

type Errors = Partial<Record<keyof GenerateDraft, string>>;

export const modelLabel: Record<GenerateModel, string> = {
  auto: "Automatic (ETS, Croston or TSB)",
  ets: "Exponential smoothing (ETS)",
  croston: "Croston",
  tsb: "TSB",
  naive: "Naive (last value)",
};

const emptyDraft = (): GenerateDraft => ({ itemId: "", siteId: "", history: "", firstPeriodStart: "", horizonPeriods: "12", model: "auto", autoSeason: true });

/** Checks the draft and returns the generate body for one item and site (FR-22.9). */
export function validateGenerate(d: GenerateDraft): { errors: Errors; body: GenerateForecastWrite | null } {
  const errors: Errors = {};
  if (!UUID.test(d.itemId.trim())) errors.itemId = "Enter the item id as a UUID.";
  if (!UUID.test(d.siteId.trim())) errors.siteId = "Enter the site id as a UUID.";
  if (!MONTH_START.test(d.firstPeriodStart)) errors.firstPeriodStart = "Enter the first day of the first month to forecast.";
  const horizon = Number(d.horizonPeriods);
  if (!/^\d+$/.test(d.horizonPeriods.trim()) || horizon < 1 || horizon > 24) errors.horizonPeriods = "Enter 1 to 24 months.";
  const history = d.history.split(/[\s,;]+/).filter((v) => v !== "");
  if (history.length === 0 || history.length > 600) errors.history = "Enter 1 to 600 monthly quantities, oldest first.";
  else if (history.some((v) => !QTY.test(v))) errors.history = "Every quantity must be 0 or more, with at most 6 decimals.";
  if (Object.keys(errors).length > 0) return { errors, body: null };
  return {
    errors,
    body: {
      firstPeriodStart: d.firstPeriodStart,
      horizonPeriods: horizon,
      series: [{ itemId: d.itemId.trim(), siteId: d.siteId.trim(), history, model: d.model, autoSeason: d.autoSeason }],
    },
  };
}

/** The confirmation shown after a generation. */
export function generateOutcome(code: string, r: GenerateForecastResult): string {
  const kept = r.cellsKept > 0 ? `, ${r.cellsKept} left as they were because a manual or consensus value holds the month` : "";
  return `Forecast model ${code}: ${r.cellsWritten} statistical cells written${kept}. The model is at version ${r.model.version}.`;
}

const pct = (v: number | null) => (v === null ? "n/a" : `${(v * 100).toFixed(1)}%`);

type Row = GenerateForecastResult["generated"][number];

const columns: DataTableColumn<Row>[] = [
  { key: "itemId", header: "Item", mono: true },
  { key: "siteId", header: "Site", mono: true },
  { key: "model", header: "Model used" },
  { key: "seasonLength", header: "Season length", align: "end" },
  { key: "mape", header: "MAPE", align: "end", render: (r) => pct(r.mape) },
  { key: "wmape", header: "wMAPE", align: "end", render: (r) => pct(r.wmape) },
  { key: "warnings", header: "Notes", render: (r) => (r.shortHistory ? "History under 24 months; naive model used (PLAN-0012)" : r.warnings.join(", ")) },
];

/**
 * Statistical forecast (FR-22.9): paste the monthly sales history of an item
 * at a site and the forecast service models it and writes statistical cells
 * into the model. Manual and consensus cells are never overwritten. Needs the
 * planning.statistical-forecast feature; the service refuses with
 * PLAN-FEATURE-OFF otherwise.
 */
export function GenerateCard({ model, onDone }: { model: ForecastModel; onDone: (message: string) => void }) {
  const [draft, setDraft] = useState<GenerateDraft>(emptyDraft);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [result, setResult] = useState<GenerateForecastResult | null>(null);
  const [busy, setBusy] = useState(false);
  const set = <K extends keyof GenerateDraft>(field: K, value: GenerateDraft[K]) => setDraft({ ...draft, [field]: value });

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, body } = validateGenerate(draft);
    setErrors(found);
    if (!body) return;
    setBusy(true);
    setFailure(null);
    try {
      const out = await generateStatisticalForecast(model.code, model.version, body);
      setResult(out);
      onDone(generateOutcome(model.code, out));
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  const title = !failure
    ? undefined
    : failure.code === "PLAN-FEATURE-OFF"
      ? "The statistical forecast is not enabled for this deployment"
      : failure.code === "PLAN-FORECAST-UNAVAILABLE"
        ? "The forecast service did not answer; try again later"
        : failure.code === "PLAN-FORECAST-RANGE"
          ? "The forecast is too large to store; check the history for a wrong quantity"
          : failure.status === 412
          ? "Someone else changed this forecast; reload it and try again"
          : "The forecast was not generated";

  return (
    <Card title="Statistical forecast">
      <p className={styles.summary}>
        Enter the monthly quantities sold, oldest first, ending the month before the first month to forecast. The forecast service picks a model and writes
        one statistical cell per month; cells a planner set by hand stay as they are.
      </p>
      <div className={styles.stack}>
        <Alert tone="danger" title={title} live="alert">
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Statistical forecast">
          <FormField id="gen-item" label="Item" hint="Item id (UUID)." error={errors.itemId}>
            {(a11y) => <Input {...a11y} value={draft.itemId} onChange={(e) => set("itemId", e.target.value)} />}
          </FormField>
          <FormField id="gen-site" label="Site" hint="Site id (UUID)." error={errors.siteId}>
            {(a11y) => <Input {...a11y} value={draft.siteId} onChange={(e) => set("siteId", e.target.value)} />}
          </FormField>
          <FormField id="gen-history" label="Monthly history" hint="Quantities separated by commas, spaces or new lines." error={errors.history}>
            {(a11y) => <Input {...a11y} inputMode="decimal" value={draft.history} onChange={(e) => set("history", e.target.value)} />}
          </FormField>
          <FormField id="gen-first" label="First month to forecast" error={errors.firstPeriodStart}>
            {(a11y) => <Input {...a11y} type="date" value={draft.firstPeriodStart} onChange={(e) => set("firstPeriodStart", e.target.value)} />}
          </FormField>
          <FormField id="gen-horizon" label="Months to forecast" error={errors.horizonPeriods}>
            {(a11y) => <Input {...a11y} inputMode="numeric" value={draft.horizonPeriods} onChange={(e) => set("horizonPeriods", e.target.value)} />}
          </FormField>
          <FormField id="gen-model" label="Method">
            {(a11y) => (
              <Select {...a11y} value={draft.model} onChange={(e) => set("model", e.target.value as GenerateModel)}>
                {(Object.keys(modelLabel) as GenerateModel[]).map((m) => (
                  <option key={m} value={m}>
                    {modelLabel[m]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <span className={styles.check}>
            <Input id="gen-auto" type="checkbox" checked={draft.autoSeason} onChange={(e) => set("autoSeason", e.target.checked)} />
            <label htmlFor="gen-auto">Detect the season length from the history</label>
          </span>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Generate forecast
            </Button>
          </div>
        </form>
        {result ? (
          <DataTable
            label="Generated series"
            columns={columns}
            rows={result.generated}
            getRowKey={(r) => `${r.itemId}-${r.siteId}`}
            empty={{ title: "Nothing generated", description: "The request held no series." }}
          />
        ) : null}
      </div>
    </Card>
  );
}
