import { useState, type FormEvent } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, Card, FormField, Input, PageHeader, Select, StatusPill } from "../../shell/ui";
import { describeProblem, validateTaxRegistration, type Problem, type TaxType, type TaxValidation } from "./api/client";
import { COUNTRY, DUTY_WRITE, taxTypeLabel } from "./format";
import styles from "./pages.module.css";
import { Entry, problemText } from "./parts";

const TYPES: TaxType[] = ["VAT", "EIN", "GSTIN"];

interface Values {
  type: TaxType;
  country: string;
  number: string;
}

const empty: Values = { type: "VAT", country: "", number: "" };

function validate(values: Values): Record<string, string> {
  const errors: Record<string, string> = {};
  if (values.number.trim() === "") errors.number = "Enter the tax number.";
  if (values.type === "VAT" && !COUNTRY.test(values.country.trim())) {
    errors.country = "Enter the two-letter country code, for example DE.";
  }
  if (values.type !== "VAT" && values.country.trim() !== "" && !COUNTRY.test(values.country.trim())) {
    errors.country = "Enter the two-letter country code, for example DE.";
  }
  return errors;
}

/** Format-check a VAT, EIN or GSTIN (POST /v1/party/tax-registrations:validate). */
export function TaxRegistrationsPage() {
  const canValidate = holdsDuty(usePrincipalDuties(), DUTY_WRITE);
  const [values, setValues] = useState<Values>(empty);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<TaxValidation | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validate(values);
    setErrors(found);
    setFailure(null);
    setResult(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const country = values.country.trim().toUpperCase();
      setResult(
        await validateTaxRegistration({
          type: values.type,
          number: values.number.trim(),
          country: country || undefined,
        }),
      );
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Tax registrations"
        description="Check a VAT, EIN or GSTIN against the country format. VAT is also looked up in VIES when that adapter is configured; otherwise the result stays unverified."
      />
      {canValidate ? (
        <form className={styles.form} onSubmit={(event) => void submit(event)} noValidate aria-label="Validate a tax registration">
          <FormField id="tax-type" label="Type">
            {(control) => (
              <Select
                {...control}
                value={values.type}
                onChange={(event) => setValues({ ...values, type: event.target.value as TaxType })}
              >
                {TYPES.map((type) => (
                  <option key={type} value={type}>
                    {taxTypeLabel[type]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField
            id="tax-country"
            label="Country"
            optional={values.type !== "VAT"}
            hint="ISO 3166-1 alpha-2. Required for VAT."
            error={errors.country}
          >
            {(control) => (
              <Input
                {...control}
                value={values.country}
                maxLength={2}
                placeholder="DE"
                className={styles.mono}
                onChange={(event) => setValues({ ...values, country: event.target.value.toUpperCase() })}
                invalid={Boolean(errors.country)}
              />
            )}
          </FormField>
          <FormField id="tax-number" label="Number" error={errors.number}>
            {(control) => (
              <Input
                {...control}
                value={values.number}
                className={styles.mono}
                onChange={(event) => setValues({ ...values, number: event.target.value })}
                invalid={Boolean(errors.number)}
              />
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Validate
            </Button>
          </div>
        </form>
      ) : (
        <Alert tone="info" title="Validation needs party.write">
          Checking a tax number is a write on the party service. Ask for the party.write duty to run this check.
        </Alert>
      )}
      {failure ? (
        <Alert tone="danger" title="Could not validate the number" live="alert">
          {problemText(failure)}
        </Alert>
      ) : null}
      {result ? (
        <Card title="Result">
          <dl className={styles.details} aria-label="Tax validation result">
            <Entry term="Type">{taxTypeLabel[result.type] ?? result.type}</Entry>
            <Entry term="Number" mono>
              {result.number}
            </Entry>
            {result.country ? (
              <Entry term="Country" mono>
                {result.country}
              </Entry>
            ) : null}
            <Entry term="Status">
              <StatusPill status={result.validationStatus} />
            </Entry>
          </dl>
        </Card>
      ) : null}
    </div>
  );
}
