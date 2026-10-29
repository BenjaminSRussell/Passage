import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  expirePermits,
  getPermit,
  listPermits,
  listPermitTypes,
  listWorkOrders,
  newKey,
  recordLock,
  requestPermit,
  transitionPermit,
  type IsolationPoint,
  type LockAction,
  type LotoLock,
  type NewPermit,
  type Permit,
  type PermitDetail,
  type PermitStatus,
  type PermitType,
  type Problem,
  type StatusChange,
  type WorkOrder,
} from "./api/client";
import { dash, minuteOf, nextPermitStatuses, permitLockable, sentence, utcFromLocalInput } from "./format";
import { Entry, Failure, PagedTable, State, StatusFilter } from "./parts";
import styles from "./pages.module.css";
import { useLoad, usePaged } from "./useLoad";

const STATUSES: PermitStatus[] = ["REQUESTED", "APPROVED", "ISSUED", "SUSPENDED", "CLOSED", "CANCELED"];
const OPEN_WORK = ["CREATED", "SCHEDULED", "RELEASED", "IN_PROGRESS", "ON_HOLD"];

const transitionLabel: Record<Exclude<PermitStatus, "REQUESTED">, string> = {
  APPROVED: "Approve",
  ISSUED: "Issue",
  SUSPENDED: "Suspend",
  CLOSED: "Close",
  CANCELED: "Cancel permit",
};

const lockLabel: Record<LockAction, string> = { APPLY: "Apply my lock", VERIFY: "Verify zero energy", REMOVE: "Remove my lock" };
const lockDone: Record<LockAction, string> = { APPLY: "applied", VERIFY: "verified", REMOVE: "removed" };

/** Messages for the permit rule codes (8.44.18), so the user sees what to do next. */
function permitProblemTitle(problem: Problem): string {
  switch (problem.code) {
    case "conflict_version":
      return "Someone else changed this permit. It has been reloaded; try again.";
    case "EAM-PMT-002":
      return "Every isolation point needs a verified lock first";
    case "EAM-PMT-003":
      return "Remove every lock before closing";
    case "EAM-PMT-004":
      return "The permit's validity has ended";
    case "EAM-PMT-005":
      return "Separation of duties";
    default:
      return "Could not update the permit";
  }
}

/** The permit register (8.44.12): every permit with its type, work order, validity and open locks. */
export function PermitsPage() {
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const workOrder = params.get("workOrder") ?? "";
  const state = usePaged((cursor, signal) => listPermits({ status, workOrder, cursor }, signal), `permits|${status}|${workOrder}`);
  const [checking, setChecking] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function runExpiry() {
    setChecking(true);
    setNotice(null);
    setFailure(null);
    try {
      const suspended = await expirePermits();
      const running = suspended.filter((p) => p.workOrderInProgress).length;
      setNotice(
        suspended.length === 0
          ? "No issued permit is past its validity."
          : `${suspended.length} expired permit${suspended.length === 1 ? " was" : "s were"} suspended${running ? `; ${running} job${running === 1 ? " is" : "s are"} still in progress without a valid permit` : ""}.`,
      );
      state.reload();
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setChecking(false);
    }
  }

  const columns: DataTableColumn<Permit>[] = [
    { key: "number", header: "Permit", mono: true, render: (row) => <Link to={`/eam/permits/${row.id}`}>{row.number}</Link> },
    { key: "permitTypeCode", header: "Type", mono: true },
    { key: "workOrderId", header: "Work order", render: (row) => <Link to={`/eam/work-orders/${row.workOrderId}`}>Open work order</Link> },
    { key: "validFrom", header: "Valid from (UTC)", mono: true, render: (row) => minuteOf(row.validFrom) },
    { key: "validTo", header: "Valid to (UTC)", mono: true, render: (row) => minuteOf(row.validTo) },
    { key: "status", header: "Status", render: (row) => <State value={row.status} /> },
    { key: "locksOpen", header: "Locks open", align: "end" },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Permits to work"
        description="A job whose type requires a permit cannot start until that permit is issued and valid. Isolation permits need every isolation point locked and verified first."
        actions={
          <div className={styles.actions}>
            <Button loading={checking} onClick={runExpiry}>
              Suspend expired permits
            </Button>
            <Link className={buttonClassName("primary")} to={workOrder ? `/eam/permits/new?workOrder=${workOrder}` : "/eam/permits/new"}>
              Request permit
            </Link>
          </div>
        }
      />
      <div className={styles.toolbar}>
        <StatusFilter
          id="permit-status"
          value={status}
          options={STATUSES}
          onChange={(next) => {
            const p: Record<string, string> = {};
            if (next) p.status = next;
            if (workOrder) p.workOrder = workOrder;
            setParams(p);
          }}
        />
        {workOrder ? (
          <p className={styles.note}>
            Showing one work order's permits. <Link to="/eam/permits">Show every permit</Link>
          </p>
        ) : null}
      </div>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? <Failure title="Could not run the expiry check" problem={failure} /> : null}
      <PagedTable
        label="Permits to work"
        noun="permits"
        columns={columns}
        state={state}
        empty={
          status || workOrder
            ? { title: "No permits match", description: "Choose another status, or request a permit for the work order." }
            : { title: "No permits yet", description: "Request a permit for a work order whose job needs one." }
        }
      />
    </div>
  );
}

interface PermitValues {
  workOrderId: string;
  permitTypeCode: string;
  validFrom: string;
  validTo: string;
}

/** The checks the service makes on a permit request (BR-44.11), and the request the form sends. */
export function validatePermit(values: PermitValues, types: PermitType[]): { errors: Partial<Record<keyof PermitValues, string>>; input?: NewPermit } {
  const errors: Partial<Record<keyof PermitValues, string>> = {};
  if (!values.workOrderId) errors.workOrderId = "Choose the work order.";
  const type = types.find((t) => t.code === values.permitTypeCode);
  if (!type) errors.permitTypeCode = "Choose the permit type.";
  const from = utcFromLocalInput(values.validFrom);
  const to = utcFromLocalInput(values.validTo);
  if (!from) errors.validFrom = "Enter when the permit starts.";
  if (!to) errors.validTo = "Enter when the permit ends.";
  else if (from) {
    const span = Date.parse(to) - Date.parse(from);
    if (span <= 0) errors.validTo = "The permit must end after it starts.";
    else if (type && span > type.maxValidityHours * 3_600_000) errors.validTo = `A ${type.name} permit is valid for at most ${type.maxValidityHours} hours.`;
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { workOrderId: values.workOrderId, permitTypeCode: values.permitTypeCode, validFrom: from!, validTo: to! } };
}

/** Request a permit for an open work order (EamPlanner). */
export function NewPermitPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const types = useLoad((signal) => listPermitTypes(signal), "permit-types");
  const orders = useLoad((signal) => listWorkOrders({ limit: 200 }, signal), "permit-work-orders");
  const [values, setValues] = useState<PermitValues>({ workOrderId: params.get("workOrder") ?? "", permitTypeCode: "", validFrom: "", validTo: "" });
  const [errors, setErrors] = useState<Partial<Record<keyof PermitValues, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<PermitValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePermit(values, types.data ?? []);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const permit = await requestPermit(input, key.current);
      navigate(`/eam/permits/${permit.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  const open: WorkOrder[] = (orders.data?.items ?? []).filter((wo) => OPEN_WORK.includes(wo.status));
  const chosenType = types.data?.find((t) => t.code === values.permitTypeCode);
  const problem = types.error ?? orders.error;

  return (
    <div className={styles.stack}>
      <PageHeader title="Request a permit" description="The permit starts as Requested. Someone other than you approves it, and a permit issuer issues it." />
      <Card>
        {problem ? (
          <Failure
            title="Could not load the permit types and work orders"
            problem={problem}
            retry={() => {
              types.reload();
              orders.reload();
            }}
          />
        ) : types.loading || orders.loading || !types.data || !orders.data ? (
          <div aria-busy="true">
            <Skeleton height="control" />
          </div>
        ) : types.data.length === 0 ? (
          <Alert tone="info" title="No permit types yet">
            Permits are requested by type. <Link to="/eam/permit-types">Set up permit types</Link> first.
          </Alert>
        ) : open.length === 0 ? (
          <Alert tone="info" title="No open work orders">
            Permits are requested for an open work order. <Link to="/eam/work-orders/new">Create a work order</Link> first.
          </Alert>
        ) : (
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField id="permit-wo" label="Work order" error={errors.workOrderId}>
              {(control) => (
                <Select {...control} value={values.workOrderId} invalid={Boolean(errors.workOrderId)} onChange={(e) => update({ workOrderId: e.target.value })}>
                  <option value="">Choose a work order</option>
                  {open.map((wo) => (
                    <option key={wo.id} value={wo.id}>
                      {wo.number} {wo.jobType ? `(${wo.jobType})` : ""} {sentence(wo.status)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField
              id="permit-type"
              label="Permit type"
              hint={chosenType ? `Valid for at most ${chosenType.maxValidityHours} hours. Approved by: ${chosenType.approverRole}.` : undefined}
              error={errors.permitTypeCode}
            >
              {(control) => (
                <Select {...control} value={values.permitTypeCode} invalid={Boolean(errors.permitTypeCode)} onChange={(e) => update({ permitTypeCode: e.target.value })}>
                  <option value="">Choose a permit type</option>
                  {types.data!.map((t) => (
                    <option key={t.code} value={t.code}>
                      {t.name} ({t.code})
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="permit-from" label="Valid from (UTC)" error={errors.validFrom}>
              {(control) => <Input {...control} type="datetime-local" value={values.validFrom} invalid={Boolean(errors.validFrom)} onChange={(e) => update({ validFrom: e.target.value })} />}
            </FormField>
            <FormField id="permit-to" label="Valid to (UTC)" error={errors.validTo}>
              {(control) => <Input {...control} type="datetime-local" value={values.validTo} invalid={Boolean(errors.validTo)} onChange={(e) => update({ validTo: e.target.value })} />}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Failure title="Could not request the permit" problem={failure} />
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Request permit
              </Button>
              <Link className={buttonClassName("ghost")} to="/eam/permits">
                Cancel
              </Link>
            </div>
          </form>
        )}
      </Card>
    </div>
  );
}

const historyColumns: DataTableColumn<StatusChange & { id: string }>[] = [
  { key: "at", header: "When (UTC)", mono: true, render: (row) => minuteOf(row.at) },
  { key: "from", header: "From", render: (row) => (row.from ? sentence(row.from) : dash) },
  { key: "to", header: "To", render: (row) => sentence(row.to) },
  { key: "actor", header: "By", mono: true, render: (row) => row.actor ?? dash },
];

/** One permit: lifecycle actions (BR-44.11), the asset's isolation points with personal locks, and its audit history. */
export function PermitDetailPage() {
  const { id = "" } = useParams();
  const [round, setRound] = useState(0);
  const state = useLoad((signal) => getPermit(id, signal), `${id}|${round}`);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const keys = useRef(new Map<string, string>());

  // One key per attempted action, kept until it succeeds, so a retry applies it once.
  function keyFor(slot: string): string {
    const key = keys.current.get(slot) ?? newKey();
    keys.current.set(slot, key);
    return key;
  }

  async function act(slot: string, run: (key: string) => Promise<string>) {
    setBusy(slot);
    setNotice(null);
    setFailure(null);
    try {
      setNotice(await run(keyFor(slot)));
      keys.current.delete(slot);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(null);
      setRound((n) => n + 1);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Permit" />
        {state.error.code === "EAM-NF-001" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="We couldn't find this permit"
              description="It may belong to another tenant, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/eam/permits">
                  Back to permits
                </Link>
              }
            />
          </Card>
        ) : (
          <Failure title="Could not load the permit" problem={state.error} retry={state.reload} />
        )}
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Permit" />
        <Card>
          <Skeleton height="control" />
        </Card>
      </div>
    );
  }

  const detail: PermitDetail = state.data;
  const permit = detail.permit;
  const next = nextPermitStatuses(permit.status);
  const lockable = permitLockable(permit.status);

  return (
    <div className={styles.stack}>
      <PageHeader title={`Permit ${permit.number}`} description={`${permit.permitTypeCode} · valid ${minuteOf(permit.validFrom)} to ${minuteOf(permit.validTo)} UTC`} meta={<State value={permit.status} />} />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? <Failure title={permitProblemTitle(failure)} problem={failure} /> : null}
      <Card title="Details">
        <dl className={styles.details}>
          <Entry term="Work order">
            <Link to={`/eam/work-orders/${permit.workOrderId}`}>Open work order</Link>
          </Entry>
          <Entry term="Asset">
            <Link to={`/eam/assets/${permit.assetId}`}>Open asset</Link>
          </Entry>
          <Entry term="Requested by" mono>
            {permit.requestedBy || dash}
          </Entry>
          <Entry term="Approved by" mono>
            {permit.approvedBy ?? dash}
          </Entry>
          <Entry term="Issued by" mono>
            {permit.issuedBy ?? dash}
          </Entry>
          <Entry term="Closed by" mono>
            {permit.closedBy ?? dash}
          </Entry>
          <Entry term="Locks open">{permit.locksOpen}</Entry>
          <Entry term="Isolation points without a verified lock">{detail.unlockedPoints}</Entry>
        </dl>
      </Card>
      <Card title="Lifecycle">
        <div className={styles.stack}>
          <p className={styles.note}>
            The approver must be someone other than the requester. An isolation permit is issued only when every isolation point has a verified lock held by
            someone besides the issuer. The permit closes when every lock is removed and the work order is not in progress.
          </p>
          {next.length === 0 ? (
            <p className={styles.note}>This permit is {sentence(permit.status).toLowerCase()}; it has no further steps.</p>
          ) : (
            <div className={styles.actions}>
              {next.map((to) => (
                <Button
                  key={to}
                  variant={to === "CANCELED" ? "danger" : to === next[0] ? "primary" : "secondary"}
                  loading={busy === `${to}|${permit.version}`}
                  onClick={() =>
                    act(`${to}|${permit.version}`, async (key) => {
                      const out = await transitionPermit(permit.id, to, permit.version, key);
                      return `Permit ${out.number} is now ${sentence(out.status).toLowerCase()}.`;
                    })
                  }
                >
                  {transitionLabel[to]}
                </Button>
              ))}
            </div>
          )}
        </div>
      </Card>
      <LockCard detail={detail} lockable={lockable} busy={busy} onAct={act} />
      <Card title="History">
        <DataTable
          label="Permit history"
          columns={historyColumns}
          rows={detail.history.map((h, i) => ({ ...h, id: `${i}` }))}
          getRowKey={(row) => row.id}
          empty={{ title: "No transitions yet" }}
        />
      </Card>
    </div>
  );
}

/** The asset's isolation points with the caller's lock actions, and every personal lock on the permit. */
function LockCard({
  detail,
  lockable,
  busy,
  onAct,
}: {
  detail: PermitDetail;
  lockable: boolean;
  busy: string | null;
  onAct(slot: string, run: (key: string) => Promise<string>): void;
}) {
  const permit = detail.permit;
  const tagOf = (pointId: string) => detail.isolationPoints.find((p) => p.id === pointId)?.tagCode ?? pointId;
  const pointLocks = (point: IsolationPoint) => detail.locks.filter((l) => l.isolationPointId === point.id && l.state !== "REMOVED");

  const pointColumns: DataTableColumn<IsolationPoint>[] = [
    { key: "tagCode", header: "Tag", mono: true },
    { key: "energyType", header: "Energy", render: (row) => sentence(row.energyType) },
    { key: "location", header: "Location" },
    { key: "method", header: "Isolation method" },
    { key: "verificationStep", header: "Verification step" },
    {
      key: "locks",
      header: "Open locks",
      render: (row) => {
        const open = pointLocks(row);
        if (open.length === 0) return "None";
        const verified = open.filter((l) => l.state === "VERIFIED").length;
        return `${open.length} (${verified} verified)`;
      },
    },
    {
      key: "action",
      header: "Your lock",
      render: (row) =>
        lockable ? (
          <div className={styles.actions}>
            {(["APPLY", "VERIFY", "REMOVE"] as const).map((action) => (
              <Button
                key={action}
                size="sm"
                variant={action === "APPLY" ? "secondary" : "ghost"}
                loading={busy === `${action}|${row.id}`}
                onClick={() =>
                  onAct(`${action}|${row.id}`, async (key) => {
                    const lock = await recordLock(permit.id, action, row.id, key);
                    return `Lock ${lock.lockNumber} on ${row.tagCode} ${lockDone[action]}.`;
                  })
                }
              >
                {lockLabel[action]}
              </Button>
            ))}
          </div>
        ) : (
          dash
        ),
    },
  ];

  const lockColumns: DataTableColumn<LotoLock>[] = [
    { key: "lockNumber", header: "Lock", mono: true },
    { key: "isolationPointId", header: "Point", mono: true, render: (row) => tagOf(row.isolationPointId) },
    { key: "technicianId", header: "Technician", mono: true },
    { key: "state", header: "State", render: (row) => <State value={row.state} /> },
    { key: "appliedAt", header: "Applied (UTC)", mono: true, render: (row) => minuteOf(row.appliedAt) },
    { key: "verifiedAt", header: "Verified (UTC)", mono: true, render: (row) => minuteOf(row.verifiedAt) },
    { key: "removedAt", header: "Removed (UTC)", mono: true, render: (row) => minuteOf(row.removedAt) },
  ];

  return (
    <Card title="Isolation and personal locks">
      <div className={styles.stack}>
        <p className={styles.note}>
          Each technician working the job applies a personal lock on every isolation point, then verifies zero energy. The buttons act on your own lock only.
          {lockable ? "" : " Locks can be changed while the permit is approved, issued or suspended."}
        </p>
        {detail.isolationPoints.length === 0 ? (
          <Alert tone="warning" title="This asset has no isolation points">
            An isolation permit cannot be issued until the asset's isolation points are defined. <Link to={`/eam/assets/${permit.assetId}`}>Add them on the asset</Link>.
          </Alert>
        ) : (
          <DataTable label="Isolation points" columns={pointColumns} rows={detail.isolationPoints} getRowKey={(row) => row.id} />
        )}
        <DataTable label="Personal locks" columns={lockColumns} rows={detail.locks} getRowKey={(row) => row.id} empty={{ title: "No locks applied yet" }} />
      </div>
    </Card>
  );
}
