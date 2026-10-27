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
  createAgingSnapshot,
  createBucketSet,
  describeError,
  getAgingSnapshot,
  listAgingSnapshots,
  listBucketSets,
  listValuationLedgers,
  type AgingBucket,
  type AgingSnapshot,
  type BucketSet,
  type CostMethod,
  type Fallback,
  type SnapshotLine,
  type SnapshotLineRequest,
  type SnapshotRequest,
} from "./api/client";
import { DEFAULT_BOUNDS, parseBounds } from "./AgingPage";
import styles from "./pages.module.css";
import { CURRENCY, FallbackField, MethodField } from "./shared";
import { useLoad } from "./useLoad";
import { LegalEntityFilterField, useLegalEntityFilter } from "./ValuationSetupPage";
import { Detail, formatDateTime, LoadError, problemCode, UUID } from "./versionParts";

const DAY = /^\d{4}-\d{2}-\d{2}$/;

function TieBadge({ ties }: { ties: boolean }) {
  return ties ? <Badge tone="success">Ties</Badge> : <Badge tone="warning">Does not tie</Badge>;
}

const bucketColumns = (currency: string): DataTableColumn<AgingBucket>[] => [
  { key: "label", header: "Age (days)" },
  { key: "qty", header: "Quantity", align: "end", mono: true },
  { key: "value", header: `Value (${currency})`, align: "end", mono: true },
];

const snapshotColumns: DataTableColumn<AgingSnapshot>[] = [
  { key: "asOf", header: "As of", mono: true },
  { key: "legalEntityId", header: "Legal entity", mono: true },
  { key: "method", header: "Costing method", mono: true },
  { key: "totalValue", header: "Total value", align: "end", mono: true, render: (row) => `${row.totalValue} ${row.currency}` },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "tiesToRemaining", header: "Tie-out", render: (row) => <TieBadge ties={row.tiesToRemaining} /> },
  { key: "createdAt", header: "Stored", render: (row) => formatDateTime(row.createdAt) },
];

/** Stored month-end aging snapshots (FR-20.17, 3.20.15), newest as-of date first. */
export function AgingSnapshotsPage() {
  const navigate = useNavigate();
  const filter = useLegalEntityFilter();
  const state = useLoad((signal) => listAgingSnapshots({ legalEntityId: filter.applied }, signal), filter.applied);
  const [more, setMore] = useState<{ key: string; rows: AgingSnapshot[]; next?: string }>({ key: "", rows: [] });
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
      const page = await listAgingSnapshots({ legalEntityId: filter.applied, cursor: next });
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
        title="Aging snapshots"
        description="Month-end inventory aging stored as evidence for the obsolescence reserve. A snapshot is never changed once stored."
        actions={
          <>
            <Link className={buttonClassName("primary")} to="/costing/aging/snapshots/new">
              New aging snapshot
            </Link>
            <Link className={buttonClassName("secondary")} to="/costing/aging/bucket-sets">
              Bucket sets
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <LegalEntityFilterField id="snapshots-le" filter={filter} />
      </div>
      {state.error ? (
        <LoadError title="Could not load the aging snapshots" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Aging snapshots"
          columns={snapshotColumns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading && !state.data}
          empty={{ title: "No aging snapshots yet", description: "Store the aging as of a month end to keep it as evidence." }}
          onRowOpen={(row) => navigate(`/costing/aging/snapshots/${row.id}`)}
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
            Load more snapshots
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/** The FR-20.1 example as one snapshot line, for the lines field. */
export const SAMPLE_LINES = JSON.stringify(
  [
    {
      productId: "22222222-2222-4222-8222-222222222222",
      siteId: "44444444-4444-4444-8444-444444444444",
      transactions: [
        { id: "rcpt-1", kind: "RECEIPT", qty: "100", value: "1000.00", financialDate: "2026-01-05" },
        { id: "rcpt-2", kind: "RECEIPT", qty: "100", value: "1200.00", financialDate: "2026-01-20" },
        { id: "iss-1", kind: "ISSUE", qty: "150", financialDate: "2026-01-25" },
      ],
    },
  ],
  null,
  2,
);

/** Parses the lines field: a non-empty JSON array of product x site lines with ids and transactions. */
export function parseSnapshotLines(text: string): { ok: true; value: SnapshotLineRequest[] } | { ok: false; error: string } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { ok: false, error: "Lines are not valid JSON." };
  }
  if (!Array.isArray(parsed) || parsed.length === 0) return { ok: false, error: "Lines must be a non-empty JSON array." };
  for (const [i, line] of parsed.entries()) {
    const l = line as Partial<SnapshotLineRequest> | null;
    if (!l || typeof l !== "object" || !UUID.test(String(l.productId)) || !UUID.test(String(l.siteId))) {
      return { ok: false, error: `Line ${i + 1} needs a productId and a siteId (UUIDs).` };
    }
    if (!Array.isArray(l.transactions) || l.transactions.length === 0) return { ok: false, error: `Line ${i + 1} needs at least one transaction.` };
  }
  return { ok: true, value: parsed as SnapshotLineRequest[] };
}

const CUSTOM = "custom";
const DEFAULT = "";

/** Store a month-end aging snapshot for a legal entity. */
export function CreateSnapshotPage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const [ledgerId, setLedgerId] = useState("");
  const [method, setMethod] = useState<CostMethod>("FIFO");
  const [currency, setCurrency] = useState("EUR");
  const [fallback, setFallback] = useState<Fallback>("RUNNING_AVG");
  const [asOf, setAsOf] = useState("2026-01-31");
  const [bucketChoice, setBucketChoice] = useState(DEFAULT);
  const [bounds, setBounds] = useState(DEFAULT_BOUNDS);
  const [lines, setLines] = useState(SAMPLE_LINES);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per form content: a retried submit is a replay, an edited form is a new snapshot.
  const key = useRef(crypto.randomUUID());
  const edited = <T,>(setter: (v: T) => void) => (v: T) => {
    key.current = crypto.randomUUID();
    setter(v);
  };

  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim().toLowerCase() : "";
  const ledgers = useLoad((signal) => (le ? listValuationLedgers(le, signal) : Promise.resolve({ items: [] })), `ledgers|${le}`);
  const sets = useLoad((signal) => (le ? listBucketSets(le, signal) : Promise.resolve({ items: [] as BucketSet[] })), `sets|${le}`);
  const ledger = ledgers.data?.items.find((l) => l.id === ledgerId);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setFailure(null);
    const found: Record<string, string> = {};
    if (!le) found.legalEntityId = "Enter the legal entity's id (a UUID).";
    if (!ledger && !CURRENCY.test(currency)) found.currency = "Use a three-letter ISO 4217 code, such as EUR.";
    if (!DAY.test(asOf)) found.asOf = "Enter the as-of date.";
    const parsedBounds: ReturnType<typeof parseBounds> = bucketChoice === CUSTOM ? parseBounds(bounds) : { ok: true };
    if (!parsedBounds.ok) found.bucketBounds = parsedBounds.error;
    const parsedLines = parseSnapshotLines(lines);
    if (!parsedLines.ok) found.lines = parsedLines.error;
    setErrors(found);
    if (Object.keys(found).length > 0 || !parsedLines.ok || !parsedBounds.ok) return;
    const body: SnapshotRequest = { legalEntityId: le, asOf, fallback, lines: parsedLines.value };
    if (ledger) body.valuationLedgerId = ledger.id;
    else Object.assign(body, { method, currency });
    if (bucketChoice === CUSTOM && parsedBounds.value) body.bucketBounds = parsedBounds.value;
    else if (bucketChoice !== CUSTOM && bucketChoice !== DEFAULT) body.bucketSetId = bucketChoice;
    setBusy(true);
    try {
      const snap = await createAgingSnapshot(body, key.current);
      navigate(`/costing/aging/snapshots/${snap.id}`);
    } catch (err) {
      if (err instanceof CostingApiError && err.problem.errors) setErrors(err.problem.errors);
      setFailure(describeError(err, "The aging snapshot could not be stored."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New aging snapshot"
        description="Ages every product and site line as of the date, the same way as the aging preview, and stores the result. Until the cost transaction projection exists, the lines carry their receipts and issues."
        actions={
          <Link className={buttonClassName("ghost")} to="/costing/aging/snapshots">
            Back to aging snapshots
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <FormField id="snapshot-le" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(e) => edited(setLegalEntityId)(e.target.value)} />}
          </FormField>
          <FormField id="snapshot-as-of" label="As of date" error={errors.asOf}>
            {(c) => <Input {...c} type="date" value={asOf} invalid={Boolean(errors.asOf)} onChange={(e) => edited(setAsOf)(e.target.value)} />}
          </FormField>
          <FormField id="snapshot-ledger" label="Valuation ledger" optional hint="A ledger sets the costing method and currency." error={errors.valuationLedgerId}>
            {(c) => (
              <Select {...c} value={ledgerId} disabled={!le} onChange={(e) => edited(setLedgerId)(e.target.value)}>
                <option value="">None: choose the method and currency</option>
                {(ledgers.data?.items ?? []).map((l) => (
                  <option key={l.id} value={l.id}>
                    {`${l.code} (${l.method}, ${l.currency})`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {ledger ? null : (
            <>
              <MethodField id="snapshot-method" value={method} onChange={edited(setMethod)} />
              <FormField id="snapshot-currency" label="Currency" error={errors.currency}>
                {(c) => <Input {...c} value={currency} maxLength={3} invalid={Boolean(errors.currency)} onChange={(e) => edited(setCurrency)(e.target.value.toUpperCase())} />}
              </FormField>
            </>
          )}
          <FallbackField id="snapshot-fallback" value={fallback} onChange={edited(setFallback)} />
          <FormField id="snapshot-buckets" label="Buckets" error={errors.bucketSetId}>
            {(c) => (
              <Select {...c} value={bucketChoice} onChange={(e) => edited(setBucketChoice)(e.target.value)}>
                <option value={DEFAULT}>Default: 30, 90, 180, 365 days</option>
                {(sets.data?.items ?? []).map((s) => (
                  <option key={s.id} value={s.id}>
                    {`${s.name}: ${s.bounds.join(", ")} days`}
                  </option>
                ))}
                <option value={CUSTOM}>Custom bounds</option>
              </Select>
            )}
          </FormField>
          {bucketChoice === CUSTOM ? (
            <FormField id="snapshot-bounds" label="Bucket bounds" hint="Upper bound of each bucket in days." error={errors.bucketBounds}>
              {(c) => <Input {...c} value={bounds} invalid={Boolean(errors.bucketBounds)} onChange={(e) => edited(setBounds)(e.target.value)} />}
            </FormField>
          ) : null}
          {ledgers.error || sets.error ? (
            <div className={styles.wide}>
              <Alert tone="warning">{describeError(ledgers.error ?? sets.error, "The legal entity's ledgers and bucket sets could not be loaded.")}</Alert>
            </div>
          ) : null}
          <div className={styles.wide}>
            <FormField
              id="snapshot-lines"
              label="Lines (JSON)"
              hint="An array of product and site lines: productId, siteId, transactions, and optionally standardUnitCost and defaultValuationType."
              error={errors.lines}
            >
              {(c) => <Textarea {...c} mono rows={14} value={lines} invalid={Boolean(errors.lines)} onChange={(e) => edited(setLines)(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Store snapshot
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

function lineTypes(line: SnapshotLine): string {
  return (line.valuationTypes ?? []).map((t) => `${t.valuationType} ${t.totalValue}`).join(", ") || "Not split";
}

/** One stored aging snapshot: totals, buckets, tie-out and the aging of each line. */
export function SnapshotDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getAgingSnapshot(id, signal), id);
  const snap = state.data;
  const back = (
    <Link className={buttonClassName("ghost")} to="/costing/aging/snapshots">
      Back to aging snapshots
    </Link>
  );

  if (state.error) {
    if (problemCode(state.error) === "CST-AGE-004") {
      return <EmptyState icon="xCircle" headingLevel={2} title="Aging snapshot not found" description="It may belong to another legal entity, or the link is wrong." action={back} />;
    }
    return <LoadError title="Could not load the aging snapshot" error={state.error} onRetry={state.reload} />;
  }
  if (!snap) {
    return (
      <div className={styles.stack} aria-busy="true">
        <span className="visually-hidden">Loading aging snapshot…</span>
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  const lineColumns: DataTableColumn<SnapshotLine>[] = [
    { key: "productId", header: "Product", mono: true },
    { key: "siteId", header: "Site", mono: true },
    { key: "totalQty", header: "Quantity", align: "end", mono: true },
    { key: "totalValue", header: `Value (${snap.currency})`, align: "end", mono: true },
    { key: "valuationTypes", header: "By valuation type", render: lineTypes },
    { key: "tiesToRemaining", header: "Tie-out", render: (row) => <TieBadge ties={row.tiesToRemaining} /> },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title={`Aging snapshot as of ${snap.asOf}`} meta={<TieBadge ties={snap.tiesToRemaining} />} description={`${snap.method} in ${snap.currency}, ${snap.lineCount} product and site lines.`} actions={back} />
      <StatGroup label="Snapshot totals">
        <StatTile label="Total quantity" value={snap.totalQty} />
        <StatTile label="Total value" value={`${snap.totalValue} ${snap.currency}`} />
        <StatTile label="Remaining value" value={`${snap.remainingValue} ${snap.currency}`} />
      </StatGroup>
      {snap.tiesToRemaining ? (
        <Alert tone="success">{`Every line's buckets tie to its remaining value; the total is ${snap.totalValue} ${snap.currency}.`}</Alert>
      ) : (
        <Alert tone="warning">{`The buckets total ${snap.totalValue} but the remaining value is ${snap.remainingValue} ${snap.currency}.`}</Alert>
      )}
      <Card title="Details" headingLevel={2}>
        <dl className={styles.details}>
          <Detail term="Legal entity" mono>
            {snap.legalEntityId}
          </Detail>
          <Detail term="Valuation ledger" mono>
            {snap.valuationLedgerId ?? "None"}
          </Detail>
          <Detail term="Bucket set" mono>
            {snap.bucketSetId ?? "Default or custom bounds"}
          </Detail>
          <Detail term="Stored">{formatDateTime(snap.createdAt)}</Detail>
        </dl>
      </Card>
      <Card title="Buckets" headingLevel={2}>
        <DataTable label="Snapshot buckets" columns={bucketColumns(snap.currency)} rows={snap.buckets} getRowKey={(b) => b.label} />
      </Card>
      <Card title="Lines" headingLevel={2}>
        <DataTable
          label="Snapshot lines"
          columns={lineColumns}
          rows={snap.lines ?? []}
          getRowKey={(l) => `${l.productId}|${l.siteId}`}
          empty={{ title: "No lines", description: "This snapshot holds no product and site lines." }}
        />
      </Card>
    </div>
  );
}

const setColumns: DataTableColumn<BucketSet>[] = [
  { key: "name", header: "Name" },
  { key: "bounds", header: "Upper bounds (days)", mono: true, render: (row) => row.bounds.join(", ") },
  { key: "legalEntityId", header: "Legal entity", mono: true },
  { key: "createdAt", header: "Saved", render: (row) => formatDateTime(row.createdAt) },
];

/** Saved aging bucket sets per legal entity (FR-20.17, AX "Inventory aging report setup"). */
export function BucketSetsPage() {
  const filter = useLegalEntityFilter();
  const [refresh, setRefresh] = useState(0);
  const state = useLoad((signal) => listBucketSets(filter.applied, signal), `${filter.applied}|${refresh}`);
  const [legalEntityId, setLegalEntityId] = useState("");
  const [name, setName] = useState("");
  const [bounds, setBounds] = useState(DEFAULT_BOUNDS);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setFailure(null);
    setNotice("");
    const found: Record<string, string> = {};
    const le = legalEntityId.trim();
    if (!UUID.test(le)) found.legalEntityId = "Enter the legal entity's id (a UUID).";
    if (name.trim() === "" || name.trim().length > 64) found.name = "Give the set a name of 1 to 64 characters.";
    const parsed = parseBounds(bounds);
    if (!parsed.ok) found.bounds = parsed.error;
    else if (!parsed.value) found.bounds = "Enter at least one bound, such as 30, 90, 180.";
    setErrors(found);
    if (Object.keys(found).length > 0 || !parsed.ok || !parsed.value) return;
    setBusy(true);
    try {
      const set = await createBucketSet({ legalEntityId: le, name: name.trim(), bounds: parsed.value });
      setNotice(`Saved the bucket set ${set.name}.`);
      setName("");
      setRefresh((n) => n + 1);
    } catch (err) {
      if (err instanceof CostingApiError && err.problem.errors) setErrors(err.problem.errors);
      setFailure(describeError(err, "The bucket set could not be saved."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Aging bucket sets"
        description="Save the day bounds a legal entity ages its inventory by, then pick the set when you store a snapshot."
        actions={
          <Link className={buttonClassName("ghost")} to="/costing/aging/snapshots">
            Back to aging snapshots
          </Link>
        }
      />
      <Card title="Save a bucket set" headingLevel={2}>
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <FormField id="set-le" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(e) => setLegalEntityId(e.target.value)} />}
          </FormField>
          <FormField id="set-name" label="Name" hint="Unique in the legal entity, such as Monthly." error={errors.name}>
            {(c) => <Input {...c} value={name} maxLength={64} invalid={Boolean(errors.name)} onChange={(e) => setName(e.target.value)} />}
          </FormField>
          <FormField id="set-bounds" label="Bucket bounds" hint="Upper bound of each bucket in days; the last bucket is open-ended." error={errors.bounds}>
            {(c) => <Input {...c} value={bounds} invalid={Boolean(errors.bounds)} onChange={(e) => setBounds(e.target.value)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save bucket set
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Alert tone="danger" live="alert">
        {failure}
      </Alert>
      <Card title="Saved bucket sets" headingLevel={2}>
        <div className={styles.stack}>
          <div className={styles.toolbar}>
            <LegalEntityFilterField id="sets-le" filter={filter} />
          </div>
          {state.error ? (
            <LoadError title="Could not load the bucket sets" error={state.error} onRetry={state.reload} />
          ) : (
            <DataTable
              label="Aging bucket sets"
              columns={setColumns}
              rows={state.data?.items ?? []}
              getRowKey={(row) => row.id}
              loading={state.loading && !state.data}
              empty={{ title: "No bucket sets yet", description: "Snapshots use 30, 90, 180 and 365 days until you save a set." }}
            />
          )}
        </div>
      </Card>
    </div>
  );
}
