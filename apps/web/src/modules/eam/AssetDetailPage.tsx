import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  activateAsset,
  assetKpis,
  createCounter,
  createRule,
  describeProblem,
  getAsset,
  listWorkOrders,
  newKey,
  recordReading,
  type AssetDetail,
  type Counter,
  type Problem,
  type Rule,
  type WorkOrder,
} from "./api/client";
import { addMoney, dash, dayOf, hours, minuteOf, percent, typeLabel } from "./format";
import { Entry, Failure, State } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const counterColumns: DataTableColumn<Counter>[] = [
  { key: "name", header: "Counter" },
  { key: "unit", header: "Unit", mono: true },
  { key: "cumulative", header: "Kind", render: (row) => (row.cumulative ? "Cumulative" : "Resettable") },
  { key: "source", header: "Source", render: (row) => (row.source === "IOT" ? "IoT" : "Manual") },
  { key: "lastReading", header: "Last reading", align: "end", render: (row) => (row.lastReadingAt ? row.lastReading : dash) },
  { key: "lastReadingAt", header: "Read at", mono: true, render: (row) => minuteOf(row.lastReadingAt) },
  { key: "avgDailyUsage", header: "Usage per day (30 days)", align: "end", render: (row) => row.avgDailyUsage.toFixed(2) },
];

const ruleColumns = (counters: Counter[]): DataTableColumn<Rule>[] => [
  { key: "name", header: "Rule" },
  { key: "counterId", header: "Counter", render: (row) => counters.find((c) => c.id === row.counterId)?.name ?? dash },
  { key: "threshold", header: "Raises a request when", render: (row) => `reading ${row.operator} ${row.threshold}` },
  { key: "severity", header: "Severity", align: "end" },
  { key: "cooldownMinutes", header: "Cooldown", align: "end", render: (row) => `${row.cooldownMinutes} min` },
];

const orderColumns: DataTableColumn<WorkOrder>[] = [
  { key: "number", header: "Work order", mono: true },
  { key: "workOrderType", header: "Type", render: (row) => typeLabel[row.workOrderType] ?? row.workOrderType },
  { key: "status", header: "Status", render: (row) => <State value={row.status} /> },
  { key: "dueAt", header: "Due", mono: true, render: (row) => dayOf(row.dueAt) },
];

const YEAR_MS = 365 * 86_400_000;

/** One asset: identity, lifecycle, counters with readings and condition rules, KPIs and recent work orders (8.44.6 Asset detail). */
export function AssetDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [notice, setNotice] = useState<string | null>(null);
  const [actionError, setActionError] = useState<Problem | null>(null);
  const [activating, setActivating] = useState(false);
  const state = useLoad((signal) => getAsset(id, signal), id);

  async function activate(asset: AssetDetail) {
    setActivating(true);
    setActionError(null);
    try {
      await activateAsset(asset.id, asset.version);
      setNotice("Asset activated. It can now receive work orders.");
      state.reload();
    } catch (error) {
      setActionError(describeProblem(error));
    } finally {
      setActivating(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Asset" />
        {state.error.code === "EAM-NF-001" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="We couldn't find this asset"
              description="It may belong to another tenant, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/eam/assets">
                  Back to assets
                </Link>
              }
            />
          </Card>
        ) : (
          <Failure title="Could not load the asset" problem={state.error} retry={state.reload} />
        )}
      </div>
    );
  }
  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Asset" />
        <Skeleton width="3/4" />
        <Skeleton width="1/2" />
      </div>
    );
  }
  const asset = state.data!;
  const canActivate = asset.lifecycleState === "CREATED" || asset.lifecycleState === "INACTIVE";

  return (
    <div className={styles.stack}>
      <PageHeader
        title={asset.name}
        description={`${asset.assetNumber} · ${asset.assetType}`}
        meta={<State value={asset.lifecycleState} />}
        actions={
          <div className={styles.actions}>
            {canActivate ? (
              <Button variant="primary" loading={activating} onClick={() => activate(asset)}>
                Activate asset
              </Button>
            ) : null}
            {asset.lifecycleState === "ACTIVE" ? (
              <Link className={buttonClassName("secondary")} to={`/eam/work-orders/new?asset=${asset.id}`}>
                New work order
              </Link>
            ) : null}
          </div>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {actionError ? <Failure title="Could not activate the asset" problem={actionError} /> : null}
      <Card title="Details">
        <dl className={styles.details}>
          <Entry term="Asset number" mono>
            {asset.assetNumber}
          </Entry>
          <Entry term="Criticality">{asset.criticality}</Entry>
          <Entry term="Site" mono>
            {asset.siteId}
          </Entry>
          <Entry term="Production resource" mono>
            {asset.productionResourceId ?? dash}
          </Entry>
          <Entry term="Open work orders">{asset.openWorkOrders}</Entry>
          <Entry term="Maintenance cost (parts + labor)" mono>
            {addMoney(asset.partsCost, asset.laborCost)}
          </Entry>
        </dl>
      </Card>
      <Card title="Counters">
        <div className={styles.stack}>
          <DataTable
            label="Counters"
            columns={counterColumns}
            rows={asset.counters}
            getRowKey={(row) => row.id}
            empty={{ title: "No counters yet", description: "Add a counter such as run hours to plan by usage." }}
          />
          {asset.counters.length > 0 ? <ReadingForm counters={asset.counters} onSaved={(text) => { setNotice(text); state.reload(); }} /> : null}
          <CounterForm assetId={asset.id} onSaved={(text) => { setNotice(text); state.reload(); }} />
        </div>
      </Card>
      <Card title="Condition rules">
        <div className={styles.stack}>
          <p className={styles.note}>A reading that breaches a rule raises one maintenance request until that request is converted, then waits for the cooldown.</p>
          <DataTable label="Condition rules" columns={ruleColumns(asset.counters)} rows={asset.rules} getRowKey={(row) => row.id} empty={{ title: "No condition rules yet" }} />
          {asset.counters.length > 0 ? <RuleForm counters={asset.counters} onSaved={(text) => { setNotice(text); state.reload(); }} /> : null}
        </div>
      </Card>
      <KpiCard assetId={asset.id} />
      <RecentOrders assetId={asset.id} onOpen={(row) => navigate(`/eam/work-orders/${row.id}`)} />
    </div>
  );
}

function KpiCard({ assetId }: { assetId: string }) {
  const [range] = useState(() => {
    const to = new Date();
    return { from: new Date(to.getTime() - YEAR_MS).toISOString(), to: to.toISOString() };
  });
  const state = useLoad((signal) => assetKpis(assetId, range.from, range.to, signal), `${assetId}|kpi`);
  return (
    <Card title="Reliability, last 365 days">
      {state.error ? (
        <Failure title="Could not load the KPIs" problem={state.error} retry={state.reload} />
      ) : state.loading || !state.data ? (
        <div aria-busy="true">
          <Skeleton width="1/2" />
        </div>
      ) : (
        <dl className={styles.details} aria-label="Reliability">
          <Entry term="Failures">{state.data.failures}</Entry>
          <Entry term="MTBF">{state.data.failures ? hours(state.data.mtbfHours) : dash}</Entry>
          <Entry term="MTTR">{state.data.failures ? hours(state.data.mttrHours) : dash}</Entry>
          <Entry term="Availability">{percent(state.data.availability)}</Entry>
        </dl>
      )}
    </Card>
  );
}

function RecentOrders({ assetId, onOpen }: { assetId: string; onOpen(row: WorkOrder): void }) {
  const state = useLoad((signal) => listWorkOrders({ asset: assetId, limit: 10 }, signal), `${assetId}|orders`);
  return (
    <Card title="Recent work orders" actions={<Link to={`/eam/work-orders?asset=${assetId}`}>All work orders</Link>}>
      {state.error ? (
        <Failure title="Could not load work orders" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Recent work orders"
          columns={orderColumns}
          rows={state.data?.items ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No work orders for this asset yet" }}
          onRowOpen={onOpen}
        />
      )}
    </Card>
  );
}

function CounterForm({ assetId, onSaved }: { assetId: string; onSaved(text: string): void }) {
  const [name, setName] = useState("");
  const [unit, setUnit] = useState("");
  const [kind, setKind] = useState("cumulative");
  const [source, setSource] = useState<"MANUAL" | "IOT">("MANUAL");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (!name.trim() || name.trim().length > 60 || !unit.trim() || unit.trim().length > 12) {
      setError("Enter a name of up to 60 and a unit of up to 12 characters.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      await createCounter(assetId, { name: name.trim(), unit: unit.trim(), cumulative: kind === "cumulative", source }, key.current);
      key.current = newKey();
      setName("");
      setUnit("");
      onSaved(`Counter ${name.trim()} added.`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Add counter">
      <FormField id="counter-name" label="Counter name" error={error ?? undefined}>
        {(control) => <Input {...control} value={name} maxLength={60} invalid={Boolean(error)} onChange={(e) => { setName(e.target.value); key.current = newKey(); }} />}
      </FormField>
      <FormField id="counter-unit" label="Unit" hint="For example h, km or °C.">
        {(control) => <Input {...control} value={unit} maxLength={12} onChange={(e) => { setUnit(e.target.value); key.current = newKey(); }} />}
      </FormField>
      <FormField id="counter-kind" label="Kind" hint="Cumulative readings never go down unless you reset them.">
        {(control) => (
          <Select {...control} value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="cumulative">Cumulative</option>
            <option value="resettable">Resettable or gauge</option>
          </Select>
        )}
      </FormField>
      <FormField id="counter-source" label="Source">
        {(control) => (
          <Select {...control} value={source} onChange={(e) => setSource(e.target.value as "MANUAL" | "IOT")}>
            <option value="MANUAL">Manual</option>
            <option value="IOT">IoT</option>
          </Select>
        )}
      </FormField>
      {failure ? (
        <div className={styles.formActions}>
          <Failure title="Could not add the counter" problem={failure} />
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" loading={busy}>
          Add counter
        </Button>
      </div>
    </form>
  );
}

function ReadingForm({ counters, onSaved }: { counters: Counter[]; onSaved(text: string): void }) {
  const [counterId, setCounterId] = useState(counters[0].id);
  const [value, setValue] = useState("");
  const [reset, setReset] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const n = Number(value);
    if (value.trim() === "" || !Number.isFinite(n)) {
      setError("Enter the reading as a number.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      await recordReading(counterId, { value: n, reset }, key.current);
      key.current = newKey();
      setValue("");
      setReset(false);
      onSaved(`Reading ${n} recorded.`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Record reading">
      <FormField id="reading-counter" label="Counter">
        {(control) => (
          <Select {...control} value={counterId} onChange={(e) => { setCounterId(e.target.value); key.current = newKey(); }}>
            {counters.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name} ({c.unit})
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="reading-value" label="Reading" error={error ?? undefined}>
        {(control) => <Input {...control} inputMode="decimal" value={value} invalid={Boolean(error)} onChange={(e) => { setValue(e.target.value); key.current = newKey(); }} />}
      </FormField>
      <div className={styles.check}>
        <Input id="reading-reset" type="checkbox" checked={reset} onChange={(e) => { setReset(e.target.checked); key.current = newKey(); }} />
        <label htmlFor="reading-reset">Counter was reset</label>
      </div>
      {failure ? (
        <div className={styles.formActions}>
          <Failure title={failure.code === "EAM-CTR-001" ? "Reading rejected" : "Could not record the reading"} problem={failure} />
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" loading={busy}>
          Record reading
        </Button>
      </div>
    </form>
  );
}

function RuleForm({ counters, onSaved }: { counters: Counter[]; onSaved(text: string): void }) {
  const [counterId, setCounterId] = useState(counters[0].id);
  const [name, setName] = useState("");
  const [operator, setOperator] = useState<">" | "<">(">");
  const [threshold, setThreshold] = useState("");
  const [severity, setSeverity] = useState("3");
  const [cooldown, setCooldown] = useState("60");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const t = Number(threshold);
    const c = Number(cooldown);
    if (!name.trim() || name.trim().length > 120 || threshold.trim() === "" || !Number.isFinite(t) || !Number.isInteger(c) || c < 1 || c > 1440) {
      setError("Enter a name, a numeric threshold and a cooldown of 1 to 1,440 minutes.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      await createRule(counterId, { name: name.trim(), operator, threshold: t, severity: Number(severity), cooldownMinutes: c }, key.current);
      key.current = newKey();
      setName("");
      setThreshold("");
      onSaved(`Rule ${name.trim()} added.`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  const touch = () => (key.current = newKey());
  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Add condition rule">
      <FormField id="rule-counter" label="Counter">
        {(control) => (
          <Select {...control} value={counterId} onChange={(e) => { setCounterId(e.target.value); touch(); }}>
            {counters.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="rule-name" label="Rule name" error={error ?? undefined}>
        {(control) => <Input {...control} value={name} maxLength={120} invalid={Boolean(error)} onChange={(e) => { setName(e.target.value); touch(); }} />}
      </FormField>
      <FormField id="rule-operator" label="When the reading is">
        {(control) => (
          <Select {...control} value={operator} onChange={(e) => { setOperator(e.target.value as ">" | "<"); touch(); }}>
            <option value=">">Above the threshold</option>
            <option value="<">Below the threshold</option>
          </Select>
        )}
      </FormField>
      <FormField id="rule-threshold" label="Threshold">
        {(control) => <Input {...control} inputMode="decimal" value={threshold} onChange={(e) => { setThreshold(e.target.value); touch(); }} />}
      </FormField>
      <FormField id="rule-severity" label="Request severity" hint="Severity 1 converts to an emergency order.">
        {(control) => (
          <Select {...control} value={severity} onChange={(e) => { setSeverity(e.target.value); touch(); }}>
            {[1, 2, 3, 4, 5].map((n) => (
              <option key={n} value={String(n)}>
                {n}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="rule-cooldown" label="Cooldown in minutes">
        {(control) => <Input {...control} inputMode="numeric" value={cooldown} onChange={(e) => { setCooldown(e.target.value); touch(); }} />}
      </FormField>
      {failure ? (
        <div className={styles.formActions}>
          <Failure title="Could not add the rule" problem={failure} />
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" loading={busy}>
          Add rule
        </Button>
      </div>
    </form>
  );
}
