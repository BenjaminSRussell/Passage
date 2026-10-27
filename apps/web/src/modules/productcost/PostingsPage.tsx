import { useState, type FormEvent } from "react";
import { Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile } from "../../shell/ui";
import { cogsPosting, describeProblem, priceDifferencePosting, type PostingPreview, type Problem, type Split } from "./api/client";
import { amount, isDecimal, isPositive } from "./format";
import { LineGrid, type LineField } from "./LineGrid";
import { ProblemAlert, journalColumns } from "./shared";
import styles from "./pages.module.css";

type ComponentKey = "component" | "amount";
type ComponentValues = Record<ComponentKey, string>;

const componentFields: LineField<ComponentKey>[] = [
  { name: "component", label: "Cost component", mono: true },
  { name: "amount", label: "Unit amount", decimal: true },
];

const emptyComponent = (): ComponentValues => ({ component: "", amount: "" });

type Errors = Record<string, string>;

function checkCurrency(value: string, errors: Errors): string | undefined {
  const currency = value.trim().toUpperCase();
  if (currency !== "" && !/^[A-Z]{3}$/.test(currency))
    errors.currency = "Enter a three-letter currency code such as USD, or leave it empty.";
  return currency || undefined;
}

/** Turns the component lines into a split, keyed as the service reports errors (path[i].field). */
export function splitFromLines(lines: ComponentValues[], path: string, errors: Errors): Split {
  const split: Split = {};
  if (lines.length === 0) errors[path] = "Add at least one cost component.";
  lines.forEach((ln, i) => {
    const component = ln.component.trim();
    if (component === "") errors[`${path}[${i}].component`] = "Enter the component code.";
    else if (component in split) errors[`${path}[${i}].component`] = "Each component appears once.";
    if (!isDecimal(ln.amount)) errors[`${path}[${i}].amount`] = "Enter an amount, 0 or more.";
    if (component !== "") split[component] = ln.amount.trim();
  });
  return split;
}

interface CogsValues {
  currency: string;
  quantity: string;
  unitCost: ComponentValues[];
}

/** AC-39.12: ten FG-100 at 104.32 issued, split DM 21.20, DL 54.00, IND-MAT 2.12, IND-LAB 27.00. */
export const cogsExample: CogsValues = {
  currency: "USD",
  quantity: "10",
  unitCost: [
    { component: "DM", amount: "21.20" },
    { component: "DL", amount: "54.00" },
    { component: "IND-MAT", amount: "2.12" },
    { component: "IND-LAB", amount: "27.00" },
  ],
};

export function validateCogs(values: CogsValues): { errors: Errors; request?: { currency?: string; quantity: string; unitCost: Split } } {
  const errors: Errors = {};
  const currency = checkCurrency(values.currency, errors);
  if (!isPositive(values.quantity)) errors.quantity = "Enter the issued quantity, above 0.";
  const unitCost = splitFromLines(values.unitCost, "unitCost", errors);
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, request: { currency, quantity: values.quantity.trim(), unitCost } };
}

interface PpvValues {
  currency: string;
  quantity: string;
  receiptUnitCost: string;
  standard: ComponentValues[];
}

/** AC-39.7: a receipt of 10 at 5.20 for an item whose standard is 5.00 material. */
export const ppvExample: PpvValues = {
  currency: "USD",
  quantity: "10",
  receiptUnitCost: "5.20",
  standard: [{ component: "DM", amount: "5.00" }],
};

export function validatePpv(values: PpvValues): {
  errors: Errors;
  request?: { currency?: string; quantity: string; receiptUnitCost: string; standard: Split };
} {
  const errors: Errors = {};
  const currency = checkCurrency(values.currency, errors);
  if (!isPositive(values.quantity)) errors.quantity = "Enter the received quantity, above 0.";
  if (!isDecimal(values.receiptUnitCost)) errors.receiptUnitCost = "Enter the receipt unit cost, 0 or more.";
  const standard = splitFromLines(values.standard, "standard", errors);
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, request: { currency, quantity: values.quantity.trim(), receiptUnitCost: values.receiptUnitCost.trim(), standard } };
}

function PostingResult({ label, preview, emptyText }: { label: string; preview: PostingPreview; emptyText: string }) {
  return (
    <div className={styles.stack}>
      <StatGroup label={`${label} total`}>
        <StatTile label="Total" value={`${amount(preview.total)}${preview.currency ? ` ${preview.currency}` : ""}`} />
        <StatTile label="Journal lines" value={preview.journal.length} />
      </StatGroup>
      <DataTable
        label={label}
        columns={journalColumns}
        rows={preview.journal}
        getRowKey={(j) => j.account}
        empty={{ title: "Nothing to post", description: emptyText }}
      />
    </div>
  );
}

function CurrencyField({ id, value, error, onChange }: { id: string; value: string; error?: string; onChange(v: string): void }) {
  return (
    <FormField id={id} label="Currency" hint="Sets the posting precision: two decimals by default, none for JPY." optional error={error}>
      {(control) => <Input {...control} value={value} onChange={(e) => onChange(e.target.value)} invalid={Boolean(error)} />}
    </FormField>
  );
}

function CogsCard() {
  const [values, setValues] = useState<CogsValues>(cogsExample);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<PostingPreview | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateCogs(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    setBusy(true);
    try {
      setPreview(await cogsPosting(request));
    } catch (err) {
      const problem = describeProblem(err);
      setErrors(problem.fields);
      setFailure(problem);
      setPreview(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Cost of goods sold by component" headingLevel={3}>
      <div className={styles.stack}>
        <p className={styles.note}>
          A sales issue debits one cost of goods sold account per component of the item&apos;s active split and credits inventory. The
          components always add up to the issue total; any rounding goes to material.
        </p>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          <FormField id="cogs-quantity" label="Issued quantity" error={errors.quantity}>
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={values.quantity}
                onChange={(e) => setValues({ ...values, quantity: e.target.value })}
                invalid={Boolean(errors.quantity)}
              />
            )}
          </FormField>
          <CurrencyField id="cogs-currency" value={values.currency} error={errors.currency} onChange={(currency) => setValues({ ...values, currency })} />
          <LineGrid
            idPrefix="cogs-component"
            noun="Component"
            path="unitCost"
            fields={componentFields}
            lines={values.unitCost}
            errors={errors}
            newLine={emptyComponent}
            onChange={(unitCost) => setValues({ ...values, unitCost })}
          />
          {failure ? <ProblemAlert title="The COGS posting was not computed" problem={failure} /> : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Preview COGS posting
            </Button>
          </div>
        </form>
        {busy ? (
          <DataTable label="COGS journal" columns={journalColumns} rows={[]} getRowKey={(j) => j.account} loading />
        ) : preview ? (
          <PostingResult label="COGS journal" preview={preview} emptyText="The issue has no cost." />
        ) : (
          <EmptyState
            icon="receipt"
            headingLevel={3}
            compact
            title="No COGS posting yet"
            description="Enter the issued quantity and the item's unit cost by component to see the journal."
          />
        )}
      </div>
    </Card>
  );
}

function PpvCard() {
  const [values, setValues] = useState<PpvValues>(ppvExample);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<PostingPreview | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validatePpv(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    setBusy(true);
    try {
      setPreview(await priceDifferencePosting(request));
    } catch (err) {
      const problem = describeProblem(err);
      setErrors(problem.fields);
      setFailure(problem);
      setPreview(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Purchase price variance by component" headingLevel={3}>
      <div className={styles.stack}>
        <p className={styles.note}>
          The variance is the receipt unit cost less the standard, times the quantity. It is split over the standard&apos;s components in
          proportion; an unfavourable part is a debit, a favourable one a credit, and any rounding goes to the largest component.
        </p>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          <FormField id="ppv-quantity" label="Received quantity" error={errors.quantity}>
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={values.quantity}
                onChange={(e) => setValues({ ...values, quantity: e.target.value })}
                invalid={Boolean(errors.quantity)}
              />
            )}
          </FormField>
          <FormField id="ppv-receipt" label="Receipt unit cost" error={errors.receiptUnitCost}>
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={values.receiptUnitCost}
                onChange={(e) => setValues({ ...values, receiptUnitCost: e.target.value })}
                invalid={Boolean(errors.receiptUnitCost)}
              />
            )}
          </FormField>
          <CurrencyField id="ppv-currency" value={values.currency} error={errors.currency} onChange={(currency) => setValues({ ...values, currency })} />
          <LineGrid
            idPrefix="ppv-component"
            noun="Standard component"
            path="standard"
            fields={componentFields}
            lines={values.standard}
            errors={errors}
            newLine={emptyComponent}
            onChange={(standard) => setValues({ ...values, standard })}
          />
          {failure ? <ProblemAlert title="The price difference was not computed" problem={failure} /> : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Preview price difference
            </Button>
          </div>
        </form>
        {busy ? (
          <DataTable label="Price difference journal" columns={journalColumns} rows={[]} getRowKey={(j) => j.account} loading />
        ) : preview ? (
          <PostingResult
            label="Price difference journal"
            preview={preview}
            emptyText="The receipt cost equals the standard, so there is no variance."
          />
        ) : (
          <EmptyState
            icon="receipt"
            headingLevel={3}
            compact
            title="No price difference yet"
            description="Enter the receipt and the item's standard by component to see the variance journal."
          />
        )}
      </div>
    </Card>
  );
}

/** Preview component-split postings: COGS on a sales issue and PPV on a receipt (FR-39.7, FR-39.15). Nothing is posted. */
export function PostingsPage() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Component postings"
        description="Preview the journals that post cost of goods sold and purchase price variances by cost component. Nothing is posted; posting on sales issues and receipts, with accounts from posting profiles, arrives with the ledger integration."
      />
      <CogsCard />
      <PpvCard />
    </div>
  );
}
