import { Button, FormField, Input, Select } from "../../shell/ui";
import styles from "./pages.module.css";

export interface RowField<K extends string> {
  name: K;
  label: string;
  mono?: boolean;
  decimal?: boolean;
  optional?: boolean;
  type?: "date";
  /** When set, the field is a select with these options. */
  options?: { value: string; label: string }[];
}

export interface RowGridProps<K extends string> {
  /** Prefix for field ids, unique per page. */
  idPrefix: string;
  /** Request path of the list, such as "matrix" or "items"; errors use `${path}[i].field`. */
  path: string;
  /** Singular noun in legends and buttons, such as "Matrix row" or "Pair". */
  noun: string;
  fields: RowField<K>[];
  rows: Record<K, string>[];
  errors: Record<string, string>;
  newRow(): Record<K, string>;
  onChange(rows: Record<K, string>[]): void;
}

/** An editable list of rows, one fieldset per row, built from the shell form primitives. */
export function RowGrid<K extends string>({ idPrefix, path, noun, fields, rows, errors, newRow, onChange }: RowGridProps<K>) {
  const update = (i: number, name: K, value: string) =>
    onChange(rows.map((row, idx) => (idx === i ? { ...row, [name]: value } : row)));
  return (
    <div className={styles.lines}>
      {rows.map((row, i) => (
        <fieldset key={i} className={styles.lineCard}>
          <legend className={styles.lineLegend}>
            {noun} {i + 1}
          </legend>
          {fields.map((f) => {
            const at = `${path}[${i}].${f.name}`;
            const id = `${idPrefix}-${i}-${f.name}`;
            return (
              <FormField key={f.name} id={id} label={f.label} optional={f.optional} error={errors[at]}>
                {(control) =>
                  f.options ? (
                    <Select {...control} value={row[f.name]} onChange={(e) => update(i, f.name, e.target.value)}>
                      {f.options.map((o) => (
                        <option key={o.value} value={o.value}>
                          {o.label}
                        </option>
                      ))}
                    </Select>
                  ) : (
                    <Input
                      {...control}
                      type={f.type}
                      inputMode={f.decimal ? "decimal" : undefined}
                      className={f.mono ? styles.mono : undefined}
                      value={row[f.name]}
                      onChange={(e) => update(i, f.name, e.target.value)}
                      invalid={Boolean(errors[at])}
                    />
                  )
                }
              </FormField>
            );
          })}
          {rows.length > 1 ? (
            <div className={styles.formActions}>
              <Button size="sm" variant="ghost" onClick={() => onChange(rows.filter((_, idx) => idx !== i))}>
                Remove {noun.toLowerCase()} {i + 1}
              </Button>
            </div>
          ) : null}
        </fieldset>
      ))}
      {errors[path] ? (
        <p role="alert" className={styles.danger}>
          {errors[path]}
        </p>
      ) : null}
      <div>
        <Button onClick={() => onChange([...rows, newRow()])}>Add {noun.toLowerCase()}</Button>
      </div>
    </div>
  );
}
