import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createRound,
  createRoundWorkOrder,
  describeProblem,
  getRound,
  listAssets,
  listRounds,
  newKey,
  recordStopOutcome,
  type Asset,
  type MaintenanceRound,
  type NewRound,
  type Problem,
  type RoundStop,
  type StopOutcome,
  type WorkOrderDetail,
  type WorkOrderStop,
} from "./api/client";
import { CODE, CURRENCY, dash, dayOf, minuteOf, PLAN_CODE, sentence, utcFromLocalInput } from "./format";
import { Entry, Failure, PagedTable, State } from "./parts";
import styles from "./pages.module.css";
import { useLoad, usePaged } from "./useLoad";

/** "6 stops · 25 min travel". */
export function roundSummary(round: Pick<MaintenanceRound, "stops" | "travelMinutes">): string {
  const n = round.stops.length;
  return `${n} ${n === 1 ? "stop" : "stops"} · ${round.travelMinutes} min travel`;
}

const roundColumns: DataTableColumn<MaintenanceRound>[] = [
  { key: "code", header: "Round", mono: true },
  { key: "name", header: "Name" },
  { key: "stops", header: "Stops", align: "end", render: (row) => row.stops.length },
  { key: "mandatory", header: "Mandatory stops", align: "end", render: (row) => row.stops.filter((s) => s.mandatory).length },
  { key: "travelMinutes", header: "Travel", align: "end", render: (row) => `${row.travelMinutes} min` },
  { key: "active", header: "State", render: (row) => <State value={row.active ? "ACTIVE" : "INACTIVE"} /> },
];

/** Maintenance rounds (FR-44.14), newest first. */
export function RoundsPage() {
  const navigate = useNavigate();
  const state = usePaged((cursor, signal) => listRounds({ cursor }, signal), "rounds");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Maintenance rounds"
        description="Routes of assets inspected together. Schedule a round with a ROUND line on a maintenance plan; each occurrence is one work order with a job per stop."
        actions={
          <Link className={buttonClassName("primary")} to="/eam/rounds/new">
            New round
          </Link>
        }
      />
      <PagedTable
        label="Maintenance rounds"
        noun="rounds"
        columns={roundColumns}
        state={state}
        empty={{ title: "No maintenance rounds yet", description: "Define one, for example a weekly walk past six valves." }}
        onRowOpen={(row) => navigate(`/eam/rounds/${row.id}`)}
      />
    </div>
  );
}

interface StopValues {
  assetId: string;
  jobType: string;
  mandatory: boolean;
  travelMinutes: string;
}

interface RoundValues {
  code: string;
  name: string;
  stops: StopValues[];
}

type StopErrors = Partial<Record<keyof StopValues, string>>;
type RoundErrors = { code?: string; name?: string; stops: StopErrors[]; form?: string };

const emptyStop = (): StopValues => ({ assetId: "", jobType: "INSPECT", mandatory: false, travelMinutes: "0" });

/**
 * The checks the service makes on a round (EAM-RND-003), and the request the form sends.
 * The round's site is the first stop's site, and every stop must be on it.
 */
export function validateRound(values: RoundValues, assets: Pick<Asset, "id" | "siteId">[]): { errors: RoundErrors; input?: NewRound } {
  const errors: RoundErrors = { stops: values.stops.map(() => ({})) };
  let bad = false;
  const code = values.code.trim().toUpperCase();
  if (!PLAN_CODE.test(code)) {
    errors.code = "Use 2 to 30 capital letters, digits, dashes or underscores, for example WEEKLY-A.";
    bad = true;
  }
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) {
    errors.name = "Enter a name of up to 120 characters.";
    bad = true;
  }
  const siteOf = (assetId: string) => assets.find((a) => a.id === assetId)?.siteId;
  const site = siteOf(values.stops[0]?.assetId ?? "");
  const stops: RoundStop[] = values.stops.map((s, i) => {
    const e = errors.stops[i];
    if (!s.assetId) e.assetId = "Choose the asset.";
    else if (site && siteOf(s.assetId) !== site) e.assetId = "Every stop must be on the first stop's site.";
    const jobType = s.jobType.trim().toUpperCase();
    if (!CODE.test(jobType)) e.jobType = "Use the job type code, for example INSPECT.";
    const travel = s.travelMinutes.trim();
    if (!/^\d{1,3}$/.test(travel) || Number(travel) > 480) e.travelMinutes = "Enter 0 to 480 minutes.";
    if (Object.keys(e).length > 0) bad = true;
    return { sequence: i + 1, assetId: s.assetId, jobType, mandatory: s.mandatory, travelMinutes: Number(travel) };
  });
  if (values.stops.length === 0 || values.stops.length > 50) {
    errors.form = "A round needs 1 to 50 stops.";
    bad = true;
  }
  if (bad || !site) return { errors };
  return { errors, input: { code, name, siteId: site, stops } };
}

function roundProblemTitle(problem: Problem): string {
  switch (problem.code) {
    case "EAM-RND-003":
      return "The round breaks a rule";
    case "EAM-NF-001":
      return "A stop's asset no longer exists";
    default:
      return "Could not create the round";
  }
}

/** Round and sequence editor (8.44.6), round part: order the stops, set each stop's asset, job type and travel. */
export function NewRoundPage() {
  const navigate = useNavigate();
  const assets = useLoad((signal) => listAssets({ limit: 200 }, signal), "round-assets");
  const [values, setValues] = useState<RoundValues>({ code: "", name: "", stops: [emptyStop()] });
  const [errors, setErrors] = useState<RoundErrors>({ stops: [{}] });
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const assetList: Asset[] = assets.data?.items ?? [];

  function update(patch: Partial<RoundValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function updateStop(i: number, patch: Partial<StopValues>) {
    update({ stops: values.stops.map((s, j) => (j === i ? { ...s, ...patch } : s)) });
  }

  function move(i: number, by: -1 | 1) {
    const stops = [...values.stops];
    [stops[i], stops[i + by]] = [stops[i + by], stops[i]];
    const errs = [...errors.stops];
    [errs[i], errs[i + by]] = [errs[i + by] ?? {}, errs[i] ?? {}];
    update({ stops });
    setErrors({ ...errors, stops: errs });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRound(values, assetList);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createRound(input, key.current);
      navigate(`/eam/rounds/${created.id}`);
    } catch (e) {
      const problem = describeProblem(e);
      if (problem.code === "EAM-DUP-001") setErrors({ ...found, code: "A round with this code already exists." });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New maintenance round" description="Stops are walked in order. A skipped mandatory stop raises a maintenance request for its asset." />
      <Card>
        {assets.error ? (
          <Failure title="Could not load the assets" problem={assets.error} retry={assets.reload} />
        ) : assets.loading && !assets.data ? (
          <div aria-busy="true">
            <Skeleton height="control" />
          </div>
        ) : assetList.length === 0 ? (
          <Alert tone="info" title="No assets yet">
            Rounds visit assets. <Link to="/eam/assets/new">Create an asset</Link> first.
          </Alert>
        ) : (
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField id="round-code" label="Round code" hint="For example WEEKLY-A." error={errors.code}>
              {(control) => <Input {...control} value={values.code} maxLength={30} invalid={Boolean(errors.code)} onChange={(e) => update({ code: e.target.value })} />}
            </FormField>
            <FormField id="round-name" label="Name" error={errors.name}>
              {(control) => <Input {...control} value={values.name} maxLength={120} invalid={Boolean(errors.name)} onChange={(e) => update({ name: e.target.value })} />}
            </FormField>
            {values.stops.map((stop, i) => {
              const e = errors.stops[i] ?? {};
              const p = `stop-${i}`;
              return (
                <fieldset key={i} className={styles.lineCard}>
                  <legend className={styles.lineTitle}>Stop {i + 1}</legend>
                  <FormField id={`${p}-asset`} label="Asset" error={e.assetId}>
                    {(control) => (
                      <Select {...control} value={stop.assetId} invalid={Boolean(e.assetId)} onChange={(ev) => updateStop(i, { assetId: ev.target.value })}>
                        <option value="">Choose an asset</option>
                        {assetList.map((a) => (
                          <option key={a.id} value={a.id}>
                            {a.assetNumber} {a.name}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                  <FormField id={`${p}-job`} label="Job type" hint="Job types that need a permit to work cannot be on a round." error={e.jobType}>
                    {(control) => <Input {...control} value={stop.jobType} maxLength={20} invalid={Boolean(e.jobType)} onChange={(ev) => updateStop(i, { jobType: ev.target.value })} />}
                  </FormField>
                  <FormField id={`${p}-travel`} label={i === 0 ? "Travel to the first stop (minutes)" : "Travel from the previous stop (minutes)"} error={e.travelMinutes}>
                    {(control) => <Input {...control} inputMode="numeric" value={stop.travelMinutes} invalid={Boolean(e.travelMinutes)} onChange={(ev) => updateStop(i, { travelMinutes: ev.target.value })} />}
                  </FormField>
                  <FormField id={`${p}-mandatory`} label="Mandatory stop" hint="Skipping it raises a maintenance request.">
                    {(control) => (
                      <Select {...control} value={stop.mandatory ? "yes" : "no"} onChange={(ev) => updateStop(i, { mandatory: ev.target.value === "yes" })}>
                        <option value="no">No</option>
                        <option value="yes">Yes</option>
                      </Select>
                    )}
                  </FormField>
                  <div className={styles.formActions}>
                    <Button variant="ghost" size="sm" disabled={i === 0} onClick={() => move(i, -1)}>
                      Move stop {i + 1} up
                    </Button>
                    <Button variant="ghost" size="sm" disabled={i === values.stops.length - 1} onClick={() => move(i, 1)}>
                      Move stop {i + 1} down
                    </Button>
                    {values.stops.length > 1 ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => {
                          update({ stops: values.stops.filter((_, j) => j !== i) });
                          setErrors({ ...errors, stops: errors.stops.filter((_, j) => j !== i) });
                        }}
                      >
                        Remove stop {i + 1}
                      </Button>
                    ) : null}
                  </div>
                </fieldset>
              );
            })}
            <div className={styles.formActions}>
              <Button
                onClick={() => {
                  update({ stops: [...values.stops, emptyStop()] });
                  setErrors({ ...errors, stops: [...errors.stops, {}] });
                }}
                disabled={values.stops.length >= 50}
              >
                Add stop
              </Button>
            </div>
            {errors.form ? <p className={styles.fieldError}>{errors.form}</p> : null}
            {failure ? (
              <div className={styles.formActions}>
                <Failure title={roundProblemTitle(failure)} problem={failure} />
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Create round
              </Button>
              <Link className={buttonClassName("ghost")} to="/eam/rounds">
                Cancel
              </Link>
            </div>
          </form>
        )}
      </Card>
    </div>
  );
}

const stopColumns: DataTableColumn<RoundStop & { id: string }>[] = [
  { key: "sequence", header: "Stop", align: "end" },
  { key: "assetId", header: "Asset", render: (row) => <Link to={`/eam/assets/${row.assetId}`}>Open asset</Link> },
  { key: "jobType", header: "Job type", mono: true },
  { key: "mandatory", header: "Mandatory", render: (row) => (row.mandatory ? "Yes" : "No") },
  { key: "travelMinutes", header: "Travel", align: "end", render: (row) => `${row.travelMinutes} min` },
];

function orderProblemTitle(problem: Problem): string {
  return problem.code === "EAM-AST-001" ? "Every stop's asset must be active" : "Could not open the round work order";
}

/** One round: its stops in order, and an extra occurrence opened as a ROUND work order. */
export function RoundDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getRound(id, signal), id);
  const [currency, setCurrency] = useState("USD");
  const [dueAt, setDueAt] = useState("");
  const [fieldErrors, setFieldErrors] = useState<{ currency?: string; dueAt?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function open(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const found: { currency?: string; dueAt?: string } = {};
    const cur = currency.trim().toUpperCase();
    if (!CURRENCY.test(cur)) found.currency = "Enter a three-letter currency code.";
    const due = dueAt ? utcFromLocalInput(dueAt) : null;
    if (dueAt && !due) found.dueAt = "Enter a date and time, or leave it empty.";
    setFieldErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const wo = await createRoundWorkOrder(id, due ? { currency: cur, dueAt: due } : { currency: cur }, key.current);
      navigate(`/eam/work-orders/${wo.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Maintenance round" />
        {state.error.code === "EAM-NF-001" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="We couldn't find this round"
              description="It may belong to another tenant, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/eam/rounds">
                  Back to rounds
                </Link>
              }
            />
          </Card>
        ) : (
          <Failure title="Could not load the round" problem={state.error} retry={state.reload} />
        )}
      </div>
    );
  }
  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Maintenance round" />
        <Skeleton width="3/4" />
        <Skeleton width="1/2" />
      </div>
    );
  }
  const round = state.data!;

  return (
    <div className={styles.stack}>
      <PageHeader title={round.name} description={`${round.code} · ${roundSummary(round)}`} meta={<State value={round.active ? "ACTIVE" : "INACTIVE"} />} />
      <Card title="Details">
        <dl className={styles.details}>
          <Entry term="Site" mono>
            {round.siteId}
          </Entry>
          <Entry term="Stops">{round.stops.length}</Entry>
          <Entry term="Mandatory stops">{round.stops.filter((s) => s.mandatory).length}</Entry>
          <Entry term="Travel">{round.travelMinutes} min</Entry>
          <Entry term="Created" mono>
            {dayOf(round.createdAt)}
          </Entry>
        </dl>
      </Card>
      <Card title="Stops">
        <DataTable label="Stops" columns={stopColumns} rows={round.stops.map((s) => ({ ...s, id: String(s.sequence) }))} getRowKey={(row) => row.id} />
      </Card>
      <Card title="Schedule">
        <p className={styles.note}>
          Add a line of kind “Maintenance round” on a <Link to="/eam/plans/new">maintenance plan</Link> to generate an order on every interval. Completing the order resets the line once for the whole round.
        </p>
      </Card>
      <Card title="Open an extra occurrence">
        {round.active ? (
          <form className={styles.form} onSubmit={open} noValidate aria-label="Open an extra occurrence">
            <FormField id="round-currency" label="Work order currency" error={fieldErrors.currency}>
              {(control) => <Input {...control} value={currency} maxLength={3} invalid={Boolean(fieldErrors.currency)} onChange={(e) => { setCurrency(e.target.value); key.current = newKey(); }} />}
            </FormField>
            <FormField id="round-due" label="Due (UTC)" optional error={fieldErrors.dueAt}>
              {(control) => <Input {...control} type="datetime-local" value={dueAt} invalid={Boolean(fieldErrors.dueAt)} onChange={(e) => { setDueAt(e.target.value); key.current = newKey(); }} />}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Failure title={orderProblemTitle(failure)} problem={failure} />
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Open round work order
              </Button>
            </div>
          </form>
        ) : (
          <p className={styles.note}>This round is inactive, so no work order can be opened for it.</p>
        )}
      </Card>
    </div>
  );
}

/** The stops of a ROUND work order with their outcomes (BR-44.9); a technician records each one while the order is in progress. */
export function RoundStopsCard({ wo, onSaved }: { wo: WorkOrderDetail; onSaved(text: string): void }) {
  const stops = wo.stops ?? [];
  const recording = wo.status === "IN_PROGRESS";
  const open = stops.filter((s) => !s.outcome).length;
  const columns: DataTableColumn<WorkOrderStop & { id: string }>[] = [
    { key: "sequence", header: "Stop", align: "end" },
    { key: "assetId", header: "Asset", render: (row) => <Link to={`/eam/assets/${row.assetId}`}>Open asset</Link> },
    { key: "jobType", header: "Job type", mono: true },
    { key: "mandatory", header: "Mandatory", render: (row) => (row.mandatory ? "Yes" : "No") },
    { key: "outcome", header: "Outcome", render: (row) => (row.outcome ? <State value={row.outcome} /> : "Open") },
    { key: "reason", header: "Reason", render: (row) => row.reason ?? dash },
    {
      key: "requestId",
      header: "Request",
      render: (row) => (row.requestId ? <Link to="/eam/requests">Request raised</Link> : dash),
    },
    { key: "recordedAt", header: "Recorded", mono: true, render: (row) => minuteOf(row.recordedAt) },
  ];
  return (
    <Card title="Round stops">
      <div className={styles.stack}>
        <p className={styles.note} aria-live="polite">
          {open === 0 ? "Every stop has an outcome." : `${open} of ${stops.length} stops have no outcome yet. The order completes once every stop is done or skipped.`}
        </p>
        <DataTable label="Round stops" columns={columns} rows={stops.map((s) => ({ ...s, id: String(s.sequence) }))} getRowKey={(row) => row.id} empty={{ title: "This round has no stops" }} />
        {recording ? (
          stops.filter((s) => !s.outcome).map((s) => <StopForm key={s.sequence} workOrderId={wo.id} stop={s} onSaved={onSaved} />)
        ) : open > 0 ? (
          <p className={styles.note}>Start the work order to record stop outcomes.</p>
        ) : null}
      </div>
    </Card>
  );
}

function StopForm({ workOrderId, stop, onSaved }: { workOrderId: string; stop: WorkOrderStop; onSaved(text: string): void }) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<StopOutcome | null>(null);
  const key = useRef(newKey());

  async function record(outcome: StopOutcome) {
    const text = reason.trim();
    if (outcome === "SKIPPED" && text === "") {
      setError("Say why the stop was skipped.");
      return;
    }
    if (text.length > 120) {
      setError("Keep the reason to 120 characters.");
      return;
    }
    setError(null);
    setFailure(null);
    setBusy(outcome);
    try {
      const saved = await recordStopOutcome(workOrderId, stop.sequence, outcome, text, key.current);
      key.current = newKey();
      onSaved(
        saved.requestId
          ? `Stop ${stop.sequence} skipped. A maintenance request was raised for its asset.`
          : `Stop ${stop.sequence} recorded as ${sentence(outcome).toLowerCase()}.`,
      );
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      if (problem.code !== "COM-0001") key.current = newKey();
    } finally {
      setBusy(null);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    void record("DONE");
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label={`Record stop ${stop.sequence}`}>
      <FormField
        id={`stop-${stop.sequence}-reason`}
        label={`Stop ${stop.sequence} · ${stop.jobType}${stop.mandatory ? " · mandatory" : ""}`}
        hint="A reason is needed to skip; optional when done."
        optional
        error={error ?? undefined}
      >
        {(control) => <Input {...control} value={reason} maxLength={120} invalid={Boolean(error)} onChange={(e) => { setReason(e.target.value); key.current = newKey(); }} />}
      </FormField>
      {failure ? (
        <div className={styles.formActions}>
          <Failure title={`Could not record stop ${stop.sequence}`} problem={failure} />
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" size="sm" loading={busy === "DONE"}>
          Done
        </Button>
        <Button variant="secondary" size="sm" loading={busy === "SKIPPED"} onClick={() => void record("SKIPPED")}>
          Skip stop {stop.sequence}
        </Button>
      </div>
    </form>
  );
}
