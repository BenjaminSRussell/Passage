import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  previewReclassification,
  type OpenItem,
  type ReclassificationRequest,
  type ReclassLevel,
  type ReclassLine,
  type ReclassRule,
  type ReclassType,
} from "./api/client";
import { Fields, type FieldSpec } from "./Fields";
import { amount, dash, isCount, isCurrency, isDecimal, isNonZero, isSignedDecimal } from "./format";
import { RowGrid, type RowField } from "./RowGrid";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";

type HeadKey = "currency" | "periodEnd";
type RuleKey = "code" | "type" | "order" | "sourceAccount" | "targetAccount" | "threshold" | "termMonths";
type ItemKey = "party" | "account" | "dueDate" | "amount";
type BalanceKey = "account" | "amount";

export interface ReclassValues {
  head: Record<HeadKey, string>;
  rules: Record<RuleKey, string>[];
  items: Record<ItemKey, string>[];
  balances: Record<BalanceKey, string>[];
}

/** Rule types with the level each runs at (FR-40.17). */
export const ruleTypes: { value: ReclassType; label: string; level: ReclassLevel }[] = [
  { value: "DebitBalanceVendors", label: "Vendors with a debit balance (entity)", level: "Entity" },
  { value: "CreditBalanceCustomers", label: "Customers with a credit balance (entity)", level: "Entity" },
  { value: "RemainingTerm", label: "Due after the remaining term (entity)", level: "Entity" },
  { value: "SignBased", label: "Negative group balance (group)", level: "Group" },
];

const headFields: FieldSpec<HeadKey>[] = [
  { name: "currency", label: "Currency", mono: true },
  { name: "periodEnd", label: "Period end", type: "date" },
];

const ruleFields: RowField<RuleKey>[] = [
  { name: "code", label: "Rule", mono: true },
  { name: "type", label: "Type", options: ruleTypes.map(({ value, label }) => ({ value, label })) },
  { name: "order", label: "Order" },
  { name: "sourceAccount", label: "From account", mono: true },
  { name: "targetAccount", label: "To account", mono: true },
  { name: "threshold", label: "Threshold", decimal: true, optional: true },
  { name: "termMonths", label: "Term (months)", optional: true },
];

const itemFields: RowField<ItemKey>[] = [
  { name: "party", label: "Vendor or customer", mono: true },
  { name: "account", label: "Account", mono: true },
  { name: "dueDate", label: "Due date", type: "date", optional: true },
  { name: "amount", label: "Balance (debit +, credit −)", decimal: true },
];

const balanceFields: RowField<BalanceKey>[] = [
  { name: "account", label: "FS item", mono: true },
  { name: "amount", label: "Balance (debit +, credit −)", decimal: true },
];

const emptyRule = (): Record<RuleKey, string> => ({ code: "", type: "DebitBalanceVendors", order: "", sourceAccount: "", targetAccount: "", threshold: "", termMonths: "" });
const emptyItem = (): Record<ItemKey, string> => ({ party: "", account: "", dueDate: "", amount: "" });
const emptyBalance = (): Record<BalanceKey, string> => ({ account: "", amount: "" });

/** AC-40.13: vendor V-100 has a USD 5,000.00 debit balance at period end; group cash is overdrawn. */
export const ac4013: ReclassValues = {
  head: { currency: "USD", periodEnd: "2026-11-30" },
  rules: [
    { code: "VDB", type: "DebitBalanceVendors", order: "1", sourceAccount: "VENDOR_RECLASS", targetAccount: "OTHER_RECEIVABLES", threshold: "", termMonths: "" },
    { code: "CASH", type: "SignBased", order: "2", sourceAccount: "CASH", targetAccount: "BANK_OVERDRAFTS", threshold: "", termMonths: "" },
  ],
  items: [
    { party: "V-100", account: "VENDOR_RECLASS", dueDate: "", amount: "5000.00" },
    { party: "V-200", account: "VENDOR_RECLASS", dueDate: "", amount: "-800.00" },
  ],
  balances: [{ account: "CASH", amount: "-250.00" }],
};

const blank = (row: Record<string, string>) => Object.values(row).every((v) => v.trim() === "");

/** Checks that mirror the service, and the request the form sends. Empty item and balance rows are left out. */
export function validateReclass(values: ReclassValues): Checked<ReclassificationRequest> {
  const errors: Record<string, string> = {};
  const currency = values.head.currency.trim().toUpperCase();
  const periodEnd = values.head.periodEnd.trim();
  if (!isCurrency(currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!/^\d{4}-\d{2}-\d{2}$/.test(periodEnd) || Number.isNaN(Date.parse(`${periodEnd}T00:00:00Z`))) errors.periodEnd = "Enter the period end date.";
  if (values.rules.length === 0) errors.rules = "Add at least one rule.";
  const codes = new Set<string>();
  const rules: ReclassRule[] = values.rules.map((row, i) => {
    const at = (k: RuleKey) => `rules[${i}].${k}`;
    const v = (k: RuleKey) => row[k].trim();
    const type = ruleTypes.find((t) => t.value === v("type"));
    if (v("code") === "") errors[at("code")] = "Enter the rule code.";
    else if (codes.has(v("code"))) errors[at("code")] = "Rule codes must be unique.";
    codes.add(v("code"));
    if (!type) errors[at("type")] = "Choose a rule type.";
    if (v("order") !== "" && !isCount(v("order"))) errors[at("order")] = "Enter a whole number.";
    if (v("sourceAccount") === "") errors[at("sourceAccount")] = "Enter the account to move from.";
    if (v("targetAccount") === "") errors[at("targetAccount")] = "Enter the account to move to.";
    else if (v("targetAccount") === v("sourceAccount")) errors[at("targetAccount")] = "Move to a different account.";
    if (v("threshold") !== "" && !isDecimal(v("threshold"))) errors[at("threshold")] = "Enter a threshold, 0 or more, or leave it empty.";
    if (v("termMonths") !== "" && !isCount(v("termMonths"))) errors[at("termMonths")] = "Enter a whole number of months, or leave it empty.";
    return {
      code: v("code"),
      level: type?.level ?? "Entity",
      type: (type?.value ?? v("type")) as ReclassType,
      order: v("order") === "" ? undefined : Number(v("order")),
      sourceAccount: v("sourceAccount"),
      targetAccount: v("targetAccount"),
      threshold: v("threshold") || undefined,
      termMonths: v("termMonths") === "" ? undefined : Number(v("termMonths")),
    };
  });
  const openItems: OpenItem[] = [];
  values.items.forEach((row, i) => {
    if (blank(row)) return;
    const at = (k: ItemKey) => `openItems[${i}].${k}`;
    const v = (k: ItemKey) => row[k].trim();
    if (v("party") === "") errors[at("party")] = "Enter the vendor or customer.";
    if (v("account") === "") errors[at("account")] = "Enter the account.";
    if (!isSignedDecimal(v("amount"))) errors[at("amount")] = "Enter the balance, negative for a credit.";
    openItems.push({ party: v("party"), account: v("account"), dueDate: v("dueDate") || undefined, amount: v("amount") });
  });
  const balances: { account: string; amount: string }[] = [];
  values.balances.forEach((row, i) => {
    if (blank(row)) return;
    const at = (k: BalanceKey) => `balances[${i}].${k}`;
    if (row.account.trim() === "") errors[at("account")] = "Enter the FS item.";
    if (!isSignedDecimal(row.amount.trim())) errors[at("amount")] = "Enter the balance, negative for a credit.";
    balances.push({ account: row.account.trim(), amount: row.amount.trim() });
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, request: { currency, periodEnd, rules, openItems, balances } };
}

const money = (v: string) => (isNonZero(v) ? amount(v) : dash);

const lineColumns: DataTableColumn<ReclassLine>[] = [
  { key: "rule", header: "Rule", render: (l) => <Badge tone="neutral">{l.rule}</Badge> },
  { key: "level", header: "Level" },
  { key: "account", header: "Account", mono: true },
  { key: "party", header: "Party", mono: true, render: (l) => l.party || dash },
  { key: "debit", header: "Debit", align: "end", render: (l) => money(l.debit) },
  { key: "credit", header: "Credit", align: "end", render: (l) => money(l.credit) },
  { key: "reverses", header: "Reverses", render: (l) => (l.reverses ? "Next period" : "No") },
];

/** Dry run of entity- and group-level reclassification rules (FR-40.17, BR-40.14, AC-40.13). */
export function ReclassificationPage() {
  const [values, setValues] = useState<ReclassValues>(ac4013);
  const { errors, failure, busy, result, run } = usePreview(previewReclassification);

  function submit(event: FormEvent) {
    event.preventDefault();
    void run(validateReclass(values));
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Reclassification and regrouping"
        description="Rules run in order. Entity-level rules move vendors in debit to other receivables, customers in credit to other payables and items due after the remaining term to non-current, and reverse on the first day of the next period. Group-level rules show a negative balance, such as overdrawn cash, on another FS item. This is a dry run; posting runs and the rerun reversal arrive with persistence."
      />
      <Card title="Rules and balances" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <Fields idPrefix="rc" fields={headFields} values={values.head} errors={errors} onChange={(head) => setValues({ ...values, head })} />
          <h4 className={styles.section}>Rules</h4>
          <RowGrid idPrefix="rc-rule" path="rules" noun="Rule" fields={ruleFields} rows={values.rules} errors={errors} newRow={emptyRule} onChange={(rules) => setValues({ ...values, rules })} />
          <h4 className={styles.section}>Open items (entity rules)</h4>
          <RowGrid idPrefix="rc-item" path="openItems" noun="Open item" fields={itemFields} rows={values.items} errors={errors} newRow={emptyItem} onChange={(items) => setValues({ ...values, items })} />
          <h4 className={styles.section}>Group balances (group rules)</h4>
          <RowGrid idPrefix="rc-bal" path="balances" noun="Balance" fields={balanceFields} rows={values.balances} errors={errors} newRow={emptyBalance} onChange={(balances) => setValues({ ...values, balances })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The dry run failed" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run dry run
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Reclassification lines" columns={lineColumns} rows={[]} getRowKey={(_, i) => String(i)} loading />
      ) : result ? (
        <>
          <StatGroup label="Dry run">
            <StatTile label="Rules applied" value={result.applied.length} />
            <StatTile label="Moved" value={`${amount(result.totals.debit)} ${result.currency}`} />
            <StatTile label="Reverses on" value={result.reversalDate ?? "No reversal"} />
          </StatGroup>
          <DataTable
            label="Reclassification lines"
            columns={lineColumns}
            rows={result.lines}
            getRowKey={(l, i) => `${l.rule}-${l.account}-${i}`}
            empty={{ title: "Nothing to reclassify", description: "No balance met a rule at this period end." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="workflow"
            headingLevel={3}
            title="No dry run yet"
            description="The form is filled in with an example that has an overdrawn group cash balance. Run it, or enter your own rules."
          />
        </Card>
      )}
    </div>
  );
}
