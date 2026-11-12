import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createSalesGroup, describeProblem, newKey, type Problem, type SalesGroupInput } from "./api/client";
import { DECIMAL, sumDecimals, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface RepDraft {
  repId: string;
  sharePct: string;
}

export interface SalesGroupDraft {
  legalEntityId: string;
  name: string;
  reps: RepDraft[];
}

type Errors = Record<string, string>;

export const emptySalesGroup: SalesGroupDraft = { legalEntityId: "", name: "", reps: [{ repId: "", sharePct: "100" }] };

export const MAX_REPS = 20;

/** Client checks that mirror the service (FR-26.18, BR-26.12); the service has the last word. */
export function validateSalesGroup(v: SalesGroupDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (v.name.trim() === "" || v.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  const seen = new Set<string>();
  v.reps.forEach((r, i) => {
    const id = r.repId.trim();
    if (id === "" || id.length > 64) errors[`reps[${i}].repId`] = "Choose the sales rep.";
    else if (/[/:]/.test(id) || id === "." || id === "..") errors[`reps[${i}].repId`] = "Choose the sales rep from the list.";
    else if (seen.has(id)) errors[`reps[${i}].repId`] = "Each rep appears once.";
    seen.add(id);
    if (!DECIMAL.test(r.sharePct) || Number(r.sharePct) <= 0 || Number(r.sharePct) > 100) errors[`reps[${i}].sharePct`] = "Enter a share above 0 and up to 100.";
  });
  if (!Object.keys(errors).some((k) => k.startsWith("reps[")) && sumDecimals(v.reps.map((r) => r.sharePct)) !== "100") {
    errors.reps = `The shares add up to ${sumDecimals(v.reps.map((r) => r.sharePct))}%; they must add up to 100%.`;
  }
  return errors;
}

export function toSalesGroupInput(v: SalesGroupDraft): SalesGroupInput {
  return { legalEntityId: v.legalEntityId.trim(), name: v.name.trim(), reps: v.reps.map((r) => ({ repId: r.repId.trim(), sharePct: r.sharePct })) };
}

/** Create a sales group with its reps and shares (FR-26.18). */
export function NewSalesGroupPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<SalesGroupDraft>(emptySalesGroup);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<SalesGroupDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }
  const setRep = (i: number, patch: Partial<RepDraft>) => update({ reps: values.reps.map((r, j) => (j === i ? { ...r, ...patch } : r)) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateSalesGroup(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createSalesGroup(toSalesGroupInput(values), key.current);
      navigate(`/pricing/commission/sales-groups/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New sales group" description="The reps share every commission the group earns. The first rep takes any rounding cent." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="sg-legal-entity" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <EntityLookup entity="legalEntity" {...c} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="sg-name" label="Sales group name" error={errors.name}>
            {(c) => <Input {...c} value={values.name} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          {values.reps.map((r, i) => (
            <fieldset key={i} className={styles.lineSet}>
              <legend className={styles.groupsLegend}>{`Rep ${i + 1}`}</legend>
              <FormField id={`sg-rep-${i}`} label="Sales rep" hint="A worker; pricing keeps only the worker's id, never the name." error={errors[`reps[${i}].repId`]}>
                {(c) => <EntityLookup {...c} entity="worker" value={r.repId} onChange={(e) => setRep(i, { repId: e.target.value })} invalid={Boolean(errors[`reps[${i}].repId`])} />}
              </FormField>
              <FormField id={`sg-share-${i}`} label="Share %" error={errors[`reps[${i}].sharePct`]}>
                {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={r.sharePct} onChange={(e) => setRep(i, { sharePct: e.target.value })} invalid={Boolean(errors[`reps[${i}].sharePct`])} />}
              </FormField>
              {values.reps.length > 1 ? (
                <Button variant="ghost" size="sm" onClick={() => update({ reps: values.reps.filter((_, j) => j !== i) })}>
                  {`Remove rep ${i + 1}`}
                </Button>
              ) : null}
            </fieldset>
          ))}
          <div className={styles.formActions}>
            {values.reps.length < MAX_REPS ? (
              <Button variant="secondary" onClick={() => update({ reps: [...values.reps, { repId: "", sharePct: "" }] })}>
                Add rep
              </Button>
            ) : null}
            <span className={styles.note}>{`Shares add up to ${sumDecimals(values.reps.map((r) => (DECIMAL.test(r.sharePct) ? r.sharePct : "0")))}%.`}</span>
          </div>
          {errors.reps ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Check the shares">
                {errors.reps}
              </Alert>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the sales group" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save sales group
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/commission/sales-groups">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
