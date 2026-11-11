import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import {
  describeProblem,
  getParameters,
  newKey,
  updateParameters,
  type Problem,
  type SchedulingParameters,
  type SchedulingParametersPatch,
} from "./api/client";
import { formatDateTime } from "./format";
import { mapFields, wholeNumber } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface ParametersValues {
  defaultMode: string;
  defaultDirection: string;
  defaultFinite: boolean;
  /** As typed. */
  dragSnapMinutes: string;
  propagationOrders: string;
  propagationLevels: string;
}

type Field = "defaultMode" | "defaultDirection" | "dragSnapMinutes" | "propagationOrders" | "propagationLevels" | "form";
type Errors = Partial<Record<Field, string>>;

const fieldNames: Record<string, Field> = {
  defaultMode: "defaultMode",
  defaultDirection: "defaultDirection",
  dragSnapMinutes: "dragSnapMinutes",
  propagationOrders: "propagationOrders",
  propagationLevels: "propagationLevels",
  body: "form",
};

export function valuesOf(p: SchedulingParameters): ParametersValues {
  return {
    defaultMode: p.defaultMode,
    defaultDirection: p.defaultDirection,
    defaultFinite: p.defaultFinite,
    dragSnapMinutes: String(p.dragSnapMinutes),
    propagationOrders: String(p.propagationOrders),
    propagationLevels: String(p.propagationLevels),
  };
}

/** Checks that mirror the service, and the patch the form sends: only what changed. */
export function validateParameters(values: ParametersValues, current: SchedulingParameters): { errors: Errors; patch?: SchedulingParametersPatch } {
  const errors: Errors = {};
  if (values.defaultMode !== "jobs" && values.defaultMode !== "operations") errors.defaultMode = "Choose jobs or operations.";
  if (values.defaultDirection !== "forward" && values.defaultDirection !== "backward") errors.defaultDirection = "Choose forward or backward.";
  const snap = wholeNumber(values.dragSnapMinutes, 1, 60);
  if (snap === null) errors.dragSnapMinutes = "Enter whole minutes from 1 to 60.";
  const orders = wholeNumber(values.propagationOrders, 1, 5000);
  if (orders === null) errors.propagationOrders = "Enter a whole number from 1 to 5000.";
  const levels = wholeNumber(values.propagationLevels, 1, 20);
  if (levels === null) errors.propagationLevels = "Enter a whole number from 1 to 20.";
  if (Object.keys(errors).length > 0) return { errors };
  const patch: SchedulingParametersPatch = {};
  if (values.defaultMode !== current.defaultMode) patch.defaultMode = values.defaultMode as SchedulingParameters["defaultMode"];
  if (values.defaultDirection !== current.defaultDirection) patch.defaultDirection = values.defaultDirection as SchedulingParameters["defaultDirection"];
  if (values.defaultFinite !== current.defaultFinite) patch.defaultFinite = values.defaultFinite;
  if (snap !== current.dragSnapMinutes) patch.dragSnapMinutes = snap!;
  if (orders !== current.propagationOrders) patch.propagationOrders = orders!;
  if (levels !== current.propagationLevels) patch.propagationLevels = levels!;
  if (Object.keys(patch).length === 0) {
    errors.form = "Nothing changed.";
    return { errors };
  }
  return { errors, patch };
}

function Form({ initial, onReload }: { initial: SchedulingParameters; onReload: () => void }) {
  const [current, setCurrent] = useState(initial);
  const [values, setValues] = useState<ParametersValues>(valuesOf(initial));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<number | null>(null);
  const key = useRef(newKey());

  function update(patch: Partial<ParametersValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, patch } = validateParameters(values, current);
    setErrors(found);
    setFailure(null);
    setSaved(null);
    if (!patch) return;
    setBusy(true);
    try {
      const next = await updateParameters(current.version, patch, key.current);
      setCurrent(next);
      setValues(valuesOf(next));
      setSaved(next.version);
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = mapFields(problem.fields, fieldNames);
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="param-mode" label="Default run mode" hint="Used when a run does not name a mode." error={errors.defaultMode}>
          {(control) => (
            <Select {...control} value={values.defaultMode} onChange={(event) => update({ defaultMode: event.target.value })}>
              <option value="jobs">Jobs</option>
              <option value="operations">Operations</option>
            </Select>
          )}
        </FormField>
        <FormField id="param-direction" label="Default direction" hint="Backward schedules from each order's due date." error={errors.defaultDirection}>
          {(control) => (
            <Select {...control} value={values.defaultDirection} onChange={(event) => update({ defaultDirection: event.target.value })}>
              <option value="forward">Forward</option>
              <option value="backward">Backward</option>
            </Select>
          )}
        </FormField>
        <div>
          <Input id="param-finite" type="checkbox" checked={values.defaultFinite} onChange={(event) => update({ defaultFinite: event.target.checked })} />
          <label htmlFor="param-finite">Finite capacity by default</label>
          <span className={styles.note}>Clear it to make infinite-capacity runs the default: jobs may overlap and show overload.</span>
        </div>
        <FormField id="param-snap" label="Gantt drag snap (minutes)" hint="A dragged bar on the workspace snaps to this grid (1 to 60 minutes)." error={errors.dragSnapMinutes}>
          {(control) => <Input {...control} inputMode="numeric" value={values.dragSnapMinutes} onChange={(event) => update({ dragSnapMinutes: event.target.value })} invalid={Boolean(errors.dragSnapMinutes)} />}
        </FormField>
        <FormField
          id="param-orders"
          label="Propagation: most orders"
          hint="A propagated move re-plans at most this many pegged orders (1 to 5000); the rest are reported as SCHED-0007."
          error={errors.propagationOrders}
        >
          {(control) => <Input {...control} inputMode="numeric" value={values.propagationOrders} onChange={(event) => update({ propagationOrders: event.target.value })} invalid={Boolean(errors.propagationOrders)} />}
        </FormField>
        <FormField id="param-levels" label="Propagation: deepest level" hint="How many peg levels a propagated move reaches (1 to 20)." error={errors.propagationLevels}>
          {(control) => <Input {...control} inputMode="numeric" value={values.propagationLevels} onChange={(event) => update({ propagationLevels: event.target.value })} invalid={Boolean(errors.propagationLevels)} />}
        </FormField>
        {errors.form ? (
          <p role="alert" className={styles.fieldError}>
            {errors.form}
          </p>
        ) : null}
        {failure ? (
          <div className={styles.formActions}>
            {failure.code === "SCHED-0140" ? (
              <Alert
                tone="danger"
                title="The parameters changed since you opened them"
                live="alert"
                action={
                  <Button size="sm" onClick={onReload}>
                    Load the latest version
                  </Button>
                }
              >
                <ProblemMessage problem={failure} /> Loading the latest version discards your changes on this screen.
              </Alert>
            ) : (
              <Alert tone="danger" title="Could not save the parameters" live="alert">
                <ProblemMessage problem={failure} /> Changing the parameters needs the scheduling.setup.write duty (Maintain scheduling setup).
              </Alert>
            )}
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save parameters
          </Button>
        </div>
      </form>
      <Alert tone="success" live="status">
        {saved ? `The parameters were saved (version ${saved}).` : ""}
      </Alert>
      <p className={styles.note}>
        {current.updatedAt ? `Last changed ${formatDateTime(current.updatedAt)}, version ${current.version}.` : "Never changed: these are the built-in defaults."}
      </p>
    </>
  );
}

/** Scheduling parameters (PRD 4.23.13): the defaults of runs and moves, the Gantt drag snap and the propagation limits. */
export function ParametersPage() {
  const state = useLoad<SchedulingParameters>((signal) => getParameters(signal), "parameters");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Scheduling parameters"
        description="Defaults for runs that leave a choice out, the drag grid of the Gantt and the limits of a propagated move. Changes apply to runs and moves started after saving; jobs already placed stay where they are."
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the parameters" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : !state.data ? (
        <div aria-busy="true">
          <span className="visually-hidden">Loading the parameters…</span>
          <Skeleton height="tile" />
        </div>
      ) : (
        <Card title="Defaults">
          <Form key={`${state.data.version}`} initial={state.data} onReload={state.reload} />
        </Card>
      )}
    </div>
  );
}
