import { useState, type FormEvent, type ReactNode } from "react";
import { Alert, Button, Input, FormField, Select } from "../../shell/ui";
import type { CharacteristicDefinition, CharacteristicType, NewProcessCategory, ProcessDestination, Problem } from "./api/client";
import { DESTINATIONS, DESTINATION_LABELS } from "./processLabels";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const DECIMAL = /^-?\d+(\.\d+)?$/;
const CODE = /^[a-z0-9][a-z0-9-]*$/;
const MAX_DEFS = 50;

interface DefRow {
  name: string;
  type: CharacteristicType;
  unit: string;
  min: string;
  max: string;
  required: boolean;
}

export interface CategoryValues {
  code: string;
  name: string;
  destinations: ProcessDestination[];
  rejectOutOfLimit: boolean;
  active: boolean;
  defs: DefRow[];
}

type Errors = Record<string, string>;

const emptyDef = (): DefRow => ({ name: "", type: "numeric", unit: "", min: "", max: "", required: false });

export function emptyCategoryValues(): CategoryValues {
  return { code: "", name: "", destinations: ["ebr"], rejectOutOfLimit: false, active: true, defs: [] };
}

/** Form values of an existing category, for the edit form. */
export function categoryValues(c: { code: string; name: string; destinations: ProcessDestination[]; rejectOutOfLimit: boolean; active: boolean; characteristics: CharacteristicDefinition[] }): CategoryValues {
  return {
    code: c.code,
    name: c.name,
    destinations: c.destinations,
    rejectOutOfLimit: c.rejectOutOfLimit,
    active: c.active,
    defs: c.characteristics.map((d) => ({ name: d.name, type: d.type, unit: d.unit ?? "", min: d.min ?? "", max: d.max ?? "", required: d.required })),
  };
}

/** Checks that mirror the service, and the request the form sends. creating also checks the code. */
export function validateCategory(values: CategoryValues, creating: boolean): { errors: Errors; input?: NewProcessCategory } {
  const errors: Errors = {};
  const code = values.code.trim();
  const name = values.name.trim();
  if (creating && !(code.length >= 1 && code.length <= 32 && CODE.test(code))) errors.code = "Use up to 32 lowercase letters, digits or hyphens.";
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (values.destinations.length < 1) errors.destinations = "Choose at least one destination.";
  const seen = new Set<string>();
  const characteristics: CharacteristicDefinition[] = [];
  values.defs.forEach((row, i) => {
    const key = (field: string) => `defs[${i}].${field}`;
    const defName = row.name.trim();
    if (defName.length < 1 || defName.length > 64) errors[key("name")] = "Enter a name of up to 64 characters.";
    else if (seen.has(defName)) errors[key("name")] = "Each characteristic is defined once.";
    seen.add(defName);
    const def: CharacteristicDefinition = { name: defName, type: row.type, required: row.required };
    if (row.unit.trim()) def.unit = row.unit.trim();
    if (row.type === "numeric") {
      const lo = row.min.trim();
      const hi = row.max.trim();
      if (lo) {
        if (DECIMAL.test(lo)) def.min = lo;
        else errors[key("min")] = "Enter the lower limit as a decimal number, or leave it empty.";
      }
      if (hi) {
        if (DECIMAL.test(hi)) def.max = hi;
        else errors[key("max")] = "Enter the upper limit as a decimal number, or leave it empty.";
      }
      if (def.min && def.max && Number(def.min) > Number(def.max)) errors[key("min")] = "The lower limit is above the upper limit.";
    }
    characteristics.push(def);
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { code, name, destinations: values.destinations, rejectOutOfLimit: values.rejectOutOfLimit, characteristics, active: values.active } };
}

/** The service names a definition's fields characteristics[i].field; the form's rows are defs[i]. */
function serviceFieldErrors(fields: Errors = {}): Errors {
  return Object.fromEntries(Object.entries(fields).map(([key, message]) => [key.replace(/^characteristics\[/, "defs["), message]));
}

interface Props {
  initial: CategoryValues;
  /** Existing categories have a fixed code. */
  creating: boolean;
  submitLabel: string;
  failureTitle: string;
  failure: Problem | null;
  busy: boolean;
  fieldErrors?: Errors;
  onSubmit(input: NewProcessCategory): void;
  onChange?(): void;
  actions?: ReactNode;
}

/** The form shared by the create and the edit page of a process message category. */
export function ProcessCategoryForm({ initial, creating, submitLabel, failureTitle, failure, busy, fieldErrors, onSubmit, onChange, actions }: Props) {
  const [values, setValues] = useState<CategoryValues>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const shown = { ...errors, ...serviceFieldErrors(fieldErrors) };

  function update(patch: Partial<CategoryValues>) {
    setValues({ ...values, ...patch });
    onChange?.();
  }

  function updateDef(i: number, patch: Partial<DefRow>) {
    update({ defs: values.defs.map((d, j) => (j === i ? { ...d, ...patch } : d)) });
  }

  function toggleDestination(dest: ProcessDestination, on: boolean) {
    update({ destinations: DESTINATIONS.filter((d) => (d === dest ? on : values.destinations.includes(d))) });
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCategory(values, creating);
    setErrors(found);
    if (input) onSubmit(input);
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate>
      <FormField id="cat-code" label="Code" hint={creating ? "Lowercase letters, digits and hyphens. A built-in code replaces that default." : "The code does not change."} error={shown.code}>
        {(control) => <Input {...control} maxLength={32} value={values.code} readOnly={!creating} onChange={(event) => update({ code: event.target.value })} invalid={Boolean(shown.code)} />}
      </FormField>
      <FormField id="cat-name" label="Name" error={shown.name}>
        {(control) => <Input {...control} maxLength={120} value={values.name} onChange={(event) => update({ name: event.target.value })} invalid={Boolean(shown.name)} />}
      </FormField>
      <fieldset className={styles.fieldset}>
        <legend className={styles.legend}>Destinations</legend>
        <p className={styles.hint}>A message of this category is delivered to each destination ticked.</p>
        {DESTINATIONS.map((dest) => (
          <label key={dest} className={styles.checkRow} htmlFor={`cat-dest-${dest}`}>
            <Input id={`cat-dest-${dest}`} type="checkbox" checked={values.destinations.includes(dest)} onChange={(event) => toggleDestination(dest, event.target.checked)} />
            {DESTINATION_LABELS[dest]}
          </label>
        ))}
        {shown.destinations ? <p role="alert">{shown.destinations}</p> : null}
      </fieldset>
      <label className={styles.checkRow} htmlFor="cat-reject">
        <Input id="cat-reject" type="checkbox" checked={values.rejectOutOfLimit} onChange={(event) => update({ rejectOutOfLimit: event.target.checked })} />
        Refuse messages with a value outside its limits (otherwise they are accepted and flagged for the batch record)
      </label>
      {!creating ? (
        <label className={styles.checkRow} htmlFor="cat-active">
          <Input id="cat-active" type="checkbox" checked={values.active} onChange={(event) => update({ active: event.target.checked })} />
          Active (an inactive category refuses new messages)
        </label>
      ) : null}
      {values.defs.map((row, i) => {
        const err = (field: string) => shown[`defs[${i}].${field}`];
        const n = i + 1;
        return (
          <fieldset key={i} className={styles.valueGroup}>
            <legend className={styles.legend}>{`Characteristic ${n}`}</legend>
            <FormField id={`cat-d${i}-name`} label={`Characteristic ${n} name`} error={err("name")}>
              {(control) => <Input {...control} maxLength={64} value={row.name} invalid={Boolean(err("name"))} onChange={(event) => updateDef(i, { name: event.target.value })} />}
            </FormField>
            <FormField id={`cat-d${i}-type`} label={`Characteristic ${n} type`}>
              {(control) => (
                <Select {...control} value={row.type} onChange={(event) => updateDef(i, { type: event.target.value as CharacteristicType, min: "", max: "" })}>
                  <option value="numeric">Number</option>
                  <option value="text">Text</option>
                  <option value="boolean">Yes or no</option>
                </Select>
              )}
            </FormField>
            {row.type === "numeric" ? (
              <>
                <FormField id={`cat-d${i}-min`} label={`Characteristic ${n} lower limit`} optional error={err("min")}>
                  {(control) => <Input {...control} inputMode="decimal" value={row.min} invalid={Boolean(err("min"))} onChange={(event) => updateDef(i, { min: event.target.value })} />}
                </FormField>
                <FormField id={`cat-d${i}-max`} label={`Characteristic ${n} upper limit`} optional error={err("max")}>
                  {(control) => <Input {...control} inputMode="decimal" value={row.max} invalid={Boolean(err("max"))} onChange={(event) => updateDef(i, { max: event.target.value })} />}
                </FormField>
              </>
            ) : null}
            <FormField id={`cat-d${i}-unit`} label={`Characteristic ${n} unit`} optional>
              {(control) => <Input {...control} maxLength={16} value={row.unit} onChange={(event) => updateDef(i, { unit: event.target.value })} />}
            </FormField>
            <label className={styles.checkRow} htmlFor={`cat-d${i}-required`}>
              <Input id={`cat-d${i}-required`} type="checkbox" checked={row.required} onChange={(event) => updateDef(i, { required: event.target.checked })} />
              {`Characteristic ${n} is required in every message`}
            </label>
            <Button variant="ghost" size="sm" onClick={() => update({ defs: values.defs.filter((_, j) => j !== i) })}>
              {`Remove characteristic ${n}`}
            </Button>
          </fieldset>
        );
      })}
      <div className={styles.formActions}>
        <Button size="sm" disabled={values.defs.length >= MAX_DEFS} onClick={() => update({ defs: [...values.defs, emptyDef()] })}>
          Add characteristic
        </Button>
      </div>
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title={failureTitle} live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          {submitLabel}
        </Button>
        {actions}
      </div>
    </form>
  );
}
