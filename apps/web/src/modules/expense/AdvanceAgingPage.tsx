import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { checkAdvanceAging, describeProblem, type AgingInput, type AgingResult, type OverdueAdvance, type Problem } from "./api/client";
import { DAY, DECIMAL, splitProblem, today } from "./format";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

export interface AdvanceValues {
  id: string;
  tripEnd: string;
  paid: string;
  settled: string;
  returned: string;
  recovered: string;
  writtenOff: string;
}

export interface AgingValues {
  asOf: string;
  limitDays: string;
  advances: AdvanceValues[];
}

const MAX_ADVANCES = 50;
const emptyAdvance: AdvanceValues = { id: "", tripEnd: "", paid: "", settled: "", returned: "", recovered: "", writtenOff: "" };
const CLEARED = ["settled", "returned", "recovered", "writtenOff"] as const;

export function emptyAging(asOf = today()): AgingValues {
  return { asOf, limitDays: "30", advances: [{ ...emptyAdvance }] };
}

export function validateAging(v: AgingValues): Errors {
  const errors: Errors = {};
  if (!DAY.test(v.asOf)) errors.asOf = "Enter the date to check on.";
  const limit = Number(v.limitDays);
  if (!/^\d+$/.test(v.limitDays.trim()) || limit < 1 || limit > 180) errors.limitDays = "Enter 1 to 180 days.";
  const seen = new Set<string>();
  v.advances.forEach((a, i) => {
    const at = (f: string) => `advances[${i}].${f}`;
    const id = a.id.trim();
    if (id === "" || id.length > 64 || seen.has(id)) errors[at("id")] = "Enter a unique advance number of up to 64 characters.";
    seen.add(id);
    if (!DAY.test(a.tripEnd)) errors[at("tripEnd")] = "Enter the trip end date.";
    if (!DECIMAL.test(a.paid.trim())) errors[at("paid")] = "Enter the amount paid, such as 1000.00.";
    for (const f of CLEARED) {
      if (a[f].trim() !== "" && !DECIMAL.test(a[f].trim())) errors[at(f)] = "Enter an amount, or leave it empty for none.";
    }
  });
  return errors;
}

export function toAgingInput(v: AgingValues): AgingInput {
  return {
    asOf: v.asOf,
    limitDays: Number(v.limitDays),
    advances: v.advances.map((a) => {
      const out: AgingInput["advances"][number] = { id: a.id.trim(), tripEnd: a.tripEnd, paid: a.paid.trim() };
      for (const f of CLEARED) if (a[f].trim() !== "") out[f] = a[f].trim();
      return out;
    }),
  };
}

const overdueColumns: DataTableColumn<OverdueAdvance>[] = [
  { key: "id", header: "Advance" },
  { key: "dueDate", header: "Settlement due", mono: true },
  { key: "daysPastDue", header: "Days past due", align: "end" },
  { key: "open", header: "Open", mono: true, align: "end" },
];

const ADVANCE_FIELDS: { key: keyof AdvanceValues; label: string; date?: boolean; decimal?: boolean }[] = [
  { key: "id", label: "Advance" },
  { key: "tripEnd", label: "Trip end", date: true },
  { key: "paid", label: "Paid", decimal: true },
  { key: "settled", label: "Settled by reports", decimal: true },
  { key: "returned", label: "Returned", decimal: true },
  { key: "recovered", label: "Recovered via payroll", decimal: true },
  { key: "writtenOff", label: "Written off", decimal: true },
];

/** FR-43.15 cash advance aging: which paid advances are past settlement due and whether new advances are blocked. Stores nothing. */
export function AdvanceAgingPage() {
  const [values, setValues] = useState<AgingValues>(() => emptyAging());
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Pick<Problem, "code" | "message"> | null>(null);
  const [result, setResult] = useState<AgingResult | null>(null);
  const [checked, setChecked] = useState(0);
  const [busy, setBusy] = useState(false);

  function update(patch: Partial<AgingValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
  }

  function setAdvance(i: number, patch: Partial<AdvanceValues>) {
    setValues((prev) => ({ ...prev, advances: prev.advances.map((a, j) => (j === i ? { ...a, ...patch } : a)) }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateAging(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await checkAdvanceAging(toAgingInput(values)));
      setChecked(values.advances.length);
    } catch (error) {
      const shown = ["asOf", "limitDays", ...values.advances.flatMap((_, i) => ADVANCE_FIELDS.map((f) => `advances[${i}].${f.key}`))];
      const split = splitProblem(describeProblem(error), shown);
      setResult(null);
      setErrors(split.fields);
      setFailure(split.failure);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cash advance aging"
        description="Check a worker's paid cash advances against their settlement due date, the trip end plus the limit in days. While any advance is past due with money still open, new advances are blocked. Nothing is saved."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="aging-asof" label="Check on" error={errors.asOf}>
            {(c) => <Input {...c} type="date" value={values.asOf} onChange={(e) => update({ asOf: e.target.value })} invalid={Boolean(errors.asOf)} />}
          </FormField>
          <FormField id="aging-limit" label="Settlement due after trip end (days)" error={errors.limitDays}>
            {(c) => <Input {...c} inputMode="numeric" value={values.limitDays} onChange={(e) => update({ limitDays: e.target.value })} invalid={Boolean(errors.limitDays)} />}
          </FormField>
          {values.advances.map((a, i) => (
            <fieldset key={i} className={styles.lineSet}>
              <legend className={styles.legend}>{`Advance ${i + 1}`}</legend>
              {ADVANCE_FIELDS.map((f) => {
                const name = `advances[${i}].${f.key}`;
                return (
                  <FormField key={f.key} id={`aging-${i}-${f.key}`} label={f.label} optional={CLEARED.includes(f.key as (typeof CLEARED)[number])} error={errors[name]}>
                    {(c) => (
                      <Input
                        {...c}
                        type={f.date ? "date" : undefined}
                        className={f.decimal ? styles.mono : undefined}
                        inputMode={f.decimal ? "decimal" : undefined}
                        value={a[f.key]}
                        onChange={(e) => setAdvance(i, { [f.key]: e.target.value })}
                        invalid={Boolean(errors[name])}
                      />
                    )}
                  </FormField>
                );
              })}
              {values.advances.length > 1 ? (
                <Button variant="ghost" size="sm" onClick={() => update({ advances: values.advances.filter((_, j) => j !== i) })}>
                  {`Remove advance ${i + 1}`}
                </Button>
              ) : null}
            </fieldset>
          ))}
          <div className={styles.formActions}>
            {values.advances.length < MAX_ADVANCES ? (
              <Button variant="secondary" onClick={() => update({ advances: [...values.advances, { ...emptyAdvance }] })}>
                Add advance
              </Button>
            ) : null}
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not check the advances" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Check aging
            </Button>
          </div>
        </form>
      </Card>
      <Card title="Result">
        {result ? (
          <div className={styles.stack}>
            {result.newAdvancesBlocked ? (
              <Alert tone="warning" title="New advances are blocked">
                {`Advances are past settlement due, so no new advance can be requested until they are settled (${result.code ?? "EXP-ADV-002"}).`}
              </Alert>
            ) : (
              <Alert tone="success" title="New advances are allowed">
                No advance is past its settlement due date.
              </Alert>
            )}
            <StatGroup label="Aging summary">
              <StatTile label="Advances checked" value={checked} />
              <StatTile label="Past due" value={result.overdue.length} tone="warning" />
            </StatGroup>
            <DataTable
              label="Overdue advances"
              columns={overdueColumns}
              rows={result.overdue}
              getRowKey={(a) => a.id}
              empty={{ title: "No overdue advances", description: "Every advance is settled or still within its due date." }}
            />
          </div>
        ) : busy ? (
          <DataTable label="Overdue advances" columns={overdueColumns} rows={[]} getRowKey={(a) => a.id} loading />
        ) : (
          <EmptyState icon="calendarCheck" title="No advances checked yet" description="Enter the paid advances and what has cleared them, then check aging." compact />
        )}
      </Card>
    </div>
  );
}
