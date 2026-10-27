import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  BACKORDER_STRATEGIES,
  describeProblem,
  previewBackorderRun,
  type BackorderLineInput,
  type BackorderPreviewInput,
  type BackorderPreviewResult,
  type BackorderStrategy,
  type PlannedLine,
  type Problem,
} from "./api/client";
import { DECIMAL, dash, sentence } from "./format";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const CLASS = /^[1-9]$/;

export const strategyHelp: Record<BackorderStrategy, string> = {
  REDISTRIBUTE: "Serve by priority class, then requested date, then order value; confirmed quantity may move to higher priority.",
  WIN: "Keep every confirmed quantity, then serve open demand by priority.",
  GAIN: "Keep every confirmed quantity, then serve the smallest open demand first.",
  FILL: "Serve by requested date (first come), ignoring priority.",
  LOSE: "Lines at or above the lose threshold may lose their confirmation.",
};

export interface LineValues {
  id: string;
  priorityClass: string;
  requestedDate: string;
  orderValue: string;
  demand: string;
  confirmed: string;
}

export interface RunValues {
  strategy: BackorderStrategy;
  available: string;
  loseThreshold: string;
  lines: LineValues[];
}

export const emptyRunLine = (n: number): LineValues => ({ id: `L${n}`, priorityClass: "3", requestedDate: "", orderValue: "0", demand: "", confirmed: "0" });

/** Checks that mirror the service (api/backorder.go, domain/backorder.go). Keys are the service's field paths. */
export function validateRun(values: RunValues): { errors: Errors; input?: BackorderPreviewInput } {
  const errors: Errors = {};
  if (!DECIMAL.test(values.available)) errors.available = "Enter the available supply (0 or more).";
  if (values.loseThreshold !== "" && !CLASS.test(values.loseThreshold)) errors.loseThreshold = "Enter a priority class from 1 to 9.";
  if (values.lines.length === 0) errors.lines = "Add at least one line.";
  const ids = new Set<string>();
  values.lines.forEach((l, i) => {
    const at = (name: string) => `lines[${i}].${name}`;
    if (l.id.trim() === "") errors[at("id")] = "Name the line.";
    else if (ids.has(l.id.trim())) errors[at("id")] = "Use a different name for each line.";
    ids.add(l.id.trim());
    if (!CLASS.test(l.priorityClass)) errors[at("priorityClass")] = "Enter 1 (highest) to 9.";
    if (!DATE.test(l.requestedDate)) errors[at("requestedDate")] = "Choose the requested date.";
    if (!DECIMAL.test(l.orderValue)) errors[at("orderValue")] = "Enter 0 or more.";
    if (!DECIMAL.test(l.demand) || Number(l.demand) <= 0) errors[at("demand")] = "Enter an open quantity above 0.";
    if (!DECIMAL.test(l.confirmed)) errors[at("confirmed")] = "Enter 0 or more.";
    else if (DECIMAL.test(l.demand) && Number(l.confirmed) > Number(l.demand)) errors[at("confirmed")] = "Cannot exceed the quantity.";
  });
  if (Object.keys(errors).length > 0) return { errors };
  const lines: BackorderLineInput[] = values.lines.map((l) => ({
    id: l.id.trim(),
    priorityClass: Number(l.priorityClass),
    requestedDate: l.requestedDate,
    orderValue: l.orderValue,
    demand: l.demand,
    confirmed: l.confirmed,
  }));
  const input: BackorderPreviewInput = { strategy: values.strategy, available: values.available, lines };
  if (values.loseThreshold !== "") input.loseThreshold = Number(values.loseThreshold);
  return { errors, input };
}

const resultColumns: DataTableColumn<PlannedLine & { rank: number }>[] = [
  { key: "rank", header: "Order served", align: "end" },
  { key: "id", header: "Line", mono: true },
  { key: "before", header: "Confirmed now", align: "end" },
  { key: "after", header: "After the run", align: "end" },
  {
    key: "change",
    header: "Change",
    render: (l) => (l.deconfirmed ? <Badge tone="danger">Loses confirmation</Badge> : l.changed ? <Badge tone="success">Gains</Badge> : dash),
  },
];

const lineFields: [keyof LineValues, string, "text" | "date" | "decimal"][] = [
  ["id", "Line", "text"],
  ["priorityClass", "Priority class", "text"],
  ["requestedDate", "Requested date", "date"],
  ["orderValue", "Order value", "decimal"],
  ["demand", "Quantity", "decimal"],
  ["confirmed", "Confirmed now", "decimal"],
];

/**
 * Backorder run preview (FR-27.17, BR-27.11): enter the open lines competing
 * for one product's supply and see what each strategy would confirm. Nothing
 * is committed; reading the lines from open orders and committing the run are
 * later slices.
 */
export function BackorderRunPage() {
  const [values, setValues] = useState<RunValues>({ strategy: "REDISTRIBUTE", available: "", loseThreshold: "", lines: [emptyRunLine(1)] });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<BackorderPreviewResult | null>(null);
  const [busy, setBusy] = useState(false);

  function updateLine(i: number, patch: Partial<LineValues>) {
    setValues((v) => ({ ...v, lines: v.lines.map((l, idx) => (idx === i ? { ...l, ...patch } : l)) }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRun(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult(await previewBackorderRun(input));
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const rows = result?.lines.map((l, i) => ({ ...l, rank: i + 1 })) ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Backorder run preview"
        description="Enter the open lines for one product and site and the supply now available, then preview how a strategy would re-confirm them."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Backorder run preview">
          <FormField id="bo-strategy" label="Strategy" hint={strategyHelp[values.strategy]}>
            {(control) => (
              <Select {...control} value={values.strategy} onChange={(e) => setValues((v) => ({ ...v, strategy: e.target.value as BackorderStrategy }))}>
                {BACKORDER_STRATEGIES.map((s) => (
                  <option key={s} value={s}>
                    {sentence(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="bo-available" label="Available supply" error={errors.available}>
            {(control) => <Input {...control} inputMode="decimal" value={values.available} onChange={(e) => setValues((v) => ({ ...v, available: e.target.value }))} invalid={Boolean(errors.available)} />}
          </FormField>
          <FormField id="bo-threshold" label="Lose threshold" optional hint="Priority class from which LOSE may deconfirm. Default 3." error={errors.loseThreshold}>
            {(control) => <Input {...control} inputMode="numeric" value={values.loseThreshold} onChange={(e) => setValues((v) => ({ ...v, loseThreshold: e.target.value }))} invalid={Boolean(errors.loseThreshold)} />}
          </FormField>
          <div className={`${styles.formActions} ${styles.lines}`}>
            {values.lines.map((l, i) => (
              <fieldset key={i} className={styles.lineCard}>
                <legend className={styles.lineLegend}>Line {i + 1}</legend>
                {lineFields.map(([field, label, kind]) => {
                  const key = `lines[${i}].${field}`;
                  return (
                    <FormField key={field} id={`bo-${i}-${field}`} label={label} error={errors[key]}>
                      {(control) => (
                        <Input
                          {...control}
                          type={kind === "date" ? "date" : "text"}
                          inputMode={kind === "decimal" ? "decimal" : undefined}
                          value={l[field]}
                          onChange={(e) => updateLine(i, { [field]: e.target.value })}
                          invalid={Boolean(errors[key])}
                        />
                      )}
                    </FormField>
                  );
                })}
                {values.lines.length > 1 ? (
                  <div className={styles.formActions}>
                    <Button size="sm" variant="ghost" onClick={() => setValues((v) => ({ ...v, lines: v.lines.filter((_, idx) => idx !== i) }))}>
                      Remove line {i + 1}
                    </Button>
                  </div>
                ) : null}
              </fieldset>
            ))}
            {errors.lines ? (
              <p role="alert" className={styles.fieldError}>
                {errors.lines}
              </p>
            ) : null}
            <div>
              <Button onClick={() => setValues((v) => ({ ...v, lines: [...v.lines, emptyRunLine(v.lines.length + 1)] }))}>Add line</Button>
            </div>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not preview the run" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Preview run
            </Button>
          </div>
        </form>
      </Card>
      <Alert
        tone={result && result.deconfirmedCount > 0 ? "warning" : "success"}
        live="status"
        title={result ? `${sentence(result.strategy)} preview: ${result.allocated} allocated` : undefined}
      >
        {result
          ? result.deconfirmedCount > 0
            ? `This run would take confirmation away from ${result.deconfirmedCount} line${result.deconfirmedCount === 1 ? "" : "s"} (${result.warningCode}). Nothing has been committed.`
            : "No line loses its confirmation. Nothing has been committed."
          : null}
      </Alert>
      {result ? <DataTable label="Preview by line" columns={resultColumns} rows={rows} getRowKey={(l) => l.id} empty={{ title: "No lines" }} /> : null}
    </div>
  );
}
