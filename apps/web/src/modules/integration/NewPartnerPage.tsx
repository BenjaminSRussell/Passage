import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { ACK_SLA, createPartner, describeProblem, ENVIRONMENTS, newKey, type Environment, type NewPartner, type Problem } from "./api/client";
import styles from "./pages.module.css";

interface Values {
  name: string;
  isaQualifier: string;
  isaId: string;
  environment: Environment;
  /** Empty means the service default (60 minutes). */
  ackSlaMinutes: string;
}

type Field = keyof Values;
type Errors = Partial<Record<Field, string>>;

const QUALIFIER = /^[A-Z0-9]{2}$/;
const ISA_ID = /^[A-Za-z0-9]{1,15}$/;
const WHOLE = /^\d{1,4}$/;

/** Checks that mirror the service (domain.PartnerProfile.Validate), and the request the form sends. */
export function validatePartner(values: Values): { errors: Errors; input?: NewPartner } {
  const errors: Errors = {};
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter the partner name, up to 120 characters.";
  if (!QUALIFIER.test(values.isaQualifier.trim())) errors.isaQualifier = "Enter the two-character ISA05 qualifier, such as ZZ or 01.";
  if (!ISA_ID.test(values.isaId.trim())) errors.isaId = "Enter the ISA06 sender id: 1 to 15 letters or digits.";
  const sla = values.ackSlaMinutes.trim();
  if (sla !== "" && (!WHOLE.test(sla) || Number(sla) < ACK_SLA.min || Number(sla) > ACK_SLA.max)) {
    errors.ackSlaMinutes = `Enter whole minutes from ${ACK_SLA.min} to ${ACK_SLA.max}, or leave it empty for ${ACK_SLA.default}.`;
  }
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewPartner = { name, isaQualifier: values.isaQualifier.trim(), isaId: values.isaId.trim(), environment: values.environment };
  if (sla !== "") input.ackSlaMinutes = Number(sla);
  return { errors, input };
}

/** Create a trading partner profile (FR-35.1). */
export function NewPartnerPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({ name: "", isaQualifier: "ZZ", isaId: "", environment: "TEST", ackSlaMinutes: "" });
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
    const { errors: found, input } = validatePartner(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const partner = await createPartner(input, key.current);
      navigate(`/integration/partners/${partner.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["name", "isaQualifier", "isaId", "environment", "ackSlaMinutes"] as const) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      if (problem.code === "EDI-0004") fieldErrors.isaId = "A partner with this ISA qualifier and id already exists in this environment.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: Exclude<Field, "environment">, label: string, hint?: string, optional?: boolean, extra?: Record<string, unknown>) => (
    <FormField id={`partner-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input
          {...control}
          {...extra}
          value={values[id]}
          onChange={(event) => update({ [id]: event.target.value } as Partial<Values>)}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New trading partner"
        description="The ISA qualifier and id must match what the partner sends in ISA05 and ISA06. One profile per identity and environment."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("name", "Partner name", undefined, false, { maxLength: 120 })}
          {text("isaQualifier", "ISA qualifier", "ISA05, two characters, such as ZZ (mutually defined) or 01 (DUNS).", false, { maxLength: 2 })}
          {text("isaId", "ISA sender id", "ISA06 without the trailing spaces.", false, { maxLength: 15 })}
          <FormField id="partner-environment" label="Environment" hint="TEST profiles only receive interchanges sent to the TEST environment.">
            {(control) => (
              <Select {...control} value={values.environment} onChange={(event) => update({ environment: event.target.value as Environment })}>
                {ENVIRONMENTS.map((env) => (
                  <option key={env} value={env}>
                    {env}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text(
            "ackSlaMinutes",
            "997 expected within (minutes)",
            `How long to wait for the partner's 997. ${ACK_SLA.min} to ${ACK_SLA.max}; empty means ${ACK_SLA.default}.`,
            true,
            { inputMode: "numeric" },
          )}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the partner" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create partner
            </Button>
            <Link className={buttonClassName("ghost")} to="/integration/partners">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
