import { useMemo, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  buttonClassName,
  type DataTableColumn,
  ListPage,
  type ListColumn,
  type ListData,
  type ListFilterDef,
  type ListQuery,
} from "../../shell/ui";
import {
  describeProblem,
  getReleaseRun,
  listReleaseRuns,
  newIdempotencyKey,
  releaseRequisitions,
  type ReleaseRun,
  type ReleaseSkip,
  type ReleaseStatus,
  type ReleasedOrder,
} from "./api/client";
import {
  Details,
  LoadError,
  StatusBadge,
  money,
  statusLabel,
  useResource,
  uuidPattern,
  type Failure,
  Named,
  useProcurementPages,
} from "./parts";
import { formatInstant } from "./RfqPages";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const STATUSES: ReleaseStatus[] = ["COMPLETED", "PARTIAL", "FAILED", "EMPTY"];
const DEFAULT_WINDOW = "7";
const DEFAULT_MAX_LINES = "200";

type Errors = Record<string, string>;

/** What a run did, in a sentence: "3 of 4 lines released into 2 orders; 1 left in the queue". */
export function summaryOf(r: ReleaseRun): string {
  if (r.linesConsidered === 0) return "No approved lines were waiting to be ordered.";
  const orders = `${r.purchaseOrders.length} ${r.purchaseOrders.length === 1 ? "order" : "orders"}`;
  const left = r.linesSkipped > 0 ? `; ${r.linesSkipped} left in the queue` : "";
  return `${r.linesReleased} of ${r.linesConsidered} ${r.linesConsidered === 1 ? "line" : "lines"} released into ${orders}${left}.`;
}

/** Checks the form before the round trip: a legal entity UUID, a window of 1 to 60 days and 1 to 500 lines (FR-17.3). */
export function validateRelease(legalEntityId: string, windowDays: string, maxLines: string): Errors {
  const errors: Errors = {};
  if (!uuidPattern.test(legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!/^\d+$/.test(windowDays) || Number(windowDays) < 1 || Number(windowDays) > 60) errors.windowDays = "Enter a whole number of days from 1 to 60.";
  if (!/^\d+$/.test(maxLines) || Number(maxLines) < 1 || Number(maxLines) > 500) errors.maxLines = "Enter a whole number of lines from 1 to 500.";
  return errors;
}

const columns: ListColumn<ReleaseRun>[] = [
  { key: "createdAt", header: "Run", mono: true, render: (r) => formatInstant(r.createdAt) },
  { key: "legalEntityId", header: "Legal entity", render: (r) => <Named entity="legalEntity" id={r.legalEntityId} /> },
  { key: "status", header: "Status", render: (r) => <StatusBadge status={r.status} /> },
  { key: "linesConsidered", header: "Lines read", align: "end" },
  { key: "linesReleased", header: "Released", align: "end" },
  { key: "linesSkipped", header: "Left in queue", align: "end" },
  { key: "orders", header: "Orders", align: "end", render: (r) => String(r.purchaseOrders.length) },
];

const listFilters: ListFilterDef[] = [{ kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: STATUSES.map((s) => ({ value: s, label: statusLabel(s) })) }];

function useReleaseRows(query: ListQuery): ListData<ReleaseRun> {
  const status = (query.filters.status ?? "") as ReleaseStatus | "";
  return useProcurementPages(status, (cursor, signal) => listReleaseRuns({ status }, { cursor, signal }));
}

/**
 * Release runs: each run took the approved requisition lines that had a vendor and ordered them as draft purchase
 * orders. A run that left lines in the consolidation queue says why on its page.
 */
export function ReleaseRunsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="purchasing-release-runs"
      title="Release runs"
      description="Each run orders the approved requisition lines that have a vendor as draft purchase orders. Lines it could not order stay in the consolidation queue."
      actions={
        <Link className={buttonClassName("primary")} to="/purchasing/releases/new">
          Start a release run
        </Link>
      }
      noun="release runs"
      singular="release run"
      columns={columns}
      filters={listFilters}
      useRows={useReleaseRows}
      getRowKey={(r) => r.id}
      onRowOpen={(r) => navigate(`/purchasing/releases/${r.id}`)}
      empty={{ title: "No release runs yet", description: "Start a run to order the approved requisition lines that already have a vendor." }}
    />
  );
}

/** Starts a release run for a legal entity and opens its record. */
export function ReleaseRunCreatePage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const [windowDays, setWindowDays] = useState(DEFAULT_WINDOW);
  const [maxLines, setMaxLines] = useState(DEFAULT_MAX_LINES);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per distinct request, so a retry after a dropped response replays the stored run instead of ordering twice.
  const key = useMemo(() => newIdempotencyKey(), [legalEntityId, windowDays, maxLines]);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateRelease(legalEntityId, windowDays, maxLines);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await releaseRequisitions(
        { legalEntityId: legalEntityId.trim(), windowDays: Number(windowDays), maxLines: Number(maxLines) },
        key,
      );
      navigate(`/purchasing/releases/${body.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Start a release run"
        description="Orders every approved requisition line of the legal entity that has a vendor, one vendor at a time, as draft purchase orders. Lines without a vendor, and vendors whose orders cannot be made, stay in the consolidation queue with the reason."
      />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <Card title="Run">
          <div className={styles.grid}>
            <FormField id="rel-legal-entity" label="Legal entity" error={errors.legalEntityId}>
              {(c) => <EntityLookup entity="legalEntity" {...c} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
            </FormField>
            <FormField id="rel-window" label="Delivery window (days)" hint="Lines wanted less than this many days apart share an order" error={errors.windowDays}>
              {(c) => <Input {...c} inputMode="numeric" value={windowDays} onChange={(e) => setWindowDays(e.target.value)} />}
            </FormField>
            <FormField id="rel-max" label="Most lines to read" hint="Lines with a vendor, oldest first; run again for a larger backlog" error={errors.maxLines}>
              {(c) => <Input {...c} inputMode="numeric" value={maxLines} onChange={(e) => setMaxLines(e.target.value)} />}
            </FormField>
          </div>
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Release lines
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/releases">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

const orderColumns: DataTableColumn<ReleasedOrder>[] = [
  {
    key: "number",
    header: "Purchase order",
    render: (o) => (
      <Link className={styles.link} to={`/purchasing/purchase-orders/${o.purchaseOrderId}`}>
        {o.number || o.purchaseOrderId.slice(0, 8)}
      </Link>
    ),
  },
  { key: "vendorId", header: "Vendor", render: (o) => <Named entity="vendor" id={o.vendorId} /> },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "total", header: "Total", align: "end", mono: true, render: (o) => money(o.total, o.currency) },
];

const skipColumns: DataTableColumn<ReleaseSkip>[] = [
  {
    key: "line",
    header: "Requisition line",
    render: (s) => (
      <Link className={styles.link} to="/purchasing/consolidation">
        {s.requisitionNumber} line {s.lineNo}
      </Link>
    ),
  },
  { key: "vendorId", header: "Vendor", render: (s) => <Named entity="vendor" id={s.vendorId} /> },
  { key: "code", header: "Code", mono: true },
  { key: "reason", header: "Why it was left" },
];

/** One run: what it ordered, and every line it left in the queue with the rule that stopped it. */
export function ReleaseRunDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource(async (signal) => (await getReleaseRun(id, signal)).body, [id]);
  const run = resource.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={run ? `Release run ${formatInstant(run.createdAt)}` : "Release run"}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/purchasing/releases">
              All runs
            </Link>
            {run && run.linesSkipped > 0 ? (
              <Link className={buttonClassName("primary")} to="/purchasing/consolidation">
                Open consolidation queue
              </Link>
            ) : null}
          </>
        }
      />
      {resource.error ? <LoadError what="the release run" error={resource.error} onRetry={resource.reload} /> : null}
      {resource.loading && !run ? <p role="status">Loading the release run…</p> : null}
      {run ? (
        <>
          <Alert
            tone={run.status === "COMPLETED" ? "success" : run.status === "EMPTY" ? "info" : run.status === "FAILED" ? "danger" : "warning"}
            title={statusLabel(run.status)}
          >
            {summaryOf(run)}
          </Alert>
          <Details
            items={[
              { label: "Status", value: <StatusBadge status={run.status} /> },
              { label: "Legal entity", value: <Named entity="legalEntity" id={run.legalEntityId} /> },
              { label: "Delivery window", value: `${run.windowDays} days` },
              { label: "Most lines to read", value: String(run.maxLines), mono: true },
              { label: "Lines read", value: String(run.linesConsidered), mono: true },
              { label: "Released", value: String(run.linesReleased), mono: true },
              { label: "Left in queue", value: String(run.linesSkipped), mono: true },
              { label: "Started by", value: run.requestedBy ?? "", mono: true },
              { label: "Run", value: run.createdAt, mono: true },
            ]}
          />
          {run.warnings.length > 0 ? (
            <Alert tone="warning" title="Warnings on the orders">
              {run.warnings.map((w) => `${w.code}: ${w.detail}`).join(" ")}
            </Alert>
          ) : null}
          <Card title="Orders created">
            <DataTable
              label="Orders created"
              columns={orderColumns}
              rows={run.purchaseOrders}
              getRowKey={(o) => o.purchaseOrderId}
              empty={{ title: "No orders were created", description: "Every line the run read was left in the queue." }}
            />
          </Card>
          <Card title="Lines left in the queue">
            <DataTable
              label="Lines left in the queue"
              columns={skipColumns}
              rows={run.skipped}
              getRowKey={(s) => s.requisitionLineId}
              empty={{ title: "Nothing was left behind", description: "Every line the run read became part of an order." }}
            />
          </Card>
        </>
      ) : null}
    </div>
  );
}
