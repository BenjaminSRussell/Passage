import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { BudgetApiError, describeError, getRule, listRules } from "./api/client";
import {
  compareScenarios,
  createPlan,
  createScenario,
  getGrid,
  getPlan,
  listOrgUnits,
  listPlans,
  putGrid,
  SCENARIO_KINDS,
  setStageOwners,
  spreadGrid,
  submitPlan,
  type CompareGroup,
  type Plan,
  type PlanScenario,
  type ScenarioKind,
  type StageOwner,
} from "./api/plans";
import { formatAmount, isMoney, UUID } from "./logic";
import {
  amountMap,
  cellError,
  cellKey,
  changedCells,
  editErrors,
  emptySpread,
  METHOD_LABELS,
  periodLabel,
  periodsOf,
  rowsOf,
  toSpreadInput,
  total,
  validateSpread,
  type GridRow,
  type SpreadForm,
} from "./planLogic";
import styles from "./pages.module.css";
import { Detail, LoadError } from "./parts";
import { useLoad } from "./useLoad";

const key = () => crypto.randomUUID();

const planColumns: DataTableColumn<Plan>[] = [
  { key: "name", header: "Plan" },
  { key: "fiscalYear", header: "Fiscal year", mono: true },
  { key: "status", header: "Status", render: (p) => <StatusPill status={p.status} /> },
  { key: "currency", header: "Currency", mono: true },
  { key: "createdBy", header: "Created by" },
];

/** Budget plans (FR-38.2): one planning process per legal entity and fiscal year, each with its scenarios. */
export function PlansPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const legalEntityId = params.get("legalEntityId") ?? "";
  const filter = UUID.test(legalEntityId) ? legalEntityId : "";
  const state = useLoad((signal) => listPlans(filter || undefined, signal), filter);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Budget plans"
        description="Plan next year's budget by account, cost center and month. Each plan holds scenarios you can compare, and is reviewed and approved before it becomes the budget."
        actions={
          <Link className={buttonClassName("primary")} to={`/budgeting/plans/new${filter ? `?legalEntityId=${filter}` : ""}`}>
            New plan
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="plans-entity" label="Legal entity" optional>
          {(c) => <EntityLookup entity="legalEntity" {...c} value={legalEntityId} onChange={(e) => setParams(e.target.value ? { legalEntityId: e.target.value } : {})} />}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load the plans" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Budget plans"
          columns={planColumns}
          rows={state.data?.items ?? []}
          getRowKey={(p) => p.id}
          loading={state.loading && !state.data}
          empty={{ title: "No plans yet", description: "Open a plan on a budget control rule to start planning." }}
          onRowOpen={(p) => navigate(`/budgeting/plans/${p.id}`)}
        />
      )}
    </div>
  );
}

/** Open a planning process on a control rule version. */
export function NewPlanPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [legalEntityId, setLegalEntityId] = useState(params.get("legalEntityId") ?? "");
  const [ruleId, setRuleId] = useState("");
  const [name, setName] = useState("");
  const [year, setYear] = useState(String(new Date().getFullYear() + 1));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const idem = useRef(key());
  const rules = useLoad((signal) => (UUID.test(legalEntityId) ? listRules(legalEntityId, signal) : Promise.resolve({ items: [] })), legalEntityId);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!UUID.test(legalEntityId)) found.legalEntityId = "Choose a legal entity.";
    if (!UUID.test(ruleId)) found.ruleVersionId = "Choose the budget control rule the plan will become.";
    if (name.trim() === "") found.name = "Give the plan a name.";
    if (!/^\d{4}$/.test(year)) found.fiscalYear = "Enter a four-digit year.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const plan = await createPlan({ legalEntityId, ruleVersionId: ruleId, name: name.trim(), fiscalYear: Number(year) }, idem.current);
      navigate(`/budgeting/plans/${plan.id}`);
    } catch (error) {
      if (error instanceof BudgetApiError && error.problem.status < 500) idem.current = key();
      setFailure(describeError(error, "The budgeting service could not be reached."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New budget plan" description="A plan starts with a Baseline scenario. Add scenarios and fill the grid from the plan page." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="np-entity" label="Legal entity" error={errors.legalEntityId}>
            {(c) => (
              <EntityLookup
                entity="legalEntity"
                {...c}
                value={legalEntityId}
                onChange={(e) => {
                  setLegalEntityId(e.target.value);
                  setRuleId("");
                }}
                invalid={Boolean(errors.legalEntityId)}
              />
            )}
          </FormField>
          <FormField id="np-rule" label="Budget control rule" error={errors.ruleVersionId}>
            {(c) => (
              <Select {...c} value={ruleId} onChange={(e) => setRuleId(e.target.value)} invalid={Boolean(errors.ruleVersionId)}>
                <option value="">Choose a rule version</option>
                {(rules.data?.items ?? []).map((r) => (
                  <option key={r.id} value={r.id}>{`${r.code} version ${r.version}, from ${r.effectiveFrom}`}</option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="np-name" label="Plan name" error={errors.name}>
            {(c) => <Input {...c} value={name} onChange={(e) => setName(e.target.value)} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="np-year" label="Fiscal year" hint="Plans run January to December." error={errors.fiscalYear}>
            {(c) => <Input {...c} className={styles.mono} inputMode="numeric" value={year} onChange={(e) => setYear(e.target.value)} invalid={Boolean(errors.fiscalYear)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the plan" live="alert">
                {failure}
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create plan
            </Button>
            <Link className={buttonClassName("secondary")} to="/budgeting/plans">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One plan: its scenarios, who reviews and approves, the comparison of two scenarios and the submit step. */
export function PlanDetailPage() {
  const { id = "" } = useParams();
  const plan = useLoad((signal) => getPlan(id, signal), id);
  const rule = useLoad((signal) => (plan.data ? getRule(plan.data.ruleVersionId, signal) : Promise.resolve(null)), plan.data?.ruleVersionId ?? "");
  const units = useLoad((signal) => (plan.data ? listOrgUnits(plan.data.legalEntityId, signal) : Promise.resolve({ items: [] })), plan.data?.legalEntityId ?? "");
  const [notice, setNotice] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function run(action: () => Promise<unknown>, done: string) {
    setBusy(true);
    setFailure(null);
    setNotice(null);
    try {
      await action();
      setNotice(done);
      plan.reload();
    } catch (error) {
      setFailure(describeError(error, "The budgeting service could not be reached."));
    } finally {
      setBusy(false);
    }
  }

  if (plan.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Budget plan" />
        <LoadError title="Could not load the plan" error={plan.error} onRetry={plan.reload} />
      </div>
    );
  }
  const p = plan.data;
  if (!p) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Budget plan" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const draft = p.status === "Draft";
  const unitName = (unitId: string) => units.data?.items.find((u) => u.id === unitId)?.name ?? "Org unit";
  const scenarioColumns: DataTableColumn<PlanScenario>[] = [
    { key: "name", header: "Scenario" },
    { key: "kind", header: "Kind" },
    { key: "total", header: `Total (${p.currency})`, align: "end", mono: true, render: (s) => formatAmount(s.total) },
    {
      key: "open",
      header: "Grid",
      render: (s) => (
        <Link className={buttonClassName("secondary", "sm")} to={`/budgeting/plans/${p.id}/scenarios/${s.id}`} aria-label={`Open the grid of ${s.name}`}>
          {draft ? "Edit grid" : "View grid"}
        </Link>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={p.name}
        description={`Fiscal year ${p.fiscalYear}. ${draft ? "Fill the grid, set who reviews and approves, then submit." : `The plan is ${p.status.toLowerCase()}, so its grid can no longer be changed.`}`}
        actions={
          <Link className={buttonClassName("secondary")} to="/budgeting/plans">
            All plans
          </Link>
        }
      />
      {notice ? (
        <Alert tone="success" title={notice} live="status">
          The plan is up to date.
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="That did not work" live="alert">
          {failure}
        </Alert>
      ) : null}
      <Card title="Summary" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Status">
            <StatusPill status={p.status} />
          </Detail>
          <Detail term="Legal entity">
            <EntityName entity="legalEntity" id={p.legalEntityId} />
          </Detail>
          <Detail term="Budget control rule">{rule.data ? `${rule.data.code} version ${rule.data.version}` : "Loading"}</Detail>
          <Detail term="Currency">{p.currency}</Detail>
          <Detail term="Submitted by">{p.submittedBy ?? "Not submitted"}</Detail>
        </dl>
        {draft ? (
          <div className={styles.actions}>
            <Button variant="primary" loading={busy} onClick={() => void run(() => submitPlan(p.id, key()), "Plan submitted for review.")}>
              Submit for review
            </Button>
          </div>
        ) : null}
      </Card>
      <Card title="Scenarios" headingLevel={3}>
        <DataTable label="Scenarios" columns={scenarioColumns} rows={p.scenarios} getRowKey={(s) => s.id} empty={{ title: "No scenarios", description: "A plan always has a Baseline." }} />
      </Card>
      {draft ? <ScenarioForm plan={p.id} scenarios={p.scenarios} onDone={() => { setNotice("Scenario added."); plan.reload(); }} /> : null}
      <CompareCard planId={p.id} scenarios={p.scenarios} currency={p.currency} />
      <OwnersCard
        owners={p.stageOwners}
        units={units.data?.items ?? []}
        unitName={unitName}
        editable={draft}
        busy={busy}
        onSave={(owners) => run(() => setStageOwners(p.id, owners, key()), "Stage owners saved.")}
      />
    </div>
  );
}

function ScenarioForm({ plan, scenarios, onDone }: { plan: string; scenarios: PlanScenario[]; onDone(): void }) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState<ScenarioKind>("Custom");
  const [copy, setCopy] = useState("");
  const [pct, setPct] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (name.trim() === "") found.name = "Give the scenario a name.";
    if (pct.trim() !== "" && (!isMoney(pct.trim()) || copy === "")) found.pct = copy === "" ? "An adjustment needs a scenario to copy." : "Enter a number such as 10 or -5.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await createScenario(plan, { name: name.trim(), kind, ...(copy ? { copyFromScenarioId: copy } : {}), ...(pct.trim() ? { adjustmentPct: pct.trim() } : {}) }, key());
      setName("");
      setCopy("");
      setPct("");
      onDone();
    } catch (error) {
      setFailure(describeError(error, "The budgeting service could not be reached."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Add a scenario" headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="sc-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} value={name} onChange={(e) => setName(e.target.value)} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="sc-kind" label="Kind">
          {(c) => (
            <Select {...c} value={kind} onChange={(e) => setKind(e.target.value as ScenarioKind)}>
              {SCENARIO_KINDS.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="sc-copy" label="Start from" optional hint="Copy the amounts of another scenario.">
          {(c) => (
            <Select {...c} value={copy} onChange={(e) => setCopy(e.target.value)}>
              <option value="">An empty grid</option>
              {scenarios.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="sc-pct" label="Adjust the copy by percent" optional hint="10 adds ten percent, -5 takes five percent off." error={errors.pct}>
          {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={pct} onChange={(e) => setPct(e.target.value)} invalid={Boolean(errors.pct)} />}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not add the scenario" live="alert">
              {failure}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Add scenario
          </Button>
        </div>
      </form>
    </Card>
  );
}

const GROUPS: { value: CompareGroup; label: string }[] = [
  { value: "account", label: "Account" },
  { value: "costCenter", label: "Cost center" },
  { value: "period", label: "Month" },
];

function CompareCard({ planId, scenarios, currency }: { planId: string; scenarios: PlanScenario[]; currency: string }) {
  const [base, setBase] = useState("");
  const [other, setOther] = useState("");
  const [group, setGroup] = useState<CompareGroup>("account");
  const ready = base !== "" && other !== "" && base !== other;
  const result = useLoad((signal) => (ready ? compareScenarios(planId, base, other, group, signal) : Promise.resolve(null)), `${base}|${other}|${group}`);
  const options = scenarios.map((s) => (
    <option key={s.id} value={s.id}>
      {s.name}
    </option>
  ));
  return (
    <Card title="Compare scenarios" headingLevel={3}>
      <div className={styles.toolbar}>
        <FormField id="cmp-base" label="Compare">
          {(c) => (
            <Select {...c} value={base} onChange={(e) => setBase(e.target.value)}>
              <option value="">Choose a scenario</option>
              {options}
            </Select>
          )}
        </FormField>
        <FormField id="cmp-other" label="With">
          {(c) => (
            <Select {...c} value={other} onChange={(e) => setOther(e.target.value)}>
              <option value="">Choose a scenario</option>
              {options}
            </Select>
          )}
        </FormField>
        <FormField id="cmp-group" label="By">
          {(c) => (
            <Select {...c} value={group} onChange={(e) => setGroup(e.target.value as CompareGroup)}>
              {GROUPS.map((g) => (
                <option key={g.value} value={g.value}>
                  {g.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {!ready ? (
        <EmptyState icon="database" title="Choose two scenarios" description="Pick a different scenario in each box to see where they differ." />
      ) : result.error ? (
        <LoadError title="Could not compare the scenarios" error={result.error} onRetry={result.reload} />
      ) : result.data ? (
        <>
          <DataTable
            label="Scenario comparison"
            columns={[
              { key: "key", header: GROUPS.find((g) => g.value === group)?.label ?? "Key", mono: true },
              { key: "base", header: `${result.data.base.name} (${currency})`, align: "end", mono: true, render: (r) => formatAmount(r.base) },
              { key: "other", header: `${result.data.other.name} (${currency})`, align: "end", mono: true, render: (r) => formatAmount(r.other) },
              { key: "delta", header: "Change", align: "end", mono: true, render: (r) => formatAmount(r.delta) },
              { key: "deltaPct", header: "Change %", align: "end", mono: true, render: (r) => (r.deltaPct ? `${r.deltaPct}%` : "n/a") },
            ]}
            rows={result.data.rows}
            getRowKey={(r) => r.key}
            empty={{ title: "Nothing to compare", description: "Neither scenario has amounts yet." }}
          />
          <p className={styles.note} role="status">{`Total change: ${formatAmount(result.data.delta)} ${currency}.`}</p>
        </>
      ) : (
        <Skeleton width="3/4" />
      )}
    </Card>
  );
}

function OwnersCard({
  owners,
  units,
  unitName,
  editable,
  busy,
  onSave,
}: {
  owners: StageOwner[];
  units: { id: string; code: string; name: string }[];
  unitName(id: string): string;
  editable: boolean;
  busy: boolean;
  onSave(owners: StageOwner[]): Promise<void>;
}) {
  const [rows, setRows] = useState<StageOwner[]>(owners);
  const [error, setError] = useState<string | null>(null);
  const columns: DataTableColumn<StageOwner>[] = [
    { key: "stage", header: "Stage" },
    { key: "orgUnitId", header: "Org unit", render: (o) => unitName(o.orgUnitId) },
    { key: "owner", header: "Owner" },
    { key: "signedBy", header: "Signed", render: (o) => (o.signedBy ? `${o.signedBy}` : "Not yet") },
  ];
  if (!editable) {
    return (
      <Card title="Reviewers and approvers" headingLevel={3}>
        <DataTable label="Stage owners" columns={columns} rows={owners} getRowKey={(o) => `${o.stage}${o.orgUnitId}`} empty={{ title: "No stage owners", description: "None were set before submission." }} />
      </Card>
    );
  }
  function patch(i: number, change: Partial<StageOwner>) {
    setRows((prev) => prev.map((r, at) => (at === i ? { ...r, ...change } : r)));
  }
  function save(event: FormEvent) {
    event.preventDefault();
    if (rows.some((r) => r.owner.trim() === "" || r.orgUnitId === "")) {
      setError("Every row needs an org unit and an owner.");
      return;
    }
    setError(null);
    void onSave(rows.map((r) => ({ ...r, owner: r.owner.trim() })));
  }
  return (
    <Card title="Reviewers and approvers" headingLevel={3}>
      <form className={styles.form} onSubmit={save} noValidate>
        {rows.map((r, i) => (
          <div key={i} className={styles.toolbar}>
            <FormField id={`ow-stage-${i}`} label="Stage">
              {(c) => (
                <Select {...c} value={r.stage} onChange={(e) => patch(i, { stage: e.target.value as StageOwner["stage"] })}>
                  <option value="Review">Review</option>
                  <option value="Approve">Approve</option>
                </Select>
              )}
            </FormField>
            <FormField id={`ow-unit-${i}`} label="Org unit">
              {(c) => (
                <Select {...c} value={r.orgUnitId} onChange={(e) => patch(i, { orgUnitId: e.target.value })}>
                  <option value="">Choose an org unit</option>
                  {units.map((u) => (
                    <option key={u.id} value={u.id}>{`${u.code} ${u.name}`}</option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id={`ow-owner-${i}`} label="Owner" hint="User name of the person who signs this stage.">
              {(c) => <Input {...c} value={r.owner} onChange={(e) => patch(i, { owner: e.target.value })} />}
            </FormField>
            <Button type="button" size="sm" onClick={() => setRows((prev) => prev.filter((_, at) => at !== i))} aria-label={`Remove owner row ${i + 1}`}>
              Remove
            </Button>
          </div>
        ))}
        {error ? (
          <Alert tone="danger" title={error} live="alert">
            One person cannot own both stages.
          </Alert>
        ) : null}
        <div className={styles.actions}>
          <Button type="button" onClick={() => setRows((prev) => [...prev, { stage: "Review", orgUnitId: "", owner: "" }])}>
            Add an owner
          </Button>
          <Button type="submit" variant="primary" loading={busy}>
            Save owners
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** The plan grid of one scenario: account and cost center rows by month, edited in place and saved in one write. */
export function PlanGridPage() {
  const { id = "", sid = "" } = useParams();
  return <PlanGrid key={`${id}-${sid}`} planId={id} scenarioId={sid} />;
}

function PlanGrid({ planId, scenarioId }: { planId: string; scenarioId: string }) {
  const [filter, setFilter] = useState({ account: "", costCenter: "" });
  const plan = useLoad((signal) => getPlan(planId, signal), planId);
  const grid = useLoad((signal) => getGrid(planId, scenarioId, { account: filter.account.trim() || undefined, costCenter: filter.costCenter.trim() || undefined }, signal), `${planId}|${scenarioId}|${filter.account}|${filter.costCenter}`);
  const [edits, setEdits] = useState<Map<string, string>>(new Map());
  const [extra, setExtra] = useState<GridRow[]>([]);
  const [newRow, setNewRow] = useState({ account: "", costCenter: "" });
  const [spread, setSpread] = useState<SpreadForm>(emptySpread());
  const [spreadErrors, setSpreadErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [stale, setStale] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const writeKey = useRef(key());

  if (grid.error || plan.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Plan grid" />
        <LoadError title="Could not load the grid" error={grid.error ?? plan.error} onRetry={() => { grid.reload(); plan.reload(); }} />
      </div>
    );
  }
  const g = grid.data;
  const p = plan.data;
  if (!g || !p) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Plan grid" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const editable = p.status === "Draft";
  const periods = periodsOf(p.fiscalYear);
  const loaded = amountMap(g.cells);
  const rows = rowsOf(g.cells, extra);
  const value = (r: GridRow, period: string) => edits.get(cellKey(r.account, r.costCenter, period)) ?? loaded.get(cellKey(r.account, r.costCenter, period)) ?? "";
  const pending = changedCells(loaded, edits);
  const bad = editErrors(edits);

  async function attempt(action: () => Promise<{ priorYearFallback?: boolean }>, done: string) {
    setBusy(true);
    setFailure(null);
    setNotice(null);
    try {
      const out = await action();
      writeKey.current = key();
      setEdits(new Map());
      setExtra([]);
      setStale(false);
      setNotice(out.priorYearFallback ? `${done} Last year had no actuals for this account, so the months got equal weights.` : done);
      grid.reload();
      plan.reload();
    } catch (error) {
      if (error instanceof BudgetApiError && (error.problem.status === 412 || error.problem.status === 428)) setStale(true);
      else if (error instanceof BudgetApiError && error.problem.status < 500) writeKey.current = key();
      setFailure(describeError(error, "The budgeting service could not be reached."));
    } finally {
      setBusy(false);
    }
  }

  function addRow(event: FormEvent) {
    event.preventDefault();
    if (newRow.account.trim() === "" || newRow.costCenter.trim() === "") return;
    setExtra((prev) => [...prev, { account: newRow.account.trim(), costCenter: newRow.costCenter.trim() }]);
    setNewRow({ account: "", costCenter: "" });
  }

  function runSpread(event: FormEvent) {
    event.preventDefault();
    const found = validateSpread(spread);
    setSpreadErrors(found);
    if (Object.keys(found).length > 0) return;
    void attempt(() => spreadGrid(planId, scenarioId, g!.scenario.version, toSpreadInput(spread), writeKey.current), "Amounts spread.");
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${g.scenario.name}: plan grid`}
        description={`${p.name}, fiscal year ${p.fiscalYear}, amounts in ${p.currency}. ${editable ? "Change the amounts, then save." : "The plan is no longer a draft, so the grid is read only."}`}
        actions={
          <Link className={buttonClassName("secondary")} to={`/budgeting/plans/${planId}`}>
            Back to the plan
          </Link>
        }
      />
      {notice ? (
        <Alert tone="success" title={notice} live="status">
          The grid shows what was saved.
        </Alert>
      ) : null}
      {stale ? (
        <Alert tone="warning" title="Someone else changed this grid" live="alert" action={<Button size="sm" onClick={() => { setStale(false); setEdits(new Map()); grid.reload(); }}>Reload the grid</Button>}>
          Reload to see their changes, then make yours again.
        </Alert>
      ) : failure ? (
        <Alert tone="danger" title="That did not work" live="alert">
          {failure}
        </Alert>
      ) : null}
      {g.truncated ? (
        <Alert tone="info" title="Only part of the grid is shown">
          More than 10,000 cells match. Filter by account or cost center to see the rest; the total still counts every cell.
        </Alert>
      ) : null}
      <Card>
        <div className={styles.toolbar}>
          <FormField id="gr-account" label="Account" optional>
            {(c) => <Input {...c} className={styles.mono} value={filter.account} onChange={(e) => setFilter({ ...filter, account: e.target.value })} />}
          </FormField>
          <FormField id="gr-cc" label="Cost center" optional>
            {(c) => <Input {...c} className={styles.mono} value={filter.costCenter} onChange={(e) => setFilter({ ...filter, costCenter: e.target.value })} />}
          </FormField>
        </div>
      </Card>
      <Card title={`Grid total: ${formatAmount(g.total)} ${p.currency}`} headingLevel={3}>
        {rows.length === 0 ? (
          <EmptyState icon="database" title="No amounts yet" description={editable ? "Add a row below, or spread a yearly total over the months." : "This scenario holds no amounts."} />
        ) : (
          <div className={styles.gridScroll} role="region" aria-label="Plan grid" tabIndex={0}>
            <table className={styles.planGrid}>
              <thead>
                <tr>
                  <th scope="col">Account</th>
                  <th scope="col">Cost center</th>
                  {periods.map((per) => (
                    <th key={per} scope="col" className={styles.num}>
                      {periodLabel(per)}
                    </th>
                  ))}
                  <th scope="col" className={styles.num}>
                    Year
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={`${r.account}|${r.costCenter}`}>
                    <th scope="row" className={styles.mono}>
                      {r.account}
                    </th>
                    <td className={styles.mono}>{r.costCenter}</td>
                    {periods.map((per) => {
                      const k = cellKey(r.account, r.costCenter, per);
                      const v = value(r, per);
                      const err = cellError(v);
                      return (
                        <td key={per} className={styles.num}>
                          {editable ? (
                            <input
                              className={styles.cell}
                              inputMode="decimal"
                              value={v}
                              aria-label={`${r.account} ${r.costCenter} ${periodLabel(per)}`}
                              aria-invalid={err ? true : undefined}
                              title={err}
                              onChange={(e) => setEdits((prev) => new Map(prev).set(k, e.target.value))}
                            />
                          ) : (
                            formatAmount(v || undefined)
                          )}
                        </td>
                      );
                    })}
                    <td className={`${styles.num} ${styles.mono}`}>{total(periods.map((per) => value(r, per)))}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr>
                  <th scope="row" colSpan={2}>
                    Month total
                  </th>
                  {periods.map((per) => (
                    <td key={per} className={`${styles.num} ${styles.mono}`}>
                      {total(rows.map((r) => value(r, per)))}
                    </td>
                  ))}
                  <td className={`${styles.num} ${styles.mono}`}>{total(rows.flatMap((r) => periods.map((per) => value(r, per))))}</td>
                </tr>
              </tfoot>
            </table>
          </div>
        )}
        {editable ? (
          <div className={styles.actions}>
            <Button variant="primary" loading={busy} disabled={pending.length === 0 || bad.length > 0} onClick={() => void attempt(() => putGrid(planId, scenarioId, g.scenario.version, pending, writeKey.current), `Saved ${pending.length} amounts.`)}>
              {pending.length > 0 ? `Save ${pending.length} changes` : "Save changes"}
            </Button>
            {bad.length > 0 ? <span role="alert">Fix the highlighted amounts before saving.</span> : null}
          </div>
        ) : null}
      </Card>
      {editable ? (
        <>
          <Card title="Add a row" headingLevel={3}>
            <form className={styles.toolbar} onSubmit={addRow}>
              <FormField id="ar-account" label="Account">
                {(c) => <Input {...c} className={styles.mono} value={newRow.account} onChange={(e) => setNewRow({ ...newRow, account: e.target.value })} />}
              </FormField>
              <FormField id="ar-cc" label="Cost center">
                {(c) => <Input {...c} className={styles.mono} value={newRow.costCenter} onChange={(e) => setNewRow({ ...newRow, costCenter: e.target.value })} />}
              </FormField>
              <Button type="submit">Add row</Button>
            </form>
          </Card>
          <Card title="Spread a yearly total" headingLevel={3}>
            <form className={styles.form} onSubmit={runSpread} noValidate>
              <FormField id="sp-account" label="Account" error={spreadErrors.account}>
                {(c) => <Input {...c} className={styles.mono} value={spread.account} onChange={(e) => setSpread({ ...spread, account: e.target.value })} invalid={Boolean(spreadErrors.account)} />}
              </FormField>
              <FormField id="sp-total" label={`Yearly total (${p.currency})`} error={spreadErrors.total}>
                {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={spread.total} onChange={(e) => setSpread({ ...spread, total: e.target.value })} invalid={Boolean(spreadErrors.total)} />}
              </FormField>
              <FormField id="sp-method" label="Spread over the months">
                {(c) => (
                  <Select {...c} value={spread.method} onChange={(e) => setSpread({ ...spread, method: e.target.value as SpreadForm["method"] })}>
                    {(Object.keys(METHOD_LABELS) as SpreadForm["method"][]).map((m) => (
                      <option key={m} value={m}>
                        {METHOD_LABELS[m]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              {spread.method === "custom" ? (
                <fieldset className={styles.weights}>
                  <legend>Weight of each month</legend>
                  {periods.map((per, i) => (
                    <label key={per}>
                      <span>{periodLabel(per)}</span>
                      <input
                        className={styles.cell}
                        inputMode="decimal"
                        value={spread.periodWeights[i]}
                        aria-label={`Weight of ${periodLabel(per)}`}
                        onChange={(e) => setSpread({ ...spread, periodWeights: spread.periodWeights.map((w, at) => (at === i ? e.target.value : w)) })}
                      />
                    </label>
                  ))}
                  {spreadErrors.periodWeights ? <p role="alert">{spreadErrors.periodWeights}</p> : null}
                </fieldset>
              ) : null}
              <fieldset className={styles.weights}>
                <legend>Share of each cost center</legend>
                {spread.costCenters.map((c, i) => (
                  <div key={i} className={styles.toolbar}>
                    <input
                      className={styles.cell}
                      aria-label={`Cost center ${i + 1}`}
                      value={c.costCenter}
                      onChange={(e) => setSpread({ ...spread, costCenters: spread.costCenters.map((x, at) => (at === i ? { ...x, costCenter: e.target.value } : x)) })}
                    />
                    <input
                      className={styles.cell}
                      inputMode="decimal"
                      aria-label={`Weight of cost center ${i + 1}`}
                      value={c.weight}
                      onChange={(e) => setSpread({ ...spread, costCenters: spread.costCenters.map((x, at) => (at === i ? { ...x, weight: e.target.value } : x)) })}
                    />
                  </div>
                ))}
                <Button type="button" size="sm" onClick={() => setSpread({ ...spread, costCenters: [...spread.costCenters, { costCenter: "", weight: "1" }] })}>
                  Add a cost center
                </Button>
                {spreadErrors.costCenters ? <p role="alert">{spreadErrors.costCenters}</p> : null}
              </fieldset>
              <div className={styles.formActions}>
                <Button type="submit" loading={busy}>
                  Spread the total
                </Button>
              </div>
              <p className={styles.note}>Spreading replaces the amounts of that account in the cost centers you list. Save any grid edits first.</p>
            </form>
          </Card>
        </>
      ) : null}
    </div>
  );
}
