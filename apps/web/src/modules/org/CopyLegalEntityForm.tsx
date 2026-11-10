import { useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, Select } from "../../shell/ui";
import { describeProblem, type LegalEntity, type Problem } from "./api/client";
import { listCountries, listCurrencies } from "./api/reference";
import { copyLegalEntity, type LegalEntityCopy } from "./api/structure";
import { codeProblems, validateLegalEntity, type LegalEntityErrors, type LegalEntityValues } from "./legalEntityValues";
import styles from "./pages.module.css";
import { useRows } from "./referenceParts";
import { ProblemMessage } from "../../shell/errors";

interface Include {
  sites: boolean;
  locations: boolean;
  roundingRules: boolean;
}

// copySummary is the one-line result shown after a copy.
export function copySummary(source: string, copy: LegalEntityCopy): string {
  const parts = [`${copy.sites} sites`, `${copy.warehouses} warehouses`, `${copy.locations} locations`, `${copy.roundingRules} rounding rules`];
  return `Copied ${source} to ${copy.legalEntity.code}: ${parts.join(", ")}${copy.calendarCopied ? ", fiscal calendar assigned" : ""}. Number sequences and parameters are set up by their own services.`;
}

interface Props {
  source: LegalEntity;
  onCopied(copy: LegalEntityCopy): void;
  onCancel(): void;
}

// CopyLegalEntityForm creates a legal entity from `source` (FR-6.15): the user gives the code, name, country and
// accounting currency, and chooses whether sites (with warehouses), locations and rounding rules come along.
export function CopyLegalEntityForm({ source, onCopied, onCancel }: Props) {
  const countries = useRows((signal) => listCountries("active", { signal }), "countries");
  const currencies = useRows((signal) => listCurrencies("active", { signal }), "currencies");
  const [values, setValues] = useState<LegalEntityValues>({ code: "", name: "", country: "", currency: "", timeZone: source.timeZone });
  const [include, setInclude] = useState<Include>({ sites: true, locations: true, roundingRules: true });
  const [errors, setErrors] = useState<LegalEntityErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateLegalEntity(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    setBusy(true);
    try {
      const copy = await copyLegalEntity(source.id, {
        ...values,
        code: values.code.toUpperCase(),
        name: values.name.trim(),
        timeZone: values.timeZone.trim(),
        include: { ...include, locations: include.sites && include.locations },
      });
      onCopied(copy);
    } catch (error) {
      const problem = describeProblem(error);
      if (codeProblems[problem.code]) {
        setErrors({ code: codeProblems[problem.code] });
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  };

  const text = (key: keyof LegalEntityValues) => ({
    value: values[key],
    onChange: (event: { target: { value: string } }) => setValues({ ...values, [key]: event.target.value }),
  });
  const check = (key: keyof Include, label: string, disabled = false) => (
    <label className={styles.checkRow}>
      <input type="checkbox" checked={include[key] && !disabled} disabled={disabled} onChange={(event) => setInclude({ ...include, [key]: event.target.checked })} />
      {label}
    </label>
  );
  const loadFailure = countries.error ?? currencies.error;

  return (
    <Card title={`Copy legal entity ${source.code}`}>
      <form onSubmit={submit} noValidate className={styles.form} aria-label={`Copy legal entity ${source.code}`}>
        <FormField id="copy-code" label="New code" error={errors.code}>
          {(control) => <Input {...control} {...text("code")} maxLength={8} />}
        </FormField>
        <FormField id="copy-name" label="New name" error={errors.name}>
          {(control) => <Input {...control} {...text("name")} maxLength={120} />}
        </FormField>
        <FormField id="copy-country" label="New country" error={errors.country}>
          {(control) => (
            <Select {...control} {...text("country")}>
              <option value="">Select a country</option>
              {countries.rows.map((row) => (
                <option key={row.code} value={row.code}>
                  {row.code} - {row.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="copy-currency" label="New accounting currency" error={errors.currency}>
          {(control) => (
            <Select {...control} {...text("currency")}>
              <option value="">Select a currency</option>
              {currencies.rows.map((row) => (
                <option key={row.code} value={row.code}>
                  {row.code} - {row.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="copy-time-zone" label="New time zone" hint={`IANA name; ${source.code} uses ${source.timeZone}.`} error={errors.timeZone}>
          {(control) => <Input {...control} {...text("timeZone")} />}
        </FormField>
        <fieldset className={styles.checkGroup}>
          <legend>Copy from {source.code}</legend>
          {check("sites", "Active sites and warehouses")}
          {check("locations", "Active locations", !include.sites)}
          {check("roundingRules", "Currency rounding rules")}
          <p className={styles.hint}>The fiscal calendar assignment is always copied. Addresses, operating units, teams and transactions are not.</p>
        </fieldset>
        {loadFailure ? (
          <Alert tone="danger" title="Could not load the country and currency lists" live="alert">
            <ProblemMessage problem={loadFailure} />
          </Alert>
        ) : null}
        {failure ? (
          <Alert tone="danger" title="Could not copy the legal entity" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        <div className={styles.rowActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Copy legal entity
          </Button>
          <Button type="button" variant="ghost" onClick={onCancel}>
            Cancel copy
          </Button>
        </div>
      </form>
    </Card>
  );
}
