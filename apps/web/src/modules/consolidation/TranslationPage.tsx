import { useState, type FormEvent } from "react";
import {
  Alert,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  StatGroup,
  StatTile,
  type DataTableColumn,
} from "../../shell/ui";
import { describeProblem, previewTranslation, type Problem, type TranslationPreview, type TranslationRequest } from "./api/client";
import { amount, isCurrency, isPositive, isSignedDecimal } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Key = "currency" | "ownershipPct" | "opening" | "average" | "closing" | "openingNetAssets" | "netIncome" | "closingNetAssets";
export type TranslationValues = Record<Key, string>;

const fields: { name: Key; label: string; path: string; hint?: string; kind: "currency" | "percent" | "rate" | "amount" }[] = [
  { name: "currency", label: "Group currency", path: "currency", kind: "currency" },
  { name: "ownershipPct", label: "Parent ownership (%)", path: "ownershipPct", kind: "percent", hint: "The rest of the CTA belongs to the non-controlling interest." },
  { name: "opening", label: "Opening rate", path: "rates.opening", kind: "rate", hint: "The rate that translated the opening net assets." },
  { name: "average", label: "Average rate", path: "rates.average", kind: "rate", hint: "Translates net income for the period." },
  { name: "closing", label: "Closing rate", path: "rates.closing", kind: "rate", hint: "Translates the closing net assets." },
  { name: "openingNetAssets", label: "Opening net assets", path: "openingNetAssets", kind: "amount", hint: "In the unit's functional currency." },
  { name: "netIncome", label: "Net income", path: "netIncome", kind: "amount" },
  { name: "closingNetAssets", label: "Closing net assets", path: "closingNetAssets", kind: "amount" },
];

/** AC-40.6: unit S (EUR) at opening 1.05, average 1.08, closing 1.10, held 80%. */
export const ac406: TranslationValues = {
  currency: "USD",
  ownershipPct: "80",
  opening: "1.05",
  average: "1.08",
  closing: "1.10",
  openingNetAssets: "700000",
  netIncome: "150000",
  closingNetAssets: "850000",
};

/** Checks that mirror the service, and the request the form sends. */
export function validateTranslation(values: TranslationValues): { errors: Record<string, string>; request?: TranslationRequest } {
  const errors: Record<string, string> = {};
  const v = (k: Key) => values[k].trim();
  for (const f of fields) {
    const raw = f.kind === "currency" ? v(f.name).toUpperCase() : v(f.name);
    if (f.kind === "currency" && !isCurrency(raw)) errors[f.path] = "Enter a three-letter currency code such as USD.";
    if (f.kind === "percent" && !(isPositive(raw) && /^0*(\d{1,2}(\.\d+)?|100(\.0+)?)$/.test(raw)))
      errors[f.path] = "Enter a percentage above 0 and at most 100.";
    if (f.kind === "rate" && !isPositive(raw)) errors[f.path] = "Enter a rate above 0.";
    if (f.kind === "amount" && !isSignedDecimal(raw)) errors[f.path] = "Enter an amount.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    request: {
      currency: v("currency").toUpperCase(),
      ownershipPct: v("ownershipPct"),
      rates: { opening: v("opening"), average: v("average"), closing: v("closing") },
      openingNetAssets: v("openingNetAssets"),
      netIncome: v("netIncome"),
      closingNetAssets: v("closingNetAssets"),
    },
  };
}

interface Row {
  line: string;
  basis: string;
  value: string;
}

const columns: DataTableColumn<Row>[] = [
  { key: "line", header: "Line" },
  { key: "basis", header: "Basis" },
  { key: "value", header: "Group currency", align: "end", render: (r) => amount(r.value) },
];

function rows(t: TranslationPreview): Row[] {
  return [
    { line: "Opening net assets", basis: "Opening rate", value: t.openingNetAssets },
    { line: "Net income", basis: "Average rate", value: t.netIncome },
    { line: "Closing net assets", basis: "Closing rate", value: t.closingNetAssets },
    { line: "Translation difference (CTA)", basis: "Closing − (opening + net income)", value: t.cta },
    { line: "NCI share of CTA", basis: "Reclassified to non-controlling interest", value: t.nciShareOfCta },
    { line: "Group share of CTA", basis: "Posts to other comprehensive income", value: t.groupShareOfCta },
  ];
}

/** Translate a unit under IAS 21 and derive the CTA with its NCI share (FR-40.8, BR-40.6, AC-40.6). */
export function TranslationPage() {
  const [values, setValues] = useState<TranslationValues>(ac406);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<TranslationPreview | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateTranslation(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    setBusy(true);
    try {
      setPreview(await previewTranslation(request));
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setPreview(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Currency translation"
        description="Assets and liabilities translate at the closing rate and income at the average rate. The translation difference closes the gap and is split between the group and the non-controlling interest by ownership. Collecting trial balances and loading rates arrive with consolidation runs."
      />
      <Card title="Unit statement" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          {fields.map((f) => (
            <FormField key={f.name} id={`tr-${f.name}`} label={f.label} hint={f.hint} error={errors[f.path]}>
              {(control) => (
                <Input
                  {...control}
                  inputMode={f.kind === "currency" ? undefined : "decimal"}
                  className={f.kind === "currency" ? styles.mono : undefined}
                  value={values[f.name]}
                  onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
                  invalid={Boolean(errors[f.path])}
                />
              )}
            </FormField>
          ))}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The unit was not translated" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Translate unit
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Translated statement" columns={columns} rows={[]} getRowKey={(r) => r.line} loading />
      ) : preview ? (
        <>
          <StatGroup label="Translation difference">
            <StatTile label="CTA" value={`${amount(preview.cta)} ${preview.currency}`} />
            <StatTile label="NCI share" value={amount(preview.nciShareOfCta)} />
            <StatTile label="Group share" value={amount(preview.groupShareOfCta)} />
          </StatGroup>
          <DataTable label="Translated statement" columns={columns} rows={rows(preview)} getRowKey={(r) => r.line} />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="percent"
            headingLevel={3}
            title="No translation yet"
            description="The form is filled in with an example. Translate it, or enter your own unit's net assets, net income and rates."
          />
        </Card>
      )}
    </div>
  );
}
