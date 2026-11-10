import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createCommodityBasis, describeProblem, newKey, type CommodityBasisInput, type Problem } from "./api/client";
import { UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

type Errors = Record<string, string>;

export const emptyBasis: CommodityBasisInput = { legalEntityId: "", code: "", name: "", currency: "USD", unit: "" };

/** Client checks that mirror the service (FR-26.21); the service has the last word. */
export function validateBasis(v: CommodityBasisInput): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (!/^[A-Z0-9_-]{1,32}$/.test(v.code.trim())) errors.code = "Use capitals, digits, - and _ (up to 32 characters).";
  if (v.name.trim() === "" || v.name.trim().length > 120) errors.name = "Enter a name (up to 120 characters).";
  if (!/^[A-Z]{3}$/.test(v.currency.trim())) errors.currency = "Enter a three-letter currency code in capitals.";
  if (v.unit.trim() === "" || v.unit.trim().length > 16) errors.unit = "Enter the unit the index is quoted per (up to 16 characters).";
  return errors;
}

export function toBasisInput(v: CommodityBasisInput): CommodityBasisInput {
  return { legalEntityId: v.legalEntityId.trim(), code: v.code.trim(), name: v.name.trim(), currency: v.currency.trim(), unit: v.unit.trim() };
}

/** Create a commodity cost basis type (FR-26.21). */
export function NewCommodityBasisPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<CommodityBasisInput>(emptyBasis);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CommodityBasisInput>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateBasis(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createCommodityBasis(toBasisInput(values), key.current);
      navigate(`/pricing/commodity/bases/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function text(id: string, field: keyof CommodityBasisInput, label: string, opts: { hint?: string; mono?: boolean } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} error={errors[field]}>
        {(c) => (
          <Input
            {...c}
            className={opts.mono ? styles.mono : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<CommodityBasisInput>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New cost basis type" description="The thing a commodity index is quoted for, in a currency and unit." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <LookupFormField id="cb-legal-entity" label="Legal entity" entity="legalEntity" value={values.legalEntityId} error={errors.legalEntityId} onChange={(legalEntityId) => update({ legalEntityId })} />
          {text("cb-code", "code", "Code", { mono: true, hint: "For example CU-LME. Unique in the legal entity." })}
          {text("cb-name", "name", "Name")}
          {text("cb-currency", "currency", "Currency", { mono: true, hint: "The currency the index is quoted in." })}
          {text("cb-unit", "unit", "Quoted per", { mono: true, hint: "The unit the index is quoted per, for example MT." })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the cost basis type" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save cost basis type
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/commodity/bases">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
