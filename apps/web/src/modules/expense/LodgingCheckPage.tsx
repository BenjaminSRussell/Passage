import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { checkLodging, describeProblem, type LodgingInput, type LodgingResult, type PolicyResult, type Problem } from "./api/client";
import { DECIMAL, splitProblem } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

export interface LodgingValues {
  amount: string;
  nights: string;
  lodgingRate: string;
  justification: string;
}

export const emptyLodging: LodgingValues = { amount: "", nights: "1", lodgingRate: "", justification: "" };

const MAX_NIGHTS = 366;

export function validateLodging(v: LodgingValues): Errors {
  const errors: Errors = {};
  if (!DECIMAL.test(v.amount.trim())) errors.amount = "Enter the cost of the whole stay, such as 450.00.";
  const nights = Number(v.nights);
  if (!/^\d+$/.test(v.nights.trim()) || nights < 1 || nights > MAX_NIGHTS) errors.nights = `Enter 1 to ${MAX_NIGHTS} nights.`;
  if (!DECIMAL.test(v.lodgingRate.trim())) errors.lodgingRate = "Enter the per-diem lodging rate per night, such as 150.00.";
  if (v.justification.length > 500) errors.justification = "Keep the justification to 500 characters.";
  return errors;
}

export function toLodgingInput(v: LodgingValues): LodgingInput {
  const input: LodgingInput = { amount: v.amount.trim(), nights: Number(v.nights), lodgingRate: v.lodgingRate.trim() };
  if (v.justification.trim() !== "") input.justification = v.justification.trim();
  return input;
}

const SHOWN = ["amount", "nights", "lodgingRate", "justification"];
const resultColumns: DataTableColumn<PolicyResult>[] = [
  { key: "ruleId", header: "Rule", mono: true },
  { key: "outcome", header: "Outcome", render: () => <Badge tone="warning">Justification required</Badge> },
  { key: "satisfied", header: "Status", render: (r) => (r.satisfied ? "Satisfied" : "Open") },
  { key: "code", header: "Code", mono: true },
  { key: "message", header: "Message" },
];

/** BR-43.5 lodging cap: a stay above 1.5 times the per-diem lodging rate per night needs a justification. Stores nothing. */
export function LodgingCheckPage() {
  const [values, setValues] = useState<LodgingValues>(emptyLodging);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Pick<Problem, "code" | "message"> | null>(null);
  const [result, setResult] = useState<LodgingResult | null>(null);
  const [busy, setBusy] = useState(false);

  function update(patch: Partial<LodgingValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateLodging(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await checkLodging(toLodgingInput(values)));
    } catch (error) {
      const split = splitProblem(describeProblem(error), SHOWN);
      setResult(null);
      setErrors(split.fields);
      setFailure(split.failure);
    } finally {
      setBusy(false);
    }
  }

  function field(name: keyof LodgingValues, label: string, opts: { hint?: string; optional?: boolean; mono?: boolean } = {}) {
    return (
      <FormField id={`lodging-${name}`} label={label} hint={opts.hint} optional={opts.optional} error={errors[name]}>
        {(c) => (
          <Input
            {...c}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.mono ? "decimal" : undefined}
            value={values[name]}
            onChange={(e) => update({ [name]: e.target.value })}
            invalid={Boolean(errors[name])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Lodging cap"
        description="A hotel stay may cost up to 1.5 times the per-diem lodging rate for each night. Above that it needs a justification before the report can be submitted. Nothing is saved."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("amount", "Cost of the stay", { mono: true })}
          {field("nights", "Nights")}
          {field("lodgingRate", "Per-diem lodging rate per night", { mono: true, hint: "From the per diem table for the city and date." })}
          <div className={styles.wide}>{field("justification", "Justification", { optional: true })}</div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not check the stay" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Check stay
            </Button>
          </div>
        </form>
      </Card>
      <Card title="Result">
        {result ? (
          <div className={styles.stack}>
            {!result.overCap ? (
              <Alert tone="success" title="Within the lodging cap" />
            ) : result.satisfied ? (
              <Alert tone="info" title="Over the cap, justified" />
            ) : (
              <Alert tone="warning" title="Over the cap: add a justification" />
            )}
            <StatGroup label="Lodging cap">
              <StatTile label="Cap per night" value={result.capPerNight} />
              <StatTile label="Cap for the stay" value={result.cap} />
            </StatGroup>
            <DataTable
              label="Policy results"
              columns={resultColumns}
              rows={result.result ? [result.result] : []}
              getRowKey={(r) => r.ruleId}
              empty={{ title: "No lodging rule applies to this stay" }}
            />
          </div>
        ) : busy ? (
          <DataTable label="Policy results" columns={resultColumns} rows={[]} getRowKey={(r) => r.ruleId} loading />
        ) : (
          <EmptyState icon="receipt" title="No stay checked yet" description="Enter the stay and the per-diem lodging rate, then check." compact />
        )}
      </Card>
    </div>
  );
}
