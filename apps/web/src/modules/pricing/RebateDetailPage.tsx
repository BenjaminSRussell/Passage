import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, StatGroup, StatTile, StatusPill, type DataTableColumn } from "../../shell/ui";
import { getRebateAgreement, listRebateAccruals, type RebateAccrual, type RebateTier } from "./api/client";
import { sumDecimals } from "./format";
import styles from "./pages.module.css";
import { BASIS_LABELS, describeRebateCustomer, PERIOD_LABELS } from "./rebateFormat";
import { useLoad } from "./useLoad";

const PERIOD = /^\d{4}(-(0[1-9]|1[0-2])|-Q[1-4])?$/;

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

const accrualColumns: DataTableColumn<RebateAccrual>[] = [
  { key: "periodId", header: "Period", mono: true },
  { key: "invoiceLineId", header: "Invoice line", mono: true },
  { key: "invoiceDate", header: "Invoice date", mono: true },
  { key: "basisValue", header: "Net amount", mono: true, align: "end" },
  { key: "basisQuantity", header: "Quantity", mono: true, align: "end" },
  { key: "amount", header: "Accrued", mono: true, align: "end" },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
];

/** One rebate agreement: its tiers and the accruals posted invoices produced (Rebate accruals drill-down, §5.26.14). */
export function RebateDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getRebateAgreement(id, signal), id);
  const [period, setPeriod] = useState("");
  const [applied, setApplied] = useState("");
  const [periodError, setPeriodError] = useState("");
  const accruals = useLoad((signal) => listRebateAccruals({ agreementId: id, period: applied }, signal), `${id}|${applied}`);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Rebate agreement" />
        <Alert tone="danger" title="Could not load the rebate agreement" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  const agreement = state.data;
  if (!agreement) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Rebate agreement" />
        <span className="visually-hidden">Loading the rebate agreement…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  function filter(event: FormEvent) {
    event.preventDefault();
    const value = period.trim();
    if (value !== "" && !PERIOD.test(value)) {
      setPeriodError("Use a period such as 2027-03, 2027-Q1 or 2027.");
      return;
    }
    setPeriodError("");
    setApplied(value);
  }

  const tierColumns: DataTableColumn<RebateTier>[] = [
    { key: "from", header: agreement.basis === "NET_AMOUNT" ? `From (${agreement.currency})` : "From (quantity)", mono: true, align: "end" },
    agreement.basis === "NET_AMOUNT"
      ? { key: "ratePct", header: "Rate", mono: true, align: "end", render: (t) => `${t.ratePct ?? "0"}%` }
      : { key: "amountPerUnit", header: "Per unit", mono: true, align: "end", render: (t) => `${t.amountPerUnit ?? "0"} ${agreement.currency}` },
  ];
  const rows = accruals.data ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title={agreement.name}
        description={`${describeRebateCustomer(agreement)} · ${PERIOD_LABELS[agreement.period].toLowerCase()} · ${agreement.currency}`}
        meta={<StatusPill status={agreement.status.toLowerCase()} />}
        actions={
          <Link className={buttonClassName("ghost")} to="/pricing/rebates">
            All rebate agreements
          </Link>
        }
      />
      <Card title="Agreement">
        <dl className={styles.details}>
          <Detail term={agreement.customerId ? "Customer" : "Customer group"} mono>
            {agreement.customerId ?? agreement.customerGroupId}
          </Detail>
          <Detail term="Basis">{BASIS_LABELS[agreement.basis]}</Detail>
          <Detail term="Accrual period">{PERIOD_LABELS[agreement.period]}</Detail>
          <Detail term="Valid" mono>
            {`${agreement.validFrom} – ${agreement.validTo}`}
          </Detail>
          <Detail term="Posting" mono>
            {`Dr ${agreement.expenseAccount} / Cr ${agreement.accrualAccount}`}
          </Detail>
          <Detail term="Legal entity" mono>
            {agreement.legalEntityId}
          </Detail>
        </dl>
      </Card>
      <Card title="Tiers">
        <div className={styles.stack}>
          <DataTable label="Rebate tiers" columns={tierColumns} rows={agreement.tiers} getRowKey={(t) => t.from} />
          <p className={styles.note}>The tier reached by the period's cumulative total applies to the whole period; crossing a tier books the difference on the next invoice.</p>
        </div>
      </Card>
      <Card title="Accruals">
        <div className={styles.stack}>
          <form className={styles.toolbar} onSubmit={filter} noValidate>
            <FormField id="rb-period-filter" label="Period" optional hint="For example 2027-03, 2027-Q1 or 2027." error={periodError || undefined}>
              {(c) => <Input {...c} className={styles.mono} value={period} onChange={(e) => setPeriod(e.target.value)} invalid={Boolean(periodError)} />}
            </FormField>
            <Button type="submit">Show accruals</Button>
          </form>
          {accruals.error ? (
            <Alert tone="danger" title="Could not load the accruals" live="alert" action={<Button size="sm" onClick={accruals.reload}>Try again</Button>}>
              {accruals.error.message} ({accruals.error.code})
            </Alert>
          ) : (
            <>
              <StatGroup label="Accrual totals">
                <StatTile label={applied ? `Accrued in ${applied}` : "Accrued, all periods"} value={accruals.data ? `${sumDecimals(rows.map((r) => r.amount))} ${agreement.currency}` : "…"} />
                <StatTile label="Invoice lines" value={accruals.data ? rows.length : "…"} />
              </StatGroup>
              <DataTable
                label="Rebate accruals"
                columns={accrualColumns}
                rows={rows}
                getRowKey={(row) => row.id}
                loading={accruals.loading}
                empty={{ title: "No accruals yet", description: "Accruals appear when posted invoices of this customer fall in the agreement's validity." }}
              />
              {rows.length >= 100 ? <p className={styles.note}>Showing the first 100 accruals; the total covers those rows only. Choose a period to narrow the list.</p> : null}
            </>
          )}
        </div>
      </Card>
    </div>
  );
}
