import { Button, FormField, Input, Select } from "../../shell/ui";
import styles from "./pages.module.css";

export interface RowField {
  key: string;
  label: string;
  /** A select over these values; otherwise a text input. */
  options?: readonly string[];
  decimal?: boolean;
  mono?: boolean;
  upper?: boolean;
  hint?: string;
}

export interface RowsEditorProps {
  /** Field name used for ids and error keys, such as costLines; row errors are `${field}[i]`. */
  field: string;
  legend: string;
  /** Singular noun for the add and remove buttons, such as "cost line". */
  noun: string;
  fields: RowField[];
  rows: Record<string, string>[];
  errors: Record<string, string>;
  /** Rows the form needs at least; remove buttons hide below this. */
  min?: number;
  blank(): Record<string, string>;
  onChange(rows: Record<string, string>[]): void;
}

/** A repeating group of inputs, one row per item, with add and remove. */
export function RowsEditor({ field, legend, noun, fields, rows, errors, min = 1, blank, onChange }: RowsEditorProps) {
  const update = (i: number, key: string, value: string) => onChange(rows.map((r, idx) => (idx === i ? { ...r, [key]: value } : r)));
  const label = (f: RowField, i: number) => `${noun.charAt(0).toUpperCase()}${noun.slice(1)} ${i + 1} ${f.label.toLowerCase()}`;
  return (
    <fieldset className={styles.valueGroup}>
      <legend className={styles.legend}>{legend}</legend>
      {rows.length === 0 ? <p className={styles.note}>None yet.</p> : null}
      {rows.map((row, i) => {
        const rowError = errors[`${field}[${i}]`] ?? Object.entries(errors).find(([k]) => k.startsWith(`${field}[${i}].`))?.[1];
        return (
          <div key={i} className={styles.rowGroup}>
            <div className={styles.lineRow}>
              {fields.map((f) => (
                <FormField key={f.key} id={`${field}-${i}-${f.key}`} label={label(f, i)} hint={i === 0 ? f.hint : undefined}>
                  {(control) =>
                    f.options ? (
                      <Select {...control} value={row[f.key] ?? ""} onChange={(e) => update(i, f.key, e.target.value)} invalid={Boolean(rowError)}>
                        {f.options.map((o) => (
                          <option key={o} value={o}>
                            {o}
                          </option>
                        ))}
                      </Select>
                    ) : (
                      <Input
                        {...control}
                        className={f.mono ? styles.mono : undefined}
                        inputMode={f.decimal ? "decimal" : undefined}
                        value={row[f.key] ?? ""}
                        onChange={(e) => update(i, f.key, f.upper ? e.target.value.toUpperCase() : e.target.value)}
                        invalid={Boolean(rowError)}
                      />
                    )
                  }
                </FormField>
              ))}
              {rows.length > min ? (
                <Button variant="ghost" size="sm" onClick={() => onChange(rows.filter((_, idx) => idx !== i))}>
                  Remove {noun} {i + 1}
                </Button>
              ) : (
                <span />
              )}
            </div>
            {rowError ? (
              <p className={styles.outside} role="alert">
                {noun.charAt(0).toUpperCase()}
                {noun.slice(1)} {i + 1}: {rowError}
              </p>
            ) : null}
          </div>
        );
      })}
      {errors[field] ? (
        <p className={styles.outside} role="alert">
          {errors[field]}
        </p>
      ) : null}
      <div className={styles.actions}>
        <Button size="sm" icon="note" onClick={() => onChange([...rows, blank()])}>
          Add {noun}
        </Button>
      </div>
    </fieldset>
  );
}
