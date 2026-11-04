import { FormField, Input } from "../../shell/ui";
import type { Party, PartyNameFields, PartyType } from "./api/client";
import type { Errors } from "./useWriteForm";

export interface NameValues {
  legalName: string;
  tradeName: string;
  registrationNumber: string;
  givenName: string;
  familyName: string;
}

export const emptyNames: NameValues = {
  legalName: "",
  tradeName: "",
  registrationNumber: "",
  givenName: "",
  familyName: "",
};

const ORGANIZATION: [keyof NameValues, string, boolean][] = [
  ["legalName", "Legal name", true],
  ["tradeName", "Trade name", false],
  ["registrationNumber", "Registration number", false],
];

const PERSON: [keyof NameValues, string, boolean][] = [
  ["givenName", "Given name", true],
  ["familyName", "Family name", true],
];

const fieldsFor = (type: PartyType) => (type === "person" ? PERSON : ORGANIZATION);

export function validateNames(type: PartyType, values: NameValues): Errors {
  const errors: Errors = {};
  for (const [key, label, required] of fieldsFor(type)) {
    if (required && values[key].trim() === "") errors[key] = `Enter the ${label.toLowerCase()}.`;
    if (values[key].length > 200) errors[key] = "Use at most 200 characters.";
  }
  return errors;
}

export function pickNames(type: PartyType, values: NameValues): PartyNameFields {
  return Object.fromEntries(
    fieldsFor(type)
      .map(([key]) => [key, values[key].trim()])
      .filter(([, value]) => value !== ""),
  );
}

export function namesOf(party: Party): NameValues {
  return {
    legalName: party.legalName ?? "",
    tradeName: party.tradeName ?? "",
    registrationNumber: party.registrationNumber ?? "",
    givenName: party.givenName ?? "",
    familyName: party.familyName ?? "",
  };
}

export function changedNames(type: PartyType, before: NameValues, after: NameValues): PartyNameFields {
  return Object.fromEntries(
    fieldsFor(type)
      .filter(([key]) => before[key] !== after[key].trim())
      .map(([key]) => [key, after[key].trim()]),
  );
}

export function NameFieldset({
  type,
  idPrefix,
  values,
  errors,
  onChange,
}: {
  type: PartyType;
  idPrefix: string;
  values: NameValues;
  errors: Errors;
  onChange(key: keyof NameValues, value: string): void;
}) {
  return (
    <>
      {fieldsFor(type).map(([key, label, required]) => (
        <FormField key={key} id={`${idPrefix}-${key}`} label={label} optional={!required} error={errors[key]}>
          {(control) => (
            <Input
              {...control}
              value={values[key]}
              maxLength={200}
              onChange={(event) => onChange(key, event.target.value)}
              invalid={Boolean(errors[key])}
            />
          )}
        </FormField>
      ))}
    </>
  );
}
