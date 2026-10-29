import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getWorkOrder,
  newKey,
  recordLabor,
  transitionWorkOrder,
  type LaborLine,
  type Problem,
  type StatusChange,
  type WorkOrderDetail,
  type WorkOrderStatus,
} from "./api/client";
import { addMoney, dash, dayOf, HOURS, minuteOf, money, nextStatuses, RATE, statusLabel, typeLabel } from "./format";
import { Entry, Failure, State } from "./parts";
import { WorkOrderPermitsCard } from "./PermitCare";
import { DowntimeCard, FaultCard } from "./WorkOrderCare";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const laborColumns: DataTableColumn<LaborLine>[] = [
  { key: "createdAt", header: "Booked", mono: true, render: (row) => minuteOf(row.createdAt) },
  { key: "hours", header: "Hours", align: "end", mono: true },
  { key: "rate", header: "Rate", align: "end", mono: true, render: (row) => money(row.rate) },
  { key: "cost", header: "Cost", align: "end", mono: true, render: (row) => money(row.cost) },
  { key: "workerId", header: "Worker", mono: true, render: (row) => row.workerId ?? dash },
];

const historyColumns: DataTableColumn<StatusChange & { id: string }>[] = [
  { key: "at", header: "When", mono: true, render: (row) => minuteOf(row.at) },
  { key: "from", header: "From", render: (row) => statusLabel[row.from as WorkOrderStatus] ?? row.from },
  { key: "to", header: "To", render: (row) => statusLabel[row.to as WorkOrderStatus] ?? row.to },
  { key: "actor", header: "By", render: (row) => row.actor ?? dash },
];

/** One work order: lifecycle actions (BR-44.3), completion with downtime, labor (FR-44.10) and its status history. */
export function WorkOrderDetailPage() {
  const { id = "" } = useParams();
  const [notice, setNotice] = useState<string | null>(null);
  const state = useLoad((signal) => getWorkOrder(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Work order" />
        {state.error.code === "EAM-NF-001" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="We couldn't find this work order"
              description="It may belong to another tenant, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/eam/work-orders">
                  Back to work orders
                </Link>
              }
            />
          </Card>
        ) : (
          <Failure title="Could not load the work order" problem={state.error} retry={state.reload} />
        )}
      </div>
    );
  }
  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Work order" />
        <Skeleton width="3/4" />
        <Skeleton width="1/2" />
      </div>
    );
  }
  const wo = state.data!;
  const open = !["COMPLETED", "CLOSED", "CANCELED"].includes(wo.status);
  const saved = (text: string) => {
    setNotice(text);
    state.reload();
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Work order ${wo.number}`}
        description={`${typeLabel[wo.workOrderType] ?? wo.workOrderType} · priority ${wo.priority}`}
        meta={<State value={wo.status} />}
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Card title="Details">
        <dl className={styles.details}>
          <Entry term="Asset">
            <Link to={`/eam/assets/${wo.assetId}`}>Open asset</Link>
          </Entry>
          <Entry term="Job type" mono>
            {wo.jobType || dash}
          </Entry>
          <Entry term="Due" mono>
            {dayOf(wo.dueAt)}
          </Entry>
          <Entry term="Actual start" mono>
            {minuteOf(wo.actualStart)}
          </Entry>
          <Entry term="Actual end" mono>
            {minuteOf(wo.actualEnd)}
          </Entry>
          <Entry term="Downtime">{wo.downtimeMinutes} min</Entry>
          <Entry term="Labor cost" mono>
            {money(wo.laborCost)} {wo.currency}
          </Entry>
          <Entry term="Parts cost" mono>
            {money(wo.partsCost)} {wo.currency}
          </Entry>
          <Entry term="Total cost" mono>
            {addMoney(wo.partsCost, wo.laborCost)} {wo.currency}
          </Entry>
          <Entry term="Warranty">{wo.warrantyCovered ? "Covered by a vendor warranty; completion raises a claim" : "Not covered"}</Entry>
          <Entry term="Origin">
            {wo.planLineId ? "Maintenance plan" : wo.requestId ? "Maintenance request" : "Raised by hand"}
          </Entry>
        </dl>
      </Card>
      <StatusCard wo={wo} onSaved={saved} />
      <WorkOrderPermitsCard wo={wo} />
      <FaultCard wo={wo} onSaved={saved} />
      <DowntimeCard wo={wo} onSaved={saved} />
      <Card title="Labor">
        <div className={styles.stack}>
          <DataTable label="Labor" columns={laborColumns} rows={wo.labor} getRowKey={(row) => row.id} empty={{ title: "No labor booked yet" }} />
          {open ? <LaborForm workOrderId={wo.id} onSaved={saved} /> : <p className={styles.note}>Labor can only be booked on an open work order.</p>}
        </div>
      </Card>
      <Card title="Status history">
        <DataTable
          label="Status history"
          columns={historyColumns}
          rows={wo.history.map((h, i) => ({ ...h, id: `${i}` }))}
          getRowKey={(row) => row.id}
          empty={{ title: "No status changes yet" }}
        />
      </Card>
    </div>
  );
}

function StatusCard({ wo, onSaved }: { wo: WorkOrderDetail; onSaved(text: string): void }) {
  const [completing, setCompleting] = useState(false);
  const [downtime, setDowntime] = useState(String(wo.downtimeMinutes));
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<WorkOrderStatus | null>(null);
  const key = useRef(newKey());
  const next = nextStatuses(wo.status, wo.priority);

  async function move(to: WorkOrderStatus, downtimeMinutes?: number) {
    setFailure(null);
    setBusy(to);
    try {
      await transitionWorkOrder(wo.id, downtimeMinutes === undefined ? { to } : { to, downtimeMinutes }, wo.version, key.current);
      key.current = newKey();
      setCompleting(false);
      onSaved(`Work order moved to ${statusLabel[to].toLowerCase()}.`);
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      // A rejected transition did not happen, so the next attempt is a new
      // action. A network failure keeps the key: a retry may replay.
      if (problem.code !== "COM-0001") key.current = newKey();
    } finally {
      setBusy(null);
    }
  }

  function complete(event: FormEvent) {
    event.preventDefault();
    const n = Number(downtime);
    if (!/^\d{1,6}$/.test(downtime.trim()) || !Number.isInteger(n)) {
      setError("Enter the downtime in whole minutes, 0 if the asset kept running.");
      return;
    }
    setError(null);
    void move("COMPLETED", n);
  }

  return (
    <Card title="Status">
      <div className={styles.stack}>
        {next.length === 0 ? (
          <p className={styles.note}>This work order is {statusLabel[wo.status].toLowerCase()}. No further status changes are possible.</p>
        ) : (
          <div className={styles.actions} role="group" aria-label="Status actions">
            {next
              .filter((to) => to !== "COMPLETED")
              .map((to) => (
                <Button key={to} variant={to === "CANCELED" ? "danger" : "secondary"} loading={busy === to} onClick={() => move(to)}>
                  {to === "CANCELED" ? "Cancel work order" : `Move to ${statusLabel[to].toLowerCase()}`}
                </Button>
              ))}
            {next.includes("COMPLETED") ? (
              <Button
                variant="primary"
                onClick={() => {
                  setDowntime(String(wo.downtimeMinutes));
                  setCompleting(true);
                }}
              >
                Complete
              </Button>
            ) : null}
          </div>
        )}
        {completing ? (
          <form className={styles.form} onSubmit={complete} noValidate aria-label="Complete work order">
            <FormField id="wo-downtime" label="Downtime in minutes" hint="How long the asset was out of service, at least the recorded downtime windows. It feeds availability and MTBF." error={error ?? undefined}>
              {(control) => <Input {...control} inputMode="numeric" value={downtime} invalid={Boolean(error)} onChange={(e) => setDowntime(e.target.value)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "COMPLETED"}>
                Complete work order
              </Button>
              <Button variant="ghost" onClick={() => setCompleting(false)}>
                Back
              </Button>
            </div>
          </form>
        ) : null}
        {failure ? (
          <Failure
            title={
              failure.code === "conflict_version"
                ? "Someone else changed this work order. Reload it and try again."
                : failure.code === "EAM-FLT-001"
                  ? "Register the fault first"
                  : "Could not change the status"
            }
            problem={failure}
          />
        ) : null}
      </div>
    </Card>
  );
}

function LaborForm({ workOrderId, onSaved }: { workOrderId: string; onSaved(text: string): void }) {
  const [hoursValue, setHours] = useState("");
  const [rate, setRate] = useState("");
  const [errors, setErrors] = useState<{ hours?: string; rate?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: { hours?: string; rate?: string } = {};
    if (!HOURS.test(hoursValue.trim()) || Number(hoursValue) <= 0) found.hours = "Enter hours above 0, with up to 2 decimals.";
    if (!RATE.test(rate.trim())) found.rate = "Enter the hourly rate, for example 45.00.";
    setErrors(found);
    setFailure(null);
    if (found.hours || found.rate) return;
    setBusy(true);
    try {
      await recordLabor(workOrderId, hoursValue.trim(), rate.trim(), key.current);
      key.current = newKey();
      setHours("");
      onSaved(`${hoursValue.trim()} h booked.`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Book labor">
      <FormField id="labor-hours" label="Hours" error={errors.hours}>
        {(control) => <Input {...control} inputMode="decimal" value={hoursValue} invalid={Boolean(errors.hours)} onChange={(e) => { setHours(e.target.value); key.current = newKey(); }} />}
      </FormField>
      <FormField id="labor-rate" label="Hourly rate" hint="The trade cost rate in the work order currency." error={errors.rate}>
        {(control) => <Input {...control} inputMode="decimal" value={rate} invalid={Boolean(errors.rate)} onChange={(e) => { setRate(e.target.value); key.current = newKey(); }} />}
      </FormField>
      {failure ? (
        <div className={styles.formActions}>
          <Failure title="Could not book labor" problem={failure} />
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" loading={busy}>
          Book labor
        </Button>
      </div>
    </form>
  );
}
