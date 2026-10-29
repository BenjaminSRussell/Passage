import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  ConfirmDialog,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import {
  ARCHIVE_NAME,
  ARCHIVE_PERIOD,
  ARCHIVE_STATUSES,
  describeProblem,
  getArchivePartition,
  listArchivePartitions,
  restoreArchivePartition,
  type ArchivePartition,
  type Problem,
} from "./api/client";
import { count, dateTime, enumLabel, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Restored side tables live for 30 days (archive.restore_ttl_days default). */
const RESTORE_DAYS = 30;

function PartitionStatus({ partition }: { partition: ArchivePartition }) {
  switch (partition.status) {
    case "VERIFIED":
      return <Badge tone="info">Verified</Badge>;
    case "DROPPED":
      return <Badge tone="success">Archived</Badge>;
    case "RESTORED":
      return <Badge tone="warning">Restored</Badge>;
    default:
      return <StatusPill status={partition.status} />;
  }
}

const columns: DataTableColumn<ArchivePartition>[] = [
  { key: "domain", header: "Domain", mono: true },
  { key: "tableName", header: "Table", mono: true },
  { key: "period", header: "Period", mono: true },
  { key: "rowsArchive", header: "Rows", align: "end", render: (p) => count(p.status === "PENDING" ? p.rowsOltp : p.rowsArchive) },
  {
    key: "contentHashArchive",
    header: "Content hash",
    mono: true,
    render: (p) => (p.contentHashArchive ? <span title={p.contentHashArchive}>{shortId(p.contentHashArchive)}</span> : ""),
  },
  { key: "movedAt", header: "Moved", render: (p) => dateTime(p.movedAt) },
  { key: "status", header: "Status", render: (p) => <PartitionStatus partition={p} /> },
];

interface Filter {
  domain: string;
  period: string;
  status: string;
}

type FilterErrors = Partial<Record<"domain" | "period", string>>;

/**
 * Archive partition inquiry (FR-46.18, PRD §9.46.14): closed OLTP partitions
 * moved to the archive tier, their verification and restore state.
 */
export function ArchivePage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<Filter>({ domain: "", period: "", status: "" });
  const [filter, setFilter] = useState<Filter>(draft);
  const [errors, setErrors] = useState<FilterErrors>({});
  const state = useLoad((signal) => listArchivePartitions(filter, undefined, signal), `${filter.domain}/${filter.period}/${filter.status}`);

  function submit(event: FormEvent) {
    event.preventDefault();
    const domain = draft.domain.trim();
    const period = draft.period.trim();
    const next: FilterErrors = {};
    if (domain !== "" && !ARCHIVE_NAME.test(domain)) next.domain = "Use a domain name such as ledger or inventory.";
    if (period !== "" && !ARCHIVE_PERIOD.test(period)) next.period = "Use a month as YYYY-MM, for example 2024-01.";
    setErrors(next);
    if (Object.keys(next).length > 0) return;
    setFilter({ domain, period, status: draft.status });
  }

  const rows = state.data ?? [];
  const mismatched = rows.filter((p) => p.status === "PENDING" && p.contentHashOltp !== "" && p.contentHashOltp !== p.contentHashArchive);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Archive partitions"
        description="Closed partitions older than their residence period move to the archive tier. A partition is dropped from the database only after its row count and content hash match the archive copy."
      />
      <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="Archive filter">
        <FormField id="archive-domain" label="Domain" hint="Leave empty for every domain." error={errors.domain} optional>
          {(control) => (
            <Input {...control} className={styles.mono} value={draft.domain} invalid={Boolean(errors.domain)} onChange={(e) => setDraft((d) => ({ ...d, domain: e.target.value }))} />
          )}
        </FormField>
        <FormField id="archive-period" label="Period" hint="Month as YYYY-MM." error={errors.period} optional>
          {(control) => (
            <Input
              {...control}
              className={styles.mono}
              placeholder="2024-01"
              inputMode="numeric"
              value={draft.period}
              invalid={Boolean(errors.period)}
              onChange={(e) => setDraft((d) => ({ ...d, period: e.target.value }))}
            />
          )}
        </FormField>
        <FormField id="archive-status" label="Status">
          {(control) => (
            <Select {...control} value={draft.status} onChange={(e) => setDraft((d) => ({ ...d, status: e.target.value }))}>
              <option value="">Any status</option>
              {ARCHIVE_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {enumLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Show partitions</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load archive partitions" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          {mismatched.length > 0 ? (
            <Alert tone="warning" title={`${mismatched.length} partitions failed verification`}>
              {mismatched.map((p) => `${p.domain}.${p.tableName} ${p.period}: counts ${p.rowsOltp} vs ${p.rowsArchive}.`).join(" ")} Both copies are kept until the
              incident is resolved.
            </Alert>
          ) : null}
          <DataTable
            label="Archive partitions"
            columns={columns}
            rows={rows}
            getRowKey={(p) => p.partitionId}
            loading={state.loading}
            empty={{ title: "No archive partitions", description: "The weekly archive job has not moved a partition that matches the filter." }}
            onRowOpen={(p) => navigate(`/analytics/archive/${encodeURIComponent(p.partitionId)}`)}
          />
        </>
      )}
    </div>
  );
}

function Entry({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

/** One partition: counts, hashes, legal-hold state and the restore action. */
export function ArchivePartitionPage() {
  const { id = "" } = useParams();
  const [restored, setRestored] = useState<ArchivePartition | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const state = useLoad((signal) => getArchivePartition(id, undefined, signal), `archive/${id}`);

  async function restore() {
    setConfirming(false);
    setBusy(true);
    setFailure(null);
    try {
      setRestored(await restoreArchivePartition(id));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Archive partition" />
        <Card>
          <div className={styles.loading}>
            <Skeleton width="1/2" />
            <Skeleton width="3/4" />
          </div>
        </Card>
        <p role="status" className={styles.muted}>
          Loading partition…
        </p>
      </div>
    );
  }
  if (state.error?.code === "ANA-0112") {
    return (
      <Card>
        <EmptyState
          headingLevel={2}
          icon="database"
          title="We couldn't find that partition"
          description="No archive partition has this id in your tenant."
          action={
            <Link className={buttonClassName("secondary")} to="/analytics/archive">
              Back to archive partitions
            </Link>
          }
        />
      </Card>
    );
  }
  if (state.error || !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Archive partition" />
        <Alert tone="danger" title="Could not load the partition" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error?.message} ({state.error?.code})
        </Alert>
      </div>
    );
  }
  const p = restored ?? state.data;
  const restorable = p.status === "DROPPED" || (p.status === "RESTORED" && p.restoreUntil !== undefined && new Date(p.restoreUntil).getTime() <= Date.now());

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${p.domain}.${p.tableName} ${p.period}`}
        description="Archived records stay readable from their record and in the explorer with source archive."
        meta={<PartitionStatus partition={p} />}
        actions={
          restorable ? (
            <Button variant="primary" loading={busy} onClick={() => setConfirming(true)}>
              Restore for {RESTORE_DAYS} days
            </Button>
          ) : null
        }
      />
      <p role="status" className={styles.muted}>
        {restored ? `Partition restored until ${dateTime(restored.restoreUntil)}.` : ""}
      </p>
      {failure ? (
        <Alert tone="danger" title="The partition was not restored" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {p.status === "RESTORED" && !restorable ? (
        <Alert tone="info" title={`Restored until ${dateTime(p.restoreUntil)}`}>
          The records are in a side table until then. After that the archive copy is again the only one.
        </Alert>
      ) : null}
      {p.legalHold ? (
        <Alert tone="warning" title="Under legal hold">
          Records under legal hold are never moved or dropped.
        </Alert>
      ) : null}
      <Card title="Verification">
        <dl className={styles.details}>
          <Entry term="Partition id">
            <span className={styles.mono}>{p.partitionId}</span>
          </Entry>
          <Entry term="Rows in the database">{count(p.rowsOltp)}</Entry>
          <Entry term="Rows in the archive">{count(p.rowsArchive)}</Entry>
          <Entry term="Database content hash">
            <span className={styles.mono}>{p.contentHashOltp || "Not computed yet"}</span>
          </Entry>
          <Entry term="Archive content hash">
            <span className={styles.mono}>{p.contentHashArchive || "Not computed yet"}</span>
          </Entry>
          <Entry term="Moved">{dateTime(p.movedAt) || "Not moved yet"}</Entry>
          <Entry term="Open fiscal period">{p.openPeriod ? "Yes" : "No"}</Entry>
        </dl>
      </Card>
      {confirming ? (
        <ConfirmDialog
          title="Restore this partition?"
          description={`The archived rows of ${p.domain}.${p.tableName} ${p.period} are copied into a side table for ${RESTORE_DAYS} days.`}
          confirmLabel="Restore partition"
          cancelLabel="Cancel"
          onConfirm={() => void restore()}
          onCancel={() => setConfirming(false)}
        />
      ) : null}
    </div>
  );
}
