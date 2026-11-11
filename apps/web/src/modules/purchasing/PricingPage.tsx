import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Button, type DataTableColumn } from "../../shell/ui";
import { describeProblem, lookupPrice, type Candidate, type LookupParams, type PriceLookup, type TieRule } from "./api/client";
import { ShortId, currencyPattern, datePattern, decimalPattern, money, todayIso, uuidPattern, type Failure } from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

const FIELDS: { name: Exclude<keyof LookupParams, "tie">; label: string; optional?: boolean; hint?: string; type?: string }[] = [
  { name: "qty", label: "Quantity" },
  { name: "unit", label: "Unit" },
  { name: "currency", label: "Currency" },
  { name: "date", label: "Price date", type: "date" },
  { name: "vendorGroup", label: "Vendor group ids", optional: true, hint: "Comma-separated, until vendor groups come from the vendor master" },
  { name: "productGroup", label: "Product group ids", optional: true, hint: "Comma-separated" },
];

export function validateLookup(p: LookupParams): Partial<Record<keyof LookupParams, string>> {
  const errors: Partial<Record<keyof LookupParams, string>> = {};
  if (!uuidPattern.test(p.vendor.trim())) errors.vendor = "Choose a vendor.";
  if (!uuidPattern.test(p.product.trim())) errors.product = "Choose a product.";
  if (!decimalPattern.test(p.qty.trim()) || Number(p.qty) <= 0) errors.qty = "Enter a positive quantity.";
  if (p.unit.trim() === "") errors.unit = "Enter a unit.";
  if (!currencyPattern.test(p.currency)) errors.currency = "Use a three-letter ISO code such as USD.";
  if (!datePattern.test(p.date)) errors.date = "Enter a date.";
  for (const name of ["vendorGroup", "productGroup"] as const) {
    const ids = (p[name] ?? "").split(",").map((s) => s.trim()).filter(Boolean);
    if (ids.some((id) => !uuidPattern.test(id))) errors[name] = "Enter UUIDs separated by commas.";
  }
  return errors;
}

const candidateColumns: DataTableColumn<Candidate>[] = [
  { key: "rank", header: "Rank", align: "end", render: (c) => (c.rank > 0 ? c.rank : "—") },
  {
    key: "agreementId",
    header: "Price row",
    render: (c) => (
      <Link className={styles.link} to={`/purchasing/trade-agreements/${c.agreementId}`}>
        <ShortId id={c.agreementId} />
      </Link>
    ),
  },
  { key: "specificity", header: "Specificity", align: "end" },
  { key: "matched", header: "Matched", render: (c) => (c.matched ? "Yes" : "No") },
  { key: "reason", header: "Why", render: (c) => (c.rank === 1 ? "Winner" : c.reason) },
];

/** Explains a trade agreement price: the winner and every candidate row considered (FR-17.6). */
export function PriceLookupPage() {
  const [form, setForm] = useState<LookupParams>({
    vendor: "",
    product: "",
    qty: "1",
    date: todayIso(),
    currency: "USD",
    unit: "EA",
    tie: "",
    vendorGroup: "",
    productGroup: "",
  });
  const [errors, setErrors] = useState<Partial<Record<keyof LookupParams, string>>>({});
  const [result, setResult] = useState<PriceLookup | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    const found = validateLookup(form);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await lookupPrice(form);
      setResult(body);
    } catch (err) {
      setResult(null);
      setFailure(describeProblem(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Price lookup"
        description="See which trade agreement row prices a line, and why every other row lost or did not apply."
      />
      <Card>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <div className={styles.grid}>
            <LookupFormField id="lookup-vendor" label="Vendor" entity="vendor" value={form.vendor} error={errors.vendor} onChange={(vendor) => setForm((prev) => ({ ...prev, vendor }))} />
            <LookupFormField
              id="lookup-product"
              label="Product"
              entity="product"
              value={form.product}
              error={errors.product}
              onChange={(product, option) => setForm((prev) => ({ ...prev, product, ...(option?.unit && (prev.unit === "" || prev.unit === "EA") ? { unit: option.unit } : {}) }))}
            />
            {FIELDS.map((f) => (
              <FormField key={f.name} id={`lookup-${f.name}`} label={f.label} optional={f.optional} hint={f.hint} error={errors[f.name]}>
                {(control) => (
                  <Input
                    {...control}
                    type={f.type}
                    value={form[f.name] ?? ""}
                    onChange={(e) => setForm({ ...form, [f.name]: f.name === "currency" ? e.target.value.toUpperCase() : e.target.value })}
                  />
                )}
              </FormField>
            ))}
            <FormField id="lookup-tie" label="Tie rule">
              {(control) => (
                <Select {...control} value={form.tie ?? ""} onChange={(e) => setForm({ ...form, tie: e.target.value as TieRule | "" })}>
                  <option value="">Latest valid from, then lowest price</option>
                  <option value="LOWEST_PRICE">Lowest price, then latest valid from</option>
                </Select>
              )}
            </FormField>
          </div>
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy}>
              Find price
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {failure ? <ProblemMessage problem={failure} /> : null}
      </Alert>
      {result ? (
        <>
          <Card title="Result">
            {result.winner ? (
              <p className={styles.value} role="status">
                {money(result.winner.price, result.winner.currency)} per {result.winner.priceUnit} {result.winner.unit}, from price row{" "}
                <Link className={styles.link} to={`/purchasing/trade-agreements/${result.winner.id}`}>
                  {result.winner.id.slice(0, 8)}
                </Link>
                .
              </p>
            ) : (
              <EmptyState compact title="No trade agreement matches" description="Enter a unit price on the purchase order line instead." />
            )}
          </Card>
          <DataTable
            label="Candidates considered"
            columns={candidateColumns}
            rows={result.candidates}
            getRowKey={(c) => c.agreementId}
            empty={{ title: "No rows for this vendor, product, currency and unit", description: "Add a price row in the trade agreement journal." }}
          />
        </>
      ) : null}
    </div>
  );
}
