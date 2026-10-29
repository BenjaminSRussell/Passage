import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createSequence,
  describeProblem,
  generateOccurrence,
  getSequence,
  listAssets,
  listOccurrences,
  listSequences,
  newKey,
  simulateSequence,
  type Asset,
  type IntervalUnit,
  type MaintenanceSequence,
  type NewSequence,
  type Problem,
  type SequenceOccurrence,
  type SequenceStep,
  type SimulatedOccurrence,
} from "./api/client";
import { CODE, CURRENCY, dash, dayOf, minuteOf, PLAN_CODE, utcFromLocalInput } from "./format";
import { Entry, Failure, PagedTable } from "./parts";
import { CounterSelect } from "./PlansPage";
import styles from "./pages.module.css";
import { useLoad, usePaged } from "./useLoad";

/** A sequence step code: ^[A-Z0-9][A-Z0-9_-]{0,19}$. */
export const STEP_CODE = /^[A-Z0-9][A-Z0-9_-]{0,19}$/;

const unitLabel: Record<IntervalUnit, string> = { DAYS: "days", WEEKS: "weeks", MONTHS: "months", YEARS: "years" };

/** The base interval in words: "every 3 months" or "every 250 on the counter". */
export function intervalText(seq: Pick<MaintenanceSequence, "baseIntervalValue" | "baseIntervalUnit" | "counterId">): string {
  if (seq.baseIntervalUnit) return `Every ${seq.baseIntervalValue} ${unitLabel[seq.baseIntervalUnit]}`;
  return `Every ${seq.baseIntervalValue} on the counter`;
}

const sequenceColumns: DataTableColumn<MaintenanceSequence>[] = [
  { key: "code", header: "Sequence", mono: true },
  { key: "name", header: "Name" },
  { key: "scope", header: "Applies to", render: (row) => (row.assetType ? `Asset type ${row.assetType}` : "One asset") },
  { key: "interval", header: "Base interval", render: (row) => intervalText(row) },
  { key: "steps", header: "Steps", render: (row) => row.steps.map((s) => `${s.code}×${s.multiple}`).join(", ") },
  { key: "lastOccurrenceNumber", header: "Occurrences", align: "end" },
];

/** Maintenance sequences (FR-44.15), newest first. */
export function SequencesPage() {
  const navigate = useNavigate();
  const state = usePaged((cursor, signal) => listSequences({ cursor }, signal), "sequences");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Maintenance sequences"
        description="Steps of growing scope over a base interval: each occurrence performs the biggest step due, which covers the smaller ones it includes."
        actions={
          <Link className={buttonClassName("primary")} to="/eam/sequences/new">
            New sequence
          </Link>
        }
      />
      <PagedTable
        label="Maintenance sequences"
        noun="sequences"
        columns={sequenceColumns}
        state={state}
        empty={{ title: "No maintenance sequences yet", description: "Define one, for example A every time, B every 4th time and C every 12th." }}
        onRowOpen={(row) => navigate(`/eam/sequences/${row.id}`)}
      />
    </div>
  );
}

interface StepValues {
  code: string;
  jobType: string;
  multiple: string;
  hierarchy: string;
  includes: string;
}

interface SequenceValues {
  code: string;
  name: string;
  scope: "ASSET" | "ASSET_TYPE";
  assetId: string;
  assetType: string;
  basis: "TIME" | "COUNTER";
  baseIntervalValue: string;
  baseIntervalUnit: IntervalUnit;
  counterId: string;
  steps: StepValues[];
}

type StepErrors = Partial<Record<keyof StepValues, string>>;
type SequenceErrors = Partial<Record<Exclude<keyof SequenceValues, "steps">, string>> & { steps: StepErrors[]; form?: string };

const emptyStep = (n: number): StepValues => ({ code: "", jobType: "", multiple: n === 0 ? "1" : "", hierarchy: String(n + 1), includes: "" });

const whole = (v: string, min: number, max: number) => /^\d{1,6}$/.test(v.trim()) && Number(v) >= min && Number(v) <= max;

/** The checks the service makes on a sequence (EAM-SEQ-001), and the request the form sends. */
export function validateSequence(values: SequenceValues): { errors: SequenceErrors; input?: NewSequence } {
  const errors: SequenceErrors = { steps: values.steps.map(() => ({})) };
  let bad = false;
  const flag = <K extends Exclude<keyof SequenceValues, "steps">>(k: K, text: string) => {
    errors[k] = text;
    bad = true;
  };
  const code = values.code.trim().toUpperCase();
  if (!PLAN_CODE.test(code)) flag("code", "Use 2 to 30 capital letters, digits, dashes or underscores, for example SEQ-PUMP.");
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) flag("name", "Enter a name of up to 120 characters.");
  const assetType = values.assetType.trim().toUpperCase();
  if (values.scope === "ASSET" && !values.assetId) flag("assetId", "Choose the asset.");
  if (values.scope === "ASSET_TYPE" && !CODE.test(assetType)) flag("assetType", "Use the asset type code, for example PUMP.");
  if (values.scope === "ASSET_TYPE" && values.basis === "COUNTER") flag("basis", "A counter belongs to one asset; an asset type sequence uses a time unit.");
  if (!whole(values.baseIntervalValue, 1, 100000)) flag("baseIntervalValue", "Enter a whole number from 1 to 100,000.");
  if (values.scope === "ASSET" && values.basis === "COUNTER" && !values.counterId) flag("counterId", "Choose the counter.");

  const steps: SequenceStep[] = [];
  const codes = values.steps.map((s) => s.code.trim().toUpperCase());
  const hierarchies = new Set<number>();
  values.steps.forEach((s, i) => {
    const e = errors.steps[i];
    const stepCode = codes[i];
    if (!STEP_CODE.test(stepCode)) e.code = "Use up to 20 capital letters, digits, dashes or underscores.";
    else if (codes.indexOf(stepCode) !== i) e.code = `Step ${stepCode} is listed twice.`;
    const jobType = s.jobType.trim().toUpperCase();
    if (!CODE.test(jobType)) e.jobType = "Use the job type code, for example OVERHAUL.";
    if (!whole(s.multiple, 1, 1000)) e.multiple = "Enter 1 to 1,000.";
    if (!whole(s.hierarchy, 1, 32767)) e.hierarchy = "Enter 1 or more.";
    else if (hierarchies.has(Number(s.hierarchy))) e.hierarchy = "Each step needs its own hierarchy.";
    else hierarchies.add(Number(s.hierarchy));
    const includes = s.includes
      .split(/[\s,]+/)
      .map((c) => c.trim().toUpperCase())
      .filter(Boolean);
    for (const inc of includes) {
      const j = codes.indexOf(inc);
      if (j < 0 || j === i) e.includes = `${inc} is not another step of this sequence.`;
      else if (whole(values.steps[j].hierarchy, 1, 32767) && Number(values.steps[j].hierarchy) >= Number(s.hierarchy)) e.includes = `${inc} must have a lower hierarchy to be included.`;
    }
    if (new Set(includes).size !== includes.length) e.includes = "List each included step once.";
    if (Object.keys(e).length > 0) bad = true;
    steps.push({ code: stepCode, jobType, multiple: Number(s.multiple), hierarchy: Number(s.hierarchy), includes });
  });
  if (values.steps.length === 0 || values.steps.length > 50) {
    errors.form = "A sequence needs 1 to 50 steps.";
    bad = true;
  }
  if (bad) return { errors };
  const input: NewSequence = { code, name, baseIntervalValue: Number(values.baseIntervalValue), steps };
  if (values.scope === "ASSET") input.assetId = values.assetId;
  else input.assetType = assetType;
  if (values.basis === "COUNTER") input.counterId = values.counterId;
  else input.baseIntervalUnit = values.baseIntervalUnit;
  return { errors, input };
}

/** The steps preview: which steps BR-44.10 selects on occurrences 1 to 12. Pure, mirrors domain.SelectSteps. */
export function previewSteps(steps: SequenceStep[], from: number, count: number): SimulatedOccurrence[] {
  const out: SimulatedOccurrence[] = [];
  for (let k = from; k < from + count; k++) {
    const due = steps.filter((s) => s.multiple >= 1 && k % s.multiple === 0);
    if (due.length === 0) {
      out.push({ occurrenceNumber: k, stepCodes: [], jobTypes: [] });
      continue;
    }
    const top = due.reduce((a, b) => (b.hierarchy > a.hierarchy ? b : a));
    const suppressed = new Set([top.code, ...(top.includes ?? [])]);
    const rest = due.filter((s) => !suppressed.has(s.code));
    out.push({ occurrenceNumber: k, stepCodes: [top.code, ...rest.map((s) => s.code)], jobTypes: [top.jobType, ...rest.map((s) => s.jobType)] });
  }
  return out;
}

const occurrenceText = (o: SimulatedOccurrence) => (o.stepCodes.length === 0 ? "Nothing due" : o.stepCodes.join(" + "));

/** Round and sequence editor (8.44.6), sequence part: scope, base interval, and the steps with multiple, hierarchy and includes. */
export function NewSequencePage() {
  const navigate = useNavigate();
  const assets = useLoad((signal) => listAssets({ limit: 200 }, signal), "sequence-assets");
  const [values, setValues] = useState<SequenceValues>({
    code: "",
    name: "",
    scope: "ASSET",
    assetId: "",
    assetType: "",
    basis: "TIME",
    baseIntervalValue: "",
    baseIntervalUnit: "MONTHS",
    counterId: "",
    steps: [emptyStep(0)],
  });
  const [errors, setErrors] = useState<SequenceErrors>({ steps: [{}] });
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<SequenceValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function updateStep(i: number, patch: Partial<StepValues>) {
    update({ steps: values.steps.map((s, j) => (j === i ? { ...s, ...patch } : s)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateSequence(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createSequence(input, key.current);
      navigate(`/eam/sequences/${created.id}`);
    } catch (e) {
      const problem = describeProblem(e);
      if (problem.code === "EAM-DUP-001") setErrors({ ...found, code: "A sequence with this code already exists." });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const { input: draft } = validateSequence(values);
  const preview = draft ? previewSteps(draft.steps, 1, 12) : null;
  const assetList: Asset[] = assets.data?.items ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New maintenance sequence"
        description="Example: A every occurrence, B every 4th including A, C every 12th including A and B, on a base interval of 250 run hours."
      />
      <Card>
        {assets.error ? (
          <Failure title="Could not load the assets" problem={assets.error} retry={assets.reload} />
        ) : assets.loading && !assets.data ? (
          <div aria-busy="true">
            <Skeleton height="control" />
          </div>
        ) : (
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField id="seq-code" label="Sequence code" hint="For example SEQ-PUMP." error={errors.code}>
              {(control) => <Input {...control} value={values.code} maxLength={30} invalid={Boolean(errors.code)} onChange={(e) => update({ code: e.target.value })} />}
            </FormField>
            <FormField id="seq-name" label="Name" error={errors.name}>
              {(control) => <Input {...control} value={values.name} maxLength={120} invalid={Boolean(errors.name)} onChange={(e) => update({ name: e.target.value })} />}
            </FormField>
            <FormField id="seq-scope" label="Applies to" hint="Asset type sequences are templates; work orders come from asset sequences.">
              {(control) => (
                <Select {...control} value={values.scope} onChange={(e) => update({ scope: e.target.value as SequenceValues["scope"], counterId: "" })}>
                  <option value="ASSET">One asset</option>
                  <option value="ASSET_TYPE">An asset type</option>
                </Select>
              )}
            </FormField>
            {values.scope === "ASSET" ? (
              <FormField id="seq-asset" label="Asset" error={errors.assetId}>
                {(control) => (
                  <Select {...control} value={values.assetId} invalid={Boolean(errors.assetId)} onChange={(e) => update({ assetId: e.target.value, counterId: "" })}>
                    <option value="">{assetList.length === 0 ? "No assets yet" : "Choose an asset"}</option>
                    {assetList.map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.assetNumber} {a.name}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            ) : (
              <FormField id="seq-type" label="Asset type" error={errors.assetType}>
                {(control) => <Input {...control} value={values.assetType} maxLength={20} invalid={Boolean(errors.assetType)} onChange={(e) => update({ assetType: e.target.value })} />}
              </FormField>
            )}
            <FormField id="seq-basis" label="Base interval on" error={errors.basis}>
              {(control) => (
                <Select {...control} value={values.basis} invalid={Boolean(errors.basis)} onChange={(e) => update({ basis: e.target.value as SequenceValues["basis"] })}>
                  <option value="TIME">The calendar</option>
                  <option value="COUNTER">A counter of the asset</option>
                </Select>
              )}
            </FormField>
            <FormField id="seq-every" label="Base interval" hint="Step multiples count in these intervals." error={errors.baseIntervalValue}>
              {(control) => (
                <Input {...control} inputMode="numeric" value={values.baseIntervalValue} invalid={Boolean(errors.baseIntervalValue)} onChange={(e) => update({ baseIntervalValue: e.target.value })} />
              )}
            </FormField>
            {values.basis === "TIME" ? (
              <FormField id="seq-unit" label="Unit">
                {(control) => (
                  <Select {...control} value={values.baseIntervalUnit} onChange={(e) => update({ baseIntervalUnit: e.target.value as IntervalUnit })}>
                    <option value="DAYS">Days</option>
                    <option value="WEEKS">Weeks</option>
                    <option value="MONTHS">Months</option>
                    <option value="YEARS">Years</option>
                  </Select>
                )}
              </FormField>
            ) : values.scope === "ASSET" ? (
              <CounterSelect id="seq-counter" assetId={values.assetId} value={values.counterId} error={errors.counterId} onChange={(counterId) => update({ counterId })} />
            ) : null}
            {values.steps.map((step, i) => {
              const e = errors.steps[i] ?? {};
              const p = `step-${i}`;
              return (
                <fieldset key={i} className={styles.lineCard}>
                  <legend className={styles.lineTitle}>Step {i + 1}</legend>
                  <FormField id={`${p}-code`} label="Step code" error={e.code}>
                    {(control) => <Input {...control} value={step.code} maxLength={20} invalid={Boolean(e.code)} onChange={(ev) => updateStep(i, { code: ev.target.value })} />}
                  </FormField>
                  <FormField id={`${p}-job`} label="Job type" error={e.jobType}>
                    {(control) => <Input {...control} value={step.jobType} maxLength={20} invalid={Boolean(e.jobType)} onChange={(ev) => updateStep(i, { jobType: ev.target.value })} />}
                  </FormField>
                  <FormField id={`${p}-multiple`} label="Every nth occurrence" hint="1 = every time, 4 = every 4th." error={e.multiple}>
                    {(control) => <Input {...control} inputMode="numeric" value={step.multiple} invalid={Boolean(e.multiple)} onChange={(ev) => updateStep(i, { multiple: ev.target.value })} />}
                  </FormField>
                  <FormField id={`${p}-hierarchy`} label="Hierarchy" hint="When several steps are due, the highest is performed." error={e.hierarchy}>
                    {(control) => <Input {...control} inputMode="numeric" value={step.hierarchy} invalid={Boolean(e.hierarchy)} onChange={(ev) => updateStep(i, { hierarchy: ev.target.value })} />}
                  </FormField>
                  <FormField id={`${p}-includes`} label="Includes steps" hint="Codes of smaller steps it covers, for example A, B." optional error={e.includes}>
                    {(control) => <Input {...control} value={step.includes} invalid={Boolean(e.includes)} onChange={(ev) => updateStep(i, { includes: ev.target.value })} />}
                  </FormField>
                  {values.steps.length > 1 ? (
                    <div className={styles.formActions}>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => {
                          update({ steps: values.steps.filter((_, j) => j !== i) });
                          setErrors({ ...errors, steps: errors.steps.filter((_, j) => j !== i) });
                        }}
                      >
                        Remove step {i + 1}
                      </Button>
                    </div>
                  ) : null}
                </fieldset>
              );
            })}
            <div className={styles.formActions}>
              <Button
                onClick={() => {
                  update({ steps: [...values.steps, emptyStep(values.steps.length)] });
                  setErrors({ ...errors, steps: [...errors.steps, {}] });
                }}
                disabled={values.steps.length >= 50}
              >
                Add step
              </Button>
            </div>
            {errors.form ? <p className={styles.fieldError}>{errors.form}</p> : null}
            <div className={styles.formActions}>
              {preview ? (
                <p className={styles.note} aria-live="polite">
                  First 12 occurrences: {preview.map(occurrenceText).join(", ")}
                </p>
              ) : (
                <p className={styles.note}>Complete the steps to preview the first 12 occurrences.</p>
              )}
            </div>
            {failure ? (
              <div className={styles.formActions}>
                <Failure title="Could not create the sequence" problem={failure} />
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Create sequence
              </Button>
              <Link className={buttonClassName("ghost")} to="/eam/sequences">
                Cancel
              </Link>
            </div>
          </form>
        )}
      </Card>
    </div>
  );
}

const stepColumns: DataTableColumn<SequenceStep>[] = [
  { key: "code", header: "Step", mono: true },
  { key: "jobType", header: "Job type", mono: true },
  { key: "multiple", header: "Every nth", align: "end" },
  { key: "hierarchy", header: "Hierarchy", align: "end" },
  { key: "includes", header: "Includes", mono: true, render: (row) => (row.includes && row.includes.length > 0 ? row.includes.join(", ") : dash) },
];

const simulationColumns: DataTableColumn<SimulatedOccurrence & { id: string }>[] = [
  { key: "occurrenceNumber", header: "Occurrence", align: "end" },
  { key: "steps", header: "Performed", mono: true, render: (row) => occurrenceText(row) },
  { key: "jobs", header: "Jobs", mono: true, render: (row) => (row.jobTypes.length > 0 ? row.jobTypes.join(", ") : dash) },
];

const occurrenceColumns: DataTableColumn<SequenceOccurrence>[] = [
  { key: "occurrenceNumber", header: "Occurrence", align: "end" },
  { key: "steps", header: "Performed", mono: true, render: (row) => occurrenceText(row) },
  { key: "jobs", header: "Jobs", mono: true, render: (row) => (row.jobTypes.length > 0 ? row.jobTypes.join(", ") : dash) },
  {
    key: "workOrderId",
    header: "Work order",
    render: (row) => (row.workOrderId ? <Link to={`/eam/work-orders/${row.workOrderId}`}>Open work order</Link> : dash),
  },
  { key: "createdAt", header: "Generated", mono: true, render: (row) => minuteOf(row.createdAt) },
  { key: "actor", header: "By", mono: true },
];

/** Messages for the occurrence rules, so the user sees what to do next. */
function occurrenceProblemTitle(problem: Problem): string {
  switch (problem.code) {
    case "EAM-SEQ-002":
      return "Finish or cancel the previous occurrence's work order first";
    case "EAM-AST-001":
      return "The asset must be active to receive work orders";
    default:
      return "Could not generate the occurrence";
  }
}

/** One sequence: steps, the next 12 occurrences, generation of the next one, and what was generated. */
export function SequenceDetailPage() {
  const { id = "" } = useParams();
  const [round, setRound] = useState(0);
  const state = useLoad((signal) => getSequence(id, signal), `${id}|${round}`);
  const simulation = useLoad((signal) => simulateSequence(id, 12, signal), `sim|${id}|${round}`);
  const occurrences = useLoad((signal) => listOccurrences(id, signal), `occ|${id}|${round}`);
  const [currency, setCurrency] = useState("USD");
  const [dueAt, setDueAt] = useState("");
  const [fieldErrors, setFieldErrors] = useState<{ currency?: string; dueAt?: string }>({});
  const [notice, setNotice] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function generate(event: FormEvent) {
    event.preventDefault();
    setNotice(null);
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
      const occ = await generateOccurrence(id, due ? { currency: cur, dueAt: due } : { currency: cur }, key.current);
      key.current = newKey();
      setNotice(
        occ.stepCodes.length === 0
          ? `Occurrence ${occ.occurrenceNumber}: no step was due, so no work order was opened.`
          : `Occurrence ${occ.occurrenceNumber}: ${occurrenceText(occ)}. A preventive work order was opened for ${occ.jobTypes[0]}.`,
      );
      setRound((n) => n + 1);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Maintenance sequence" />
        {state.error.code === "EAM-NF-001" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="We couldn't find this sequence"
              description="It may belong to another tenant, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/eam/sequences">
                  Back to sequences
                </Link>
              }
            />
          </Card>
        ) : (
          <Failure title="Could not load the sequence" problem={state.error} retry={state.reload} />
        )}
      </div>
    );
  }
  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Maintenance sequence" />
        <Skeleton width="3/4" />
        <Skeleton width="1/2" />
      </div>
    );
  }
  const seq = state.data!;

  return (
    <div className={styles.stack}>
      <PageHeader title={seq.name} description={`${seq.code} · ${intervalText(seq)}`} />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Card title="Details">
        <dl className={styles.details}>
          <Entry term="Applies to">
            {seq.assetId ? <Link to={`/eam/assets/${seq.assetId}`}>One asset</Link> : `Asset type ${seq.assetType ?? dash}`}
          </Entry>
          <Entry term="Base interval">{intervalText(seq)}</Entry>
          <Entry term="Counter" mono>
            {seq.counterId ?? dash}
          </Entry>
          <Entry term="Occurrences generated">{seq.lastOccurrenceNumber}</Entry>
          <Entry term="Created" mono>
            {dayOf(seq.createdAt)}
          </Entry>
        </dl>
      </Card>
      <Card title="Steps">
        <DataTable label="Steps" columns={stepColumns} rows={seq.steps} getRowKey={(row) => row.code} />
      </Card>
      <Card title="Next 12 occurrences">
        {simulation.error ? (
          <Failure title="Could not simulate the sequence" problem={simulation.error} retry={simulation.reload} />
        ) : (
          <DataTable
            label="Next 12 occurrences"
            columns={simulationColumns}
            rows={(simulation.data ?? []).map((o) => ({ ...o, id: String(o.occurrenceNumber) }))}
            getRowKey={(row) => row.id}
            loading={simulation.loading}
            empty={{ title: "Nothing to simulate" }}
          />
        )}
      </Card>
      <Card title="Generate the next occurrence">
        {seq.assetId ? (
          <form className={styles.form} onSubmit={generate} noValidate aria-label="Generate the next occurrence">
            <p className={`${styles.note} ${styles.formActions}`}>
              Occurrence {seq.lastOccurrenceNumber + 1} opens a preventive work order for the step it performs. The previous occurrence's work order must be finished or canceled first.
            </p>
            <FormField id="occ-currency" label="Work order currency" error={fieldErrors.currency}>
              {(control) => <Input {...control} value={currency} maxLength={3} invalid={Boolean(fieldErrors.currency)} onChange={(e) => { setCurrency(e.target.value); key.current = newKey(); }} />}
            </FormField>
            <FormField id="occ-due" label="Due (UTC)" optional error={fieldErrors.dueAt}>
              {(control) => <Input {...control} type="datetime-local" value={dueAt} invalid={Boolean(fieldErrors.dueAt)} onChange={(e) => { setDueAt(e.target.value); key.current = newKey(); }} />}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Failure title={occurrenceProblemTitle(failure)} problem={failure} />
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Generate occurrence {seq.lastOccurrenceNumber + 1}
              </Button>
            </div>
          </form>
        ) : (
          <p className={styles.note}>This sequence is a template for asset type {seq.assetType}. Create an asset sequence to generate work orders.</p>
        )}
      </Card>
      <Card title="Generated occurrences">
        {occurrences.error ? (
          <Failure title="Could not load the occurrences" problem={occurrences.error} retry={occurrences.reload} />
        ) : (
          <DataTable
            label="Generated occurrences"
            columns={occurrenceColumns}
            rows={occurrences.data ?? []}
            getRowKey={(row) => row.id}
            loading={occurrences.loading}
            empty={{ title: "No occurrence generated yet", description: seq.assetId ? "Generate the first one above." : undefined }}
          />
        )}
      </Card>
    </div>
  );
}
