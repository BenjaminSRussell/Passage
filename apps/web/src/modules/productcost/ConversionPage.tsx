import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  advanceConversion,
  CONVERSION_STEPS,
  describeProblem,
  validateConversion,
  type ConversionBlocker,
  type ConversionInput,
  type ConversionReport,
  type ConversionRun,
  type ConversionStep,
  type Problem,
} from "./api/client";
import { amount, dash, isDecimal, isSignedDecimal } from "./format";
import { LineGrid, type LineField } from "./LineGrid";
import { DiagnosticsTable, journalColumns, ProblemAlert } from "./shared";
import styles from "./pages.module.css";

type PositionKey = "itemId" | "siteId" | "onHand" | "standardUnit" | "actualValue" | "inTransit" | "startedOrders";
type PositionValues = Record<PositionKey, string>;

interface Values {
  conversionDate: string;
  closeThrough: string;
  currency: string;
  blockers: Record<ConversionBlocker, boolean>;
  positions: PositionValues[];
}

const positionFields: LineField<PositionKey>[] = [
  { name: "itemId", label: "Item", mono: true },
  { name: "siteId", label: "Site", mono: true },
  { name: "onHand", label: "On-hand quantity", decimal: true },
  { name: "actualValue", label: "Value at actual cost", decimal: true },
  { name: "standardUnit", label: "New standard", decimal: true },
  { name: "inTransit", label: "Open in-transit transfers" },
  { name: "startedOrders", label: "Started production orders" },
];

const emptyPosition = (): PositionValues => ({
  itemId: "",
  siteId: "",
  onHand: "",
  standardUnit: "",
  actualValue: "",
  inTransit: "0",
  startedOrders: "0",
});

const blockerLabels: Record<ConversionBlocker, string> = {
  NegativeOnHand: "Negative on-hand blocks conversion",
  InTransit: "Open in-transit transfers block conversion",
  StartedOrders: "Started production orders block conversion",
};

/** AC-39.10: 500 on hand at FIFO 4,925.00 converted to a standard of 10.00 on 2026-11-01. */
export const ac3910: Values = {
  conversionDate: "2026-11-01",
  closeThrough: "2026-10-31",
  currency: "USD",
  blockers: { NegativeOnHand: true, InTransit: true, StartedOrders: true },
  positions: [
    { itemId: "RM-301", siteId: "PLANT-1", onHand: "500", standardUnit: "10.00", actualValue: "4925.00", inTransit: "0", startedOrders: "0" },
  ],
};

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const COUNT = /^\d+$/;

/** Checks that mirror the service, and the conversion input the page sends. */
export function validateInput(values: Values): { errors: Record<string, string>; input?: ConversionInput } {
  const errors: Record<string, string> = {};
  if (!DATE.test(values.conversionDate.trim())) errors.conversionDate = "Enter the conversion date as YYYY-MM-DD.";
  const closeThrough = values.closeThrough.trim();
  if (closeThrough !== "" && !DATE.test(closeThrough)) errors.closeThrough = "Enter the close date as YYYY-MM-DD, or leave it empty.";
  const currency = values.currency.trim().toUpperCase();
  if (currency !== "" && !/^[A-Z]{3}$/.test(currency)) errors.currency = "Enter a three-letter currency code such as USD, or leave it empty.";
  if (values.positions.length === 0) errors.positions = "Add at least one position.";
  const seen = new Set<string>();
  const positions = values.positions.map((p, i) => {
    const at = (k: PositionKey) => `positions[${i}].${k}`;
    const itemId = p.itemId.trim();
    const siteId = p.siteId.trim();
    if (itemId === "") errors[at("itemId")] = "Enter the item.";
    if (siteId === "") errors[at("siteId")] = "Enter the site.";
    if (itemId !== "" && siteId !== "") {
      if (seen.has(`${itemId}@${siteId}`)) errors[at("itemId")] = "Each item and site is listed once.";
      seen.add(`${itemId}@${siteId}`);
    }
    if (!isSignedDecimal(p.onHand)) errors[at("onHand")] = "Enter the on-hand quantity.";
    if (!isSignedDecimal(p.actualValue)) errors[at("actualValue")] = "Enter the value at actual cost.";
    if (!isDecimal(p.standardUnit)) errors[at("standardUnit")] = "Enter the new standard, 0 or more.";
    if (!COUNT.test(p.inTransit.trim())) errors[at("inTransit")] = "Enter a whole number, 0 or more.";
    if (!COUNT.test(p.startedOrders.trim())) errors[at("startedOrders")] = "Enter a whole number, 0 or more.";
    return {
      itemId,
      siteId,
      onHand: p.onHand.trim(),
      standardUnit: p.standardUnit.trim(),
      actualValue: p.actualValue.trim(),
      inTransit: Number.parseInt(p.inTransit.trim(), 10),
      startedOrders: Number.parseInt(p.startedOrders.trim(), 10),
    };
  });
  if (Object.keys(errors).length > 0) return { errors };
  const blockers = (Object.keys(values.blockers) as ConversionBlocker[]).filter((b) => values.blockers[b]);
  return {
    errors,
    input: {
      conversionDate: values.conversionDate.trim(),
      ...(closeThrough !== "" ? { closeThrough } : {}),
      ...(currency !== "" ? { currency } : {}),
      blockers,
      positions,
    },
  };
}

const stepLabels: Record<ConversionStep, string> = {
  select: "Select items and sites",
  roll: "Roll pending standards",
  validate: "Validate",
  preview: "Preview revaluation",
  close: "Inventory close",
  activate: "Activate",
};

interface StepRow {
  step: ConversionStep;
  state: "Done" | "Next" | "Waiting";
  user?: string;
  at?: string;
}

/** One row per checklist step: who ran it and when, the next one, or waiting. */
export function stepRows(run: ConversionRun): StepRow[] {
  return CONVERSION_STEPS.map((step, i) => {
    const done = run.checklist[i];
    if (done) return { step, state: "Done", user: done.user, at: done.at };
    return { step, state: i === run.checklist.length ? "Next" : "Waiting" };
  });
}

const stepColumns: DataTableColumn<StepRow>[] = [
  { key: "step", header: "Step", render: (r) => stepLabels[r.step] },
  {
    key: "state",
    header: "Status",
    render: (r) => <Badge tone={r.state === "Done" ? "success" : r.state === "Next" ? "info" : "neutral"}>{r.state}</Badge>,
  },
  { key: "user", header: "Done by", render: (r) => r.user ?? dash },
  { key: "at", header: "At", mono: true, render: (r) => r.at ?? dash },
];

type PreviewRow = ConversionReport["lines"][number];

const previewColumns: DataTableColumn<PreviewRow>[] = [
  { key: "itemId", header: "Item", mono: true },
  { key: "siteId", header: "Site", mono: true },
  { key: "onHand", header: "On hand", align: "end" },
  { key: "actualValue", header: "Value at actual", align: "end", render: (r) => amount(r.actualValue) },
  { key: "standardUnit", header: "New standard", align: "end", render: (r) => amount(r.standardUnit) },
  { key: "amount", header: "Revaluation", align: "end", render: (r) => amount(r.amount) },
];

function ReportView({ report }: { report: ConversionReport }) {
  const currency = report.currency ? ` ${report.currency}` : "";
  const diagnostics = report.diagnostics ?? [];
  return (
    <Card title="Validation and revaluation preview" headingLevel={3}>
      <div className={styles.stack}>
        {report.blocked ? (
          <Alert tone="danger" title="The conversion is blocked">
            Resolve the errors below; validation and activation are refused while they remain.
          </Alert>
        ) : !report.closeComplete ? (
          <Alert tone="warning" title="Inventory close needed">
            Validation passes, but the close and activate steps need the inventory close through the day before the conversion date (CST-0731).
          </Alert>
        ) : (
          <Alert tone="success" title="Ready to convert">
            No blockers, and the inventory close is complete.
          </Alert>
        )}
        <StatGroup label="Conversion revaluation">
          <StatTile label={`Revaluation${currency}`} value={amount(report.revaluation)} />
          <StatTile label="Positions" value={report.lines.length} />
          <StatTile label="Errors" value={diagnostics.filter((d) => d.severity === "error").length} tone="danger" />
        </StatGroup>
        {diagnostics.length > 0 ? <DiagnosticsTable label="Conversion diagnostics" diagnostics={diagnostics} /> : null}
        <DataTable
          label="Revaluation by item and site"
          columns={previewColumns}
          rows={report.lines}
          getRowKey={(r, i) => `${r.itemId}-${r.siteId}-${i}`}
          empty={{ title: "No positions", description: "Nothing on hand to convert." }}
        />
        <DataTable
          label="Conversion journal"
          columns={journalColumns}
          rows={report.journal}
          getRowKey={(j) => j.account}
          empty={{ title: "Nothing to post", description: "On hand at the new standard equals its value at actual cost." }}
        />
      </div>
    </Card>
  );
}

const emptyRun: ConversionRun = { status: "Draft", checklist: [] };

/** Standard cost conversion checklist (FR-39.12): validate, preview and record each step. Nothing is posted yet. */
export function ConversionPage() {
  const [values, setValues] = useState<Values>(ac3910);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [busy, setBusy] = useState<"validate" | "step" | null>(null);
  const [report, setReport] = useState<ConversionReport | null>(null);
  const [run, setRun] = useState<ConversionRun>(emptyRun);

  const next = CONVERSION_STEPS[run.checklist.length] as ConversionStep | undefined;

  function prepare(): ConversionInput | undefined {
    const { errors: found, input } = validateInput(values);
    setErrors(found);
    setFailure(null);
    return input;
  }

  function refused(title: string, err: unknown) {
    const problem = describeProblem(err);
    if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
    setFailure({ title, problem });
  }

  async function validate(event: FormEvent) {
    event.preventDefault();
    const input = prepare();
    if (!input) return;
    setBusy("validate");
    try {
      setReport(await validateConversion(input));
    } catch (err) {
      refused("The conversion was not validated", err);
      setReport(null);
    } finally {
      setBusy(null);
    }
  }

  async function advance() {
    if (!next) return;
    const input = prepare();
    if (!input) return;
    setBusy("step");
    try {
      const res = await advanceConversion({ run, step: next, input });
      setRun(res.run);
      setReport(res.report);
    } catch (err) {
      refused(`${stepLabels[next]} was refused`, err);
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Standard cost conversion"
        description="Convert items from an actual costing method to standard cost at a conversion date. The revaluation is on hand at the new standard less its value at actual cost after the close. Steps run in order and record who ran them. Posting the journal and storing the run arrive with persistence."
      />
      <Card title="Conversion" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void validate(e)} noValidate>
          <FormField id="conversion-date" label="Conversion date" hint="YYYY-MM-DD" error={errors.conversionDate}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.conversionDate}
                onChange={(e) => setValues({ ...values, conversionDate: e.target.value })}
                invalid={Boolean(errors.conversionDate)}
              />
            )}
          </FormField>
          <FormField
            id="conversion-close"
            label="Inventory closed through"
            hint="End date of the last completed inventory close; it must be the day before the conversion date."
            optional
            error={errors.closeThrough}
          >
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.closeThrough}
                onChange={(e) => setValues({ ...values, closeThrough: e.target.value })}
                invalid={Boolean(errors.closeThrough)}
              />
            )}
          </FormField>
          <FormField id="conversion-currency" label="Currency" hint="Sets the posting precision." optional error={errors.currency}>
            {(control) => (
              <Input
                {...control}
                value={values.currency}
                onChange={(e) => setValues({ ...values, currency: e.target.value })}
                invalid={Boolean(errors.currency)}
              />
            )}
          </FormField>
          <fieldset className={`${styles.lineCard} ${styles.wide}`}>
            <legend className={styles.lineLegend}>Blockers</legend>
            {(Object.keys(blockerLabels) as ConversionBlocker[]).map((b) => (
              <div key={b} className={styles.check}>
                <Input
                  id={`conversion-blocker-${b}`}
                  type="checkbox"
                  checked={values.blockers[b]}
                  onChange={(e) => setValues({ ...values, blockers: { ...values.blockers, [b]: e.target.checked } })}
                />
                <label htmlFor={`conversion-blocker-${b}`}>{blockerLabels[b]}</label>
              </div>
            ))}
          </fieldset>
          <LineGrid
            idPrefix="conversion-position"
            noun="Position"
            path="positions"
            fields={positionFields}
            lines={values.positions}
            errors={errors}
            newLine={emptyPosition}
            onChange={(positions) => setValues({ ...values, positions })}
          />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "validate"} disabled={busy === "step"}>
              Validate and preview
            </Button>
          </div>
        </form>
      </Card>

      <Card title="Checklist" headingLevel={3}>
        <div className={styles.stack}>
          <p className={styles.note}>
            Status: <Badge tone={run.status === "Activated" ? "success" : "info"}>{run.status}</Badge>
          </p>
          <DataTable label="Conversion checklist" columns={stepColumns} rows={stepRows(run)} getRowKey={(r) => r.step} />
          <div className={styles.formActions}>
            {next ? (
              <Button variant="primary" loading={busy === "step"} disabled={busy === "validate"} onClick={() => void advance()}>
                Run step: {stepLabels[next]}
              </Button>
            ) : (
              <Alert tone="success" title="Conversion activated">
                Every checklist step is done.
              </Alert>
            )}
            <Button variant="ghost" disabled={busy !== null || run.checklist.length === 0} onClick={() => setRun(emptyRun)}>
              Start over
            </Button>
          </div>
        </div>
      </Card>

      {failure ? <ProblemAlert title={failure.title} problem={failure.problem} /> : null}

      {busy === "validate" ? (
        <DataTable label="Revaluation by item and site" columns={previewColumns} rows={[]} getRowKey={(r) => r.itemId} loading />
      ) : report ? (
        <ReportView report={report} />
      ) : (
        <Card>
          <EmptyState
            icon="calendarCheck"
            headingLevel={3}
            title="Not validated yet"
            description="Validate the conversion to see its blockers, the revaluation per item and site and the journal it would post."
          />
        </Card>
      )}
    </div>
  );
}
