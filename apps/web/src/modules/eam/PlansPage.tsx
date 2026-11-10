import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createPlan,
  describeProblem,
  getAsset,
  listAssets,
  listPlans,
  listRounds,
  newKey,
  type Asset,
  type Counter,
  type IntervalUnit,
  type LineKind,
  type MaintenanceRound,
  type NewPlan,
  type NewPlanLine,
  type Plan,
  type PlanLine,
  type Problem,
} from "./api/client";
import { CODE, CURRENCY, dash, dayOf, kindLabel, PLAN_CODE, sentence } from "./format";
import { Failure, PagedTable } from "./parts";
import styles from "./pages.module.css";
import { useLoad, usePaged } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

function interval(line: PlanLine): string {
  const parts: string[] = [];
  if (line.intervalValue && line.intervalUnit) parts.push(`${line.intervalValue} ${sentence(line.intervalUnit).toLowerCase()}`);
  if (line.counterInterval) parts.push(`${line.counterInterval} on the counter`);
  return parts.join(line.kind === "TIME_AND_COUNTER" ? " and " : " or ") || dash;
}

const lineColumns: DataTableColumn<PlanLine>[] = [
  {
    key: "jobType",
    header: "Job type",
    mono: true,
    render: (row) => (row.roundId ? <Link to={`/eam/rounds/${row.roundId}`}>Round</Link> : row.jobType),
  },
  { key: "kind", header: "Kind", render: (row) => kindLabel[row.kind] ?? row.kind },
  { key: "interval", header: "Every", render: interval },
  { key: "nextDueAt", header: "Next due", mono: true, render: (row) => dayOf(row.nextDueAt) },
  { key: "nextDueReading", header: "Due at reading", align: "end", render: (row) => row.nextDueReading ?? dash },
  { key: "nextDueForecastAt", header: "Counter forecast", mono: true, render: (row) => dayOf(row.nextDueForecastAt) },
];

const planColumns: DataTableColumn<Plan>[] = [
  { key: "code", header: "Plan", mono: true },
  { key: "name", header: "Name" },
  { key: "lines", header: "Lines", align: "end", render: (row) => row.lines.length },
  {
    key: "next",
    header: "Next due",
    mono: true,
    render: (row) => dayOf(row.lines.map((l) => l.nextDueAt).filter((d): d is string => Boolean(d)).sort()[0]),
  },
];

/** Maintenance plans (FR-44.4) with the next due point of each line (8.44.14 Scheduling overview). */
export function PlansPage() {
  const state = usePaged((cursor, signal) => listPlans({ cursor }, signal), "plans");
  const [openId, setOpenId] = useState<string | null>(null);
  const open = state.rows.find((p) => p.id === openId);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Maintenance plans"
        description="Each line schedules from its last completion. A work order is created when the next due date, less the early tolerance, falls inside the horizon."
        actions={
          <Link className={buttonClassName("primary")} to="/eam/plans/new">
            New plan
          </Link>
        }
      />
      <PagedTable
        label="Maintenance plans"
        noun="plans"
        columns={planColumns}
        state={state}
        empty={{ title: "No maintenance plans yet", description: "Create a plan to generate preventive work orders." }}
        onRowOpen={(row) => setOpenId(row.id)}
      />
      {open ? (
        <Card title={`Lines of ${open.code}`}>
          <DataTable label={`Lines of ${open.code}`} columns={lineColumns} rows={open.lines} getRowKey={(row) => row.id} empty={{ title: "No lines" }} />
        </Card>
      ) : null}
    </div>
  );
}

interface LineValues {
  assetId: string;
  /** ROUND lines only. */
  roundId?: string;
  kind: LineKind;
  intervalValue: string;
  intervalUnit: IntervalUnit;
  counterId: string;
  counterInterval: string;
  jobType: string;
  horizonDays: string;
  startAt: string;
}

interface PlanValues {
  code: string;
  name: string;
  currency: string;
  lines: LineValues[];
}

type LineErrors = Partial<Record<keyof LineValues, string>>;

interface PlanErrors {
  code?: string;
  name?: string;
  currency?: string;
  lines: LineErrors[];
}

const usesTime = (kind: LineKind) => kind !== "COUNTER";
const usesCounter = (kind: LineKind) => kind !== "TIME" && kind !== "ROUND";

const emptyLine = (assetId = ""): LineValues => ({
  assetId,
  roundId: "",
  kind: "TIME",
  intervalValue: "",
  intervalUnit: "DAYS",
  counterId: "",
  counterInterval: "",
  jobType: "",
  horizonDays: "90",
  startAt: "",
});

/** The checks the service makes on a plan (FR-44.4), and the request the form sends. */
export function validatePlan(values: PlanValues): { errors: PlanErrors; input?: NewPlan } {
  const errors: PlanErrors = { lines: values.lines.map(() => ({})) };
  const code = values.code.trim().toUpperCase();
  if (!PLAN_CODE.test(code)) errors.code = "Use 2 to 30 characters: A to Z, 0 to 9, a hyphen or an underscore.";
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  const currency = values.currency.trim().toUpperCase();
  if (!CURRENCY.test(currency)) errors.currency = "Enter a three-letter currency code.";
  const lines: NewPlanLine[] = [];
  values.lines.forEach((l, i) => {
    const e = errors.lines[i];
    let line: NewPlanLine;
    if (l.kind === "ROUND") {
      // A round line takes its assets and jobs from the round's stops (FR-44.14).
      if (!l.roundId) e.roundId = "Choose the round.";
      line = { roundId: l.roundId ?? "", kind: l.kind };
    } else {
      if (!l.assetId) e.assetId = "Choose the asset.";
      const jobType = l.jobType.trim().toUpperCase();
      if (!CODE.test(jobType)) e.jobType = "Use 2 to 20 characters: A to Z, 0 to 9 or an underscore.";
      line = { assetId: l.assetId, kind: l.kind, jobType };
    }
    if (usesTime(l.kind)) {
      const n = Number(l.intervalValue);
      if (!/^\d+$/.test(l.intervalValue.trim()) || n < 1) e.intervalValue = "Enter a whole number of 1 or more.";
      line.intervalValue = n;
      line.intervalUnit = l.intervalUnit;
    }
    if (usesCounter(l.kind)) {
      if (!l.counterId) e.counterId = "Choose a counter of this asset.";
      const c = Number(l.counterInterval);
      if (l.counterInterval.trim() === "" || !Number.isFinite(c) || c <= 0) e.counterInterval = "Enter a counter interval above 0.";
      line.counterId = l.counterId;
      line.counterInterval = c;
    }
    const horizon = Number(l.horizonDays);
    if (!/^\d+$/.test(l.horizonDays.trim()) || horizon < 1 || horizon > 730) e.horizonDays = "Enter 1 to 730 days.";
    line.horizonDays = horizon;
    if (l.startAt) {
      if (!/^\d{4}-\d{2}-\d{2}$/.test(l.startAt)) e.startAt = "Enter the date as YYYY-MM-DD.";
      line.startAt = `${l.startAt}T00:00:00Z`;
    }
    lines.push(line);
  });
  const invalid = errors.code || errors.name || errors.currency || errors.lines.some((e) => Object.keys(e).length > 0);
  if (invalid) return { errors };
  return { errors, input: { code, name, currency, lines } };
}

/** Counters of one asset, loaded from the asset detail (plan lines and sequences on a counter). */
export function CounterSelect({ id, assetId, value, error, onChange }: { id: string; assetId: string; value: string; error?: string; onChange(next: string): void }) {
  const state = useLoad<Counter[]>(async (signal) => (assetId ? (await getAsset(assetId, signal)).counters : []), `counters|${assetId}`);
  return (
    <FormField id={id} label="Counter" error={error ?? (state.error ? `Could not load counters (${state.error.code}).` : undefined)}>
      {(control) => (
        <Select {...control} value={value} invalid={Boolean(error)} disabled={!assetId || state.loading} onChange={(e) => onChange(e.target.value)}>
          <option value="">{assetId ? (state.data?.length === 0 ? "This asset has no counters" : "Choose a counter") : "Choose the asset first"}</option>
          {(state.data ?? []).map((c) => (
            <option key={c.id} value={c.id}>
              {c.name} ({c.unit})
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );
}

function LineEditor({
  index,
  line,
  errors,
  assets,
  rounds,
  roundsError,
  canRemove,
  onChange,
  onRemove,
}: {
  index: number;
  line: LineValues;
  errors: LineErrors;
  assets: Asset[];
  rounds: MaintenanceRound[];
  roundsError?: string;
  canRemove: boolean;
  onChange(patch: Partial<LineValues>): void;
  onRemove(): void;
}) {
  const p = `line-${index}`;
  return (
    <fieldset className={styles.lineCard}>
      <legend className={styles.lineTitle}>Line {index + 1}</legend>
      <FormField id={`${p}-kind`} label="Kind">
        {(control) => (
          <Select {...control} value={line.kind} onChange={(e) => onChange({ kind: e.target.value as LineKind })}>
            {(["TIME", "COUNTER", "TIME_OR_COUNTER", "TIME_AND_COUNTER", "ROUND"] as const).map((k) => (
              <option key={k} value={k}>
                {kindLabel[k]}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      {line.kind === "ROUND" ? (
        <FormField id={`${p}-round`} label="Round" hint="Each occurrence is one work order with a job per stop." error={errors.roundId ?? roundsError}>
          {(control) => (
            <Select {...control} value={line.roundId ?? ""} invalid={Boolean(errors.roundId)} onChange={(e) => onChange({ roundId: e.target.value })}>
              <option value="">{rounds.length === 0 ? "No rounds yet" : "Choose a round"}</option>
              {rounds.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.code} {r.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      ) : (
        <>
          <FormField id={`${p}-asset`} label="Asset" error={errors.assetId}>
            {(control) => (
              <Select {...control} value={line.assetId} invalid={Boolean(errors.assetId)} onChange={(e) => onChange({ assetId: e.target.value, counterId: "" })}>
                <option value="">Choose an asset</option>
                {assets.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.assetNumber} {a.name}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id={`${p}-job`} label="Job type" hint="A code such as LUBE or OVERHAUL." error={errors.jobType}>
            {(control) => <Input {...control} value={line.jobType} maxLength={20} invalid={Boolean(errors.jobType)} onChange={(e) => onChange({ jobType: e.target.value })} />}
          </FormField>
        </>
      )}
      {usesTime(line.kind) ? (
        <>
          <FormField id={`${p}-every`} label="Every" error={errors.intervalValue}>
            {(control) => <Input {...control} inputMode="numeric" value={line.intervalValue} invalid={Boolean(errors.intervalValue)} onChange={(e) => onChange({ intervalValue: e.target.value })} />}
          </FormField>
          <FormField id={`${p}-unit`} label="Unit">
            {(control) => (
              <Select {...control} value={line.intervalUnit} onChange={(e) => onChange({ intervalUnit: e.target.value as IntervalUnit })}>
                <option value="DAYS">Days</option>
                <option value="WEEKS">Weeks</option>
                <option value="MONTHS">Months</option>
                <option value="YEARS">Years</option>
              </Select>
            )}
          </FormField>
        </>
      ) : null}
      {usesCounter(line.kind) ? (
        <>
          <CounterSelect id={`${p}-counter`} assetId={line.assetId} value={line.counterId} error={errors.counterId} onChange={(counterId) => onChange({ counterId })} />
          <FormField id={`${p}-counter-interval`} label="Counter interval" hint="For example 500 run hours." error={errors.counterInterval}>
            {(control) => <Input {...control} inputMode="decimal" value={line.counterInterval} invalid={Boolean(errors.counterInterval)} onChange={(e) => onChange({ counterInterval: e.target.value })} />}
          </FormField>
        </>
      ) : null}
      <FormField id={`${p}-horizon`} label="Horizon in days" hint="Work orders are created this far ahead." error={errors.horizonDays}>
        {(control) => <Input {...control} inputMode="numeric" value={line.horizonDays} invalid={Boolean(errors.horizonDays)} onChange={(e) => onChange({ horizonDays: e.target.value })} />}
      </FormField>
      <FormField id={`${p}-start`} label="Plan start" hint="The first interval counts from here. Defaults to today." optional error={errors.startAt}>
        {(control) => <Input {...control} type="date" value={line.startAt} invalid={Boolean(errors.startAt)} onChange={(e) => onChange({ startAt: e.target.value })} />}
      </FormField>
      {canRemove ? (
        <div className={styles.formActions}>
          <Button variant="ghost" size="sm" onClick={onRemove}>
            Remove line {index + 1}
          </Button>
        </div>
      ) : null}
    </fieldset>
  );
}

/** Maintenance plan editor (8.44.6): a plan with time and counter lines. Creating it schedules the lines at once (BR-44.1). */
export function NewPlanPage() {
  const navigate = useNavigate();
  const assets = useLoad((signal) => listAssets({ limit: 200 }, signal), "plan-assets");
  const rounds = useLoad((signal) => listRounds({ limit: 200 }, signal), "plan-rounds");
  const [values, setValues] = useState<PlanValues>({ code: "", name: "", currency: "USD", lines: [emptyLine()] });
  const [errors, setErrors] = useState<PlanErrors>({ lines: [{}] });
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<PlanValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function updateLine(i: number, patch: Partial<LineValues>) {
    update({ lines: values.lines.map((l, j) => (j === i ? { ...l, ...patch } : l)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePlan(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createPlan(input, key.current);
      navigate("/eam/plans");
    } catch (e) {
      const problem = describeProblem(e);
      if (problem.code === "EAM-DUP-001") setErrors({ ...found, code: "A plan with this code already exists." });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New maintenance plan"
        description="Add one line per job. Time lines repeat on the calendar; counter lines follow the asset's usage; round lines schedule a maintenance round."
      />
      <Card>
        {assets.error ? (
          <Failure title="Could not load the assets" problem={assets.error} retry={assets.reload} />
        ) : assets.loading || !assets.data ? (
          <div aria-busy="true">
            <Skeleton height="control" />
          </div>
        ) : assets.data.items.length === 0 ? (
          <Alert tone="info" title="No assets yet">
            Plans schedule work on assets. <Link to="/eam/assets/new">Create an asset</Link> first.
          </Alert>
        ) : (
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField id="plan-code" label="Plan code" hint="For example PM-PUMP-90." error={errors.code}>
              {(control) => <Input {...control} value={values.code} maxLength={30} invalid={Boolean(errors.code)} onChange={(e) => update({ code: e.target.value })} />}
            </FormField>
            <FormField id="plan-name" label="Name" error={errors.name}>
              {(control) => <Input {...control} value={values.name} maxLength={120} invalid={Boolean(errors.name)} onChange={(e) => update({ name: e.target.value })} />}
            </FormField>
            <FormField id="plan-currency" label="Currency" hint="Generated work orders use it." error={errors.currency}>
              {(control) => <Input {...control} value={values.currency} maxLength={3} invalid={Boolean(errors.currency)} onChange={(e) => update({ currency: e.target.value })} />}
            </FormField>
            {values.lines.map((line, i) => (
              <LineEditor
                key={i}
                index={i}
                line={line}
                errors={errors.lines[i] ?? {}}
                assets={assets.data!.items}
                rounds={rounds.data?.items ?? []}
                roundsError={rounds.error ? `Could not load the rounds (${rounds.error.code}).` : undefined}
                canRemove={values.lines.length > 1}
                onChange={(patch) => updateLine(i, patch)}
                onRemove={() => {
                  update({ lines: values.lines.filter((_, j) => j !== i) });
                  setErrors({ ...errors, lines: errors.lines.filter((_, j) => j !== i) });
                }}
              />
            ))}
            <div className={styles.formActions}>
              <Button
                onClick={() => {
                  update({ lines: [...values.lines, emptyLine(values.lines[values.lines.length - 1]?.assetId)] });
                  setErrors({ ...errors, lines: [...errors.lines, {}] });
                }}
              >
                Add line
              </Button>
            </div>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not create the plan" live="alert">
                  <ProblemMessage problem={failure} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Create plan
              </Button>
              <Link className={buttonClassName("ghost")} to="/eam/plans">
                Cancel
              </Link>
            </div>
          </form>
        )}
      </Card>
    </div>
  );
}
