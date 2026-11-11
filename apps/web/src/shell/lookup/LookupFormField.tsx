import { FormField } from "../ui";
import { EntityLookup } from "./EntityLookup";
import type { LookupEntityKey, LookupOption, LookupScope } from "./entities";

export interface LookupFormFieldProps {
  id: string;
  label: string;
  entity: LookupEntityKey;
  value: string;
  onChange(value: string, option: LookupOption | null): void;
  hint?: string;
  error?: string;
  optional?: boolean;
  required?: boolean;
  scope?: LookupScope;
  prefill?: boolean;
  disabled?: boolean;
  readOnly?: boolean;
  allowCreate?: boolean;
  placeholder?: string;
}

/** A labelled form field whose control is an entity lookup, so a form picks a record by name instead of typing its id. */
export function LookupFormField({ id, label, hint, error, optional, required, entity, value, onChange, ...lookup }: LookupFormFieldProps) {
  return (
    <FormField id={id} label={label} hint={hint} error={error} optional={optional} required={required}>
      {(control) => <EntityLookup {...control} {...lookup} entity={entity} value={value} invalid={Boolean(error)} onChange={(event) => onChange(event.target.value, event.option)} />}
    </FormField>
  );
}
