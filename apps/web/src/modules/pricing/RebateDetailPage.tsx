import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Skeleton,
  StatGroup,
  StatTile,
  StatusPill,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  describeProblem,
  getRebateAgreement,
  listRebateAccruals,
  newKey,
  retroRecompute,
  type Problem,
  type RebateAccrual,
  type RebateAgreement,
  type RebateTier,
  type RetroDelta,
  type RetroLineInput,
  type RetroResult,
} from "./api/client";
import { DAY, SIGNED_DECIMAL, sumDecimals, UUID } from "./format";
import styles from "./pages.module.css";
import { basisLabel, describePosting, describeProductScope, describeRebateCustomer, PARTY_LABELS, PERIOD_LABELS } from "./rebateFormat";
import { useLoad } from "./useLoad";
import { RebateLifecycleCard } from "./RebateLifecycleCard";
import { RebateStatementCard } from "./RebateStatementCard";
import { VendorClaimsCard } from "./VendorClaimsCard";
import { ProblemMessage } from "../../shell/errors";

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
  { key: "retroactive", header: "Source", render: (row) => (row.retroactive ? <Badge tone="info">Retroactive</Badge> : "Invoice feed") },
];

const deltaColumns: DataTableColumn<RetroDelta>[] = [
  { key: "periodId", header: "Period", mono: true },
  { key: "posted", header: "Accrued before", mono: true, align: "end" },
  { key: "target", header: "Owed now", mono: true, align: "end" },
  { key: "delta", header: "Delta posted", mono: true, align: "end" },
];

/**
 * Parses pasted invoice lines, one per row: invoice line id, invoice date, net value[, quantity[, product id[, product group id]]].
 * Returns the lines or the first row's error.
 */
export function parseRetroLines(text: string, currency: string): { lines: RetroLineInput[]; error?: string } {
  const lines: RetroLineInput[] = [];
  const rows = text.split("\n").map((r) => r.trim()).filter((r) => r !== "");
  for (const [i, row] of rows.entries()) {
    const [id = "", date = "", value = "", qty = "", productId = "", productGroupId = ""] = row.split(",").map((c) => c.trim());
    // An empty quantity cell (a row that skips it to reach the product columns) is 0.
    const quantity = qty === "" ? "0" : qty;
    if (id === "" || id.length > 128) return { lines: [], error: `Row ${i + 1}: enter the invoice line id.` };
    if (!DAY.test(date)) return { lines: [], error: `Row ${i + 1}: enter the invoice date as YYYY-MM-DD.` };
    if (!SIGNED_DECIMAL.test(value) || !SIGNED_DECIMAL.test(quantity)) return { lines: [], error: `Row ${i + 1}: value and quantity must be numbers.` };
    if ((productId !== "" && !UUID.test(productId)) || (productGroupId !== "" && !UUID.test(productGroupId))) {
      return { lines: [], error: `Row ${i + 1}: the product and product group ids must be UUIDs.` };
    }
    lines.push({ invoiceLineId: id, invoiceDate: date, value, quantity, currency, ...(productId ? { productId } : {}), ...(productGroupId ? { productGroupId } : {}) });
  }
  if (lines.length > 5000) return { lines: [], error: "Paste at most 5,000 invoice lines per run." };
  return { lines };
}

/**
 * One rebate agreement: its tiers and the accruals posted invoices produced (Rebate accruals
 * drill-down, §5.26.14); for a vendor rebate also its period claims (FR-26.15).
 */
export function RebateDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getRebateAgreement(id, signal), id);
  const [period, setPeriod] = useState("");
  const [applied, setApplied] = useState("");
  const [periodError, setPeriodError] = useState("");
  const accruals = useLoad((signal) => listRebateAccruals({ agreementId: id, period: applied }, signal), `${id}|${applied}`);
  const [retroText, setRetroText] = useState("");
  const [retroError, setRetroError] = useState("");
  const [retroFailure, setRetroFailure] = useState<Problem | null>(null);
  const [retroResult, setRetroResult] = useState<RetroResult | null>(null);
  const [retroBusy, setRetroBusy] = useState(false);
  const retroKey = useRef(newKey());
  // An agreement returned by a revise or close belongs to this id only; another id shows its own load.
  const [changed, setChanged] = useState<RebateAgreement | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Rebate agreement" />
        <Alert tone="danger" title="Could not load the rebate agreement" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  const agreement = changed && changed.id === id ? changed : state.data;
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

  const currency = agreement.currency;
  async function recompute(event: FormEvent) {
    event.preventDefault();
    setRetroFailure(null);
    const parsed = parseRetroLines(retroText, currency);
    if (parsed.error) {
      setRetroError(parsed.error);
      return;
    }
    setRetroError("");
    setRetroBusy(true);
    try {
      setRetroResult(await retroRecompute(id, parsed.lines, retroKey.current));
      retroKey.current = newKey();
      accruals.reload();
    } catch (error) {
      setRetroFailure(describeProblem(error));
    } finally {
      setRetroBusy(false);
    }
  }

  const tierColumns: DataTableColumn<RebateTier>[] = [
    { key: "from", header: agreement.basis === "NET_AMOUNT" ? `From (${agreement.currency})` : "From (quantity)", mono: true, align: "end" },
    agreement.basis === "NET_AMOUNT"
      ? { key: "ratePct", header: "Rate", mono: true, align: "end", render: (t) => `${t.ratePct ?? "0"}%` }
      : { key: "amountPerUnit", header: "Per unit", mono: true, align: "end", render: (t) => `${t.amountPerUnit ?? "0"} ${agreement.currency}` },
  ];
  const rows = accruals.data ?? [];
  const vendor = agreement.partyRole === "VENDOR";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={agreement.name}
        description={`${PARTY_LABELS[agreement.partyRole ?? "CUSTOMER"]} · ${describeRebateCustomer(agreement)} · ${PERIOD_LABELS[agreement.period].toLowerCase()} · ${agreement.currency}`}
        meta={<StatusPill status={agreement.status.toLowerCase()} />}
        actions={
          <Link className={buttonClassName("ghost")} to="/pricing/rebates">
            All rebate agreements
          </Link>
        }
      />
      <Card title="Agreement">
        <dl className={styles.details}>
          <Detail term={vendor ? "Vendor" : agreement.customerId ? "Customer" : "Customer group"} mono>
            {vendor ? agreement.vendorId : (agreement.customerId ?? agreement.customerGroupId)}
          </Detail>
          <Detail term="Products">{describeProductScope(agreement)}</Detail>
          <Detail term="Basis">{basisLabel(agreement)}</Detail>
          <Detail term="Accrual period">{PERIOD_LABELS[agreement.period]}</Detail>
          <Detail term="Valid" mono>
            {`${agreement.validFrom} – ${agreement.validTo}`}
          </Detail>
          <Detail term="Posting" mono>
            {describePosting(agreement)}
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
              <ProblemMessage problem={accruals.error} />
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
                empty={{
                  title: "No accruals yet",
                  description: vendor
                    ? "Accruals appear when posted invoices of this vendor fall in the agreement's validity."
                    : "Accruals appear when posted invoices of this customer fall in the agreement's validity.",
                }}
              />
              {rows.length >= 100 ? <p className={styles.note}>Showing the first 100 accruals; the total covers those rows only. Choose a period to narrow the list.</p> : null}
            </>
          )}
        </div>
      </Card>
      <RebateStatementCard key={`statement-${agreement.id}`} agreement={agreement} />
      <RebateLifecycleCard key={agreement.id} agreement={agreement} onChange={setChanged} />
      {vendor ? <VendorClaimsCard agreement={agreement} /> : null}
      <Card title="Retroactive recompute">
        <form className={styles.form} onSubmit={recompute} noValidate>
          <p className={`${styles.note} ${styles.wide}`}>
            When an agreement starts before the day it was activated, re-accrue the invoices posted in between. Posted invoices are never re-priced: each period posts only the difference, marked retroactive.
          </p>
          <div className={styles.wide}>
            <FormField
              id="rb-retro-lines"
              label="Posted invoice lines"
              hint={`One per row: invoice line id, invoice date, net value in ${currency}, quantity, product id, product group id (the last three optional; an agreement limited to a product or group needs its id). For example INV-100/1, ${agreement.validFrom}, 20000`}
              error={retroError || undefined}
            >
              {(c) => <Textarea {...c} mono rows={5} value={retroText} onChange={(e) => { setRetroText(e.target.value); retroKey.current = newKey(); }} invalid={Boolean(retroError)} />}
            </FormField>
          </div>
          {retroFailure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not recompute the agreement" live="alert">
                <ProblemMessage problem={retroFailure} />
              </Alert>
            </div>
          ) : null}
          {retroResult ? (
            <div className={`${styles.stack} ${styles.wide}`}>
              <Alert tone={retroResult.deltas.length > 0 ? "success" : "info"} live="status" title={retroResult.notice ? `${retroResult.notice.message} (${retroResult.notice.code})` : undefined}>
                {retroResult.deltas.length > 0
                  ? `Posted ${sumDecimals(retroResult.deltas.map((d) => d.delta))} ${currency} across ${retroResult.deltas.length} period(s); activated ${retroResult.activatedOn}.`
                  : `Every period was already at its target; nothing was posted. Activated ${retroResult.activatedOn}.`}
              </Alert>
              {retroResult.deltas.length > 0 ? <DataTable label="Retroactive deltas" columns={deltaColumns} rows={retroResult.deltas} getRowKey={(d) => d.periodId} /> : null}
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={retroBusy}>
              Recompute
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
