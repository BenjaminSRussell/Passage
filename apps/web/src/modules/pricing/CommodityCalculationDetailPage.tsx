import { useRef, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getCommodityCalculation,
  listCommodityRuns,
  newKey,
  PricingApiError,
  previewCommodityRun,
  runCommodityCalculation,
  withdrawCommodityCalculation,
  type CommodityCalculation,
  type CommodityRun,
  type CommodityRunPreview,
  type Problem,
} from "./api/client";
import { CALC_STATES, calcState, describeFormula, describeTarget } from "./commodityFormat";
import { DAY, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

const runColumns: DataTableColumn<CommodityRun>[] = [
  { key: "asOf", header: "Run date", mono: true },
  { key: "indexValue", header: "Index", mono: true, render: (r) => `${r.indexValue} (${r.indexAsOf}, rev ${r.indexRevision})` },
  { key: "price", header: "Price", mono: true },
  { key: "changedCount", header: "Repriced", mono: true, render: (r) => `${r.changedCount} changed, ${r.unchangedCount} unchanged` },
  {
    key: "journalId",
    header: "Price journal",
    render: (r) =>
      r.journalId ? (
        <Link to={`/pricing/journals/${r.journalId}`}>Review journal</Link>
      ) : (
        "None needed"
      ),
  },
  { key: "stale", header: "Index age", render: (r) => (r.stale ? <Badge tone="warning">Stale</Badge> : <Badge tone="success">Current</Badge>) },
  { key: "runBy", header: "Run by", mono: true, render: (r) => r.runBy || "Service" },
];

const changeColumns: DataTableColumn<CommodityRunPreview["changes"][number]>[] = [
  { key: "recordId", header: "Trade agreement record", mono: true },
  { key: "currentAmount", header: "Now", mono: true },
  { key: "newAmount", header: "New price", mono: true },
];

/** One price calculation: preview, run, withdraw and the history of runs (FR-26.21). */
export function CommodityCalculationDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getCommodityCalculation(id, signal), id);
  const runs = useLoad((signal) => listCommodityRuns(id, signal), id);
  const [changed, setChanged] = useState<CommodityCalculation | null>(null);
  const [asOf, setAsOf] = useState(today());
  const [preview, setPreview] = useState<CommodityRunPreview | null>(null);
  const [asOfError, setAsOfError] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState("");
  const runKey = useRef(newKey());
  const commandKey = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Price calculation" />
        <Alert tone="danger" title="Could not load the calculation" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Price calculation" />
        <span className="visually-hidden">Loading the calculation…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const calc = changed && changed.id === id && changed.version >= state.data.version ? changed : state.data;
  const status = CALC_STATES[calcState(calc)];

  function dayOk(): boolean {
    if (!DAY.test(asOf)) {
      setAsOfError("Enter the run date as YYYY-MM-DD.");
      return false;
    }
    setAsOfError("");
    return true;
  }

  async function doPreview() {
    setFailure(null);
    setDone("");
    setPreview(null);
    if (!dayOk()) return;
    setBusy("preview");
    try {
      setPreview(await previewCommodityRun(calc.id, asOf));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy("");
    }
  }

  async function doRun() {
    setFailure(null);
    setDone("");
    if (!dayOk()) return;
    setBusy("run");
    try {
      const run = await runCommodityCalculation(calc.id, asOf, runKey.current);
      const stale = run.warnings.map((w) => ` ${w.code}: ${w.detail}.`).join("");
      setDone(
        (run.journalId
          ? `Run complete: ${run.changedCount} price${run.changedCount === 1 ? "" : "s"} changed to ${run.price}. Another pricing manager reviews the price journal and posts it to apply them.`
          : `Run complete: every price is already ${run.price}, so no price journal was needed.`) + stale,
      );
      setPreview(null);
      runKey.current = newKey();
      runs.reload();
    } catch (error) {
      setFailure(describeProblem(error));
      if (!(error instanceof PricingApiError) || error.status < 500) runKey.current = newKey();
    } finally {
      setBusy("");
    }
  }

  async function withdraw() {
    setFailure(null);
    setDone("");
    setBusy("withdraw");
    try {
      setChanged(await withdrawCommodityCalculation(calc.id, calc.version, commandKey.current));
      setDone("The calculation is withdrawn. It takes no further runs.");
      setPreview(null);
      commandKey.current = newKey();
    } catch (error) {
      setFailure(describeProblem(error));
      if (!(error instanceof PricingApiError) || error.status < 500) commandKey.current = newKey();
    } finally {
      setBusy("");
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={calc.name}
        description={`${describeFormula(calc)} · version ${calc.version}`}
        meta={<Badge tone={status.tone}>{status.label}</Badge>}
        actions={
          <Link className={buttonClassName("ghost")} to="/pricing/commodity/calculations">
            All price calculations
          </Link>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="The request failed" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <Card title="Calculation">
        <dl className={styles.details}>
          <Detail term="Reprices">{describeTarget(calc)}</Detail>
          <Detail term="Formula">{describeFormula(calc)}</Detail>
          <Detail term="Index is stale after">{calc.maxIndexAgeDays} days</Detail>
          <Detail term="Cost basis type">
            <Link to={`/pricing/commodity/bases/${calc.basisId}`}>{calc.basisId}</Link>
          </Detail>
          <Detail term="Legal entity" mono>
            {calc.legalEntityId}
          </Detail>
          <Detail term="Calculation id" mono>
            {calc.id}
          </Detail>
        </dl>
      </Card>
      {calc.withdrawn ? null : (
        <Card title="Run">
          <div className={styles.stack}>
            <p className={styles.note}>
              A run uses the newest index quote on or before the run date and reprices the active trade agreement prices on this key. It creates a price journal; nothing changes until the journal is posted.
            </p>
            <FormField id="run-day" label="Run date" error={asOfError}>
              {(c) => <Input {...c} type="date" className={styles.mono} value={asOf} onChange={(e) => setAsOf(e.target.value)} invalid={Boolean(asOfError)} />}
            </FormField>
            <div className={styles.actions}>
              <Button loading={busy === "preview"} onClick={doPreview}>
                Preview run
              </Button>
              <Button variant="primary" loading={busy === "run"} onClick={doRun}>
                Run calculation
              </Button>
            </div>
            {preview ? (
              <div className={styles.stack}>
                {preview.warnings.map((w) => (
                  <Alert key={w.code} tone="warning" title={w.code} live="status">
                    {w.detail}
                  </Alert>
                ))}
                <p>
                  Index {preview.index.value} from {preview.index.asOf} (revision {preview.index.revision}) gives a price of <strong>{preview.price}</strong> {calc.currency}.{" "}
                  {preview.changes.length} record{preview.changes.length === 1 ? "" : "s"} would change, {preview.unchangedCount} already at the price.
                </p>
                <DataTable
                  label="Prices the run would change"
                  columns={changeColumns}
                  rows={preview.changes}
                  getRowKey={(row) => row.recordId}
                  empty={{ title: "Nothing to change", description: "Every active price on this key is already at the calculated price." }}
                />
              </div>
            ) : null}
          </div>
        </Card>
      )}
      <Card title="Runs">
        {runs.error ? (
          <Alert tone="danger" title="Could not load the runs" live="alert" action={<Button size="sm" onClick={runs.reload}>Try again</Button>}>
            {runs.error.message} ({runs.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Calculation runs"
            columns={runColumns}
            rows={runs.data ?? []}
            getRowKey={(row) => row.id}
            loading={runs.loading}
            empty={{ title: "No runs yet", description: "Preview a run, then run the calculation." }}
          />
        )}
      </Card>
      <Card title="Withdraw">
        <div className={styles.stack}>
          <p className={styles.note}>
            {calc.withdrawn
              ? "This calculation is withdrawn. Create a new calculation to price from the index again."
              : "Withdrawing the calculation stops further runs. Price journals it already created stay as they are."}
          </p>
          {calc.withdrawn ? null : (
            <div className={styles.actions}>
              <Button variant="danger" loading={busy === "withdraw"} onClick={withdraw}>
                Withdraw calculation
              </Button>
            </div>
          )}
        </div>
      </Card>
    </div>
  );
}
