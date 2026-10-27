import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  assignNextWork,
  confirmLine,
  createWork,
  describeProblem,
  getWork,
  listWork,
  newKey,
  WORK_STATUSES,
  WORK_TYPES,
  workException,
  type Direction,
  type Planned,
  type Problem,
  type Work,
  type WorkLine,
  type WorkLineInput,
} from "./api/client";
import styles from "./pages.module.css";
import { ChooseWarehouse, DECIMAL, Detail, INTEGER, LINE_STATUS_TONES, sentence, shortId, useWarehouse, UUID, WarehouseField, when, WORK_STATUS_TONES } from "./shared";
import { useLoad } from "./useLoad";

const PAGE = 100;

const workColumns: DataTableColumn<Work>[] = [
  { key: "id", header: "Work", mono: true, render: (w) => shortId(w.id) },
  { key: "workType", header: "Type", render: (w) => sentence(w.workType) },
  { key: "status", header: "Status", render: (w) => <Badge tone={WORK_STATUS_TONES[w.status] ?? "neutral"}>{sentence(w.status)}</Badge> },
  { key: "priority", header: "Priority", align: "end" },
  { key: "assignedUser", header: "Assigned to", render: (w) => w.assignedUser || "—" },
  { key: "createdAt", header: "Created", mono: true, render: (w) => when(w.createdAt) },
];

/** Work of a warehouse, best priority first (FR-31.7): open it to execute, or take the next one. */
export function WorkListPage() {
  const navigate = useNavigate();
  const [warehouse, setWarehouse] = useWarehouse();
  const [status, setStatus] = useState("");
  const valid = UUID.test(warehouse.trim());
  const state = useLoad(
    (signal) => (valid ? listWork({ warehouseId: warehouse.trim(), status, limit: PAGE }, signal) : Promise.resolve(null)),
    `${warehouse.trim()}|${status}`,
  );
  const [assigning, setAssigning] = useState(false);
  const [assignFailure, setAssignFailure] = useState<Problem | null>(null);
  const [nothing, setNothing] = useState(false);
  const assignKey = useRef(newKey());

  async function assign() {
    setAssigning(true);
    setAssignFailure(null);
    setNothing(false);
    try {
      const { work } = await assignNextWork(warehouse.trim().toLowerCase(), assignKey.current);
      assignKey.current = newKey();
      if (work) {
        navigate(`/warehouse/work/${work.id}`);
        return;
      }
      setNothing(true);
    } catch (error) {
      setAssignFailure(describeProblem(error));
    }
    setAssigning(false);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Work"
        description="Directed put-away and pick work. Take the next work to execute it, or open any work to see its lines."
        actions={
          <>
            <Button variant="primary" onClick={assign} loading={assigning} disabled={!valid}>
              Assign next work to me
            </Button>
            <Link className={buttonClassName("secondary")} to={`/warehouse/work/new?warehouse=${encodeURIComponent(warehouse.trim())}`}>
              Create work
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <WarehouseField value={warehouse} onChange={setWarehouse} />
        <FormField id="work-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">All statuses</option>
              {WORK_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      <Alert tone="danger" title={assignFailure ? "Could not assign work" : undefined} live="alert">
        {assignFailure ? `${assignFailure.message} (${assignFailure.code})` : null}
      </Alert>
      <Alert tone="info" live="status">
        {nothing ? "No work is waiting in this warehouse." : null}
      </Alert>
      {!valid ? (
        <ChooseWarehouse />
      ) : state.error ? (
        <Alert tone="danger" title="Could not load the work" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Work"
          columns={workColumns}
          rows={state.data?.items ?? []}
          getRowKey={(w) => w.id}
          loading={state.loading}
          empty={{ title: "No work", description: "Create work for received or ordered lines; the directives choose each location." }}
          onRowOpen={(w) => navigate(`/warehouse/work/${w.id}`)}
        />
      )}
      {state.data?.nextCursor ? <p className={styles.note}>Showing the first {PAGE} works; filter by status to narrow the list.</p> : null}
    </div>
  );
}

interface LineDraft {
  itemId: string;
  direction: Direction;
  qty: string;
  weight: string;
  volume: string;
  lps: string;
}

export interface WorkDraft {
  warehouseId: string;
  workType: string;
  priority: string;
  lines: LineDraft[];
}

type WorkErrors = Partial<Record<"warehouseId" | "priority" | `lines.${number}.${"itemId" | "qty" | "weight" | "volume" | "lps"}`, string>>;

const blankLine = (direction: Direction): LineDraft => ({ itemId: "", direction, qty: "", weight: "", volume: "", lps: "" });

export function validateWorkDraft(d: WorkDraft): WorkErrors {
  const errors: WorkErrors = {};
  if (!UUID.test(d.warehouseId.trim())) errors.warehouseId = "Enter the warehouse id, a UUID.";
  if (d.priority !== "" && (!INTEGER.test(d.priority) || Number(d.priority) < 1 || Number(d.priority) > 99)) errors.priority = "Enter 1 (highest) to 99.";
  d.lines.forEach((l, i) => {
    if (l.itemId.trim() === "") errors[`lines.${i}.itemId`] = "Enter the item.";
    if (!DECIMAL.test(l.qty) || Number(l.qty) <= 0) errors[`lines.${i}.qty`] = "Enter a quantity above 0.";
    if (l.weight !== "" && !DECIMAL.test(l.weight)) errors[`lines.${i}.weight`] = "Enter a weight of 0 or more.";
    if (l.volume !== "" && !DECIMAL.test(l.volume)) errors[`lines.${i}.volume`] = "Enter a volume of 0 or more.";
    if (l.lps !== "" && !INTEGER.test(l.lps)) errors[`lines.${i}.lps`] = "Enter a whole number.";
  });
  return errors;
}

export function toWorkInput(d: WorkDraft) {
  return {
    warehouseId: d.warehouseId.trim().toLowerCase(),
    workType: d.workType,
    ...(d.priority !== "" ? { priority: Number(d.priority) } : {}),
    lines: d.lines.map(
      (l): WorkLineInput => ({
        itemId: l.itemId.trim(),
        direction: l.direction,
        qty: l.qty,
        ...(l.weight !== "" ? { weight: l.weight } : {}),
        ...(l.volume !== "" ? { volume: l.volume } : {}),
        ...(l.lps !== "" ? { lps: Number(l.lps) } : {}),
      }),
    ),
  };
}

/** Creates work from lines (CreateWorkFromOrderLines); the warehouse's directives choose each location (BR-31.1). */
export function NewWorkPage() {
  const [warehouse] = useWarehouse();
  const [draft, setDraft] = useState<WorkDraft>({ warehouseId: warehouse, workType: "PURCHASE_PUTAWAY", priority: "", lines: [blankLine("PUT")] });
  const [errors, setErrors] = useState<WorkErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<Planned | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  const set = (patch: Partial<WorkDraft>) => {
    setDraft((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  };
  const setLine = (i: number, patch: Partial<LineDraft>) => set({ lines: draft.lines.map((l, j) => (j === i ? { ...l, ...patch } : l)) });
  const defaultDirection: Direction = draft.workType.endsWith("PICK") || draft.workType === "TRANSFER_ISSUE" ? "PICK" : "PUT";

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateWorkDraft(draft);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await createWork(toWorkInput(draft), key.current));
      key.current = newKey();
    } catch (error) {
      setResult(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Create work" description="Enter the lines to move. Each line is placed by the warehouse's location directives; a line no directive can place blocks the work." />
      <Card title="Work">
        <form className={styles.form} onSubmit={submit} noValidate>
          <div className={styles.wide}>
            <WarehouseField id="new-work-warehouse" value={draft.warehouseId} onChange={(v) => set({ warehouseId: v })} error={errors.warehouseId} />
          </div>
          <FormField id="new-work-type" label="Work type">
            {(c) => (
              <Select {...c} value={draft.workType} onChange={(e) => set({ workType: e.target.value })}>
                {WORK_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="new-work-priority" label="Priority" optional hint="1 is the highest; 50 when blank." error={errors.priority}>
            {(c) => <Input {...c} inputMode="numeric" value={draft.priority} onChange={(e) => set({ priority: e.target.value })} invalid={Boolean(errors.priority)} />}
          </FormField>
          {draft.lines.map((line, i) => {
            const field = (name: "itemId" | "qty" | "weight" | "volume" | "lps", label: string, optional = false) => (
              <FormField id={`new-work-${i}-${name}`} label={label} optional={optional} error={errors[`lines.${i}.${name}`]}>
                {(c) => (
                  <Input
                    {...c}
                    inputMode={name === "itemId" ? undefined : "decimal"}
                    value={line[name]}
                    onChange={(e) => setLine(i, { [name]: e.target.value })}
                    invalid={Boolean(errors[`lines.${i}.${name}`])}
                  />
                )}
              </FormField>
            );
            return (
              <fieldset key={i} className={styles.lineRow} aria-label={`Line ${i + 1}`}>
                {field("itemId", `Line ${i + 1} item`)}
                <FormField id={`new-work-${i}-direction`} label={`Line ${i + 1} direction`}>
                  {(c) => (
                    <Select {...c} value={line.direction} onChange={(e) => setLine(i, { direction: e.target.value as Direction })}>
                      <option value="PUT">Put-away</option>
                      <option value="PICK">Pick</option>
                    </Select>
                  )}
                </FormField>
                {field("qty", `Line ${i + 1} quantity`)}
                {field("weight", `Line ${i + 1} weight (kg)`, true)}
                {field("volume", `Line ${i + 1} volume (m³)`, true)}
                {field("lps", `Line ${i + 1} license plates`, true)}
                {draft.lines.length > 1 ? (
                  <Button variant="ghost" size="sm" onClick={() => set({ lines: draft.lines.filter((_, j) => j !== i) })}>
                    Remove line {i + 1}
                  </Button>
                ) : null}
              </fieldset>
            );
          })}
          <div className={styles.formActions}>
            <Button onClick={() => set({ lines: [...draft.lines, blankLine(defaultDirection)] })} disabled={draft.lines.length >= 500}>
              Add line
            </Button>
            <Button type="submit" variant="primary" loading={busy}>
              Create work
            </Button>
          </div>
          <div className={styles.formActions}>
            <Alert tone="danger" title={failure ? "The work was not created" : undefined} live="alert">
              {failure
                ? `${failure.message} (${failure.code})${Object.keys(failure.fields).length > 0 ? `: ${Object.entries(failure.fields).map(([f, m]) => `${f} ${m}`).join("; ")}` : ""}`
                : null}
            </Alert>
          </div>
        </form>
      </Card>
      {result ? <PlannedResult planned={result} /> : null}
    </div>
  );
}

function PlannedResult({ planned }: { planned: Planned }) {
  const { work, traces } = planned;
  const blocked = work.status === "BLOCKED_NO_LOCATION";
  const rows = work.lines.map((l, i) => ({ line: l, trace: traces[i] }));
  return (
    <Card title="Created work">
      <div className={styles.stack}>
        <p role="status" className={styles.summary}>
          <Badge tone={WORK_STATUS_TONES[work.status]}>{sentence(work.status)}</Badge>{" "}
          {blocked ? "No directive could place every line (WMS-0101); the work waits until it is re-evaluated or cancelled." : `Work ${shortId(work.id)} is ready to assign.`}
        </p>
        <DataTable
          label="Planned lines"
          columns={[
            { key: "lineNo", header: "Line", align: "end", render: (r) => r.line.lineNo },
            { key: "itemId", header: "Item", mono: true, render: (r) => r.line.itemId },
            { key: "qty", header: "Quantity", align: "end", render: (r) => r.line.qty },
            { key: "location", header: "Location", mono: true, render: (r) => shortId(r.line.fromLocationId ?? r.line.toLocationId) },
            { key: "directive", header: "Directive", mono: true, render: (r) => (r.trace?.blocked ? (r.trace.code ?? "WMS-0101") : shortId(r.trace?.directive)) },
            { key: "reason", header: "Last rule", render: (r) => r.trace?.trace.at(-1)?.reason ?? "No directive applies to this work type." },
          ]}
          rows={rows}
          getRowKey={(r) => String(r.line.lineNo)}
        />
        <div className={styles.actions}>
          <Link className={buttonClassName("primary")} to={`/warehouse/work/${work.id}`}>
            Open work
          </Link>
        </div>
      </div>
    </Card>
  );
}

const EXECUTABLE = new Set(["ASSIGNED", "IN_PROGRESS"]);
const CANCELLABLE = new Set(["OPEN", "ASSIGNED", "BLOCKED_NO_LOCATION"]);

/** One work with its lines: confirm, short pick or cancel (FR-31.7, BR-31.3). */
export function WorkDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getWork(id, signal), id);
  const [current, setCurrent] = useState<Work | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [notice, setNotice] = useState<{ text: string; remainder?: Work | null } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [short, setShort] = useState<{ lineNo: number; qty: string; reason: string } | null>(null);
  const [cancelReason, setCancelReason] = useState("");
  const keys = useRef<Record<string, string>>({});
  useEffect(() => {
    setCurrent(null);
    setNotice(null);
    setFailure(null);
    setShort(null);
  }, [id]);

  const keyFor = (action: string) => (keys.current[action] ??= newKey());
  const work = current ?? state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Work" />
        <Alert tone="danger" title="Could not load the work" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!work) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Work" />
        <span className="visually-hidden">Loading the work…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const loaded = work;

  async function run(action: string, title: string, call: (key: string) => Promise<{ work: Work; text: string; remainder?: Work | null }>) {
    setBusy(action);
    setFailure(null);
    setNotice(null);
    try {
      const out = await call(keyFor(action));
      delete keys.current[action];
      setCurrent(out.work);
      setNotice({ text: out.text, remainder: out.remainder });
      setShort(null);
    } catch (error) {
      setFailure({ title, problem: describeProblem(error) });
    } finally {
      setBusy(null);
    }
  }

  const confirm = (line: WorkLine) =>
    run(`confirm-${line.lineNo}`, `Line ${line.lineNo} was not confirmed`, async (key) => {
      const w = await confirmLine(loaded, line.lineNo, line.qty, key);
      return { work: w, text: `Line ${line.lineNo} confirmed: ${line.qty} ${line.itemId}.` };
    });

  function submitShort(event: FormEvent) {
    event.preventDefault();
    if (!short) return;
    if (!DECIMAL.test(short.qty) || short.reason.trim() === "") {
      setFailure({ title: "Check the short pick", problem: { code: "COM-0001", message: "Enter the quantity picked (0 or more) and a reason.", fields: {} } });
      return;
    }
    void run(`short-${short.lineNo}`, `Line ${short.lineNo} was not short picked`, async (key) => {
      const out = await workException(loaded, { action: "SHORT_PICK", lineNo: short.lineNo, confirmedQty: short.qty, reason: short.reason.trim() }, key);
      return { work: out.work, remainder: out.remainder, text: `Line ${short.lineNo} short picked at ${short.qty}.` };
    });
  }

  function submitCancel(event: FormEvent) {
    event.preventDefault();
    if (cancelReason.trim() === "") {
      setFailure({ title: "Check the cancellation", problem: { code: "COM-0001", message: "Enter why the work is cancelled.", fields: {} } });
      return;
    }
    void run("cancel", "The work was not cancelled", async (key) => {
      const out = await workException(loaded, { action: "CANCEL", reason: cancelReason.trim() }, key);
      return { work: out.work, text: "Work cancelled." };
    });
  }

  const executable = EXECUTABLE.has(loaded.status);
  const lineColumns: DataTableColumn<WorkLine>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "itemId", header: "Item", mono: true },
    { key: "direction", header: "Direction", render: (l) => (l.direction === "PUT" ? "Put-away" : "Pick") },
    { key: "qty", header: "Quantity", align: "end" },
    { key: "confirmedQty", header: "Confirmed", align: "end" },
    { key: "location", header: "Location", mono: true, render: (l) => <span title={l.fromLocationId ?? l.toLocationId}>{shortId(l.fromLocationId ?? l.toLocationId)}</span> },
    { key: "status", header: "Status", render: (l) => <Badge tone={LINE_STATUS_TONES[l.status] ?? "neutral"}>{sentence(l.status)}</Badge> },
    { key: "reason", header: "Reason", render: (l) => l.reason || "—" },
    {
      key: "actions",
      header: "Actions",
      render: (l) =>
        executable && l.status === "OPEN" ? (
          <div className={styles.actions}>
            <Button size="sm" variant="primary" loading={busy === `confirm-${l.lineNo}`} onClick={() => confirm(l)} aria-label={`Confirm line ${l.lineNo}`}>
              Confirm
            </Button>
            {l.direction === "PICK" ? (
              <Button size="sm" onClick={() => setShort({ lineNo: l.lineNo, qty: "", reason: "" })} aria-label={`Short pick line ${l.lineNo}`}>
                Short pick
              </Button>
            ) : null}
          </div>
        ) : (
          "—"
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title={`Work ${shortId(loaded.id)}`} meta={<Badge tone={WORK_STATUS_TONES[loaded.status] ?? "neutral"}>{sentence(loaded.status)}</Badge>} />
      <Card>
        <dl className={styles.details}>
          <Detail term="Work id" mono>
            {loaded.id}
          </Detail>
          <Detail term="Type">{sentence(loaded.workType)}</Detail>
          <Detail term="Priority">{loaded.priority}</Detail>
          <Detail term="Assigned to">{loaded.assignedUser || "Nobody"}</Detail>
          <Detail term="Assigned at">{when(loaded.assignedAt)}</Detail>
          <Detail term="Created">{when(loaded.createdAt)}</Detail>
          <Detail term="Parent work" mono>
            {loaded.parentWorkId ? <Link to={`/warehouse/work/${loaded.parentWorkId}`}>{shortId(loaded.parentWorkId)}</Link> : "None"}
          </Detail>
          <Detail term="Version">{loaded.version}</Detail>
        </dl>
      </Card>
      <Alert tone="danger" title={failure?.title} live="alert">
        {failure ? `${failure.problem.message} (${failure.problem.code})` : null}
      </Alert>
      <Alert tone="success" live="status">
        {notice ? (
          <>
            {notice.text}
            {notice.remainder ? (
              <>
                {" "}
                The remainder is work <Link to={`/warehouse/work/${notice.remainder.id}`}>{shortId(notice.remainder.id)}</Link>.
              </>
            ) : null}
          </>
        ) : null}
      </Alert>
      {loaded.status === "OPEN" ? <p className={styles.note}>Assign this work from the work list before confirming its lines.</p> : null}
      <DataTable label="Work lines" columns={lineColumns} rows={loaded.lines} getRowKey={(l) => String(l.lineNo)} />
      {short ? (
        <Card title={`Short pick line ${short.lineNo}`}>
          <form className={styles.form} onSubmit={submitShort} noValidate>
            <FormField id="short-qty" label="Quantity picked" hint="What you could pick, 0 or more and below the line quantity; the rest becomes new work.">
              {(c) => <Input {...c} inputMode="decimal" value={short.qty} onChange={(e) => setShort({ ...short, qty: e.target.value })} />}
            </FormField>
            <FormField id="short-reason" label="Reason" hint="For example EMPTY or DAMAGED.">
              {(c) => <Input {...c} value={short.reason} onChange={(e) => setShort({ ...short, reason: e.target.value })} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === `short-${short.lineNo}`}>
                Record short pick
              </Button>
              <Button variant="ghost" onClick={() => setShort(null)}>
                Keep the line open
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {CANCELLABLE.has(loaded.status) ? (
        <Card title="Cancel work">
          <form className={styles.form} onSubmit={submitCancel} noValidate>
            <FormField id="cancel-reason" label="Cancellation reason">
              {(c) => <Input {...c} value={cancelReason} onChange={(e) => setCancelReason(e.target.value)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="danger" loading={busy === "cancel"}>
                Cancel work
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}
