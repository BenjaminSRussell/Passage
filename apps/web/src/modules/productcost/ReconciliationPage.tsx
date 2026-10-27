import { useState, type FormEvent } from "react";
import { Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  reconcileStandard,
  type Problem,
  type Reconciliation,
  type SiteBalance,
  type SiteReconciliation,
  type StandardOnHand,
} from "./api/client";
import { amount, isDecimal, isSignedDecimal } from "./format";
import { LineGrid, type LineField } from "./LineGrid";
import { ProblemAlert } from "./shared";
import styles from "./pages.module.css";

type OnHandKey = keyof StandardOnHand;
type BalanceKey = keyof SiteBalance;

interface Values {
  currency: string;
  onHand: Record<OnHandKey, string>[];
  glBalances: Record<BalanceKey, string>[];
}

const onHandFields: LineField<OnHandKey>[] = [
  { name: "siteId", label: "Site", mono: true },
  { name: "itemId", label: "Item", mono: true },
  { name: "qty", label: "On-hand quantity", decimal: true },
  { name: "stdUnit", label: "Active standard", decimal: true },
];

const balanceFields: LineField<BalanceKey>[] = [
  { name: "siteId", label: "Site", mono: true },
  { name: "amount", label: "GL inventory balance", decimal: true },
];

const emptyOnHand = (): Record<OnHandKey, string> => ({ siteId: "", itemId: "", qty: "", stdUnit: "" });
const emptyBalance = (): Record<BalanceKey, string> => ({ siteId: "", amount: "" });

/** Month end at two sites: PLANT-1 ties; PLANT-2 is 2.00 short of the GL. */
export const monthEnd: Values = {
  currency: "USD",
  onHand: [
    { siteId: "PLANT-1", itemId: "RM-301", qty: "1000", stdUnit: "10.50" },
    { siteId: "PLANT-1", itemId: "FG-100", qty: "10", stdUnit: "104.32" },
    { siteId: "PLANT-2", itemId: "SA-200", qty: "50", stdUnit: "31.96" },
  ],
  glBalances: [
    { siteId: "PLANT-1", amount: "11543.20" },
    { siteId: "PLANT-2", amount: "1600.00" },
  ],
};

type Errors = Record<string, string>;

/** Checks that mirror the service, and the request the form sends. */
export function validateReconciliation(values: Values): {
  errors: Errors;
  request?: { currency?: string; onHand: StandardOnHand[]; glBalances: SiteBalance[] };
} {
  const errors: Errors = {};
  const currency = values.currency.trim().toUpperCase();
  if (currency !== "" && !/^[A-Z]{3}$/.test(currency))
    errors.currency = "Enter a three-letter currency code such as USD, or leave it empty.";
  if (values.onHand.length === 0 && values.glBalances.length === 0) errors.onHand = "Add on-hand lines or GL balances.";
  const onHand = values.onHand.map((l, i) => {
    const at = (k: OnHandKey) => `onHand[${i}].${k}`;
    if (l.siteId.trim() === "") errors[at("siteId")] = "Enter the site.";
    if (l.itemId.trim() === "") errors[at("itemId")] = "Enter the item.";
    if (!isSignedDecimal(l.qty)) errors[at("qty")] = "Enter the on-hand quantity.";
    if (!isDecimal(l.stdUnit)) errors[at("stdUnit")] = "Enter the active standard, 0 or more.";
    return { siteId: l.siteId.trim(), itemId: l.itemId.trim(), qty: l.qty.trim(), stdUnit: l.stdUnit.trim() };
  });
  const glBalances = values.glBalances.map((b, i) => {
    if (b.siteId.trim() === "") errors[`glBalances[${i}].siteId`] = "Enter the site.";
    if (!isSignedDecimal(b.amount)) errors[`glBalances[${i}].amount`] = "Enter the GL balance.";
    return { siteId: b.siteId.trim(), amount: b.amount.trim() };
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, request: { currency: currency || undefined, onHand, glBalances } };
}

const siteColumns: DataTableColumn<SiteReconciliation>[] = [
  { key: "siteId", header: "Site", mono: true },
  { key: "lines", header: "On-hand lines", align: "end" },
  { key: "standardValue", header: "Value at standard", align: "end", render: (s) => amount(s.standardValue) },
  { key: "glBalance", header: "GL balance", align: "end", render: (s) => amount(s.glBalance) },
  { key: "difference", header: "Difference", align: "end", render: (s) => amount(s.difference) },
  {
    key: "status",
    header: "Status",
    render: (s) => (s.status === "Reconciled" ? <Badge tone="success">Reconciled</Badge> : <Badge tone="danger">Difference</Badge>),
  },
];

/** Reconcile on-hand at the active standard to the GL inventory balance per site (FR-39.11, AC-39.9). */
export function ReconciliationPage() {
  const [values, setValues] = useState<Values>(monthEnd);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Reconciliation | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateReconciliation(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    setBusy(true);
    try {
      setResult(await reconcileStandard(request));
    } catch (err) {
      const problem = describeProblem(err);
      setErrors(problem.fields);
      setFailure(problem);
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Standard cost reconciliation"
        description="On-hand quantity times the active standard, summed per site, is compared with the GL inventory balance. Any difference is reported. The nightly run on the inventory value report and ledger balances arrives with stored standards."
      />
      <Card title="On hand and GL balances" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          <FormField
            id="recon-currency"
            label="Currency"
            hint="Sets the valuation precision: two decimals by default, none for JPY."
            optional
            error={errors.currency}
          >
            {(control) => (
              <Input
                {...control}
                value={values.currency}
                onChange={(e) => setValues({ ...values, currency: e.target.value })}
                invalid={Boolean(errors.currency)}
              />
            )}
          </FormField>
          <LineGrid
            idPrefix="recon-onhand"
            noun="On-hand line"
            path="onHand"
            fields={onHandFields}
            lines={values.onHand}
            errors={errors}
            newLine={emptyOnHand}
            onChange={(onHand) => setValues({ ...values, onHand })}
          />
          <LineGrid
            idPrefix="recon-balance"
            noun="GL balance"
            path="glBalances"
            fields={balanceFields}
            lines={values.glBalances}
            errors={errors}
            newLine={emptyBalance}
            onChange={(glBalances) => setValues({ ...values, glBalances })}
          />
          {failure ? <ProblemAlert title="The reconciliation was not computed" problem={failure} /> : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Reconcile
            </Button>
            <Button variant="ghost" onClick={() => setValues(monthEnd)}>
              Load example
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Reconciliation by site" columns={siteColumns} rows={[]} getRowKey={(s) => s.siteId} loading />
      ) : result ? (
        <>
          <StatGroup label="Reconciliation">
            <StatTile label="Sites" value={result.sites.length} />
            <StatTile label="Differences" value={result.differences} tone="danger" />
          </StatGroup>
          <DataTable
            label="Reconciliation by site"
            columns={siteColumns}
            rows={result.sites}
            getRowKey={(s) => s.siteId}
            empty={{ title: "No sites", description: "There was nothing to reconcile." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="checkCircle"
            headingLevel={3}
            title="No reconciliation yet"
            description="Enter the on-hand lines at standard and the GL inventory balance per site to see whether they tie."
          />
        </Card>
      )}
    </div>
  );
}
