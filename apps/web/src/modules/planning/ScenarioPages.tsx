import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
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
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createScenario,
  getScenario,
  listScenarios,
  runScenario,
  updateScenario,
  type PlanVersion,
  type Scenario,
  type ScenarioSettings,
} from "./api/client";
import { describeError, PLAN_CODE, useLoad, usePlanCode, withPlan, type LoadError } from "./hooks";
import styles from "./pages.module.css";
import { SAMPLE_ITEMS, validateRun } from "./RunPlanPage";

const LIST = "/planning/scenarios";
const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const FACTOR = /^\d{1,3}(\.\d{1,4})?$/;
const QTY = /^\d+(\.\d{1,6})?$/;
const MAX_LINES = 500;

/** The scenario form as typed; the three line lists are JSON text. */
export interface ScenarioDraft {
  code: string;
  planCode: string;
  name: string;
  demandFactor: string;
  active: boolean;
  itemFactors: string;
  addedDemand: string;
  leadTimeOverrides: string;
}

type DraftErrors = Partial<Record<keyof ScenarioDraft, string>>;

const pretty = (rows: unknown[]) => (rows.length === 0 ? "[]" : JSON.stringify(rows, null, 2));

export function draftOf(plan: string, s?: Scenario): ScenarioDraft {
  return {
    code: s?.code ?? "",
    planCode: s?.planCode ?? plan,
    name: s?.name ?? "",
    demandFactor: s?.demandFactor ?? "1",
    active: s?.active ?? true,
    itemFactors: pretty(s?.itemFactors ?? []),
    addedDemand: pretty(s?.addedDemand ?? []),
    leadTimeOverrides: pretty(s?.leadTimeOverrides ?? []),
  };
}

function parseLines(text: string, check: (row: Record<string, unknown>, i: number) => string | null): { rows: unknown[]; error?: string } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text.trim() === "" ? "[]" : text);
  } catch {
    return { rows: [], error: "Not valid JSON." };
  }
  if (!Array.isArray(parsed)) return { rows: [], error: "Enter a JSON array." };
  if (parsed.length > MAX_LINES) return { rows: [], error: `Enter at most ${MAX_LINES} lines.` };
  for (let i = 0; i < parsed.length; i++) {
    const row = parsed[i] as Record<string, unknown> | null;
    if (typeof row !== "object" || row === null) return { rows: [], error: `Line ${i + 1} must be an object.` };
    const bad = check(row, i);
    if (bad) return { rows: [], error: `Line ${i + 1}: ${bad}` };
  }
  return { rows: parsed };
}

const isUuid = (v: unknown) => typeof v === "string" && UUID.test(v);

/** Checks a scenario draft with the service's rules and returns the settings or the field errors. */
export function validateScenario(d: ScenarioDraft, creating: boolean): { errors: DraftErrors; settings: ScenarioSettings | null } {
  const errors: DraftErrors = {};
  if (creating && !PLAN_CODE.test(d.code.trim().toUpperCase())) errors.code = "Use 2 to 20 capital letters, digits, _ or -.";
  if (!PLAN_CODE.test(d.planCode.trim())) errors.planCode = "Use 2 to 20 capital letters, digits, _ or -.";
  if (d.name.trim() === "" || d.name.length > 120) errors.name = "Enter a name of 1 to 120 characters.";
  const factor = d.demandFactor.trim();
  if (!FACTOR.test(factor) || Number(factor) > 100) errors.demandFactor = "Enter a factor from 0 to 100 with at most 4 decimals, e.g. 1.2.";
  const factors = parseLines(d.itemFactors, (r) => {
    if (!isUuid(r.itemId)) return "itemId must be a UUID.";
    if (typeof r.factor !== "string" || !FACTOR.test(r.factor) || Number(r.factor) > 100) return 'factor must be a string from "0" to "100".';
    return null;
  });
  if (factors.error) errors.itemFactors = factors.error;
  const added = parseLines(d.addedDemand, (r) => {
    if (!isUuid(r.itemId)) return "itemId must be a UUID.";
    if (typeof r.date !== "string" || !DATE.test(r.date)) return "date must be YYYY-MM-DD.";
    if (typeof r.qty !== "string" || !QTY.test(r.qty) || Number(r.qty) <= 0) return 'qty must be a string greater than "0".';
    return null;
  });
  if (added.error) errors.addedDemand = added.error;
  const lead = parseLines(d.leadTimeOverrides, (r) => {
    if (!isUuid(r.itemId)) return "itemId must be a UUID.";
    if (!Number.isInteger(r.leadTimeDays) || (r.leadTimeDays as number) < 0 || (r.leadTimeDays as number) > 3650) return "leadTimeDays must be a whole number from 0 to 3650.";
    return null;
  });
  if (lead.error) errors.leadTimeOverrides = lead.error;
  if (Object.keys(errors).length > 0) return { errors, settings: null };
  return {
    errors,
    settings: {
      planCode: d.planCode.trim(),
      name: d.name.trim(),
      demandFactor: factor,
      itemFactors: factors.rows as ScenarioSettings["itemFactors"],
      addedDemand: added.rows as ScenarioSettings["addedDemand"],
      leadTimeOverrides: lead.rows as ScenarioSettings["leadTimeOverrides"],
      active: d.active,
    },
  };
}

function newKey(): string {
  const c = globalThis.crypto as Crypto | undefined;
  return c?.randomUUID ? c.randomUUID() : `sc-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

const columns: DataTableColumn<Scenario>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "planCode", header: "Plan", mono: true },
  { key: "demandFactor", header: "Demand factor", align: "end", render: (s) => `×${s.demandFactor}` },
  {
    key: "lines",
    header: "Item changes",
    align: "end",
    render: (s) => String(s.itemFactors.length + s.addedDemand.length + s.leadTimeOverrides.length),
  },
  { key: "active", header: "Status", render: (s) => <Badge tone={s.active ? "success" : "neutral"}>{s.active ? "Active" : "Inactive"}</Badge> },
];

/** Planning scenarios (FR-22.15): the scenarios of the plan in view, each a set of changes a simulation applies. */
export function ScenariosPage() {
  const navigate = useNavigate();
  const [plan] = usePlanCode();
  const state = useLoad((signal) => listScenarios(plan, { signal }), [plan]);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Scenarios"
        description={`What-if changes to demand and lead times for plan ${plan}. A scenario run is stored as a simulation version beside the operational plan and never reaches purchasing or sales.`}
        actions={
          <Link className={buttonClassName("primary")} to={withPlan(`${LIST}/new`, plan)}>
            New scenario
          </Link>
        }
      />
      <Alert tone="danger" title={state.error ? "Could not load scenarios" : undefined} live="alert">
        {state.error ? `${state.error.message} (${state.error.code})` : null}
      </Alert>
      {state.error ? null : (
        <DataTable
          label="Scenarios"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(s) => s.id}
          loading={state.loading}
          onRowOpen={(s) => navigate(`${LIST}/${encodeURIComponent(s.code)}`)}
          empty={{ title: "No scenarios yet", description: "Create one, for example demand +20%, to see what the plan would do." }}
        />
      )}
    </div>
  );
}

interface FormProps {
  scenario?: Scenario;
  defaultPlan: string;
  onSaved: (s: Scenario) => void;
  onReload?: () => void;
}

function ScenarioForm({ scenario, defaultPlan, onSaved, onReload }: FormProps) {
  const creating = !scenario;
  const [draft, setDraft] = useState<ScenarioDraft>(() => draftOf(defaultPlan, scenario));
  const [errors, setErrors] = useState<DraftErrors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  useEffect(() => {
    if (scenario) {
      setDraft(draftOf(scenario.planCode, scenario));
      setErrors({});
    }
  }, [scenario?.id, scenario?.version]);

  const set = <K extends keyof ScenarioDraft>(field: K, value: ScenarioDraft[K]) => {
    setDraft((d) => ({ ...d, [field]: value }));
    key.current = newKey();
  };

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, settings } = validateScenario(draft, creating);
    setErrors(found);
    if (!settings) return;
    setBusy(true);
    setFailure(null);
    try {
      const saved = scenario
        ? await updateScenario(scenario.code, scenario.version, settings)
        : await createScenario(draft.code.trim().toUpperCase(), settings, key.current);
      onSaved(saved);
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.status === 412;
  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label={creating ? "New scenario" : `Edit scenario ${scenario.code}`}>
      <div className={styles.wide}>
        <Alert
          tone="danger"
          title={failure ? (stale ? "Someone else changed this scenario" : "The scenario was not saved") : undefined}
          live="alert"
          action={
            stale && onReload ? (
              <Button size="sm" onClick={onReload}>
                Reload scenario
              </Button>
            ) : undefined
          }
        >
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
      </div>
      {creating ? (
        <FormField id="sc-code" label="Code" hint="2 to 20 capital letters, digits, _ or -." error={errors.code}>
          {(a11y) => <Input {...a11y} className={styles.mono} value={draft.code} maxLength={20} onChange={(e) => set("code", e.target.value.toUpperCase())} />}
        </FormField>
      ) : null}
      <FormField id="sc-plan" label="Plan" hint={creating ? "A plan that has run at least once; the scenario belongs to it and its legal entity. At most 10 scenarios per plan are active." : "The plan of a scenario cannot change."} error={errors.planCode}>
        {(a11y) => (
          <Input {...a11y} className={styles.mono} value={draft.planCode} maxLength={20} readOnly={!creating} onChange={(e) => set("planCode", e.target.value.toUpperCase())} />
        )}
      </FormField>
      <FormField id="sc-name" label="Name" error={errors.name}>
        {(a11y) => <Input {...a11y} value={draft.name} maxLength={120} onChange={(e) => set("name", e.target.value)} />}
      </FormField>
      <FormField id="sc-factor" label="Demand factor" hint="Multiplies the sales and forecast demand of every item without its own factor; 1.2 is demand +20%." error={errors.demandFactor}>
        {(a11y) => <Input {...a11y} inputMode="decimal" value={draft.demandFactor} onChange={(e) => set("demandFactor", e.target.value)} />}
      </FormField>
      <div className={styles.wide}>
        <FormField id="sc-item-factors" label="Item factors" optional hint='JSON array of {"itemId", "factor"}; replaces the demand factor for that item.' error={errors.itemFactors}>
          {(a11y) => <Textarea {...a11y} className={styles.mono} rows={4} value={draft.itemFactors} onChange={(e) => set("itemFactors", e.target.value)} />}
        </FormField>
      </div>
      <div className={styles.wide}>
        <FormField id="sc-added" label="Added demand" optional hint='JSON array of {"itemId", "date", "qty"}: sales demand the scenario adds.' error={errors.addedDemand}>
          {(a11y) => <Textarea {...a11y} className={styles.mono} rows={4} value={draft.addedDemand} onChange={(e) => set("addedDemand", e.target.value)} />}
        </FormField>
      </div>
      <div className={styles.wide}>
        <FormField id="sc-lead" label="Lead-time overrides" optional hint='JSON array of {"itemId", "leadTimeDays"}.' error={errors.leadTimeOverrides}>
          {(a11y) => <Textarea {...a11y} className={styles.mono} rows={4} value={draft.leadTimeOverrides} onChange={(e) => set("leadTimeOverrides", e.target.value)} />}
        </FormField>
      </div>
      <span className={`${styles.check} ${styles.wide}`}>
        <Input id="sc-active" type="checkbox" checked={draft.active} onChange={(e) => set("active", e.target.checked)} />
        <label htmlFor="sc-active">Active (can be run)</label>
      </span>
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          {creating ? "Create scenario" : "Save changes"}
        </Button>
        <Link className={buttonClassName("secondary")} to={withPlan(LIST, draft.planCode || defaultPlan)}>
          Cancel
        </Link>
      </div>
    </form>
  );
}

/** Creates a scenario; the form starts from a neutral one (factor 1, no changes). */
export function NewScenarioPage() {
  const navigate = useNavigate();
  const [plan] = usePlanCode();
  return (
    <div className={styles.stack}>
      <PageHeader title="New scenario" description="Name the change you want to try. Nothing is planned until you run the scenario on a snapshot." />
      <Card>
        <ScenarioForm defaultPlan={plan} onSaved={(s) => navigate(`${LIST}/${encodeURIComponent(s.code)}`)} />
      </Card>
    </div>
  );
}

/** Runs a scenario on a snapshot; the result is a simulation version of the scenario's plan. */
function RunScenarioCard({ scenario, today }: { scenario: Scenario; today: string }) {
  const [asOf, setAsOf] = useState(today);
  const [siteId, setSiteId] = useState("");
  const [items, setItems] = useState(SAMPLE_ITEMS);
  const [carryForward, setCarryForward] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [result, setResult] = useState<PlanVersion | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, items: parsed } = validateRun({ plan: scenario.planCode, asOf, siteId, items });
    const { plan: _plan, ...fieldErrors } = found;
    setErrors(fieldErrors as Record<string, string>);
    if (Object.keys(fieldErrors).length > 0) return;
    setBusy(true);
    setFailure(null);
    setResult(null);
    try {
      setResult(await runScenario(scenario.code, key.current, { asOf, siteId: siteId.trim(), ...(carryForward ? { carryForward: true } : {}), items: parsed }));
      key.current = newKey();
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Run simulation">
      <form className={styles.form} onSubmit={submit} noValidate aria-label={`Run scenario ${scenario.code}`}>
        <div className={styles.wide}>
          <Alert tone="danger" title={failure ? "The simulation did not run" : undefined} live="alert">
            {failure ? `${failure.message} (${failure.code})` : null}
          </Alert>
          <Alert tone="success" title={result ? `Simulation version ${result.number} of plan ${result.planCode} stored` : undefined} live="status">
            {result ? (
              <>
                {`${result.plannedOrders} planned orders and ${result.messages} messages from ${result.itemsPlanned} items; the operational plan is unchanged. `}
                <Link to={`/planning/planned-orders?plan=${encodeURIComponent(result.planCode)}&version=${result.number}`}>View simulated orders</Link>
                {" · "}
                <Link to={`/planning/runs/compare?plan=${encodeURIComponent(result.planCode)}&to=${result.number}`}>Compare with the operational plan</Link>
              </>
            ) : null}
          </Alert>
        </div>
        {scenario.active ? null : (
          <div className={styles.wide}>
            <Alert tone="warning" title="This scenario is inactive and cannot be run" live="status" />
          </div>
        )}
        <FormField id="sim-as-of" label="Plan date" error={errors.asOf}>
          {(a11y) => (
            <Input
              {...a11y}
              type="date"
              value={asOf}
              onChange={(e) => {
                key.current = newKey();
                setAsOf(e.target.value);
              }}
            />
          )}
        </FormField>
        <FormField id="sim-site" label="Site" hint="Site id (UUID) the snapshot belongs to." error={errors.siteId}>
          {(a11y) => (
            <Input
              {...a11y}
              value={siteId}
              onChange={(e) => {
                key.current = newKey();
                setSiteId(e.target.value);
              }}
            />
          )}
        </FormField>
        <div className={styles.wide}>
          <FormField id="sim-items" label="Items" hint="The same snapshot a plan run takes (RunItem in the planning OpenAPI)." error={errors.items}>
            {(a11y) => (
              <Textarea
                {...a11y}
                className={styles.mono}
                rows={10}
                value={items}
                onChange={(e) => {
                  key.current = newKey();
                  setItems(e.target.value);
                }}
              />
            )}
          </FormField>
        </div>
        <div className={styles.wide}>
          <label className={styles.check} htmlFor="sim-carry">
            <Input
              id="sim-carry"
              type="checkbox"
              aria-describedby="sim-carry-hint"
              checked={carryForward}
              onChange={(e) => {
                key.current = newKey();
                setCarryForward(e.target.checked);
              }}
            />
            Count firmed orders as supply
          </label>
          <p id="sim-carry-hint" className={styles.summary}>
            Adds the plan's firmed orders that are not yet delivered as receipts of their items, as a plan run with this option does.
          </p>
        </div>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy} disabled={!scenario.active}>
            Run simulation
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Shows and edits one scenario under If-Match on its version, and runs it. */
export function ScenarioPage({ today = new Date().toISOString().slice(0, 10) }: { today?: string }) {
  const { code = "" } = useParams();
  const [reload, setReload] = useState(0);
  const state = useLoad((signal) => getScenario(code, { signal }), [code, reload]);
  const [saved, setSaved] = useState<Scenario | null>(null);
  const scenario = saved && saved.code === code ? saved : state.data;

  useEffect(() => setSaved(null), [state.data]);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Scenario ${code}`}
        description="Changes apply to the next simulation run; versions already stored keep the result they were run with."
        actions={
          <Link className={buttonClassName("secondary")} to={withPlan(LIST, scenario?.planCode ?? "MAIN")}>
            All scenarios
          </Link>
        }
      />
      <Alert tone="danger" title={state.error ? `Could not load scenario ${code}` : undefined} live="alert">
        {state.error ? `${state.error.message} (${state.error.code})` : null}
      </Alert>
      <Alert tone="success" live="status">
        {saved ? `Saved. Version ${saved.version}.` : null}
      </Alert>
      {state.loading ? (
        <Card>
          <div className={styles.stack} aria-busy="true" aria-label="Loading scenario">
            <Skeleton width="1/2" />
            <Skeleton height="control" />
            <Skeleton height="control" />
            <Skeleton width="3/4" />
          </div>
        </Card>
      ) : null}
      {scenario ? (
        <>
          <Card title="Settings">
            <ScenarioForm scenario={scenario} defaultPlan={scenario.planCode} onSaved={setSaved} onReload={() => setReload((n) => n + 1)} />
          </Card>
          <RunScenarioCard scenario={scenario} today={today} />
        </>
      ) : null}
    </div>
  );
}
