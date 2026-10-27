import { Button, FormField, Input, Select } from "../../shell/ui";
import styles from "./pages.module.css";

export interface LineField<K extends string> {
  name: K;
  label: string;
  mono?: boolean;
  decimal?: boolean;
  /** When set, the field is a select with these options. */
  options?: { value: string; label: string }[];
}

export interface LineGridProps<K extends string> {
  /** Prefix for field ids, unique per page. */
  idPrefix: string;
  /** Singular noun in legends and buttons, such as "Line" or "Position". */
  noun: string;
  fields: LineField<K>[];
  lines: Record<K, string>[];
  /** Errors keyed by the service's paths, such as lines[0].stdUnitCost. */
  errors: Record<string, string>;
  /** Request field the lines are sent as, which prefixes the error paths. Default "lines". */
  path?: string;
  newLine(): Record<K, string>;
  onChange(lines: Record<K, string>[]): void;
  /** Hides fields that do not apply to a line (for example estimate per lot on a variable line). */
  applies?(line: Record<K, string>, field: K): boolean;
}

/** An editable list of lines, one fieldset per line, built from the shell form primitives. */
export function LineGrid<K extends string>({
  idPrefix,
  noun,
  fields,
  lines,
  errors,
  path: listPath = "lines",
  newLine,
  onChange,
  applies,
}: LineGridProps<K>) {
  const updateLine = (i: number, patch: Partial<Record<K, string>>) =>
    onChange(lines.map((l, idx) => (idx === i ? { ...l, ...patch } : l)));
  return (
    <div className={styles.lines}>
      {lines.map((line, i) => (
        <fieldset key={i} className={styles.lineCard}>
          <legend className={styles.lineLegend}>
            {noun} {i + 1}
          </legend>
          {fields
            .filter((f) => !applies || applies(line, f.name))
            .map((f) => {
              const path = `${listPath}[${i}].${f.name}`;
              const id = `${idPrefix}-${i}-${f.name}`;
              return (
                <FormField key={f.name} id={id} label={f.label} error={errors[path]}>
                  {(control) =>
                    f.options ? (
                      <Select
                        {...control}
                        value={line[f.name]}
                        onChange={(e) => updateLine(i, { [f.name]: e.target.value } as Partial<Record<K, string>>)}
                      >
                        {f.options.map((o) => (
                          <option key={o.value} value={o.value}>
                            {o.label}
                          </option>
                        ))}
                      </Select>
                    ) : (
                      <Input
                        {...control}
                        inputMode={f.decimal ? "decimal" : undefined}
                        className={f.mono ? styles.mono : undefined}
                        value={line[f.name]}
                        onChange={(e) => updateLine(i, { [f.name]: e.target.value } as Partial<Record<K, string>>)}
                        invalid={Boolean(errors[path])}
                      />
                    )
                  }
                </FormField>
              );
            })}
          {lines.length > 1 ? (
            <div className={styles.formActions}>
              <Button size="sm" variant="ghost" onClick={() => onChange(lines.filter((_, idx) => idx !== i))}>
                Remove {noun.toLowerCase()} {i + 1}
              </Button>
            </div>
          ) : null}
        </fieldset>
      ))}
      {errors[listPath] ? (
        <p role="alert" className={styles.unfavourable}>
          {errors[listPath]}
        </p>
      ) : null}
      <div>
        <Button onClick={() => onChange([...lines, newLine()])}>Add {noun.toLowerCase()}</Button>
      </div>
    </div>
  );
}
