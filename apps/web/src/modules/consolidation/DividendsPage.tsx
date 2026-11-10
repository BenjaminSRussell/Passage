import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, PageHeader, StatGroup, StatTile } from "../../shell/ui";
import { previewDividends, type DividendRequest, type Entry } from "./api/client";
import { entryColumns } from "./entries";
import { Fields, type FieldSpec } from "./Fields";
import { amount, isCurrency, isNonZero, isPositive } from "./format";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Key = keyof DividendRequest;

const fields: FieldSpec<Key>[] = [
  { name: "currency", label: "Group currency", mono: true },
  { name: "ownershipPct", label: "Parent ownership (%)", decimal: true, hint: "Above 0 and at most 100." },
  { name: "amount", label: "Dividend declared", decimal: true, hint: "In the subsidiary's currency." },
  { name: "rate", label: "Rate on the dividend date", decimal: true, hint: "Subsidiary currency to group currency." },
];

/** S1 (80 %, EUR) declares EUR 50,000.00, translated at 1.10. */
export const sampleDividend: Record<Key, string> = { currency: "USD", ownershipPct: "80", amount: "50000.00", rate: "1.10" };

const percentText = /^0*(\d{1,2}(\.\d+)?|100(\.0+)?)$/;

/** Checks that mirror the service, and the request the form sends. */
export function validateDividend(values: Record<Key, string>): Checked<DividendRequest> {
  const errors: Record<string, string> = {};
  const v = (k: Key) => values[k].trim();
  if (!isCurrency(v("currency").toUpperCase())) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!(isPositive(v("ownershipPct")) && percentText.test(v("ownershipPct")))) errors.ownershipPct = "Enter an ownership above 0 and at most 100.";
  if (!isPositive(v("amount"))) errors.amount = "Enter a dividend above 0.";
  if (!isPositive(v("rate"))) errors.rate = "Enter a rate above 0.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, request: { currency: v("currency").toUpperCase(), ownershipPct: v("ownershipPct"), amount: v("amount"), rate: v("rate") } };
}

const columns = entryColumns<Entry>();

/** E6: eliminate a dividend paid within the group, with the per-rule rounding line (FR-40.9, BR-40.9). */
export function DividendsPage() {
  const [values, setValues] = useState(sampleDividend);
  const { errors, failure, busy, result, run } = usePreview(previewDividends);

  function submit(event: FormEvent) {
    event.preventDefault();
    void run(validateDividend(values));
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Dividends within the group"
        description="A dividend a subsidiary pays to its parent is income in the parent and a distribution in the subsidiary; inside the group it is neither. E6 removes the parent's share from dividend income and the non-controlling share from NCI equity against dividends declared. When rounding each line leaves a cent, a rounding line keeps the rule balanced. Collecting dividends from unit balances arrives with runs."
      />
      <Card title="Dividend" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <Fields idPrefix="div" fields={fields} values={values} errors={errors} onChange={setValues} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The dividend was not eliminated" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Preview E6
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="E6 entries" columns={columns} rows={[]} getRowKey={(_, i) => String(i)} loading />
      ) : result ? (
        <>
          <StatGroup label="E6 totals">
            <StatTile label="Debit" value={`${amount(result.totals.debit)} ${result.currency}`} />
            <StatTile label="Credit" value={amount(result.totals.credit)} />
            <StatTile label="Rounding line" value={isNonZero(result.rounding) ? amount(result.rounding) : "None"} tone={isNonZero(result.rounding) ? "warning" : undefined} />
          </StatGroup>
          <Alert tone={result.balanced ? "success" : "danger"} title={result.balanced ? "E6 balances" : "E6 does not balance"}>
            Debits and credits of the dividend elimination agree after rounding to the group currency.
          </Alert>
          <DataTable
            label="E6 entries"
            columns={columns}
            rows={result.entries}
            getRowKey={(e, i) => `${e.account}-${i}`}
            empty={{ title: "No entries", description: "The service returned no E6 entries." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="percent"
            headingLevel={3}
            title="No dividend eliminated yet"
            description="The form holds an 80% subsidiary's EUR 50,000.00 dividend. Preview it, or enter your own."
          />
        </Card>
      )}
    </div>
  );
}
