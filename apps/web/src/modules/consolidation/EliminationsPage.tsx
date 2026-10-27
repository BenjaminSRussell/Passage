import { useState, type FormEvent } from "react";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  type DataTableColumn,
  type FormFieldControlProps,
} from "../../shell/ui";
import {
  describeProblem,
  previewEliminations,
  type EliminationEntry,
  type EliminationPreview,
  type EliminationRequest,
  type Problem,
  type RuleCode,
} from "./api/client";
import { amount, dash, isCurrency, isDecimal, isNonZero, isPositive, isSignedDecimal } from "./format";
import { accountNames } from "./entries";
import styles from "./pages.module.css";

type AmountKey =
  | "ownershipPct"
  | "closing"
  | "average"
  | "historical"
  | "icSales"
  | "icCost"
  | "unsoldShare"
  | "icReceivable"
  | "payableGroup"
  | "shareCapital"
  | "preAcqRe"
  | "investmentCost"
  | "netIncome";

export type EliminationValues = Record<AmountKey, string> & { currency: string; direction: "upstream" | "downstream" };

interface Field {
  name: AmountKey;
  label: string;
  hint?: string;
  /** Request path for the service's field errors. */
  path: string;
  signed?: boolean;
}

const rate = (name: "closing" | "average" | "historical", label: string, hint: string): Field => ({ name, label, hint, path: `rates.${name}` });
const plain = (name: AmountKey, label: string, hint?: string, signed = false): Field => ({ name, label, hint, path: name, signed });

const sections: { title: string; fields: Field[] }[] = [
  {
    title: "Rates, subsidiary to group currency",
    fields: [
      rate("closing", "Closing rate", "Translates the intercompany receivable (E2)."),
      rate("average", "Average rate", "Translates intercompany sales, unrealized profit and net income (E1, E3, E5)."),
      rate("historical", "Historical rate", "The rate at acquisition, for the acquired equity (E4)."),
    ],
  },
  {
    title: "Intercompany trade",
    fields: [
      plain("icSales", "Intercompany sales", "In the seller's currency."),
      plain("icCost", "Cost of those sales"),
      plain("unsoldShare", "Share still in inventory", "A fraction from 0 to 1, such as 0.4 for 40%."),
    ],
  },
  {
    title: "Intercompany balances",
    fields: [
      plain("icReceivable", "Subsidiary receivable", "In the subsidiary's currency."),
      plain("payableGroup", "Parent payable", "As recorded, in group currency."),
    ],
  },
  {
    title: "Acquisition and current year",
    fields: [
      plain("shareCapital", "Share capital at acquisition"),
      plain("preAcqRe", "Retained earnings at acquisition", undefined, true),
      plain("investmentCost", "Investment cost", "The parent's investment, in group currency."),
      plain("netIncome", "Subsidiary net income", "Current year, in the subsidiary's currency.", true),
    ],
  },
];

/** The FR-40.9 worked example: P owns 80% of S (EUR), S sold goods to P. */
export const workedExample: EliminationValues = {
  currency: "USD",
  ownershipPct: "80",
  direction: "upstream",
  closing: "1.10",
  average: "1.08",
  historical: "1.05",
  icSales: "100000",
  icCost: "70000",
  unsoldShare: "0.4",
  icReceivable: "100000",
  payableGroup: "108500",
  shareCapital: "500000",
  preAcqRe: "200000",
  investmentCost: "620000",
  netIncome: "150000",
};

/** Checks that mirror the service, and the request the form sends. */
export function validateEliminations(values: EliminationValues): { errors: Record<string, string>; request?: EliminationRequest } {
  const errors: Record<string, string> = {};
  const currency = values.currency.trim().toUpperCase();
  if (!isCurrency(currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  const own = values.ownershipPct.trim();
  if (!isPositive(own) || !/^0*(\d{1,2}(\.\d+)?|100(\.0+)?)$/.test(own)) errors.ownershipPct = "Enter a percentage above 0 and at most 100.";
  const v = (name: AmountKey) => values[name].trim();
  for (const section of sections) {
    for (const f of section.fields) {
      const raw = v(f.name);
      if (f.path.startsWith("rates.")) {
        if (!isPositive(raw)) errors[f.path] = "Enter a rate above 0.";
      } else if (raw !== "" && !(f.signed ? isSignedDecimal(raw) : isDecimal(raw))) {
        errors[f.path] = f.signed ? "Enter an amount, or leave it empty for 0." : "Enter an amount, 0 or more, or leave it empty.";
      }
    }
  }
  if (v("unsoldShare") !== "" && isDecimal(v("unsoldShare")) && !/^0*(0(\.\d+)?|1(\.0+)?|\.\d+)$/.test(v("unsoldShare")))
    errors.unsoldShare = "Enter a fraction from 0 to 1.";
  if (Object.keys(errors).length > 0) return { errors };
  const opt = (name: AmountKey) => (v(name) === "" ? undefined : v(name));
  return {
    errors,
    request: {
      currency,
      ownershipPct: own,
      rates: { closing: v("closing"), average: v("average"), historical: v("historical") },
      icSales: opt("icSales"),
      icCost: opt("icCost"),
      unsoldShare: opt("unsoldShare"),
      icReceivable: opt("icReceivable"),
      payableGroup: opt("payableGroup"),
      shareCapital: opt("shareCapital"),
      preAcqRe: opt("preAcqRe"),
      investmentCost: opt("investmentCost"),
      netIncome: opt("netIncome"),
      sellerIsSubsidiary: values.direction === "upstream",
    },
  };
}

const ruleNames: Record<RuleCode, string> = {
  E1: "Intercompany revenue and cost of sales",
  E2: "Intercompany receivables and payables",
  E3: "Unrealized profit in inventory",
  E4: "Investment against acquired equity",
  E5: "Non-controlling share of profit",
};


const money = (v: string) => (isNonZero(v) ? amount(v) : dash);

const entryColumns: DataTableColumn<EliminationEntry>[] = [
  { key: "rule", header: "Rule", render: (e) => <Badge tone="neutral">{e.rule}</Badge> },
  { key: "account", header: "Account", render: (e) => accountNames[e.account] ?? e.account },
  { key: "code", header: "Code", mono: true, render: (e) => e.account },
  { key: "debit", header: "Debit", align: "end", render: (e) => money(e.debit) },
  { key: "credit", header: "Credit", align: "end", render: (e) => money(e.credit) },
];

interface RuleRow {
  rule: RuleCode;
  debit: string;
  credit: string;
}

const ruleColumns: DataTableColumn<RuleRow>[] = [
  { key: "rule", header: "Rule", mono: true },
  { key: "name", header: "Elimination", render: (r) => ruleNames[r.rule] ?? r.rule },
  { key: "debit", header: "Debit", align: "end", render: (r) => amount(r.debit) },
  { key: "credit", header: "Credit", align: "end", render: (r) => amount(r.credit) },
];

function ruleRows(preview: EliminationPreview): RuleRow[] {
  return (Object.keys(preview.rules) as RuleCode[])
    .sort()
    .map((rule) => ({ rule, debit: preview.rules[rule]?.debit ?? "0", credit: preview.rules[rule]?.credit ?? "0" }));
}

/** Build the E1–E5 elimination entries for one parent and subsidiary (FR-40.9, AC-40.7). Nothing is posted. */
export function EliminationsPage() {
  const [values, setValues] = useState<EliminationValues>(workedExample);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<EliminationPreview | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateEliminations(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    setBusy(true);
    try {
      setPreview(await previewEliminations(request));
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setPreview(null);
    } finally {
      setBusy(false);
    }
  }

  const input = (name: AmountKey | "currency", error?: string) => (control: FormFieldControlProps) => (
    <Input
      {...control}
      inputMode={name === "currency" ? undefined : "decimal"}
      className={name === "currency" ? styles.mono : undefined}
      value={values[name]}
      onChange={(e) => setValues({ ...values, [name]: e.target.value })}
      invalid={Boolean(error)}
    />
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Elimination entries"
        description="Entries E1 to E5 for one parent and one subsidiary, each rounded to the group currency and balanced per rule. Upstream unrealized profit is shared with the non-controlling interest; downstream profit belongs to the parent. Dividends (E6), stored runs and drill to source balances arrive with the next slices."
      />
      <Card title="Parent and subsidiary" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          <FormField id="elim-currency" label="Group currency" error={errors.currency}>
            {input("currency", errors.currency)}
          </FormField>
          <FormField id="elim-ownershipPct" label="Parent ownership (%)" error={errors.ownershipPct}>
            {input("ownershipPct", errors.ownershipPct)}
          </FormField>
          <FormField id="elim-direction" label="Direction of the sale">
            {(control) => (
              <Select
                {...control}
                value={values.direction}
                onChange={(e) => setValues({ ...values, direction: e.target.value as EliminationValues["direction"] })}
              >
                <option value="upstream">Upstream, subsidiary sold to parent</option>
                <option value="downstream">Downstream, parent sold to subsidiary</option>
              </Select>
            )}
          </FormField>
          {sections.map((section) => (
            <fieldset key={section.title} className={styles.lineCard}>
              <legend className={styles.lineLegend}>{section.title}</legend>
              {section.fields.map((f) => (
                <FormField key={f.name} id={`elim-${f.name}`} label={f.label} hint={f.hint} error={errors[f.path]}>
                  {input(f.name, errors[f.path])}
                </FormField>
              ))}
            </fieldset>
          ))}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The eliminations were not built" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Preview eliminations
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Elimination entries" columns={entryColumns} rows={[]} getRowKey={(_, i) => String(i)} loading />
      ) : preview ? (
        <>
          <Alert tone={preview.balanced ? "success" : "danger"} title={preview.balanced ? "Every rule balances" : "A rule does not balance"}>
            Total debits {amount(preview.totals.debit)} and credits {amount(preview.totals.credit)} {preview.currency}.
          </Alert>
          <DataTable label="Totals by rule" columns={ruleColumns} rows={ruleRows(preview)} getRowKey={(r) => r.rule} />
          <DataTable
            label="Elimination entries"
            columns={entryColumns}
            rows={preview.entries}
            getRowKey={(e, i) => `${e.rule}-${e.account}-${i}`}
            empty={{ title: "Nothing to eliminate", description: "Every amount is zero, so no entries post." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="grid"
            headingLevel={3}
            title="No eliminations yet"
            description="The form holds the worked example from the requirements. Preview it, or enter your own parent and subsidiary."
          />
        </Card>
      )}
    </div>
  );
}
