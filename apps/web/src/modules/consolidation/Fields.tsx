import { FormField, Input, Select } from "../../shell/ui";
import styles from "./pages.module.css";

export interface FieldSpec<K extends string> {
  name: K;
  label: string;
  /** Request path used for errors; defaults to the name. */
  path?: string;
  hint?: string;
  optional?: boolean;
  mono?: boolean;
  decimal?: boolean;
  type?: "date";
  options?: { value: string; label: string }[];
}

export interface FieldsProps<K extends string> {
  idPrefix: string;
  fields: FieldSpec<K>[];
  values: Record<K, string>;
  errors: Record<string, string>;
  onChange(values: Record<K, string>): void;
}

/** A run of labelled form fields built from the shell primitives. */
export function Fields<K extends string>({ idPrefix, fields, values, errors, onChange }: FieldsProps<K>) {
  return (
    <>
      {fields.map((f) => {
        const error = errors[f.path ?? f.name];
        return (
          <FormField key={f.name} id={`${idPrefix}-${f.name}`} label={f.label} hint={f.hint} optional={f.optional} error={error}>
            {(control) =>
              f.options ? (
                <Select {...control} value={values[f.name]} onChange={(e) => onChange({ ...values, [f.name]: e.target.value })}>
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
                  value={values[f.name]}
                  onChange={(e) => onChange({ ...values, [f.name]: e.target.value })}
                  invalid={Boolean(error)}
                />
              )
            }
          </FormField>
        );
      })}
    </>
  );
}
