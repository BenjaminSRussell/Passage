import { Button, FormField, Input } from "../../shell/ui";
import type { Line } from "./api/client";
import { AMOUNT } from "./format";
import styles from "./pages.module.css";

export const emptyLine = (): Line => ({ chargeCode: "", amount: "" });

/** Client checks that mirror domain.ValidateLines: a code of 1 to 20 characters and a non-negative amount. */
export function validateLines(lines: Line[], field: string): Record<string, string> {
  const errors: Record<string, string> = {};
  if (lines.length === 0) errors[field] = "Add at least one charge.";
  lines.forEach((line, i) => {
    const code = line.chargeCode.trim();
    if (code.length < 1 || code.length > 20) errors[`${field}[${i}].chargeCode`] = "Enter a charge code of 1 to 20 characters, such as BASE or FUEL.";
    if (!AMOUNT.test(line.amount.trim())) errors[`${field}[${i}].amount`] = "Enter an amount such as 412.50.";
  });
  return errors;
}

export function cleanLines(lines: Line[]): Line[] {
  return lines.map((l) => ({ chargeCode: l.chargeCode.trim(), amount: l.amount.trim() }));
}

export interface ChargeLinesEditorProps {
  /** Field name used for ids and error keys: expectedLines or lines. */
  field: string;
  legend: string;
  lines: Line[];
  errors: Record<string, string>;
  onChange(lines: Line[]): void;
}

/** One row per charge (BASE, FUEL, ACC:LIFTGATE …) with its amount. */
export function ChargeLinesEditor({ field, legend, lines, errors, onChange }: ChargeLinesEditorProps) {
  const update = (i: number, patch: Partial<Line>) => onChange(lines.map((l, idx) => (idx === i ? { ...l, ...patch } : l)));
  return (
    <fieldset className={styles.valueGroup}>
      <legend className={styles.legend}>{legend}</legend>
      {lines.map((line, i) => (
        <div key={i} className={styles.lineRow}>
          <FormField id={`${field}-${i}-code`} label={`Charge ${i + 1} code`} error={errors[`${field}[${i}].chargeCode`]}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                maxLength={20}
                value={line.chargeCode}
                onChange={(e) => update(i, { chargeCode: e.target.value.toUpperCase() })}
                invalid={Boolean(errors[`${field}[${i}].chargeCode`])}
              />
            )}
          </FormField>
          <FormField id={`${field}-${i}-amount`} label={`Charge ${i + 1} amount`} error={errors[`${field}[${i}].amount`]}>
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={line.amount}
                onChange={(e) => update(i, { amount: e.target.value })}
                invalid={Boolean(errors[`${field}[${i}].amount`])}
              />
            )}
          </FormField>
          {lines.length > 1 ? (
            <Button variant="ghost" size="sm" onClick={() => onChange(lines.filter((_, idx) => idx !== i))}>
              Remove charge {i + 1}
            </Button>
          ) : (
            <span />
          )}
        </div>
      ))}
      {errors[field] ? (
        <p className={styles.outside} role="alert">
          {errors[field]}
        </p>
      ) : null}
      <div className={styles.actions}>
        <Button size="sm" icon="note" onClick={() => onChange([...lines, emptyLine()])}>
          Add charge
        </Button>
      </div>
    </fieldset>
  );
}
