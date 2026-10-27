import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createProcedure, describeProblem, getProcedure, newKey, type ProcedureInput, type ProcedureStep, type Problem } from "./api/client";
import { PROCEDURE_NAME } from "./campaignFormat";
import { UUID } from "./format";
import styles from "./pages.module.css";
import { cleanSteps, ProcedureStepsEditor, referenceSteps, validateSteps } from "./ProcedureStepsEditor";

type Errors = Record<string, string>;

/** Client checks that mirror the service (FR-26.1); the service has the last word. */
export function validateProcedure(v: ProcedureInput): Errors {
  const errors: Errors = validateSteps(v.steps);
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!PROCEDURE_NAME.test(v.name.trim())) errors.name = "Use upper-case letters, digits, _ or -, such as AX_STANDARD.";
  return errors;
}

/** Save a new DRAFT version; ?from=<id> starts from an existing version (the next version of it). */
export function NewProcedurePage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const from = params.get("from");
  const [values, setValues] = useState<ProcedureInput>({ legalEntityId: "", name: "AX_STANDARD", steps: referenceSteps() });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  useEffect(() => {
    if (!from) return;
    const controller = new AbortController();
    getProcedure(from, controller.signal).then(
      (p) => {
        setValues({ legalEntityId: p.legalEntityId, name: p.name, steps: p.steps });
        key.current = newKey();
      },
      (error: unknown) => {
        if (!controller.signal.aborted) setFailure(describeProblem(error));
      },
    );
    return () => controller.abort();
  }, [from]);

  function update(patch: Partial<ProcedureInput>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateProcedure(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createProcedure({ legalEntityId: values.legalEntityId.trim(), name: values.name.trim(), steps: cleanSteps(values.steps) }, key.current);
      navigate(`/pricing/procedures/${created.id}`);
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
      <PageHeader
        title={from ? "New procedure version" : "New pricing procedure"}
        description="The version is saved as a draft with the next version number for the legal entity and name. You can change a draft until you activate it."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="pp-legal-entity" label="Legal entity id" error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="pp-name" label="Procedure name" hint="For example AX_STANDARD or SAP_RVAA01." error={errors.name}>
            {(c) => <Input {...c} className={styles.mono} value={values.name} onChange={(e) => update({ name: e.target.value.toUpperCase() })} invalid={Boolean(errors.name)} />}
          </FormField>
          <ProcedureStepsEditor idPrefix="pp-step" steps={values.steps} errors={errors} onChange={(steps: ProcedureStep[]) => update({ steps })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the procedure" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save draft
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/procedures">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
