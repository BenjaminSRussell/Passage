import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  addPolicy,
  describeProblem,
  getAsset,
  INSURANCE_BASES,
  INSURANCE_TYPES,
  listIndexSeries,
  newKey,
  putIndexSeries,
  type IndexSeries,
  type IndexSeriesInput,
  type InsurancePolicy,
  type NewPolicy,
  type Problem,
} from "./api/client";
import { DATE, DECIMAL, money, parseYearLines, sentence, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface PolicyValues {
  insurerPartyId: string;
  policyNumber: string;
  insuranceType: NewPolicy["insuranceType"];
  coverageFrom: string;
  coverageTo: string;
  premium: string;
  currency: string;
  basis: NewPolicy["basis"];
  baseValue: string;
  sumInsured: string;
  indexSeriesCode: string;
  indexBaseYear: string;
  valuationYear: string;
}

type PolicyErrors = Partial<Record<keyof PolicyValues, string>>;

const YEAR = /^[12]\d{3}$/;

/** Checks that mirror the service (internal/api addInsurancePolicy). */
export function validatePolicy(values: PolicyValues): { errors: PolicyErrors; input?: NewPolicy } {
  const errors: PolicyErrors = {};
  const number = values.policyNumber.trim();
  if (!UUID.test(values.insurerPartyId.trim())) errors.insurerPartyId = "Enter the insurer's party UUID.";
  if (number.length < 1 || number.length > 60) errors.policyNumber = "Enter a policy number of up to 60 characters.";
  if (!DATE.test(values.coverageFrom)) errors.coverageFrom = "Choose the coverage start.";
  if (!DATE.test(values.coverageTo)) errors.coverageTo = "Choose the coverage end.";
  else if (DATE.test(values.coverageFrom) && values.coverageTo <= values.coverageFrom) errors.coverageTo = "Coverage must end after it starts.";
  if (values.premium.trim() !== "" && !DECIMAL.test(values.premium.trim())) errors.premium = "Enter the premium as a decimal, or leave it empty.";
  if (!/^[A-Z]{3}$/.test(values.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  if (!DECIMAL.test(values.baseValue.trim())) errors.baseValue = "Enter the replacement value as a decimal.";
  if (!DECIMAL.test(values.sumInsured.trim())) errors.sumInsured = "Enter the sum insured as a decimal.";
  const indexed = values.indexSeriesCode !== "";
  if (indexed && !YEAR.test(values.indexBaseYear.trim())) errors.indexBaseYear = "Enter the index base year, such as 2024.";
  if (indexed && values.valuationYear.trim() !== "" && !YEAR.test(values.valuationYear.trim())) errors.valuationYear = "Enter a year, or leave it empty for the coverage start year.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewPolicy = {
    insurerPartyId: values.insurerPartyId.trim(),
    policyNumber: number,
    insuranceType: values.insuranceType,
    coverageFrom: values.coverageFrom,
    coverageTo: values.coverageTo,
    currency: values.currency,
    basis: values.basis,
    baseValue: values.baseValue.trim(),
    sumInsured: values.sumInsured.trim(),
  };
  if (values.premium.trim() !== "") input.premium = values.premium.trim();
  if (indexed) {
    input.indexSeriesCode = values.indexSeriesCode;
    input.indexBaseYear = Number(values.indexBaseYear.trim());
    if (values.valuationYear.trim() !== "") input.valuationYear = Number(values.valuationYear.trim());
  }
  return { errors, input };
}

function blankPolicy(now: Date = new Date()): PolicyValues {
  const from = today(now);
  return {
    insurerPartyId: "",
    policyNumber: "",
    insuranceType: "Property",
    coverageFrom: from,
    coverageTo: `${Number(from.slice(0, 4)) + 1}${from.slice(4)}`,
    premium: "",
    currency: "USD",
    basis: "Replacement",
    baseValue: "",
    sumInsured: "",
    indexSeriesCode: "",
    indexBaseYear: "",
    valuationYear: "",
  };
}

/** Record an insurance policy on an asset with an indexed replacement value (FR-37.15). */
export function NewPolicyPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getAsset(id, signal), id);
  const series = useLoad((signal) => listIndexSeries(signal), "index-series");
  const [values, setValues] = useState<PolicyValues>(() => blankPolicy());
  const [errors, setErrors] = useState<PolicyErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<InsurancePolicy | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<PolicyValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="New insurance policy" />
        <Alert tone="danger" title="Could not load the asset" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="New insurance policy" />
        <span className="visually-hidden">Loading the asset…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const asset = state.data.value;

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePolicy(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult(await addPolicy(id, input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as PolicyErrors);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (result) {
    return (
      <div className={styles.stack}>
        <PageHeader title={`Policy ${result.policyNumber} on ${asset.assetNumber}`} />
        <Alert tone="success" title="Policy recorded" live="status">
          {result.indexedValue
            ? `Indexed replacement value ${money(result.indexedValue, result.currency)} (index year ${result.indexYearUsed}); sum insured ${money(result.sumInsured, result.currency)}.`
            : `Sum insured ${money(result.sumInsured, result.currency)}; no index series.`}
        </Alert>
        {result.warnings.map((w) => (
          <Alert key={w} tone="warning" title={w.startsWith("AST-0532") ? "Under-insured" : "Index year missing"} live="off">
            {w}
          </Alert>
        ))}
        <div>
          <Link className={buttonClassName("primary")} to={`/assets/${id}`}>
            Back to the asset
          </Link>
        </div>
      </div>
    );
  }

  const text = (name: Exclude<keyof PolicyValues, "insuranceType" | "basis" | "indexSeriesCode">, label: string, hint?: string, extra: { type?: string; mono?: boolean; upper?: boolean } = {}) => (
    <FormField id={`policy-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: extra.upper ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`New insurance policy on ${asset.assetNumber}`}
        description="With an index series the service computes the indexed replacement value as base value × index for the valuation year ÷ index for the base year. Cover below 90% of that value is flagged as under-insured."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New insurance policy">
          {text("insurerPartyId", "Insurer", "Party UUID of the insurance company.", { mono: true })}
          {text("policyNumber", "Policy number", undefined, { mono: true })}
          <FormField id="policy-insuranceType" label="Insurance type">
            {(control) => (
              <Select {...control} value={values.insuranceType} onChange={(e) => update({ insuranceType: e.target.value as PolicyValues["insuranceType"] })}>
                {INSURANCE_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("coverageFrom", "Coverage from", undefined, { type: "date" })}
          {text("coverageTo", "Coverage to", undefined, { type: "date" })}
          {text("premium", "Premium", "Optional.")}
          {text("currency", "Currency", undefined, { upper: true })}
          <FormField id="policy-basis" label="Insured value basis">
            {(control) => (
              <Select {...control} value={values.basis} onChange={(e) => update({ basis: e.target.value as PolicyValues["basis"] })}>
                {INSURANCE_BASES.map((b) => (
                  <option key={b} value={b}>
                    {b === "NBV" ? "NBV" : b}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("baseValue", "Base value", "Replacement value at the index base year.")}
          {text("sumInsured", "Sum insured", "The policy's cover.")}
          <FormField id="policy-indexSeriesCode" label="Index series" hint={series.error ? "Index series could not be loaded." : undefined}>
            {(control) => (
              <Select {...control} value={values.indexSeriesCode} onChange={(e) => update({ indexSeriesCode: e.target.value })}>
                <option value="">No index</option>
                {(series.data ?? []).map((s) => (
                  <option key={s.code} value={s.code}>
                    {`${s.code} (${s.country}, ${s.classCode})`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.indexSeriesCode !== "" ? (
            <>
              {text("indexBaseYear", "Index base year", undefined, { type: "number" })}
              {text("valuationYear", "Valuation year", "Empty uses the coverage start year.", { type: "number" })}
            </>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "AST-0025" ? "That policy number is taken" : "Could not record the policy"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record policy
            </Button>
            <Link className={buttonClassName("ghost")} to={`/assets/${id}`}>
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

export interface SeriesValues {
  code: string;
  country: string;
  classCode: string;
  values: string;
}

type SeriesErrors = Partial<Record<keyof SeriesValues, string>>;

/** Checks that mirror the service (internal/api putIndexSeries); values are "2026=118.5" lines. */
export function validateSeries(values: SeriesValues): { errors: SeriesErrors; code?: string; input?: IndexSeriesInput } {
  const errors: SeriesErrors = {};
  const code = values.code.trim().toUpperCase();
  if (!/^[A-Z0-9][A-Z0-9_-]{0,31}$/.test(code)) errors.code = "Enter a code of letters, digits, hyphen or underscore.";
  if (!/^[A-Z]{2}$/.test(values.country)) errors.country = "Enter a two-letter country code, such as US.";
  if (!/^[A-Z0-9][A-Z0-9_-]{0,19}$/.test(values.classCode)) errors.classCode = "Enter the asset class code.";
  const parsed = parseYearLines(values.values);
  if (parsed.error) errors.values = parsed.error;
  else if (Object.keys(parsed.values).length === 0) errors.values = "Enter at least one year, such as 2026=118.5.";
  else if (Object.values(parsed.values).some((v) => /^0+(\.0+)?$/.test(v) || /\.\d{5,}$/.test(v))) errors.values = "Every index value must be above 0 with at most 4 decimals.";
  if (Object.keys(errors).length > 0) return { errors };
  const points = Object.entries(parsed.values).map(([year, value]) => ({ year: Number(year), value }));
  return { errors, code, input: { country: values.country, classCode: values.classCode, values: points } };
}

const seriesColumns: DataTableColumn<IndexSeries>[] = [
  { key: "code", header: "Series", mono: true },
  { key: "country", header: "Country", mono: true },
  { key: "classCode", header: "Asset class", mono: true },
  { key: "values", header: "Values", render: (s) => s.values.map((v) => `${v.year}: ${v.value}`).join(", ") },
  { key: "updatedAt", header: "Updated", render: (s) => s.updatedAt.slice(0, 10) },
];

/** Insured-value index series (insurance.indexSeries): list and load yearly values. */
export function IndexSeriesPage() {
  const state = useLoad((signal) => listIndexSeries(signal), "index-series");
  const [values, setValues] = useState<SeriesValues>({ code: "", country: "US", classCode: "", values: "" });
  const [errors, setErrors] = useState<SeriesErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState<IndexSeries | null>(null);
  const [busy, setBusy] = useState(false);

  function update(patch: Partial<SeriesValues>) {
    setValues((v) => ({ ...v, ...patch }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, code, input } = validateSeries(values);
    setErrors(found);
    setFailure(null);
    setSaved(null);
    if (!input || !code) return;
    setBusy(true);
    try {
      setSaved(await putIndexSeries(code, input));
      setValues((v) => ({ ...v, values: "" }));
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as SeriesErrors);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Index series" description="Yearly index values per country and asset class. Insurance policies use them to index the replacement value; a missing year falls back to the latest loaded one." />
      <Card title="Load index values">
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Load index values">
          <FormField id="series-code" label="Series code" error={errors.code}>
            {(control) => <Input {...control} className={styles.mono} value={values.code} onChange={(e) => update({ code: e.target.value.toUpperCase() })} invalid={Boolean(errors.code)} />}
          </FormField>
          <FormField id="series-country" label="Country" error={errors.country}>
            {(control) => <Input {...control} value={values.country} onChange={(e) => update({ country: e.target.value.toUpperCase() })} invalid={Boolean(errors.country)} />}
          </FormField>
          <FormField id="series-classCode" label="Asset class" error={errors.classCode}>
            {(control) => <Input {...control} className={styles.mono} value={values.classCode} onChange={(e) => update({ classCode: e.target.value.toUpperCase() })} invalid={Boolean(errors.classCode)} />}
          </FormField>
          <FormField id="series-values" label="Values" hint="One year per line, such as 2026=118.5. Years you leave out keep their value." error={errors.values}>
            {(control) => <Textarea {...control} mono rows={4} value={values.values} onChange={(e) => update({ values: e.target.value })} invalid={Boolean(errors.values)} />}
          </FormField>
          {saved ? (
            <div className={styles.formActions}>
              <Alert tone="success" title={`Series ${saved.code} saved`} live="status">
                {`${saved.values.length} years loaded.`}
              </Alert>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the series" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save series
            </Button>
          </div>
        </form>
      </Card>
      {state.error ? (
        <Alert tone="danger" title="Could not load index series" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Index series"
          columns={seriesColumns}
          rows={state.data ?? []}
          getRowKey={(s) => s.code}
          loading={state.loading}
          empty={{ title: "No index series yet", description: "Load a series above to index insured values." }}
        />
      )}
    </div>
  );
}
