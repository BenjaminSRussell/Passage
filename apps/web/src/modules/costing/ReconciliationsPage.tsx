import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
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
  Skeleton,
  StatGroup,
  StatTile,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  CostingApiError,
  createReconciliation,
  describeError,
  getReconciliation,
  listReconciliations,
  listValuationLedgers,
  REPORT_DIMENSIONS,
  type ReconciliationRun,
  type ReportDimension,
  type TieOutRow,
} from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { LegalEntityFilterField, useLegalEntityFilter } from "./ValuationSetupPage";
import { DIMENSION_LABEL, parseLedgerBalances } from "./ValueReportPage";
import { dash, Detail, formatDateTime, LoadError, problemCode, UUID } from "./versionParts";

const DAY = /^\d{4}-\d{2}-\d{2}$/;
const ZERO = /^-?0(\.0+)?$/;

function TieBadge({ ties }: { ties: boolean }) {
  return ties ? <Badge tone="success">Ties</Badge> : <Badge tone="warning">Differs</Badge>;
}

function grouping(groupBy: ReportDimension[]): string {
  return groupBy.length === 0 ? "One total" : groupBy.map((d) => DIMENSION_LABEL[d]).join(", ");
}

const runColumns: DataTableColumn<ReconciliationRun>[] = [
  { key: "asOf", header: "As of", mono: true },
  { key: "legalEntityId", header: "Legal entity", mono: true },
  { key: "valuationLedgerCode", header: "Valuation ledger", render: (row) => row.valuationLedgerCode ?? "Primary valuation" },
  { key: "groupBy", header: "Grouped by", render: (row) => grouping(row.groupBy) },
  { key: "ties", header: "Result", render: (row) => <TieBadge ties={row.ties} /> },
  { key: "differences", header: "Groups that differ", align: "end" },
  { key: "totalDiff", header: "Net difference", align: "end", mono: true, render: (row) => `${row.totalDiff} ${row.currency}`.trim() },
  { key: "createdAt", header: "Run", render: (row) => formatDateTime(row.createdAt) },
];

/** Stored inventory-to-GL reconciliation runs (FR-20.4), newest as-of date first. */
export function ReconciliationsPage() {
  const navigate = useNavigate();
  const filter = useLegalEntityFilter();
  const state = useLoad((signal) => listReconciliations({ legalEntityId: filter.applied }, signal), filter.applied);
  const [more, setMore] = useState<{ key: string; rows: ReconciliationRun[]; next?: string }>({ key: "", rows: [] });
  const [moreError, setMoreError] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const extra = more.key === filter.applied ? more : { key: filter.applied, rows: [], next: undefined };
  const rows = [...(state.data?.items ?? []), ...extra.rows];
  const next = extra.rows.length > 0 ? extra.next : state.data?.nextCursor;

  async function loadMore() {
    if (!next) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listReconciliations({ legalEntityId: filter.applied, cursor: next });
      setMore({ key: filter.applied, rows: [...extra.rows, ...page.items], next: page.nextCursor });
    } catch (err) {
      setMoreError(describeError(err, "The next page could not be loaded."));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Reconciliations"
        description="Stored tie-outs of the inventory value report against the inventory account balances. Every group that differs raises a reconciliation difference alert."
        actions={
          <Link className={buttonClassName("primary")} to="/costing/reconciliations/new">
            New reconciliation
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <LegalEntityFilterField id="reconciliations-le" filter={filter} />
      </div>
      {state.error ? (
        <LoadError title="Could not load the reconciliations" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Reconciliations"
          columns={runColumns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading && !state.data}
          empty={{ title: "No reconciliations yet", description: "Run one against the ledger balances at a date to keep it as evidence." }}
          onRowOpen={(row) => navigate(`/costing/reconciliations/${row.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" live="alert">
          {moreError}
        </Alert>
      ) : null}
      {next && !state.error ? (
        <div className={styles.more}>
          <Button loading={loadingMore} onClick={loadMore}>
            Load more reconciliations
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/** Run the tie-out against pasted ledger balances and store it. */
export function CreateReconciliationPage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const [asOf, setAsOf] = useState(new Date().toISOString().slice(0, 10));
  const [ledgerId, setLedgerId] = useState("");
  const [siteId, setSiteId] = useState("");
  const [groupBy, setGroupBy] = useState<ReportDimension[]>(["site"]);
  const [balances, setBalances] = useState("site,balance\n");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per form content: a retried submit is a replay, an edited form is a new run.
  const key = useRef(crypto.randomUUID());
  const edited = <T,>(setter: (v: T) => void) => (v: T) => {
    key.current = crypto.randomUUID();
    setter(v);
  };

  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim().toLowerCase() : "";
  const ledgers = useLoad((signal) => (le ? listValuationLedgers(le, signal) : Promise.resolve({ items: [] })), `ledgers|${le}`);

  function toggle(d: ReportDimension) {
    key.current = crypto.randomUUID();
    const nextGroup = groupBy.includes(d) ? groupBy.filter((x) => x !== d) : REPORT_DIMENSIONS.filter((x) => x === d || groupBy.includes(x));
    setGroupBy(nextGroup);
    // The header line follows the grouping; the pasted balance lines are kept.
    setBalances((prev) => {
      const [, ...rest] = prev.split("\n");
      return [[...nextGroup, "balance"].join(","), ...rest].join("\n");
    });
  }

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setFailure(null);
    const found: Record<string, string> = {};
    if (!le) found.legalEntityId = "Enter the legal entity's id (a UUID).";
    if (!DAY.test(asOf)) found.asOf = "Enter the as-of date.";
    if (siteId.trim() !== "" && !UUID.test(siteId.trim())) found.siteId = "Enter a site id (a UUID), or leave it empty for every site.";
    const parsed = parseLedgerBalances(balances, groupBy);
    if (!parsed.ok) found.glBalances = parsed.error;
    setErrors(found);
    if (Object.keys(found).length > 0 || !parsed.ok) return;
    setBusy(true);
    try {
      const body = {
        legalEntityId: le,
        asOf,
        groupBy,
        glBalances: parsed.value,
        ...(ledgerId ? { valuationLedgerId: ledgerId } : {}),
        ...(siteId.trim() ? { siteId: siteId.trim().toLowerCase() } : {}),
      };
      const run = await createReconciliation(body, key.current);
      navigate(`/costing/reconciliations/${run.id}`);
    } catch (err) {
      if (err instanceof CostingApiError && err.problem.errors) setErrors(err.problem.errors);
      setFailure(describeError(err, "The reconciliation could not be run."));
    } finally {
      setBusy(false);
    }
  }

  const header = [...groupBy, "balance"].join(",");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="New reconciliation"
        description="Compares the inventory value at a date with the balances of the inventory accounts, grouped the same way, and stores the result. The daily reconciliation job does the same; until it reads the ledger itself, paste the balances here."
        actions={
          <Link className={buttonClassName("ghost")} to="/costing/reconciliations">
            Back to reconciliations
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <FormField id="rec-le" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(e) => edited(setLegalEntityId)(e.target.value)} />}
          </FormField>
          <FormField id="rec-as-of" label="As of date" error={errors.asOf}>
            {(c) => <Input {...c} type="date" value={asOf} invalid={Boolean(errors.asOf)} onChange={(e) => edited(setAsOf)(e.target.value)} />}
          </FormField>
          <FormField id="rec-ledger" label="Valuation ledger" error={errors.valuationLedgerId}>
            {(c) => (
              <Select {...c} value={ledgerId} disabled={!le} onChange={(e) => edited(setLedgerId)(e.target.value)}>
                <option value="">Primary valuation</option>
                {(ledgers.data?.items ?? [])
                  .filter((l) => !l.primary)
                  .map((l) => (
                    <option key={l.id} value={l.id}>
                      {`${l.code} (${l.currency})`}
                    </option>
                  ))}
              </Select>
            )}
          </FormField>
          <FormField id="rec-site" label="Site" optional hint="Empty reconciles every site." error={errors.siteId}>
            {(c) => <Input {...c} className={styles.mono} value={siteId} invalid={Boolean(errors.siteId)} onChange={(e) => edited(setSiteId)(e.target.value)} />}
          </FormField>
          <fieldset className={styles.lineSet}>
            <legend className={styles.legend}>Group by (none for one total)</legend>
            {REPORT_DIMENSIONS.map((d) => (
              <span key={d} className={styles.check}>
                <Input id={`rec-group-${d}`} type="checkbox" checked={groupBy.includes(d)} onChange={() => toggle(d)} />
                <label htmlFor={`rec-group-${d}`}>{DIMENSION_LABEL[d]}</label>
              </span>
            ))}
          </fieldset>
          {ledgers.error ? (
            <div className={styles.wide}>
              <Alert tone="warning">{describeError(ledgers.error, "The legal entity's valuation ledgers could not be loaded.")}</Alert>
            </div>
          ) : null}
          <div className={styles.wide}>
            <FormField
              id="rec-balances"
              label="Ledger balances (CSV)"
              hint={`One line per group with the columns ${header}. A group missing on either side is compared with zero.`}
              error={errors.glBalances}
            >
              {(c) => <Textarea {...c} mono rows={8} value={balances} invalid={Boolean(errors.glBalances)} onChange={(e) => edited(setBalances)(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run and store
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {failure}
      </Alert>
    </div>
  );
}

function rowKey(row: TieOutRow): string {
  return JSON.stringify(REPORT_DIMENSIONS.map((d) => row.key[d] ?? ""));
}

/** One stored reconciliation run: the result and every group compared. */
export function ReconciliationDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getReconciliation(id, signal), id);
  const run = state.data;
  const back = (
    <Link className={buttonClassName("ghost")} to="/costing/reconciliations">
      Back to reconciliations
    </Link>
  );

  if (state.error) {
    if (problemCode(state.error) === "CST-REC-002") {
      return <EmptyState icon="xCircle" headingLevel={2} title="Reconciliation not found" description="It may belong to another legal entity, or the link is wrong." action={back} />;
    }
    return <LoadError title="Could not load the reconciliation" error={state.error} onRetry={state.reload} />;
  }
  if (!run) {
    return (
      <div className={styles.stack} aria-busy="true">
        <span className="visually-hidden">Loading reconciliation…</span>
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  const currency = run.currency ? ` (${run.currency})` : "";
  const columns: DataTableColumn<TieOutRow>[] = [
    ...run.groupBy.map((d) => ({
      key: `key.${d}`,
      header: DIMENSION_LABEL[d],
      mono: d !== "valuationType",
      render: (row: TieOutRow) => row.key[d] || (d === "valuationType" || d === "warehouse" ? "None" : dash),
    })),
    { key: "inventory", header: `Inventory value${currency}`, align: "end", mono: true },
    { key: "ledger", header: `Ledger balance${currency}`, align: "end", mono: true },
    { key: "diff", header: "Difference", align: "end", mono: true, render: (row) => (ZERO.test(row.diff) ? row.diff : <Badge tone="warning">{row.diff}</Badge>) },
  ];
  const report = new URLSearchParams({ legalEntityId: run.legalEntityId, asOf: run.asOf, groupBy: run.groupBy.join(",") });
  if (run.valuationLedgerId) report.set("valuationLedgerId", run.valuationLedgerId);
  if (run.siteId) report.set("siteId", run.siteId);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Reconciliation as of ${run.asOf}`}
        meta={<TieBadge ties={run.ties} />}
        description={`${run.valuationLedgerCode ?? "Primary valuation"}, grouped by ${grouping(run.groupBy).toLowerCase()}.`}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={`/costing/value-report?${report.toString()}`}>
              Value report
            </Link>
            {back}
          </>
        }
      />
      <StatGroup label="Reconciliation result">
        <StatTile label="Groups compared" value={String(run.rowCount)} />
        <StatTile label="Groups that differ" value={String(run.differences)} tone={run.ties ? "neutral" : "warning"} />
        <StatTile label={`Net difference${currency}`} value={run.totalDiff} />
      </StatGroup>
      {run.ties ? (
        <Alert tone="success">Every group agrees with the ledger; no difference was raised.</Alert>
      ) : (
        <Alert tone="warning">{`${run.differences} ${run.differences === 1 ? "group differs" : "groups differ"}; each raised a reconciliation difference alert.`}</Alert>
      )}
      <Card title="Details" headingLevel={2}>
        <dl className={styles.details}>
          <Detail term="Legal entity" mono>
            {run.legalEntityId}
          </Detail>
          <Detail term="Site" mono>
            {run.siteId ?? "Every site"}
          </Detail>
          <Detail term="Run">{formatDateTime(run.createdAt)}</Detail>
        </dl>
      </Card>
      <Card title="Groups" headingLevel={2}>
        <DataTable
          label="Reconciliation by group"
          columns={columns}
          rows={run.rows ?? []}
          getRowKey={rowKey}
          empty={{ title: "Nothing was compared", description: "Neither the report nor the ledger had a balance." }}
        />
      </Card>
    </div>
  );
}
