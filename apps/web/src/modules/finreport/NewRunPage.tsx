import { useRef, useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createRun, CURRENCY_TYPES, describeProblem, listDefinitions, newKey, type CurrencyType, type Problem, type RunRequest } from "./api/client";
import { CURRENCY_TYPE_LABELS, fiscalCalendar, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface RunValues {
  definition: string;
  version: string;
  ledgerId: string;
  currencyType: CurrencyType;
  year: string;
  period: string;
  periodsPerYear: string;
  firstYear: string;
  includeAdjustmentPeriods: boolean;
}

type Errors = Partial<Record<keyof RunValues, string>>;

const INTEGER = /^\d+$/;

/** Client checks that mirror the service (POST /runs); the service has the last word. */
export function validateRun(v: RunValues): Errors {
  const errors: Errors = {};
  if (v.definition === "") errors.definition = "Choose a published definition.";
  if (v.version !== "" && (!INTEGER.test(v.version) || Number(v.version) < 1)) errors.version = "Enter a published version number, or leave it empty for the latest.";
  if (!UUID.test(v.ledgerId.trim())) errors.ledgerId = "Enter the ledger id (a UUID).";
  if (!INTEGER.test(v.year) || Number(v.year) < 1900) errors.year = "Enter the fiscal year, such as 2026.";
  if (!INTEGER.test(v.periodsPerYear) || Number(v.periodsPerYear) < 1 || Number(v.periodsPerYear) > 16) errors.periodsPerYear = "Enter 1 to 16 periods per year.";
  else if (!INTEGER.test(v.period) || Number(v.period) > Number(v.periodsPerYear)) errors.period = `Enter a period from 0 to ${v.periodsPerYear}.`;
  if (!INTEGER.test(v.firstYear) || (INTEGER.test(v.year) && Number(v.firstYear) > Number(v.year))) errors.firstYear = "Enter a year on or before the fiscal year.";
  else if (INTEGER.test(v.year) && Number(v.year) - Number(v.firstYear) > 40) errors.firstYear = "The calendar may span at most 40 years.";
  return errors;
}

/** The request the form sends; the calendar covers firstYear..year so prior-year columns resolve. */
export function toRunRequest(v: RunValues): RunRequest {
  const year = Number(v.year);
  const no = Number(v.period);
  const calendar = fiscalCalendar(Number(v.firstYear), year, Number(v.periodsPerYear));
  const period = calendar.find((p) => p.year === year && p.no === no) ?? { year, no, quarter: 1 };
  return {
    definition: v.definition,
    ...(v.version !== "" ? { definitionVersion: Number(v.version) } : {}),
    ledgerId: v.ledgerId.trim(),
    currencyType: v.currencyType,
    period,
    calendar,
    ...(v.includeAdjustmentPeriods ? { includeAdjustmentPeriods: true } : {}),
  };
}

/** Run a published definition for a ledger and period (FR-15.6). */
export function NewRunPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const thisYear = new Date().getFullYear();
  const [values, setValues] = useState<RunValues>({
    definition: params.get("definition") ?? "",
    version: "",
    ledgerId: "",
    currencyType: "acct",
    year: String(thisYear),
    period: "1",
    periodsPerYear: "12",
    firstYear: String(thisYear - 1),
    includeAdjustmentPeriods: false,
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const definitions = useLoad((signal) => listDefinitions(signal), "definitions");
  const published = (definitions.data ?? []).filter((d) => d.latestVersion > 0);

  function update(patch: Partial<RunValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateRun(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const run = await createRun(toRunRequest(values), key.current);
      navigate(`/finreport/runs/${run.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "REPORT-0003") setErrors({ period: problem.message });
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: string, name: keyof RunValues, label: string, extra: { hint?: string; optional?: boolean; mono?: boolean; inputMode?: "numeric" } = {}) => (
    <FormField id={id} label={label} hint={extra.hint} optional={extra.optional} error={errors[name]}>
      {(control) => (
        <Input
          {...control}
          inputMode={extra.inputMode}
          className={extra.mono ? styles.mono : undefined}
          value={String(values[name])}
          onChange={(e) => update({ [name]: e.target.value } as Partial<RunValues>)}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Run a statement"
        description="Evaluates a published definition over the reporting read model. The run records the posting sequence it reflects and a hash of its result."
      />
      {definitions.error ? (
        <Alert tone="danger" title="Could not load the definitions" live="alert" action={<Button size="sm" onClick={definitions.reload}>Try again</Button>}>
          {definitions.error.message} ({definitions.error.code})
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="fr-run-definition" label="Definition" error={errors.definition}>
            {(control) => (
              <Select {...control} value={values.definition} onChange={(e) => update({ definition: e.target.value })} invalid={Boolean(errors.definition)}>
                <option value="">{definitions.loading ? "Loading definitions…" : published.length === 0 ? "No published definitions" : "Choose a definition"}</option>
                {published.map((d) => (
                  <option key={d.code} value={d.code}>
                    {d.code} {d.name} (v{d.latestVersion})
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("fr-run-version", "version", "Version", { optional: true, hint: "Empty runs the latest published version.", inputMode: "numeric" })}
          {text("fr-run-ledger", "ledgerId", "Ledger id", { mono: true })}
          <FormField id="fr-run-currency" label="Currency type" hint="Columns that do not name a currency type use this one.">
            {(control) => (
              <Select {...control} value={values.currencyType} onChange={(e) => update({ currencyType: e.target.value as CurrencyType })}>
                {CURRENCY_TYPES.map((c) => (
                  <option key={c} value={c}>
                    {CURRENCY_TYPE_LABELS[c]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("fr-run-year", "year", "Fiscal year", { inputMode: "numeric" })}
          {text("fr-run-period", "period", "Period", { hint: "0 is the opening period.", inputMode: "numeric" })}
          {text("fr-run-periods", "periodsPerYear", "Periods per year", { inputMode: "numeric" })}
          {text("fr-run-first-year", "firstYear", "Calendar starts in", { hint: "Include the prior year for PY and PYTD columns.", inputMode: "numeric" })}
          <span className={styles.check}>
            <Input
              id="fr-run-adjustment"
              type="checkbox"
              checked={values.includeAdjustmentPeriods}
              onChange={(e) => update({ includeAdjustmentPeriods: e.target.checked })}
            />
            <label htmlFor="fr-run-adjustment">Include adjustment periods</label>
          </span>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run statement
            </Button>
            <Button variant="ghost" onClick={() => navigate("/finreport")}>
              Cancel
            </Button>
          </div>
        </form>
      </Card>
      {failure ? (
        <Alert tone="danger" title="The statement did not run" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
    </div>
  );
}
