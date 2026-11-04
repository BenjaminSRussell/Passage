import { useState } from "react";
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
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import {
  calculatePay,
  describeProblem,
  getWorker,
  listRegistrations,
  listWorkerDays,
  MAX_PAY_RANGE_DAYS,
  type Problem,
  type Registration,
  type Worker,
  type WorkerDay,
} from "./api/client";
import { addDays, browserZone, dash, daysBetween, fixed, formatDateTime, formatHours, formatOffset, today, REGISTRATION_FLAG_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { dayColumns } from "./dayColumns";
import { WorkerHomeResource } from "./WorkerHomeResource";
import { WorkerMaintenance } from "./WorkerMaintenance";
import { WorkerPaySetup } from "./WorkerPaySetup";

/** Longest range GET registrations accepts, in days. */
const MAX_LIST_DAYS = 31;

interface Detail {
  worker: Worker;
  registrations: Registration[];
  days: WorkerDay[];
}

const registrationColumns: DataTableColumn<Registration>[] = [
  { key: "kind", header: "Kind" },
  { key: "startedAt", header: "Started", mono: true, render: (row) => formatDateTime(row.startedAt) },
  { key: "stoppedAt", header: "Stopped", mono: true, render: (row) => formatDateTime(row.stoppedAt) },
  { key: "hours", header: "Hours", align: "end", render: (row) => formatHours(row.hours) },
  { key: "qtyGood", header: "Good", align: "end", render: (row) => fixed(row.qtyGood, 2) },
  { key: "qtyScrap", header: "Scrap", align: "end", render: (row) => fixed(row.qtyScrap, 2) },
  {
    key: "bundleId",
    header: "Bundle",
    render: (row) => (row.bundleId ? <Link to={`/shopfloor/bundles/${row.bundleId}`}>View bundle</Link> : dash),
  },
  {
    key: "correct",
    header: "Action",
    render: (row) =>
      row.bundleId ? (
        dash
      ) : (
        <Link to={`/shopfloor/registrations/${row.id}/correct`} state={{ registration: row }} aria-label={`Correct ${row.kind} at ${formatDateTime(row.startedAt)}`}>
          Correct
        </Link>
      ),
  },
  {
    key: "offline",
    header: "Source",
    render: (row) => (
      <span className={styles.flags}>
        {row.offline ? <Badge tone="info">Offline replay</Badge> : "Online"}
        {row.clockOffsetSeconds !== null ? <Badge tone="warning">{`Clock corrected ${formatOffset(row.clockOffsetSeconds)}`}</Badge> : null}
        {(row.flags ?? []).map((flag) => (
          <Badge key={flag} tone="warning">
            {REGISTRATION_FLAG_LABELS[flag] ?? flag}
          </Badge>
        ))}
      </span>
    ),
  },
];


/** A worker, their registrations and their calculated worker-days for a date range. */
export function WorkerDetailPage() {
  const { id = "" } = useParams();
  const [from, setFrom] = useState(addDays(today(), -6));
  const [to, setTo] = useState(today());
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const span = daysBetween(from, to);
  const rangeError =
    span === null ? "Enter both dates." : span < 1 ? "To must not be before From." : span > MAX_LIST_DAYS ? `Show at most ${MAX_LIST_DAYS} days.` : null;
  const zone = browserZone();

  const state = useLoad<Detail>(
    async (signal) => {
      const worker = await getWorker(id, signal);
      if (rangeError) return { worker, registrations: [], days: [] };
      const [registrations, days] = await Promise.all([
        listRegistrations({ workerId: id, from, to, timeZone: zone }, signal),
        listWorkerDays({ workerId: id, from, to }, signal),
      ]);
      return { worker, registrations, days };
    },
    `${id}|${from}|${to}`,
  );

  async function calculate() {
    setNotice(null);
    setFailure(null);
    if (span === null || span < 1 || span > MAX_PAY_RANGE_DAYS) {
      setFailure({ code: "COM-0001", message: `Pay is calculated for at most ${MAX_PAY_RANGE_DAYS} days at a time.`, fields: {} });
      return;
    }
    setBusy(true);
    try {
      const res = await calculatePay({ workerId: id, from, to, timeZone: zone });
      setNotice(`Calculated ${res.days.length} ${res.days.length === 1 ? "day" : "days"} in ${res.timeZone}.`);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Worker" />
        <Alert tone="danger" title="Could not load the worker" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Worker" />
        <span className="visually-hidden">Loading the worker…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { worker, registrations, days } = state.data;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={worker.name}
        description={worker.badgeId ? `Badge ${worker.badgeId}` : "No badge assigned"}
        meta={<StatusPill status={worker.active ? "active" : "inactive"} />}
        actions={
          <>
            <Button variant="primary" loading={busy} onClick={calculate}>
              Calculate pay
            </Button>
            <Link className={buttonClassName("secondary")} to={`/shopfloor/workers/${id}/revise`}>
              Revise pay
            </Link>
            <Link className={buttonClassName("secondary")} to="/shopfloor/time">
              Time approval
            </Link>
          </>
        }
      />
      <Card title="Worker">
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Cost rate</dt>
            <dd className={styles.value}>{`${fixed(worker.costRate, 2)} ${worker.currency} per hour`}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Sign-in user</dt>
            <dd className={styles.value}>{worker.userId ?? dash}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Party id</dt>
            <dd className={`${styles.value} ${styles.mono}`}>{worker.partyId}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Created</dt>
            <dd className={styles.value}>{formatDateTime(worker.createdAt)}</dd>
          </div>
        </dl>
      </Card>
      <WorkerMaintenance key={`edit-${worker.id}`} worker={worker} onSaved={state.reload} />
      <WorkerPaySetup key={worker.id} worker={worker} onSaved={state.reload} />
      <WorkerHomeResource key={`home-${worker.id}`} worker={worker} onSaved={state.reload} />
      <div className={styles.toolbar}>
        <FormField id="worker-from" label="From">
          {(control) => <Input {...control} type="date" value={from} onChange={(event) => setFrom(event.target.value)} />}
        </FormField>
        <FormField id="worker-to" label="To" error={rangeError ?? undefined}>
          {(control) => <Input {...control} type="date" value={to} onChange={(event) => setTo(event.target.value)} invalid={Boolean(rangeError)} />}
        </FormField>
      </div>
      <p className={styles.note}>{`Registrations are shown by local day in ${zone}. Pay is calculated in the same zone, for at most ${MAX_PAY_RANGE_DAYS} days at a time.`}</p>
      {notice ? (
        <Alert tone="success" title="Pay calculated" live="status">
          {notice}
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not calculate pay" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <Card title="Worker-days">
        <DataTable
          label="Worker-days"
          columns={dayColumns}
          rows={days}
          getRowKey={(row) => row.workDate}
          loading={state.loading}
          empty={{ title: "No calculated days in this range", description: "Calculate pay to turn the registrations into pay lines." }}
        />
      </Card>
      <Card title="Registrations">
        <DataTable
          label="Registrations"
          columns={registrationColumns}
          rows={registrations}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No registrations in this range", description: "Clock-ins, clock-outs and job registrations from the kiosk appear here." }}
        />
      </Card>
    </div>
  );
}
