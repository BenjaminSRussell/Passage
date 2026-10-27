import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import {
  assignToProgram,
  carryForwardProgram,
  createProgram,
  describeError,
  distributeProgram,
  getProgram,
  listPrograms,
  type AssignedObjectType,
  type CarryForwardOutcome,
  type PositionCategory,
  type Program,
  type ProgramAssignment,
  type ProgramPosition,
  type ProgramView,
} from "./api/client";
import {
  ASSIGNED_OBJECT_TYPES,
  formatAmount,
  fromUnits,
  MAX_PROGRAM_LEVELS,
  POSITION_CATEGORIES,
  positionTree,
  toUnits,
  UUID,
  validateAssignment,
  validateDistribution,
  validateProgram,
  type AllocationRow,
  type AssignForm,
  type ProgramForm,
} from "./logic";
import styles from "./pages.module.css";
import { dash, Detail, formatDateTime, LoadError } from "./parts";
import { useLoad } from "./useLoad";

const money = (key: string, header: string) => ({ key, header, align: "end" as const, mono: true });

const programColumns: DataTableColumn<Program>[] = [
  { key: "code", header: "Program", mono: true },
  { key: "name", header: "Name", render: (p) => p.name || dash },
  { key: "approvalYear", header: "Approval year", mono: true },
  { ...money("budget", "Budget"), render: (p) => formatAmount(p.budget) },
  { ...money("distributed", "Distributed"), render: (p) => formatAmount(p.distributed) },
  { key: "currency", header: "Currency", mono: true },
  { key: "status", header: "Status", render: (p) => <StatusPill status={p.status} /> },
];

/** Investment programs (FR-38.21, PRD 7.38.14 investment program overview). */
export function ProgramsPage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const state = useLoad((signal) => listPrograms(le || undefined, signal), le);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Investment programs"
        description="Each program holds the capex budget of one approval year. Distribute it down a hierarchy of positions, assign capex requests and projects, and carry what is unused into the next year."
        actions={
          <Link className={buttonClassName("primary")} to="/budgeting/programs/new">
            New program
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="programs-legal-entity" label="Legal entity" optional>
          {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load investment programs" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Investment programs"
          columns={programColumns}
          rows={state.data?.items ?? []}
          getRowKey={(p) => p.id}
          loading={state.loading && !state.data}
          empty={{ title: "No investment programs yet", description: "Create a program for an approval year to start distributing its budget." }}
          onRowOpen={(p) => navigate(`/budgeting/programs/${p.id}`)}
        />
      )}
    </div>
  );
}

const blankProgram = (): ProgramForm => ({ legalEntityId: "", code: "", name: "", approvalYear: String(new Date().getFullYear()), currency: "USD", budget: "" });

/** Creates a program for one approval year. */
export function CreateProgramPage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<ProgramForm>(blankProgram);
  const [errors, setErrors] = useState<Partial<Record<keyof ProgramForm, string>>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  async function submit(e: FormEvent) {
    e.preventDefault();
    const clean = { ...form, legalEntityId: form.legalEntityId.trim(), code: form.code.trim(), name: form.name.trim(), budget: form.budget.trim() };
    const found = validateProgram(clean);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const input = { ...clean, approvalYear: Number(clean.approvalYear) };
    const body = JSON.stringify(input);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      const created = await createProgram(input, key.current.value);
      navigate(`/budgeting/programs/${created.id}`);
    } catch (err) {
      setFailure(describeError(err, "The program could not be created."));
    } finally {
      setBusy(false);
    }
  }

  const field = (id: keyof ProgramForm, label: string, hint?: string, mono = false, optional = false) => (
    <FormField id={`program-${id}`} label={label} hint={hint} error={errors[id]} optional={optional}>
      {(c) => (
        <Input
          {...c}
          className={mono ? styles.mono : undefined}
          inputMode={id === "budget" || id === "approvalYear" ? "decimal" : undefined}
          value={form[id]}
          invalid={Boolean(errors[id])}
          onChange={(e) => setForm((f) => ({ ...f, [id]: id === "currency" || id === "code" ? e.target.value.toUpperCase() : e.target.value }))}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New investment program" description="The program budget is the top of the distribution: its positions together can never hold more." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("legalEntityId", "Legal entity", undefined, true)}
          {field("code", "Program code", "Unique per legal entity and year, for example CAPEX-26.", true)}
          {field("name", "Name", undefined, false, true)}
          {field("approvalYear", "Approval year", undefined, true)}
          {field("currency", "Currency", undefined, true)}
          {field("budget", "Program budget", undefined, true)}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create program
            </Button>
            <Link className={buttonClassName("ghost")} to="/budgeting/programs">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {failure}
      </Alert>
    </div>
  );
}

const depthClass = [styles.depth0, styles.depth1, styles.depth2, styles.depth3, styles.depth4, styles.depth5];

const assignmentColumns: DataTableColumn<ProgramAssignment & { positionCode: string }>[] = [
  { key: "positionCode", header: "Position", mono: true },
  { key: "objectType", header: "Object", render: (a) => ASSIGNED_OBJECT_TYPES.find((t) => t.value === a.objectType)?.label ?? a.objectType },
  { key: "objectRef", header: "Reference", mono: true },
  { ...money("plan", "Plan"), render: (a) => formatAmount(a.plan) },
  { ...money("actual", "Actual"), render: (a) => formatAmount(a.actual) },
  { ...money("commitment", "Commitment"), render: (a) => formatAmount(a.commitment) },
  { key: "updatedAt", header: "Reported", render: (a) => formatDateTime(a.updatedAt) },
];

/** One program: totals, the position hierarchy with rollup, distribution, assignment and carry-forward. */
export function ProgramDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getProgram(id, signal), id);
  const [view, setView] = useState<ProgramView | null>(null);
  const program = view && view.id === id && state.data && view.version >= state.data.version ? view : state.data;

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Investment program" />
        <LoadError title="Could not load this program" error={state.error} onRetry={state.reload} />
      </div>
    );
  }
  if (!program) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Investment program" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const p = program;
  const tree = positionTree(p.positions);
  const positionColumns: DataTableColumn<{ position: ProgramPosition; depth: number }>[] = [
    {
      key: "code",
      header: "Position",
      mono: true,
      render: (r) => <span className={depthClass[Math.min(r.depth, depthClass.length - 1)]}>{r.position.code}</span>,
    },
    { key: "level", header: "Level", align: "end", render: (r) => r.position.level },
    { key: "category", header: "Category", render: (r) => r.position.category },
    { ...money("budget", "Budget"), render: (r) => formatAmount(r.position.budget) },
    { ...money("distributed", "Distributed"), render: (r) => formatAmount(r.position.distributed) },
    { ...money("plan", "Plan"), render: (r) => formatAmount(r.position.plan) },
    { ...money("actual", "Actual"), render: (r) => formatAmount(r.position.actual) },
    { ...money("commitment", "Commitment"), render: (r) => formatAmount(r.position.commitment) },
    { ...money("available", "Available"), render: (r) => formatAmount(r.position.available) },
    { ...money("carriedForward", "Carried forward"), render: (r) => formatAmount(r.position.carriedForward) },
  ];
  const assignments = p.positions.flatMap((pos) => pos.assignments.map((a) => ({ ...a, positionCode: pos.code })));

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Program ${p.code} (${p.approvalYear})`}
        description="Plan, actual and commitment roll up from the assigned capex requests, assets under construction and projects to every parent position."
        meta={<StatusPill status={p.status} />}
      />
      <Card>
        <dl className={styles.details}>
          <Detail term="Name">{p.name || dash}</Detail>
          <Detail term="Legal entity" mono>
            {p.legalEntityId}
          </Detail>
          <Detail term="Program type">{p.programType}</Detail>
          <Detail term="Currency" mono>
            {p.currency}
          </Detail>
          <Detail term="Created">{formatDateTime(p.createdAt)}</Detail>
        </dl>
      </Card>
      <StatGroup label="Program totals">
        <StatTile label={`Budget (${p.currency})`} value={formatAmount(p.budget)} />
        <StatTile label="Distributed" value={formatAmount(p.distributed)} />
        <StatTile label="Actual" value={formatAmount(p.actual)} />
        <StatTile label="Commitment" value={formatAmount(p.commitment)} />
        <StatTile label="Available" value={formatAmount(p.available)} />
        <StatTile label="Carried forward" value={formatAmount(p.carriedForward)} />
      </StatGroup>

      <h3 className={styles.sectionTitle}>Positions</h3>
      <DataTable
        label="Program positions"
        columns={positionColumns}
        rows={tree}
        getRowKey={(r) => r.position.id}
        empty={{ title: "No positions yet", description: "Distribute the program budget to top-level positions below." }}
      />

      <div className={styles.split}>
        <DistributePanel program={p} onDone={setView} />
        <AssignPanel program={p} onDone={setView} />
      </div>

      <h3 className={styles.sectionTitle}>Assigned objects</h3>
      <DataTable
        label="Assigned objects"
        columns={assignmentColumns}
        rows={assignments}
        getRowKey={(a) => a.id}
        empty={{ title: "Nothing assigned yet", description: "Assign capex requests, assets under construction or projects to positions to track their spend." }}
      />

      <CarryForwardPanel program={p} onDone={state.reload} />
    </div>
  );
}

const emptyRow = (): AllocationRow & { key: string } => ({ key: crypto.randomUUID(), positionCode: "", category: "", budget: "" });

function DistributePanel({ program, onDone }: { program: ProgramView; onDone(view: ProgramView): void }) {
  const [parentId, setParentId] = useState("");
  const [rows, setRows] = useState([emptyRow()]);
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });
  const parent = program.positions.find((pos) => pos.id === parentId);
  const parents = program.positions.filter((pos) => pos.level < MAX_PROGRAM_LEVELS);
  const children = program.positions.filter((pos) => (pos.parentId ?? "") === parentId);

  function update(i: number, patch: Partial<AllocationRow>) {
    setRows((cur) => cur.map((r, k) => (k === i ? { ...r, ...patch } : r)));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const clean = rows.map((r) => ({ positionCode: r.positionCode.trim(), category: r.category, budget: r.budget.trim() }));
    const found = validateDistribution(parent ? parent.budget : program.budget, children, clean);
    setErrors(found);
    setFailure(null);
    setDone(null);
    if (found.length > 0) return;
    const input = {
      parentPositionId: parentId || undefined,
      allocations: clean.map((r) => ({ positionCode: r.positionCode, budget: r.budget, category: (r.category || undefined) as PositionCategory | undefined })),
    };
    const body = JSON.stringify({ id: program.id, input });
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      const next = await distributeProgram(program.id, input, key.current.value);
      setRows([emptyRow()]);
      setDone(`Distributed under ${parent ? parent.code : "the program"}.`);
      onDone(next);
    } catch (err) {
      setFailure(describeError(err, "The distribution could not be saved."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Distribute budget">
        <h3 className={`${styles.sectionTitle} ${styles.wide}`}>Distribute budget</h3>
        <FormField
          id="distribute-parent"
          label="Parent"
          hint={`Undistributed: ${formatAmount(parent ? parent.undistributed : fromUnits(toUnits(program.budget) - toUnits(program.distributed)))}`}
        >
          {(c) => (
            <Select {...c} value={parentId} onChange={(e) => setParentId(e.target.value)}>
              <option value="">The program ({program.code})</option>
              {parents.map((pos) => (
                <option key={pos.id} value={pos.id}>
                  {pos.code} (level {pos.level})
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {rows.map((row, i) => (
          <fieldset key={row.key} className={styles.lineSet}>
            <legend className={styles.legend}>Position {i + 1}</legend>
            <FormField id={`distribute-code-${i}`} label="Position code" hint="An existing code sets its budget; a new code creates the position.">
              {(c) => <Input {...c} className={styles.mono} value={row.positionCode} onChange={(e) => update(i, { positionCode: e.target.value.toUpperCase() })} />}
            </FormField>
            <FormField id={`distribute-category-${i}`} label="Category">
              {(c) => (
                <Select {...c} value={row.category} onChange={(e) => update(i, { category: e.target.value })}>
                  <option value="">Keep, or Capex if new</option>
                  {POSITION_CATEGORIES.map((cat) => (
                    <option key={cat} value={cat}>
                      {cat}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id={`distribute-budget-${i}`} label="Budget">
              {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={row.budget} onChange={(e) => update(i, { budget: e.target.value })} />}
            </FormField>
            {rows.length > 1 ? (
              <Button variant="ghost" onClick={() => setRows((cur) => cur.filter((_, k) => k !== i))}>
                Remove position {i + 1}
              </Button>
            ) : null}
          </fieldset>
        ))}
        <div className={styles.formActions}>
          <Button onClick={() => setRows((cur) => [...cur, emptyRow()])}>Add position</Button>
          <Button type="submit" variant="primary" loading={busy}>
            Save distribution
          </Button>
        </div>
      </form>
      <FormMessages errors={errors} failure={failure} done={done} />
    </Card>
  );
}

const blankAssign = (): AssignForm => ({ positionId: "", objectType: "CapexRequest", objectRef: "", plan: "0", actual: "0", commitment: "0" });

function AssignPanel({ program, onDone }: { program: ProgramView; onDone(view: ProgramView): void }) {
  const [form, setForm] = useState<AssignForm>(blankAssign);
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  async function submit(e: FormEvent) {
    e.preventDefault();
    const clean = { ...form, objectRef: form.objectRef.trim(), plan: form.plan.trim(), actual: form.actual.trim(), commitment: form.commitment.trim() };
    const found = validateAssignment(clean);
    setErrors(found);
    setFailure(null);
    setDone(null);
    if (found.length > 0) return;
    const input = { ...clean, objectType: clean.objectType as AssignedObjectType };
    const body = JSON.stringify({ id: program.id, input });
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      const next = await assignToProgram(program.id, input, key.current.value);
      setDone(`${clean.objectRef} is assigned.`);
      setForm(blankAssign());
      onDone(next);
    } catch (err) {
      setFailure(describeError(err, "The assignment could not be saved."));
    } finally {
      setBusy(false);
    }
  }

  const amount = (field: "plan" | "actual" | "commitment", label: string) => (
    <FormField id={`assign-${field}`} label={label}>
      {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={form[field]} onChange={(e) => setForm((f) => ({ ...f, [field]: e.target.value }))} />}
    </FormField>
  );

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Assign to a position">
        <h3 className={`${styles.sectionTitle} ${styles.wide}`}>Assign to a position</h3>
        <FormField id="assign-position" label="Position">
          {(c) => (
            <Select {...c} value={form.positionId} onChange={(e) => setForm((f) => ({ ...f, positionId: e.target.value }))}>
              <option value="">{program.positions.length > 0 ? "Choose a position" : "Distribute the budget first"}</option>
              {program.positions.map((pos) => (
                <option key={pos.id} value={pos.id}>
                  {pos.code}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="assign-type" label="Object type">
          {(c) => (
            <Select {...c} value={form.objectType} onChange={(e) => setForm((f) => ({ ...f, objectType: e.target.value }))}>
              {ASSIGNED_OBJECT_TYPES.map((t) => (
                <option key={t.value} value={t.value}>
                  {t.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="assign-ref" label="Reference" hint="The capex request, asset or project number. Assigning it again replaces its amounts.">
          {(c) => <Input {...c} className={styles.mono} value={form.objectRef} onChange={(e) => setForm((f) => ({ ...f, objectRef: e.target.value }))} />}
        </FormField>
        {amount("plan", "Plan")}
        {amount("actual", "Actual")}
        {amount("commitment", "Commitment")}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save assignment
          </Button>
        </div>
      </form>
      <FormMessages errors={errors} failure={failure} done={done} />
    </Card>
  );
}

function CarryForwardPanel({ program, onDone }: { program: ProgramView; onDone(): void }) {
  const [result, setResult] = useState<CarryForwardOutcome | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(crypto.randomUUID());
  const toYear = program.approvalYear + 1;

  async function carry() {
    setFailure(null);
    setBusy(true);
    try {
      const out = await carryForwardProgram(program.id, toYear, key.current);
      setResult(out);
      key.current = crypto.randomUUID();
      onDone();
    } catch (err) {
      setFailure(describeError(err, "The carry-forward could not be completed."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <div className={styles.stack}>
        <h3 className={styles.sectionTitle}>Carry forward to {toYear}</h3>
        <p className={styles.note}>
          Each position carries its undistributed budget less the actual and commitment of the objects assigned to it, once. Parents and children are never counted twice.
        </p>
        <div>
          <Button variant="secondary" loading={busy} onClick={carry}>
            Carry unused budget to {toYear}
          </Button>
        </div>
        {result ? (
          <Alert tone="success" title={`Carried ${formatAmount(result.amount)} ${program.currency} to ${result.toYear}`} live="status">
            {result.positions.length > 0 ? result.positions.map((c) => `${c.code} ${formatAmount(c.amount)}`).join(", ") : "No position had unused budget."}
          </Alert>
        ) : null}
        <Alert tone="danger" live="alert">
          {failure}
        </Alert>
      </div>
    </Card>
  );
}

function FormMessages({ errors, failure, done }: { errors: string[]; failure: string | null; done: string | null }) {
  return (
    <>
      <Alert tone="danger" live="alert">
        {errors.length > 0 ? (
          <ul className={styles.errorList}>
            {errors.map((msg) => (
              <li key={msg}>{msg}</li>
            ))}
          </ul>
        ) : (
          failure
        )}
      </Alert>
      {done ? (
        <Alert tone="success" live="status">
          {done}
        </Alert>
      ) : null}
    </>
  );
}
