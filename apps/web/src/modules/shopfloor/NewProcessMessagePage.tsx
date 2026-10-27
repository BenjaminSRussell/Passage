import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import {
  createProcessMessage,
  describeProblem,
  newKey,
  type Characteristic,
  type CharacteristicType,
  type NewProcessMessage,
  type ProcessCategory,
  type Problem,
} from "./api/client";
import { UUID } from "./format";
import styles from "./pages.module.css";
import { CATEGORIES, CATEGORY_LABELS } from "./processLabels";

export interface ValueRow {
  name: string;
  type: CharacteristicType;
  value: string;
  min: string;
  max: string;
  unit: string;
}

export interface Values {
  categoryCode: ProcessCategory;
  orderId: string;
  phasePath: string;
  /** datetime-local value in the viewer's zone, e.g. 2026-10-26T08:00. */
  occurredAt: string;
  values: ValueRow[];
}

export type Errors = Record<string, string>;

const DECIMAL = /^-?\d{1,18}(\.\d{1,9})?$/;
const MAX_VALUES = 50;

const emptyRow = (): ValueRow => ({ name: "", type: "numeric", value: "", min: "", max: "", unit: "" });

/** "2026-10-26T08:00" for now, in the viewer's zone, for a datetime-local field. */
function nowLocal(): string {
  const d = new Date();
  d.setMinutes(d.getMinutes() - d.getTimezoneOffset());
  return d.toISOString().slice(0, 16);
}

/** Checks that mirror the service (FR-25.17, BR-25.13), and the request the form sends. */
export function validateProcessMessage(values: Values, sourceMessageId: string): { errors: Errors; input?: NewProcessMessage } {
  const errors: Errors = {};
  const orderId = values.orderId.trim();
  if (!UUID.test(orderId)) errors.orderId = "Enter the production or batch order id (a UUID).";
  const phasePath = values.phasePath.trim();
  if (phasePath.length > 255) errors.phasePath = "Keep the phase path to 255 characters.";
  const occurred = new Date(values.occurredAt);
  if (values.occurredAt === "" || Number.isNaN(occurred.getTime())) errors.occurredAt = "Enter when it happened.";
  const seen = new Set<string>();
  const out: Characteristic[] = [];
  values.values.forEach((row, i) => {
    const name = row.name.trim();
    const value = row.value.trim();
    if (name.length < 1 || name.length > 64) errors[`values[${i}].name`] = "Enter a name of up to 64 characters.";
    else if (seen.has(name)) errors[`values[${i}].name`] = "Each characteristic appears once.";
    seen.add(name);
    const c: Characteristic = { name, type: row.type, value };
    if (row.unit.trim()) c.unit = row.unit.trim();
    if (row.type === "numeric") {
      if (!DECIMAL.test(value)) errors[`values[${i}].value`] = "Enter a decimal number, such as 7.25.";
      if (row.min.trim()) {
        if (DECIMAL.test(row.min.trim())) c.min = row.min.trim();
        else errors[`values[${i}].min`] = "Enter the lower limit as a decimal number, or leave it empty.";
      }
      if (row.max.trim()) {
        if (DECIMAL.test(row.max.trim())) c.max = row.max.trim();
        else errors[`values[${i}].max`] = "Enter the upper limit as a decimal number, or leave it empty.";
      }
    } else if (row.type === "boolean") {
      if (value !== "true" && value !== "false") errors[`values[${i}].value`] = "Choose yes or no.";
    } else if (value.length > 500) {
      errors[`values[${i}].value`] = "Keep text to 500 characters.";
    }
    out.push(c);
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      source: "manual",
      sourceMessageId,
      categoryCode: values.categoryCode,
      orderId,
      phasePath,
      occurredAt: occurred.toISOString(),
      values: out,
    },
  };
}

/** Manual entry of a process message (duty Create process messages). */
export function NewProcessMessagePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({ categoryCode: "process-data", orderId: "", phasePath: "", occurredAt: nowLocal(), values: [emptyRow()] });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // The source message id is the idempotency key of a manual message: a resend of
  // the same form after a lost response returns the stored message.
  const sourceId = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    sourceId.current = newKey();
  }

  function updateRow(i: number, patch: Partial<ValueRow>) {
    update({ values: values.values.map((row, j) => (j === i ? { ...row, ...patch } : row)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateProcessMessage(values, sourceId.current);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const res = await createProcessMessage(input);
      navigate(`/shopfloor/process-messages/${res.message.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Record process message"
        description="Enter a message the PI sheet or process control system could not send. Its category decides where it is delivered; values outside their limits are flagged for the batch record."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="pm-category" label="Category">
            {(control) => (
              <Select {...control} value={values.categoryCode} onChange={(event) => update({ categoryCode: event.target.value as ProcessCategory })}>
                {CATEGORIES.map((c) => (
                  <option key={c} value={c}>
                    {CATEGORY_LABELS[c]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="pm-order" label="Order id" hint="The production or batch order the message belongs to." error={errors.orderId}>
            {(control) => (
              <Input {...control} value={values.orderId} invalid={Boolean(errors.orderId)} onChange={(event) => update({ orderId: event.target.value })} />
            )}
          </FormField>
          <FormField id="pm-phase" label="Phase path" hint="ISA-88 path, e.g. /mix/unit1/heat/hold." optional error={errors.phasePath}>
            {(control) => (
              <Input {...control} value={values.phasePath} maxLength={255} invalid={Boolean(errors.phasePath)} onChange={(event) => update({ phasePath: event.target.value })} />
            )}
          </FormField>
          <FormField id="pm-occurred" label="Occurred at" hint="In your local time." error={errors.occurredAt}>
            {(control) => (
              <Input
                {...control}
                type="datetime-local"
                value={values.occurredAt}
                invalid={Boolean(errors.occurredAt)}
                onChange={(event) => update({ occurredAt: event.target.value })}
              />
            )}
          </FormField>
          {values.values.map((row, i) => {
            const err = (field: string) => errors[`values[${i}].${field}`];
            const n = i + 1;
            return (
              <fieldset key={i} className={styles.valueGroup}>
                <legend className={styles.legend}>{`Value ${n}`}</legend>
                <FormField id={`pm-v${i}-name`} label={`Value ${n} name`} error={err("name")}>
                  {(control) => (
                    <Input {...control} value={row.name} maxLength={64} invalid={Boolean(err("name"))} onChange={(event) => updateRow(i, { name: event.target.value })} />
                  )}
                </FormField>
                <FormField id={`pm-v${i}-type`} label={`Value ${n} type`} error={err("type")}>
                  {(control) => (
                    <Select
                      {...control}
                      value={row.type}
                      onChange={(event) => updateRow(i, { type: event.target.value as CharacteristicType, value: "", min: "", max: "" })}
                    >
                      <option value="numeric">Number</option>
                      <option value="text">Text</option>
                      <option value="boolean">Yes or no</option>
                    </Select>
                  )}
                </FormField>
                <FormField id={`pm-v${i}-value`} label={`Value ${n}`} error={err("value")}>
                  {(control) =>
                    row.type === "boolean" ? (
                      <Select {...control} value={row.value} onChange={(event) => updateRow(i, { value: event.target.value })}>
                        <option value="">Choose</option>
                        <option value="true">Yes</option>
                        <option value="false">No</option>
                      </Select>
                    ) : (
                      <Input
                        {...control}
                        value={row.value}
                        inputMode={row.type === "numeric" ? "decimal" : undefined}
                        invalid={Boolean(err("value"))}
                        onChange={(event) => updateRow(i, { value: event.target.value })}
                      />
                    )
                  }
                </FormField>
                {row.type === "numeric" ? (
                  <>
                    <FormField id={`pm-v${i}-min`} label={`Value ${n} lower limit`} optional error={err("min")}>
                      {(control) => (
                        <Input {...control} value={row.min} inputMode="decimal" invalid={Boolean(err("min"))} onChange={(event) => updateRow(i, { min: event.target.value })} />
                      )}
                    </FormField>
                    <FormField id={`pm-v${i}-max`} label={`Value ${n} upper limit`} optional error={err("max")}>
                      {(control) => (
                        <Input {...control} value={row.max} inputMode="decimal" invalid={Boolean(err("max"))} onChange={(event) => updateRow(i, { max: event.target.value })} />
                      )}
                    </FormField>
                    <FormField id={`pm-v${i}-unit`} label={`Value ${n} unit`} optional>
                      {(control) => <Input {...control} value={row.unit} maxLength={16} onChange={(event) => updateRow(i, { unit: event.target.value })} />}
                    </FormField>
                  </>
                ) : null}
                <Button variant="ghost" size="sm" onClick={() => update({ values: values.values.filter((_, j) => j !== i) })}>
                  {`Remove value ${n}`}
                </Button>
              </fieldset>
            );
          })}
          <div className={styles.formActions}>
            <Button size="sm" disabled={values.values.length >= MAX_VALUES} onClick={() => update({ values: [...values.values, emptyRow()] })}>
              Add value
            </Button>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not record the message" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record message
            </Button>
            <Link className={buttonClassName("ghost")} to="/shopfloor/process-messages">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
