import { useState, type FormEvent, type ReactNode } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, StatGroup, StatTile, StatusPill, type DataTableColumn } from "../../shell/ui";
import { describeProblem, exportRebateStatement, getRebateStatement, type Problem, type RebateAgreement, type RebateStatementLine } from "./api/client";
import { downloadCsv } from "./download";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** The period key an agreement accrues by, with the words the field shows. */
const PERIOD_FORMS: Record<RebateAgreement["period"], { pattern: RegExp; example: string; noun: string }> = {
  MONTH: { pattern: /^\d{4}-(0[1-9]|1[0-2])$/, example: "2027-03", noun: "month" },
  QUARTER: { pattern: /^\d{4}-Q[1-4]$/, example: "2027-Q1", noun: "quarter" },
  YEAR: { pattern: /^\d{4}$/, example: "2027", noun: "year" },
};

const SOURCE_LABELS: Record<RebateStatementLine["source"], ReactNode> = {
  INVOICE: "Invoice feed",
  REPLAYED: <Badge tone="info">Replayed invoice</Badge>,
  RETRO_DELTA: <Badge tone="info">Retroactive delta</Badge>,
};

const lineColumns: DataTableColumn<RebateStatementLine>[] = [
  { key: "invoiceLineId", header: "Invoice line", mono: true },
  { key: "invoiceDate", header: "Invoice date", mono: true },
  { key: "basisValue", header: "Net amount", mono: true, align: "end", render: (row) => row.basisValue ?? "—" },
  { key: "basisQuantity", header: "Quantity", mono: true, align: "end", render: (row) => row.basisQuantity ?? "—" },
  { key: "amount", header: "Accrued", mono: true, align: "end" },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
  { key: "source", header: "Source", render: (row) => SOURCE_LABELS[row.source] },
];

/**
 * The rebate agreement statement of one accrual period (PRD §5.26.15): the tier in force, every
 * accrual row and the accrued, settled and open totals, downloadable as CSV. The totals come from
 * the service, so they stay exact when the list is cut.
 */
export function RebateStatementCard({ agreement }: { agreement: RebateAgreement }) {
  const form = PERIOD_FORMS[agreement.period];
  const [period, setPeriod] = useState("");
  const [applied, setApplied] = useState("");
  const [periodError, setPeriodError] = useState("");
  const [exporting, setExporting] = useState(false);
  const [exportFailure, setExportFailure] = useState<Problem | null>(null);
  const [exported, setExported] = useState("");
  const state = useLoad((signal) => (applied === "" ? Promise.resolve(null) : getRebateStatement(agreement.id, applied, signal)), `${agreement.id}|${applied}`);

  function show(event: FormEvent) {
    event.preventDefault();
    const value = period.trim();
    if (!form.pattern.test(value)) {
      setPeriodError(`Enter the ${form.noun} such as ${form.example}.`);
      return;
    }
    setPeriodError("");
    setExported("");
    setApplied(value);
  }

  async function download() {
    setExporting(true);
    setExported("");
    setExportFailure(null);
    try {
      const file = await exportRebateStatement(agreement.id, applied);
      downloadCsv(file.filename, file.csv);
      setExported(`Downloaded ${file.filename}.${file.truncated ? " The file lists the first 5,000 accrual rows; the totals cover all of them." : ""}`);
    } catch (error) {
      setExportFailure(describeProblem(error));
    } finally {
      setExporting(false);
    }
  }

  const st = state.data;
  const currency = agreement.currency;
  return (
    <Card title="Statement">
      <div className={styles.stack}>
        <form className={styles.toolbar} onSubmit={show} noValidate>
          <FormField id="rb-statement-period" label="Statement period" hint={`The ${form.noun} to report, for example ${form.example}.`} error={periodError || undefined}>
            {(c) => <Input {...c} className={styles.mono} value={period} onChange={(e) => setPeriod(e.target.value)} invalid={Boolean(periodError)} />}
          </FormField>
          <Button type="submit">Show statement</Button>
        </form>
        {applied === "" ? (
          <p className={styles.note}>Choose a period to see what the agreement accrued, what was settled and what is still open.</p>
        ) : state.error ? (
          <Alert tone="danger" title="Could not load the statement" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : (
          <>
            <StatGroup label="Statement totals">
              <StatTile label="Accrued" value={st ? `${st.accruedAmount} ${currency}` : "…"} />
              <StatTile label="Settled" value={st ? `${st.settledAmount} ${currency}` : "…"} />
              <StatTile label="Open" value={st ? `${st.openAmount} ${currency}` : "…"} />
            </StatGroup>
            {st ? (
              <p className={styles.note}>
                {st.tier
                  ? `Tier in force: from ${st.tier.from}, ${agreement.basis === "NET_AMOUNT" ? `${st.tier.ratePct ?? "0"}% of net value` : `${st.tier.amountPerUnit ?? "0"} ${currency} per unit`}.`
                  : "No tier is reached yet."}{" "}
                {st.reconciled ? (
                  <Badge tone="success">Matches the tier's amount</Badge>
                ) : (
                  <Badge tone="warning">{`Does not reconcile: the tier owes ${st.targetAmount} ${currency}, the period shows ${st.accruedAmount} and its rows add up to ${st.linesAccruedAmount}`}</Badge>
                )}
              </p>
            ) : null}
            <DataTable
              label="Statement lines"
              columns={lineColumns}
              rows={st?.lines ?? []}
              getRowKey={(row) => row.invoiceLineId}
              loading={state.loading}
              empty={{ title: "Nothing accrued in this period", description: "No posted invoice fell in the period, so the statement has no lines and nothing open." }}
            />
            {st?.linesTruncated ? <p className={styles.note}>{`Showing the first ${st.lines.length} of ${st.lineCount} accrual rows; the totals cover all of them.`}</p> : null}
            <div className={styles.actions}>
              <Button variant="secondary" loading={exporting} disabled={!st} onClick={() => void download()}>
                Download CSV
              </Button>
            </div>
            <Alert tone="success" live="status">
              {exported}
            </Alert>
            {exportFailure ? (
              <Alert tone="danger" title="Could not download the statement" live="alert">
                {exportFailure.message} ({exportFailure.code})
              </Alert>
            ) : null}
          </>
        )}
      </div>
    </Card>
  );
}
