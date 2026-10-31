import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  METHODS,
  previewAllocation,
  RATABLE_BASES,
  RESIDUAL_RULES,
  SSP_SOURCES,
  type AllocatedObligation,
  type AllocationPreview,
  type AllocationPreviewInput,
  type ObligationInput,
  type Problem,
  type RatableBasis,
  type RecognitionMethod,
  type ResidualRule,
  type SchedulePeriod,
  type SspSource,
} from "./api/client";
import { BASIS_HINT, BASIS_LABELS, CURRENCY, DAY, DECIMAL, METHOD_LABELS, RESIDUAL_SSP_HINT, SSP_SOURCE_LABELS } from "./format";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

export interface ObligationValues {
  id: string;
  ssp: string;
  sspSource: SspSource;
  method: RecognitionMethod;
  termStart: string;
  termEnd: string;
}

export interface PreviewValues {
  currency: string;
  transactionPrice: string;
  residualRule: ResidualRule;
  ratableBasis: RatableBasis;
  obligations: ObligationValues[];
}

const MAX_OBLIGATIONS = 50;
const emptyObligation: ObligationValues = { id: "", ssp: "", sspSource: "EXPLICIT", method: "POINT_IN_TIME", termStart: "", termEnd: "" };

export const emptyPreview: PreviewValues = { currency: "USD", transactionPrice: "", residualRule: "LARGEST", ratableBasis: "DAILY", obligations: [{ ...emptyObligation }] };

/** Client checks that mirror the revrec preview; the service has the last word. */
export function validatePreview(v: PreviewValues): Errors {
  const errors: Errors = {};
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!DECIMAL.test(v.transactionPrice.trim())) errors.transactionPrice = "Enter the contract price as a number such as 10800.00.";
  const seen = new Set<string>();
  v.obligations.forEach((o, i) => {
    const at = (f: string) => `obligations[${i}].${f}`;
    const id = o.id.trim();
    if (id === "" || id.length > 64 || seen.has(id)) errors[at("id")] = "Enter a unique name of up to 64 characters.";
    seen.add(id);
    if (o.sspSource !== "RESIDUAL" && (!DECIMAL.test(o.ssp.trim()) || Number(o.ssp) <= 0)) errors[at("ssp")] = "Enter a standalone selling price above zero.";
    if (o.method === "RATABLE" && (!DAY.test(o.termStart) || !DAY.test(o.termEnd) || o.termEnd < o.termStart)) {
      errors[at("term")] = "A ratable obligation needs a term start and an end on or after it.";
    }
  });
  return errors;
}

export function toPreviewInput(v: PreviewValues): AllocationPreviewInput {
  return {
    currency: v.currency.trim(),
    transactionPrice: v.transactionPrice.trim(),
    residualRule: v.residualRule,
    ratableBasis: v.ratableBasis,
    obligations: v.obligations.map((o): ObligationInput => ({
      id: o.id.trim(),
      ...(o.sspSource === "RESIDUAL" ? {} : { ssp: o.ssp.trim() }),
      sspSource: o.sspSource,
      method: o.method,
      ...(o.method === "RATABLE" ? { termStart: o.termStart, termEnd: o.termEnd } : {}),
    })),
  };
}

const scheduleColumns: DataTableColumn<SchedulePeriod>[] = [
  { key: "start", header: "From", mono: true },
  { key: "end", header: "To", mono: true },
  { key: "amount", header: "Amount", mono: true, align: "end" },
];

/** Revenue allocation preview (FR-29.7, BR-29.1): SSP allocation of a contract price and ratable schedules. Stores nothing. */
export function AllocationPreviewPage() {
  const [values, setValues] = useState<PreviewValues>(emptyPreview);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<AllocationPreview | null>(null);
  const [busy, setBusy] = useState(false);

  function update(patch: Partial<PreviewValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
  }

  function setObligation(i: number, patch: Partial<ObligationValues>) {
    update({ obligations: values.obligations.map((o, j) => (j === i ? { ...o, ...patch } : o)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validatePreview(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await previewAllocation(toPreviewInput(values)));
    } catch (error) {
      const problem = describeProblem(error);
      setResult(null);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const currency = result?.currency ?? "";
  const allocationColumns: DataTableColumn<AllocatedObligation>[] = [
    { key: "id", header: "Obligation" },
    { key: "method", header: "Recognition", render: (o) => METHOD_LABELS[o.method] ?? o.method },
    { key: "sspSource", header: "SSP source", render: (o) => SSP_SOURCE_LABELS[o.sspSource] ?? o.sspSource },
    { key: "ssp", header: "SSP", mono: true, align: "end", render: (o) => `${o.ssp} ${currency}` },
    { key: "allocated", header: "Allocated", mono: true, align: "end", render: (o) => `${o.allocated} ${currency}` },
  ];

  function text(i: number, field: "id" | "ssp" | "termStart" | "termEnd", label: string, opts: { mono?: boolean; decimal?: boolean; type?: string; error?: string } = {}) {
    const name = `obligations[${i}].${field}`;
    const error = opts.error ?? errors[name];
    return (
      <FormField id={`rr-ob-${i}-${field}`} label={label} error={error}>
        {(c) => (
          <Input
            {...c}
            type={opts.type}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values.obligations[i][field]}
            onChange={(e) => setObligation(i, { [field]: e.target.value })}
            invalid={Boolean(error)}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Revenue allocation"
        description="Split a contract price across its performance obligations by standalone selling price (ASC 606 / IFRS 15) and see the ratable schedules. Nothing is saved."
        actions={
          <Link className={buttonClassName("ghost")} to="/billing">
            All invoices
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="rr-currency" label="Currency" error={errors.currency}>
            {(c) => <Input {...c} className={styles.mono} value={values.currency} onChange={(e) => update({ currency: e.target.value })} invalid={Boolean(errors.currency)} />}
          </FormField>
          <FormField id="rr-price" label="Contract price" hint="The transaction price after any bundle discount." error={errors.transactionPrice}>
            {(c) => (
              <Input
                {...c}
                className={styles.mono}
                inputMode="decimal"
                value={values.transactionPrice}
                onChange={(e) => update({ transactionPrice: e.target.value })}
                invalid={Boolean(errors.transactionPrice)}
              />
            )}
          </FormField>
          <FormField id="rr-residual" label="Rounding residual to" hint="Which obligation absorbs the cents left after rounding." error={errors.residualRule}>
            {(c) => (
              <Select {...c} value={values.residualRule} onChange={(e) => update({ residualRule: e.target.value as ResidualRule })}>
                {RESIDUAL_RULES.map((r) => (
                  <option key={r} value={r}>
                    {r === "LARGEST" ? "Largest obligation" : r === "FIRST" ? "First obligation" : "Last obligation"}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="rr-basis" label="Ratable basis" hint={BASIS_HINT} error={errors.ratableBasis}>
            {(c) => (
              <Select {...c} value={values.ratableBasis} onChange={(e) => update({ ratableBasis: e.target.value as RatableBasis })}>
                {RATABLE_BASES.map((b) => (
                  <option key={b} value={b}>
                    {BASIS_LABELS[b]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.obligations.map((o, i) => (
            <fieldset key={i} className={styles.lineSet}>
              <legend className={styles.groupsLegend}>{`Obligation ${i + 1}`}</legend>
              {text(i, "id", "Name", {})}
              <FormField id={`rr-ob-${i}-source`} label="SSP source" hint={o.sspSource === "RESIDUAL" ? RESIDUAL_SSP_HINT : undefined}>
                {(c) => (
                  <Select {...c} value={o.sspSource} onChange={(e) => setObligation(i, { sspSource: e.target.value as SspSource })}>
                    {SSP_SOURCES.map((s) => (
                      <option key={s} value={s}>
                        {SSP_SOURCE_LABELS[s]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              {o.sspSource === "RESIDUAL" ? null : text(i, "ssp", "Standalone selling price", { mono: true, decimal: true })}
              <FormField id={`rr-ob-${i}-method`} label="Recognition">
                {(c) => (
                  <Select {...c} value={o.method} onChange={(e) => setObligation(i, { method: e.target.value as RecognitionMethod })}>
                    {METHODS.map((m) => (
                      <option key={m} value={m}>
                        {METHOD_LABELS[m]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              {o.method === "RATABLE" ? (
                <>
                  {text(i, "termStart", "Term start", { type: "date", mono: true, error: errors[`obligations[${i}].term`] })}
                  {text(i, "termEnd", "Term end", { type: "date", mono: true })}
                </>
              ) : null}
              {values.obligations.length > 1 ? (
                <Button variant="ghost" size="sm" onClick={() => update({ obligations: values.obligations.filter((_, j) => j !== i) })}>
                  {`Remove obligation ${i + 1}`}
                </Button>
              ) : null}
            </fieldset>
          ))}
          <div className={styles.formActions}>
            {values.obligations.length < MAX_OBLIGATIONS ? (
              <Button variant="secondary" onClick={() => update({ obligations: [...values.obligations, { ...emptyObligation }] })}>
                Add obligation
              </Button>
            ) : null}
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not allocate the contract" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Allocate
            </Button>
          </div>
        </form>
      </Card>
      {result ? (
        <>
          <Card title="Allocation">
            <div className={styles.stack}>
              <p className={styles.note}>{`Contract price ${result.transactionPrice} ${currency}; the rounding residual goes to the ${result.residualRule.toLowerCase()} obligation; ratable schedules use the ${result.ratableBasis.toLowerCase()} basis.`}</p>
              <DataTable label="Allocation" columns={allocationColumns} rows={result.obligations} getRowKey={(o) => o.id} />
            </div>
          </Card>
          {result.obligations
            .filter((o) => (o.schedule ?? []).length > 0)
            .map((o) => (
              <Card key={o.id} title={`Schedule: ${o.id}`}>
                <DataTable label={`Schedule for ${o.id}`} columns={scheduleColumns} rows={o.schedule ?? []} getRowKey={(p) => p.start} />
              </Card>
            ))}
        </>
      ) : null}
    </div>
  );
}
