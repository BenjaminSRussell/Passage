import { FormField, Input, Select } from "../../shell/ui";
import { AQL_VALUES, type SamplingMethod, type SamplingPlan } from "./api/client";
import { DECIMAL } from "./format";

export interface PlanValues {
  method: SamplingMethod;
  aql: string;
  fixedQty: string;
  percent: string;
}

export type PlanErrors = Partial<Record<keyof PlanValues, string>>;

export const emptyPlan: PlanValues = { method: "AQL", aql: "2.5", fixedQty: "", percent: "" };

/** Checks that mirror the service's ValidatePlan, and the plan the form sends. */
export function validatePlan(values: PlanValues): { errors: PlanErrors; plan?: SamplingPlan } {
  const errors: PlanErrors = {};
  switch (values.method) {
    case "AQL":
      if (!(AQL_VALUES as readonly string[]).includes(values.aql)) errors.aql = "Choose AQL 1.0, 2.5 or 4.0.";
      break;
    case "FIXED":
      if (!/^\d{1,9}$/.test(values.fixedQty.trim()) || Number(values.fixedQty) < 1) errors.fixedQty = "Enter a whole number of units, 1 or more.";
      break;
    case "PERCENT": {
      const p = values.percent.trim();
      if (!DECIMAL.test(p) || Number(p) <= 0 || Number(p) > 100) errors.percent = "Enter a percentage above 0 and at most 100.";
      break;
    }
  }
  if (Object.keys(errors).length > 0) return { errors };
  const plan: SamplingPlan =
    values.method === "AQL"
      ? { method: "AQL", aql: values.aql, severity: "NORMAL", inspectionLevel: "II" }
      : values.method === "FIXED"
        ? { method: "FIXED", fixedQty: values.fixedQty.trim() }
        : { method: "PERCENT", percent: values.percent.trim() };
  return { errors, plan };
}

/** Maps service field errors (samplingPlan.aql, …) onto the plan fields. */
export function planFieldErrors(fields: Record<string, string>, prefix = "samplingPlan"): PlanErrors {
  const out: PlanErrors = {};
  for (const name of ["method", "aql", "fixedQty", "percent"] as const) {
    const key = prefix ? `${prefix}.${name}` : name;
    if (fields[key]) out[name] = fields[key];
  }
  return out;
}

/** Sampling method and its parameter (FR-33.2): AQL at level II normal, a fixed quantity, or a percentage. */
export function PlanFields({
  idPrefix,
  values,
  errors,
  onChange,
}: {
  idPrefix: string;
  values: PlanValues;
  errors: PlanErrors;
  onChange(patch: Partial<PlanValues>): void;
}) {
  return (
    <>
      <FormField id={`${idPrefix}-method`} label="Sampling method" error={errors.method}>
        {(control) => (
          <Select {...control} value={values.method} onChange={(event) => onChange({ method: event.target.value as SamplingMethod })}>
            <option value="AQL">AQL (ANSI/ASQ Z1.4, level II)</option>
            <option value="FIXED">Fixed quantity</option>
            <option value="PERCENT">Percentage of the lot</option>
          </Select>
        )}
      </FormField>
      {values.method === "AQL" ? (
        <FormField id={`${idPrefix}-aql`} label="AQL" hint="Normal inspection; tightened and reduced tables are not loaded yet." error={errors.aql}>
          {(control) => (
            <Select {...control} value={values.aql} invalid={Boolean(errors.aql)} onChange={(event) => onChange({ aql: event.target.value })}>
              {AQL_VALUES.map((a) => (
                <option key={a} value={a}>
                  {a}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      ) : values.method === "FIXED" ? (
        <FormField id={`${idPrefix}-fixed`} label="Sample quantity" hint="Capped at the lot quantity." error={errors.fixedQty}>
          {(control) => (
            <Input {...control} inputMode="numeric" value={values.fixedQty} invalid={Boolean(errors.fixedQty)} onChange={(event) => onChange({ fixedQty: event.target.value })} />
          )}
        </FormField>
      ) : (
        <FormField id={`${idPrefix}-percent`} label="Percentage" hint="Rounded up to whole units." error={errors.percent}>
          {(control) => (
            <Input {...control} inputMode="decimal" value={values.percent} invalid={Boolean(errors.percent)} onChange={(event) => onChange({ percent: event.target.value })} />
          )}
        </FormField>
      )}
    </>
  );
}
