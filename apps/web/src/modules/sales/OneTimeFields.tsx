import { FormField, Input } from "../../shell/ui";
import type { OneTimeParty } from "./api/client";
import styles from "./pages.module.css";

/** Form values of the walk-in buyer, as typed. */
export interface OneTimeValues {
  name: string;
  line1: string;
  line2: string;
  city: string;
  region: string;
  postalCode: string;
  country: string;
  taxId: string;
}

export const emptyOneTime: OneTimeValues = { name: "", line1: "", line2: "", city: "", region: "", postalCode: "", country: "", taxId: "" };

const LIMITS: Record<keyof OneTimeValues, number> = { name: 140, line1: 100, line2: 100, city: 100, region: 100, postalCode: 20, country: 2, taxId: 40 };

/** Checks that mirror the service (domain.OneTimeParty.Validate). Error keys are prefixed oneTime. as the service's field paths are. */
export function validateOneTime(v: OneTimeValues, errors: Record<string, string>): OneTimeParty | undefined {
  const before = Object.keys(errors).length;
  const required: [keyof OneTimeValues, string][] = [
    ["name", "Enter the buyer's name."],
    ["line1", "Enter the first address line."],
    ["city", "Enter the city."],
    ["postalCode", "Enter the postal code."],
  ];
  for (const [key, message] of required) {
    if (v[key].trim() === "") errors[`oneTime.${key}`] = message;
  }
  for (const key of Object.keys(LIMITS) as (keyof OneTimeValues)[]) {
    if (key !== "country" && !errors[`oneTime.${key}`] && v[key].length > LIMITS[key]) errors[`oneTime.${key}`] = `Use at most ${LIMITS[key]} characters.`;
  }
  if (!/^[A-Z]{2}$/.test(v.country)) errors["oneTime.country"] = "Enter a two-letter ISO country code, such as DE.";
  if (Object.keys(errors).length > before) return undefined;
  const party: OneTimeParty = { name: v.name.trim(), line1: v.line1.trim(), city: v.city.trim(), postalCode: v.postalCode.trim(), country: v.country };
  if (v.line2.trim()) party.line2 = v.line2.trim();
  if (v.region.trim()) party.region = v.region.trim();
  if (v.taxId.trim()) party.taxId = v.taxId.trim();
  return party;
}

interface Props {
  values: OneTimeValues;
  errors: Record<string, string>;
  onChange: (values: OneTimeValues) => void;
}

const FIELDS: { key: keyof OneTimeValues; label: string; hint?: string; optional?: boolean }[] = [
  { key: "name", label: "Buyer name" },
  { key: "line1", label: "Address line 1" },
  { key: "line2", label: "Address line 2", optional: true },
  { key: "city", label: "City" },
  { key: "region", label: "State or region", optional: true },
  { key: "postalCode", label: "Postal code" },
  { key: "country", label: "Country", hint: "Two-letter ISO code, such as DE." },
  { key: "taxId", label: "Tax id", hint: "Carried to the invoice.", optional: true },
];

/** The walk-in buyer's document-level name, address and tax id (FR-27.22). */
export function OneTimeFields({ values, errors, onChange }: Props) {
  return (
    <fieldset className={styles.form}>
      <legend>One-time customer</legend>
      <p className={styles.note}>
        The customer above is the legal entity's shared one-time account. The buyer below is printed on the invoice. The order may not exceed the one-time order limit set in Sales parameters (5,000 USD or 4,500 EUR by default).
      </p>
      {FIELDS.map(({ key, label, hint, optional }) => (
        <FormField key={key} id={`onetime-${key}`} label={label} hint={hint} error={errors[`oneTime.${key}`]} optional={optional}>
          {(control) => (
            <Input
              {...control}
              value={values[key]}
              onChange={(e) => onChange({ ...values, [key]: key === "country" ? e.target.value.toUpperCase() : e.target.value })}
              invalid={Boolean(errors[`oneTime.${key}`])}
            />
          )}
        </FormField>
      ))}
    </fieldset>
  );
}
