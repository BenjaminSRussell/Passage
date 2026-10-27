import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  StatGroup,
  StatTile,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  CostingApiError,
  describeError,
  getValueReport,
  listValuationLedgers,
  REPORT_DIMENSIONS,
  tieOutValueReport,
  type LedgerBalance,
  type ReportDimension,
  type TieOut,
  type TieOutRow,
  type ValueReport,
  type ValueReportParams,
  type ValueReportRow,
} from "./api/client";
import styles from "./pages.module.css";
import { DECIMAL } from "./shared";
import { useLoad } from "./useLoad";
import { dash, LoadError, UUID } from "./versionParts";

const DAY = /^\d{4}-\d{2}-\d{2}$/;
const ZERO = /^-?0(\.0+)?$/;

export const DIMENSION_LABEL: Record<ReportDimension, string> = {
  product: "Product",
  site: "Site",
  warehouse: "Warehouse",
  valuationType: "Valuation type",
};

/** The report the URL asks for, or null while it lacks a legal entity or a date. */
export function paramsFromSearch(search: URLSearchParams): ValueReportParams | null {
  const legalEntityId = (search.get("legalEntityId") ?? "").toLowerCase();
  const asOf = search.get("asOf") ?? "";
  if (!UUID.test(legalEntityId) || !DAY.test(asOf)) return null;
  const raw = search.get("groupBy");
  const groupBy = (raw === null ? ["site"] : raw.split(",")).filter((d): d is ReportDimension => (REPORT_DIMENSIONS as readonly string[]).includes(d));
  const out: ValueReportParams = { legalEntityId, asOf, groupBy };
  const ledger = search.get("valuationLedgerId");
  const site = search.get("siteId");
  if (ledger) out.valuationLedgerId = ledger;
  if (site) out.siteId = site;
  return out;
}

function searchFromParams(p: ValueReportParams): Record<string, string> {
  const out: Record<string, string> = { legalEntityId: p.legalEntityId, asOf: p.asOf, groupBy: p.groupBy.join(",") };
  if (p.valuationLedgerId) out.valuationLedgerId = p.valuationLedgerId;
  if (p.siteId) out.siteId = p.siteId;
  return out;
}

/**
 * Parses pasted ledger balances: a header naming exactly the report's grouping
 * dimensions and "balance" (any order), then one line per group.
 */
export function parseLedgerBalances(text: string, groupBy: ReportDimension[]): { ok: true; value: LedgerBalance[] } | { ok: false; error: string } {
  const lines = text
    .split(/\r?\n/)
    .map((line, i) => ({ line: i + 1, cells: line.split(",").map((c) => c.trim()) }))
    .filter((l) => l.cells.some((c) => c !== ""));
  const expected = [...groupBy, "balance"];
  const header = lines[0]?.cells ?? [];
  if (header.length !== expected.length || !expected.every((h) => header.includes(h))) {
    return { ok: false, error: `The first line must name the columns ${expected.join(", ")}.` };
  }
  if (lines.length < 2) return { ok: false, error: "Enter at least one ledger balance below the header." };
  const value: LedgerBalance[] = [];
  for (const { line, cells } of lines.slice(1)) {
    if (cells.length !== header.length) return { ok: false, error: `Line ${line}: expected ${header.length} values.` };
    const dimensions: Partial<Record<ReportDimension, string>> = {};
    let balance = "";
    header.forEach((h, i) => {
      if (h === "balance") balance = cells[i];
      else dimensions[h as ReportDimension] = cells[i];
    });
    if (!DECIMAL.test(balance)) return { ok: false, error: `Line ${line}: the balance must be a number such as 1250.00.` };
    value.push({ dimensions, balance });
  }
  return { ok: true, value };
}

function keyColumns<T extends { key: Partial<Record<ReportDimension, string>> }>(groupBy: ReportDimension[]): DataTableColumn<T>[] {
  return groupBy.map((d) => ({
    key: `key.${d}`,
    header: DIMENSION_LABEL[d],
    mono: d !== "valuationType",
    render: (row: T) => row.key[d] || (d === "valuationType" || d === "warehouse" ? "None" : dash),
  }));
}

function rowKey(row: { key: Partial<Record<ReportDimension, string>> }): string {
  return JSON.stringify(REPORT_DIMENSIONS.map((d) => row.key[d] ?? ""));
}

/** Inventory value report (FR-20.4, 3.20.6): value by dimension at a date for one valuation ledger, with a GL tie-out. */
export function ValueReportPage() {
  const navigate = useNavigate();
  const [search, setSearch] = useSearchParams();
  const applied = paramsFromSearch(search);
  const [legalEntityId, setLegalEntityId] = useState(applied?.legalEntityId ?? search.get("legalEntityId") ?? "");
  const [asOf, setAsOf] = useState(applied?.asOf ?? new Date().toISOString().slice(0, 10));
  const [ledgerId, setLedgerId] = useState(applied?.valuationLedgerId ?? "");
  const [siteId, setSiteId] = useState(applied?.siteId ?? "");
  const [groupBy, setGroupBy] = useState<ReportDimension[]>(applied?.groupBy ?? ["site"]);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim().toLowerCase() : "";
  const ledgers = useLoad((signal) => (le ? listValuationLedgers(le, signal) : Promise.resolve({ items: [] })), `ledgers|${le}`);
  const appliedKey = applied ? JSON.stringify(applied) : "";
  const report = useLoad((signal) => (applied ? getValueReport(applied, signal) : Promise.resolve(null)), appliedKey);

  function toggle(d: ReportDimension) {
    setGroupBy((prev) => (prev.includes(d) ? prev.filter((x) => x !== d) : REPORT_DIMENSIONS.filter((x) => x === d || prev.includes(x))));
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    const found: Record<string, string> = {};
    if (!le) found.legalEntityId = "Enter the legal entity's id (a UUID).";
    if (!DAY.test(asOf)) found.asOf = "Enter the as-of date.";
    if (siteId.trim() !== "" && !UUID.test(siteId.trim())) found.siteId = "Enter a site id (a UUID), or leave it empty for every site.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    const next: ValueReportParams = { legalEntityId: le, asOf, groupBy };
    if (ledgerId) next.valuationLedgerId = ledgerId;
    if (siteId.trim()) next.siteId = siteId.trim().toLowerCase();
    setSearch(searchFromParams(next), { replace: true });
  }

  function openRow(row: ValueReportRow) {
    if (!applied) return;
    const q: Record<string, string> = { legalEntityId: applied.legalEntityId, asOf: applied.asOf };
    if (applied.valuationLedgerId) q.valuationLedgerId = applied.valuationLedgerId;
    const site = row.key.site ?? applied.siteId;
    if (site) q.siteId = site;
    if (row.key.product) q.productId = row.key.product;
    navigate(`/costing/cost-transactions?${new URLSearchParams(q).toString()}`);
  }

  const data = report.data;
  const currency = data?.currency ? ` (${data.currency})` : "";
  const columns: DataTableColumn<ValueReportRow>[] = [
    ...keyColumns<ValueReportRow>(data?.groupBy ?? []),
    { key: "qty", header: "Quantity", align: "end", mono: true },
    { key: "physicalValue", header: `Physical value${currency}`, align: "end", mono: true },
    { key: "financialValue", header: `Financial value${currency}`, align: "end", mono: true },
    { key: "value", header: `Value${currency}`, align: "end", mono: true },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Inventory value report"
        description="On-hand quantity and value at a date from the cost transactions of one valuation ledger. Physical value is received or shipped but not yet invoiced; purchase price variance posts to its own account and never counts. Open a row to see its cost transactions."
        actions={
          <Link className={buttonClassName("secondary")} to="/costing/cost-transactions">
            Cost transactions
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <FormField id="report-le" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(e) => setLegalEntityId(e.target.value)} />}
          </FormField>
          <FormField id="report-as-of" label="As of date" hint="Cost transactions with a financial date up to and including this day." error={errors.asOf}>
            {(c) => <Input {...c} type="date" value={asOf} invalid={Boolean(errors.asOf)} onChange={(e) => setAsOf(e.target.value)} />}
          </FormField>
          <FormField id="report-ledger" label="Valuation ledger" hint="Each ledger values the same stock in its own method and currency.">
            {(c) => (
              <Select {...c} value={ledgerId} disabled={!le} onChange={(e) => setLedgerId(e.target.value)}>
                <option value="">Primary valuation</option>
                {(ledgers.data?.items ?? [])
                  .filter((l) => !l.primary)
                  .map((l) => (
                    <option key={l.id} value={l.id}>
                      {`${l.code} (${l.method}, ${l.currency})`}
                    </option>
                  ))}
              </Select>
            )}
          </FormField>
          <FormField id="report-site" label="Site" optional hint="Empty reports every site." error={errors.siteId}>
            {(c) => <Input {...c} className={styles.mono} value={siteId} invalid={Boolean(errors.siteId)} onChange={(e) => setSiteId(e.target.value)} />}
          </FormField>
          <fieldset className={styles.lineSet}>
            <legend className={styles.legend}>Group by (none for one total)</legend>
            {REPORT_DIMENSIONS.map((d) => (
              <span key={d} className={styles.check}>
                <Input id={`report-group-${d}`} type="checkbox" checked={groupBy.includes(d)} onChange={() => toggle(d)} />
                <label htmlFor={`report-group-${d}`}>{DIMENSION_LABEL[d]}</label>
              </span>
            ))}
          </fieldset>
          {ledgers.error ? (
            <div className={styles.wide}>
              <Alert tone="warning">{describeError(ledgers.error, "The legal entity's valuation ledgers could not be loaded.")}</Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={report.loading && Boolean(applied)}>
              Run report
            </Button>
          </div>
        </form>
      </Card>
      {!applied ? (
        <EmptyState icon="database" headingLevel={2} title="Choose a legal entity and a date" description="The report sums every cost transaction of the legal entity up to that date." />
      ) : report.error ? (
        <LoadError title="Could not load the value report" error={report.error} onRetry={report.reload} />
      ) : (
        <>
          {data ? (
            <StatGroup label="Report totals">
              <StatTile label="Quantity" value={data.total.qty} />
              <StatTile label={`Physical value${currency}`} value={data.total.physicalValue} hint="Not yet invoiced" />
              <StatTile label={`Financial value${currency}`} value={data.total.financialValue} />
              <StatTile label={`Inventory value${currency}`} value={data.total.value} hint={data.valuationLedgerCode ? `Ledger ${data.valuationLedgerCode}` : "Primary valuation"} />
            </StatGroup>
          ) : null}
          <DataTable
            label="Inventory value by group"
            columns={columns}
            rows={data?.rows ?? []}
            getRowKey={rowKey}
            loading={report.loading && !data}
            empty={{ title: "No stock is valued at this date", description: "No cost transaction of this legal entity and ledger has a financial date up to the as-of date." }}
            onRowOpen={openRow}
          />
          {data ? <TieOutCard key={appliedKey} params={applied} report={data} /> : null}
        </>
      )}
    </div>
  );
}

function TieOutCard({ params, report }: { params: ValueReportParams; report: ValueReport }) {
  const header = [...report.groupBy, "balance"].join(",");
  const [text, setText] = useState(`${header}\n`);
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<TieOut | null>(null);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setFailure(null);
    const parsed = parseLedgerBalances(text, report.groupBy);
    if (!parsed.ok) {
      setError(parsed.error);
      return;
    }
    setError(null);
    setBusy(true);
    try {
      setResult(await tieOutValueReport({ ...params, glBalances: parsed.value }));
    } catch (err) {
      setResult(null);
      if (err instanceof CostingApiError && err.problem.errors) setError(Object.values(err.problem.errors).join(" "));
      setFailure(describeError(err, "The tie-out could not be run."));
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<TieOutRow>[] = [
    ...keyColumns<TieOutRow>(report.groupBy),
    { key: "inventory", header: "Inventory value", align: "end", mono: true },
    { key: "ledger", header: "Ledger balance", align: "end", mono: true },
    {
      key: "diff",
      header: "Difference",
      align: "end",
      mono: true,
      render: (row) => (ZERO.test(row.diff) ? row.diff : <Badge tone="warning">{row.diff}</Badge>),
    },
  ];

  return (
    <Card title="Tie out to the general ledger" headingLevel={2}>
      <div className={styles.stack}>
        <p className={styles.note}>
          Paste the balances of the inventory accounts at the same date, one line per group, with the columns {header}. A group missing on either side is compared with zero.
        </p>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <div className={styles.wide}>
            <FormField id="tie-out-balances" label="Ledger balances (CSV)" error={error ?? undefined}>
              {(c) => <Textarea {...c} mono rows={6} value={text} invalid={Boolean(error)} onChange={(e) => setText(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Tie out
            </Button>
          </div>
        </form>
        <Alert tone="danger" live="alert">
          {failure}
        </Alert>
        {result ? (
          <>
            <Alert tone={result.ties ? "success" : "warning"} live="status" title={result.ties ? "Ties to the ledger" : "Does not tie to the ledger"}>
              {result.ties
                ? `Every group agrees with the ledger${result.currency ? ` in ${result.currency}` : ""}.`
                : `${result.differences} ${result.differences === 1 ? "group differs" : "groups differ"}; net difference ${result.totalDiff}${result.currency ? ` ${result.currency}` : ""}.`}
            </Alert>
            <DataTable label="Tie-out by group" columns={columns} rows={result.rows} getRowKey={rowKey} empty={{ title: "Nothing to compare" }} />
          </>
        ) : null}
      </div>
    </Card>
  );
}
