import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { createSetupMatrix, describeProblem, newKey, type NewSetupMatrix, type Problem, type SetupEntry } from "./api/client";
import { today } from "./format";
import { CODE, CURRENCY, DATE, decimalText, mapFields, wholeNumber, WEEK_MINUTES } from "./forms";
import styles from "./pages.module.css";
import { ResourceGate } from "./ResourceGate";
import { ProblemMessage } from "../../shell/errors";

interface Values {
  resourceId: string;
  activeFrom: string;
  currency: string;
  entries: string;
}

type Errors = Partial<Record<keyof Values, string>>;

/**
 * Reads one changeover per line: from group, to group, minutes, cost and an
 * optional cleaning operation set, separated by commas.
 */
export function parseEntries(text: string): { entries: SetupEntry[]; error?: string } {
  const entries: SetupEntry[] = [];
  const seen = new Set<string>();
  const lines = text.split("\n").map((l) => l.trim());
  for (const [i, line] of lines.entries()) {
    if (line === "") continue;
    const at = `Line ${i + 1}`;
    const parts = line.split(",").map((p) => p.trim());
    if (parts.length < 4 || parts.length > 5) return { entries, error: `${at}: write from, to, minutes, cost and an optional cleaning set.` };
    const [fromRaw, toRaw, minutesRaw, costRaw, cleaning = ""] = parts;
    const fromGroup = fromRaw.toUpperCase();
    const toGroup = toRaw.toUpperCase();
    if (!CODE.test(fromGroup) || !CODE.test(toGroup)) return { entries, error: `${at}: setup groups are 1 to 32 capital letters, digits, _ or -.` };
    if (fromGroup === toGroup) return { entries, error: `${at}: a group needs no changeover to itself.` };
    const minutes = wholeNumber(minutesRaw, 0, WEEK_MINUTES);
    if (minutes === null) return { entries, error: `${at}: minutes must be 0 to ${WEEK_MINUTES}.` };
    const cost = decimalText(costRaw, 6, 14);
    if (cost === null) return { entries, error: `${at}: cost must be a non-negative amount such as 45.50.` };
    if (cleaning.length > 64) return { entries, error: `${at}: the cleaning set is at most 64 characters.` };
    const pair = `${fromGroup}->${toGroup}`;
    if (seen.has(pair)) return { entries, error: `${at}: ${pair} is listed twice.` };
    seen.add(pair);
    const entry: SetupEntry = { fromGroup, toGroup, minutes, cost };
    if (cleaning !== "") entry.cleaningSetId = cleaning;
    entries.push(entry);
  }
  if (entries.length === 0) return { entries, error: "List at least one changeover." };
  return { entries };
}

export function validateMatrix(values: Values): { errors: Errors; input?: NewSetupMatrix } {
  const errors: Errors = {};
  if (values.resourceId === "") errors.resourceId = "Choose the resource.";
  if (!DATE.test(values.activeFrom)) errors.activeFrom = "Enter the first day the matrix applies.";
  if (!CURRENCY.test(values.currency)) errors.currency = "Enter a three-letter currency code.";
  const parsed = parseEntries(values.entries);
  if (parsed.error) errors.entries = parsed.error;
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { resourceId: values.resourceId, activeFrom: values.activeFrom, currency: values.currency, entries: parsed.entries } };
}

const fieldNames: Record<string, keyof Values> = { resourceId: "resourceId", activeFrom: "activeFrom", currency: "currency", entries: "entries" };

/** Save the next version of a resource's setup matrix. */
export function NewSetupMatrixPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({ resourceId: "", activeFrom: today(), currency: "USD", entries: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateMatrix(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const matrix = await createSetupMatrix(input, key.current);
      navigate(`/scheduling/setup-matrices/${matrix.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = mapFields(problem.fields, fieldNames);
      if (problem.code === "SCHED-0107") fieldErrors.resourceId = "This resource no longer exists.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New setup matrix version" description="Each save is a new version; campaigns use the highest version active on their planned start." />
      <Card>
        <ResourceGate need="A setup matrix belongs to one resource.">
          {(resources) => (
            <form className={styles.form} onSubmit={submit} noValidate>
              <FormField id="matrix-resource" label="Resource" error={errors.resourceId}>
                {(control) => (
                  <Select {...control} value={values.resourceId} onChange={(event) => update({ resourceId: event.target.value })} invalid={Boolean(errors.resourceId)}>
                    <option value="">Choose a resource</option>
                    {resources.map((r) => (
                      <option key={r.id} value={r.id}>
                        {`${r.code} · ${r.name}`}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="matrix-from" label="Active from" error={errors.activeFrom}>
                {(control) => <Input {...control} type="date" value={values.activeFrom} onChange={(event) => update({ activeFrom: event.target.value })} invalid={Boolean(errors.activeFrom)} />}
              </FormField>
              <FormField id="matrix-currency" label="Cost currency" error={errors.currency}>
                {(control) => (
                  <Input {...control} maxLength={3} value={values.currency} onChange={(event) => update({ currency: event.target.value.toUpperCase() })} invalid={Boolean(errors.currency)} />
                )}
              </FormField>
              <div className={styles.wide}>
                <FormField
                  id="matrix-entries"
                  label="Changeovers"
                  hint="One per line: from group, to group, minutes, cost, optional cleaning set. Example: PEANUT, PEANUT-FREE, 240, 900.00, WET-CLEAN"
                  error={errors.entries}
                >
                  {(control) => (
                    <Textarea {...control} rows={8} value={values.entries} onChange={(event) => update({ entries: event.target.value })} invalid={Boolean(errors.entries)} />
                  )}
                </FormField>
              </div>
              {failure ? (
                <div className={styles.formActions}>
                  <Alert tone="danger" title="Could not save the matrix" live="alert">
                    <ProblemMessage problem={failure} />
                  </Alert>
                </div>
              ) : null}
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy}>
                  Save matrix
                </Button>
                <Link className={buttonClassName("ghost")} to="/scheduling/setup-matrices">
                  Cancel
                </Link>
              </div>
            </form>
          )}
        </ResourceGate>
      </Card>
    </div>
  );
}
