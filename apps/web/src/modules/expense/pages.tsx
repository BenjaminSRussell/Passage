import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { describeProblem, evaluateReport, type EvaluationResponse, type Outcome, type PolicyResult, type Problem } from "./api/client";
import { DECIMAL, splitList, today } from "./format";
import styles from "./pages.module.css";

const OUTCOME_TONE: Record<Outcome, BadgeTone> = { BLOCK: "danger", REQUIRE_JUSTIFICATION: "warning", REQUIRE_RECEIPT: "warning", WARN: "info" };
const OUTCOME_LABEL: Record<Outcome, string> = { BLOCK: "Blocked", REQUIRE_JUSTIFICATION: "Justification required", REQUIRE_RECEIPT: "Receipt required", WARN: "Warning" };

const resultColumns: DataTableColumn<PolicyResult>[] = [
  { key: "ruleId", header: "Rule", mono: true },
  { key: "outcome", header: "Outcome", render: (r) => <Badge tone={OUTCOME_TONE[r.outcome]}>{OUTCOME_LABEL[r.outcome]}</Badge> },
  { key: "satisfied", header: "Status", render: (r) => (r.satisfied ? "Satisfied" : "Open") },
  { key: "code", header: "Code", mono: true },
  { key: "message", header: "Message" },
];

/** Check one expense line against the policy rules before it goes on a report. */
export function ExpenseHome() {
  const [category, setCategory] = useState("MEALS");
  const [amount, setAmount] = useState("80.00");
  const [hasReceipt, setHasReceipt] = useState(true);
  const [justification, setJustification] = useState("");
  const [requisitionCategories, setRequisitionCategories] = useState("");
  const [hasRequisition, setHasRequisition] = useState(false);
  const [categoriesError, setCategoriesError] = useState<string | undefined>();
  const [result, setResult] = useState<EvaluationResponse | null>(null);
  const [amountError, setAmountError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setFailure(null);
    if (!DECIMAL.test(amount.trim())) {
      setAmountError("Enter the amount as a number such as 80.00.");
      return;
    }
    setAmountError(undefined);
    const categories = splitList(requisitionCategories).map((c) => c.toUpperCase());
    if (categories.length > 50 || categories.some((c) => c.length > 32)) {
      setCategoriesError("Enter at most 50 categories of up to 32 characters each.");
      return;
    }
    setCategoriesError(undefined);
    const date = today();
    setBusy(true);
    try {
      setResult(
        await evaluateReport(date, [
          { id: "draft-1", category, txnDate: date, amount: amount.trim(), merchant: "", attendees: 0, hasReceipt, justification, paidBy: "EMPLOYEE" },
        ], { requisitionCategories: categories, hasRequisition }),
      );
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
        title="Check an expense line"
        description="Policy outcomes are shown before you submit. A blocked line cannot be submitted, including a line whose category needs a travel requisition the report does not have. Nothing is saved."
      />
      <Card>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <FormField id="line-category" label="Category">
            {(c) => (
              <Select {...c} value={category} onChange={(e) => setCategory(e.target.value)}>
                <option value="MEALS">Meals</option>
                <option value="TRAVEL">Travel</option>
                <option value="AIRFARE">Airfare</option>
                <option value="LODGING">Lodging</option>
                <option value="ALCOHOL">Alcohol</option>
              </Select>
            )}
          </FormField>
          <FormField id="line-amount" label="Amount" error={amountError}>
            {(c) => <Input {...c} className={styles.mono} value={amount} onChange={(e) => setAmount(e.target.value)} inputMode="decimal" invalid={Boolean(amountError)} />}
          </FormField>
          <FormField id="line-justification" label="Justification" optional>
            {(c) => <Input {...c} value={justification} onChange={(e) => setJustification(e.target.value)} />}
          </FormField>
          <FormField id="line-requisition-categories" label="Categories that need a travel requisition" optional hint="Comma separated, such as AIRFARE, LODGING." error={categoriesError}>
            {(c) => <Input {...c} className={styles.mono} value={requisitionCategories} onChange={(e) => setRequisitionCategories(e.target.value)} invalid={Boolean(categoriesError)} />}
          </FormField>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="line-receipt" type="checkbox" checked={hasReceipt} onChange={(e) => setHasReceipt(e.target.checked)} />
              <label htmlFor="line-receipt">Receipt attached</label>
            </span>
            <span className={styles.check}>
              <Input id="line-has-requisition" type="checkbox" checked={hasRequisition} onChange={(e) => setHasRequisition(e.target.checked)} />
              <label htmlFor="line-has-requisition">Report has an approved travel requisition</label>
            </span>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The line could not be checked" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Check line
            </Button>
          </div>
        </form>
      </Card>
      <Card title="Result">
        {result ? (
          <div className={styles.stack}>
            {result.submittable ? (
              <Alert tone="success" title="Ready to submit" />
            ) : (
              <Alert tone="warning" title="Cannot submit yet" />
            )}
            <DataTable
              label="Policy results"
              columns={resultColumns}
              rows={result.results}
              getRowKey={(r) => `${r.lineId}-${r.ruleId}`}
              empty={{ title: "No policy rule applies to this line" }}
            />
          </div>
        ) : busy ? (
          <DataTable label="Policy results" columns={resultColumns} rows={[]} getRowKey={(r) => `${r.lineId}-${r.ruleId}`} loading />
        ) : (
          <EmptyState icon="receipt" title="No line checked yet" description="Enter the line, then check it against the policy." compact />
        )}
      </Card>
    </div>
  );
}
