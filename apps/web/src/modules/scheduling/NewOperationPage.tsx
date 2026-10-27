import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, EmptyState, FormField, Input, PageHeader, Skeleton } from "../../shell/ui";
import { createOperation, describeProblem, listResources, newKey, type NewOperation, type Problem } from "./api/client";
import { fromUtcInput, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface Values {
  orderId: string;
  operNumber: string;
  minutes: string;
  dueAt: string;
  readyAt: string;
  priority: string;
  resourceIds: string[];
}

type Errors = Partial<Record<keyof Values, string>>;

function wholeNumber(value: string, min: number, max: number): number | null {
  const n = Number(value);
  return value.trim() !== "" && Number.isInteger(n) && n >= min && n <= max ? n : null;
}

/** Checks that mirror the service, and the request the form sends. */
export function validateOperation(values: Values): { errors: Errors; input?: NewOperation } {
  const errors: Errors = {};
  const orderId = values.orderId.trim().toLowerCase();
  if (!UUID.test(orderId)) errors.orderId = "Enter the production order id (a UUID).";
  const operNumber = wholeNumber(values.operNumber, 1, 1_000_000);
  if (operNumber === null) errors.operNumber = "Enter a whole number of at least 1.";
  const minutes = wholeNumber(values.minutes, 1, 100_000);
  if (minutes === null) errors.minutes = "Enter 1 to 100,000 minutes.";
  const dueAt = values.dueAt === "" ? undefined : fromUtcInput(values.dueAt);
  if (dueAt === null) errors.dueAt = "Enter a date and time.";
  const readyAt = values.readyAt === "" ? undefined : fromUtcInput(values.readyAt);
  if (readyAt === null) errors.readyAt = "Enter a date and time.";
  const priority = values.priority === "" ? undefined : wholeNumber(values.priority, 0, 1000);
  if (priority === null) errors.priority = "Enter 0 to 1000.";
  if (values.resourceIds.length === 0) errors.resourceIds = "Choose at least one resource.";
  if (Object.keys(errors).length > 0 || operNumber === null || minutes === null) return { errors };
  const input: NewOperation = { orderId, operNumber, minutes, resourceIds: values.resourceIds };
  if (dueAt) input.dueAt = dueAt;
  if (readyAt) input.readyAt = readyAt;
  if (priority !== undefined && priority !== null) input.priority = priority;
  return { errors, input };
}

const fieldNames: Record<string, keyof Values> = {
  orderId: "orderId",
  operNumber: "operNumber",
  minutes: "minutes",
  priority: "priority",
  resourceIds: "resourceIds",
};

/** Add one operation of a production order with its candidate resources (FR-23.2). */
export function NewOperationPage() {
  const navigate = useNavigate();
  const resources = useLoad((signal) => listResources(signal), "resources");
  const [values, setValues] = useState<Values>({ orderId: "", operNumber: "10", minutes: "", dueAt: "", readyAt: "", priority: "", resourceIds: [] });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  /** Checked resources keep the order they were checked in: the first is preferred. */
  function toggle(id: string) {
    update({ resourceIds: values.resourceIds.includes(id) ? values.resourceIds.filter((r) => r !== id) : [...values.resourceIds, id] });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateOperation(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createOperation(input, key.current);
      navigate("/scheduling/operations");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const [name, message] of Object.entries(problem.fields)) {
        const field = fieldNames[name];
        if (field) fieldErrors[field] = message;
      }
      if (problem.code === "SCHED-0103") fieldErrors.operNumber = "This order already has an operation with that number.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const field = (name: "orderId" | "operNumber" | "minutes" | "priority", props: { inputMode?: "numeric" } = {}) =>
    function Control(control: { id: string }) {
      return <Input {...control} {...props} value={values[name]} onChange={(event) => update({ [name]: event.target.value })} invalid={Boolean(errors[name])} />;
    };

  return (
    <div className={styles.stack}>
      <PageHeader title="New operation" description="Production orders from wave 21 will feed operations automatically; until then add them here." />
      <Card>
        {resources.error ? (
          <Alert tone="danger" title="Could not load resources" live="alert" action={<Button size="sm" onClick={resources.reload}>Try again</Button>}>
            {resources.error.message} ({resources.error.code})
          </Alert>
        ) : resources.loading && !resources.data ? (
          <div className={styles.loading} aria-busy="true">
            <span className="visually-hidden">Loading resources…</span>
            <Skeleton height="control" />
            <Skeleton height="control" />
          </div>
        ) : (resources.data ?? []).length === 0 ? (
          <EmptyState
            headingLevel={3}
            title="Add a resource first"
            description="Every operation needs at least one candidate resource."
            action={
              <Link className={buttonClassName("secondary")} to="/scheduling/resources/new">
                Add a resource
              </Link>
            }
          />
        ) : (
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField id="operation-order" label="Order id" hint="The production order this operation belongs to." error={errors.orderId}>
              {field("orderId")}
            </FormField>
            <FormField id="operation-number" label="Operation number" error={errors.operNumber}>
              {field("operNumber", { inputMode: "numeric" })}
            </FormField>
            <FormField id="operation-minutes" label="Minutes" hint="Process time on the resource." error={errors.minutes}>
              {field("minutes", { inputMode: "numeric" })}
            </FormField>
            <FormField id="operation-priority" label="Priority" optional hint="0 to 1000; lower goes first among orders due together." error={errors.priority}>
              {field("priority", { inputMode: "numeric" })}
            </FormField>
            <FormField id="operation-due" label="Due (UTC)" optional error={errors.dueAt}>
              {(control) => <Input {...control} type="datetime-local" value={values.dueAt} onChange={(event) => update({ dueAt: event.target.value })} invalid={Boolean(errors.dueAt)} />}
            </FormField>
            <FormField id="operation-ready" label="Ready (UTC)" optional hint="Earliest start, such as material arrival." error={errors.readyAt}>
              {(control) => <Input {...control} type="datetime-local" value={values.readyAt} onChange={(event) => update({ readyAt: event.target.value })} invalid={Boolean(errors.readyAt)} />}
            </FormField>
            <fieldset className={styles.groups} aria-describedby={errors.resourceIds ? "operation-resources-error" : "operation-resources-hint"}>
              <legend className={styles.groupsLegend}>Candidate resources</legend>
              <p id="operation-resources-hint" className={styles.muted}>
                The scheduler prefers them in the order you check them.
              </p>
              {(resources.data ?? []).map((r) => {
                const rank = values.resourceIds.indexOf(r.id);
                return (
                  <span key={r.id} className={styles.check}>
                    <Input type="checkbox" id={`operation-resource-${r.id}`} checked={rank >= 0} onChange={() => toggle(r.id)} />
                    <label htmlFor={`operation-resource-${r.id}`}>{r.code}</label>
                    {rank >= 0 ? <span className={styles.rank}>{`#${rank + 1}`}</span> : null}
                  </span>
                );
              })}
              {errors.resourceIds ? (
                <p id="operation-resources-error" role="alert" className={styles.fieldError}>
                  {errors.resourceIds}
                </p>
              ) : null}
            </fieldset>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not add the operation" live="alert">
                  {failure.message} ({failure.code})
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Add operation
              </Button>
              <Link className={buttonClassName("ghost")} to="/scheduling/operations">
                Cancel
              </Link>
            </div>
          </form>
        )}
      </Card>
    </div>
  );
}
