import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeProblem, exportCommissionStatement, getCommissionStatement, type CommissionTransaction, type Problem } from "./api/client";
import { downloadCsv } from "./download";
import { UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

const MONTH = /^\d{4}-(0[1-9]|1[0-2])$/;

interface Draft {
  legalEntityId: string;
  repId: string;
  period: string;
}
type Errors = Partial<Record<keyof Draft, string>>;

export function validateStatement(v: Draft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  const rep = v.repId.trim();
  // The rep id is a path segment: the service refuses / and :, and . or .. would be resolved away.
  if (rep === "" || rep.length > 64) errors.repId = "Enter the rep id (up to 64 characters).";
  else if (/[/:]/.test(rep) || rep === "." || rep === "..") errors.repId = "A rep id has no / or : and is not . or ..";
  if (!MONTH.test(v.period.trim())) errors.period = "Use a month such as 2027-03.";
  return errors;
}

const columns: DataTableColumn<CommissionTransaction>[] = [
  { key: "invoiceDate", header: "Invoice date", mono: true },
  {
    key: "invoiceLineId",
    header: "Invoice line",
    mono: true,
    render: (t) => (t.reversesInvoiceLineId ? `${t.invoiceLineId} (credits ${t.reversesInvoiceLineId})` : t.invoiceLineId),
  },
  { key: "customerId", header: "Customer", mono: true, render: (t) => t.customerId ?? "" },
  { key: "basisAmount", header: "Basis amount", mono: true, align: "end" },
  { key: "ratePct", header: "Rate", mono: true, align: "end", render: (t) => `${t.ratePct}% × ${t.sharePct}%` },
  { key: "amount", header: "Commission", mono: true, align: "end", render: (t) => `${t.amount} ${t.currency}` },
];

/**
 * Commission statement of one rep for one invoice month (PRD §5.26.15): every commission
 * transaction, credit notes negative, and the earned, credited and net totals per currency,
 * which the service sums over all transactions, listed or not. Downloadable as CSV.
 */
export function CommissionStatementPage() {
  const [values, setValues] = useState<Draft>({ legalEntityId: "", repId: "", period: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [applied, setApplied] = useState<Draft | null>(null);
  const [exporting, setExporting] = useState(false);
  const [exportFailure, setExportFailure] = useState<Problem | null>(null);
  const [exported, setExported] = useState("");
  const state = useLoad(
    (signal) => (applied ? getCommissionStatement(applied, signal) : Promise.resolve(null)),
    applied ? `${applied.legalEntityId}|${applied.repId}|${applied.period}` : "",
  );

  function show(event: FormEvent) {
    event.preventDefault();
    const found = validateStatement(values);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setExported("");
    setExportFailure(null);
    setApplied({ legalEntityId: values.legalEntityId.trim(), repId: values.repId.trim(), period: values.period.trim() });
  }

  async function download() {
    if (!applied) return;
    setExporting(true);
    setExported("");
    setExportFailure(null);
    try {
      const file = await exportCommissionStatement(applied);
      downloadCsv(file.filename, file.csv);
      setExported(`Downloaded ${file.filename}.`);
    } catch (error) {
      setExportFailure(describeProblem(error));
    } finally {
      setExporting(false);
    }
  }

  const st = state.data;
  const field = (key: keyof Draft, label: string, hint?: string) => (
    <FormField id={`cst-${key}`} label={label} hint={hint} error={errors[key]}>
      {(c) => <Input {...c} className={styles.mono} value={values[key]} onChange={(e) => setValues((v) => ({ ...v, [key]: e.target.value }))} invalid={Boolean(errors[key])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Commission statement"
        description="What one rep earned in an invoice month: every commission transaction, credit notes netted, and the totals per currency. Only the rep id is shown."
        actions={
          <Link className={buttonClassName("ghost")} to="/pricing/commission/transactions">
            Commission transactions
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={show} noValidate>
          <LookupFormField id="cst-legalEntityId" label="Legal entity" entity="legalEntity" value={values.legalEntityId} error={errors.legalEntityId} onChange={(legalEntityId) => setValues((v) => ({ ...v, legalEntityId }))} />
          {field("repId", "Rep id")}
          {field("period", "Invoice month", "For example 2027-03.")}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Show statement
            </Button>
          </div>
        </form>
      </Card>
      {!applied ? (
        <p className={styles.note}>Choose a rep and a month to see what was earned, what was credited and what is owed.</p>
      ) : state.error ? (
        <Alert tone="danger" title="Could not load the statement" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <>
          <StatGroup label="Statement totals">
            {st && st.totals.length > 0 ? (
              st.totals.flatMap((t) => [
                <StatTile key={`${t.currency}-e`} label={`Earned ${t.currency}`} value={t.earnedAmount} />,
                <StatTile key={`${t.currency}-c`} label={`Credited ${t.currency}`} value={t.creditedAmount} />,
                <StatTile key={`${t.currency}-n`} label={`Owed ${t.currency}`} value={t.netAmount} />,
              ])
            ) : (
              <StatTile label="Owed" value={st ? "0" : "…"} />
            )}
          </StatGroup>
          <DataTable
            label="Statement lines"
            columns={columns}
            rows={st?.lines ?? []}
            getRowKey={(t) => t.id}
            loading={state.loading}
            empty={{ title: "No commission in this month", description: "The rep has no commission transactions for invoices dated in the month." }}
          />
          {st?.linesTruncated ? <p className={styles.note}>{`Showing the first ${st.lines.length} of ${st.lineCount} transactions; the totals cover all of them. The CSV lists every one.`}</p> : null}
          <div className={styles.actions}>
            <Button variant="secondary" loading={exporting} disabled={!st || st.lineCount === 0} onClick={() => void download()}>
              Download CSV
            </Button>
          </div>
          <Alert tone="success" live="status">
            {exported}
          </Alert>
          {exportFailure ? (
            <Alert tone="danger" title="Could not download the statement" live="alert">
              <ProblemMessage problem={exportFailure} />
            </Alert>
          ) : null}
        </>
      )}
    </div>
  );
}
