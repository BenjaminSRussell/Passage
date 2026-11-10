import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, EmptyState, FormField, Input, PageHeader, Select, Skeleton, Textarea } from "../../shell/ui";
import { createOperation, describeProblem, listNetworks, listResources, newKey, type NewOperation, type Problem } from "./api/client";
import { fromUtcInput, UUID } from "./format";
import { CODE, compareDecimal, decimalText, optionalWhole, positive, wholeNumber, WEEK_MINUTES } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { OrderSelect } from "../mfg-shared/orders";

/** The service books at most this many secondary resources per operation (BR-23.4). */
export const MAX_SECONDARIES = 5;

export interface Values {
  orderId: string;
  operNumber: string;
  minutes: string;
  dueAt: string;
  readyAt: string;
  priority: string;
  resourceIds: string[];
  secondaryResourceIds: string[];
  setupGroup: string;
  setupMinutes: string;
  queueBeforeMinutes: string;
  queueAfterMinutes: string;
  quantity: string;
  overlapQuantity: string;
  attributes: string;
  networkId: string;
}

type Errors = Partial<Record<keyof Values, string>>;

export const emptyOperation: Values = {
  orderId: "",
  operNumber: "10",
  minutes: "",
  dueAt: "",
  readyAt: "",
  priority: "",
  resourceIds: [],
  secondaryResourceIds: [],
  setupGroup: "",
  setupMinutes: "",
  queueBeforeMinutes: "",
  queueAfterMinutes: "",
  quantity: "",
  overlapQuantity: "",
  attributes: "",
  networkId: "",
};

/**
 * Reads one attribute per line as name = value (for example color = white),
 * the values capacity block conditions match on (FR-23.17). campaignKey is
 * the key campaign proposals group the order by (FR-23.14).
 */
export function parseAttributes(text: string): { attributes: Record<string, string>; error?: string } {
  const attributes: Record<string, string> = {};
  const lines = text.split("\n").map((l) => l.trim()).filter((l) => l !== "");
  for (const [i, line] of lines.entries()) {
    const at = `Line ${i + 1}`;
    const eq = line.indexOf("=");
    if (eq < 0) return { attributes, error: `${at}: write name = value.` };
    const name = line.slice(0, eq).trim();
    const value = line.slice(eq + 1).trim();
    if (name === "" || name.length > 64) return { attributes, error: `${at}: the name is 1 to 64 characters.` };
    if (value.length > 120) return { attributes, error: `${at}: the value is at most 120 characters.` };
    if (name === "campaignKey" && value.length > 64) return { attributes, error: `${at}: a campaign key is at most 64 characters.` };
    if (name in attributes) return { attributes, error: `${at}: ${name} is listed twice.` };
    attributes[name] = value;
  }
  if (Object.keys(attributes).length > 20) return { attributes, error: "Use at most 20 attributes." };
  return { attributes };
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
  if (values.secondaryResourceIds.length > MAX_SECONDARIES) errors.secondaryResourceIds = `Choose at most ${MAX_SECONDARIES} secondary resources.`;
  else if (values.secondaryResourceIds.some((id) => values.resourceIds.includes(id))) errors.secondaryResourceIds = "A candidate resource cannot also be a secondary resource.";
  const setupGroup = values.setupGroup.trim();
  if (setupGroup !== "" && !CODE.test(setupGroup)) errors.setupGroup = "Use 1 to 32 capital letters, digits, _ or -.";
  const setupMinutes = optionalWhole(values.setupMinutes, 0, WEEK_MINUTES);
  if (setupMinutes === null) errors.setupMinutes = `Enter 0 to ${WEEK_MINUTES.toLocaleString("en-US")} minutes.`;
  const queueBefore = optionalWhole(values.queueBeforeMinutes, 0, WEEK_MINUTES);
  if (queueBefore === null) errors.queueBeforeMinutes = `Enter 0 to ${WEEK_MINUTES.toLocaleString("en-US")} minutes.`;
  const queueAfter = optionalWhole(values.queueAfterMinutes, 0, WEEK_MINUTES);
  if (queueAfter === null) errors.queueAfterMinutes = `Enter 0 to ${WEEK_MINUTES.toLocaleString("en-US")} minutes.`;
  const quantity = values.quantity.trim();
  if (quantity !== "" && (decimalText(quantity, 4, 14) === null || !positive(quantity))) errors.quantity = "Enter a quantity above zero with at most 4 decimals.";
  const overlap = values.overlapQuantity.trim();
  if (overlap !== "") {
    if (decimalText(overlap, 4, 14) === null || !positive(overlap)) errors.overlapQuantity = "Enter a quantity above zero with at most 4 decimals.";
    else if (quantity === "") errors.overlapQuantity = "An overlap needs the quantity above.";
    else if (!errors.quantity && compareDecimal(overlap, quantity) > 0) errors.overlapQuantity = "The overlap cannot exceed the quantity.";
  }
  const { attributes, error: attributesError } = parseAttributes(values.attributes);
  if (attributesError) errors.attributes = attributesError;
  if (Object.keys(errors).length > 0 || operNumber === null || minutes === null) return { errors };
  const input: NewOperation = { orderId, operNumber, minutes, resourceIds: values.resourceIds };
  if (dueAt) input.dueAt = dueAt;
  if (readyAt) input.readyAt = readyAt;
  if (priority !== undefined && priority !== null) input.priority = priority;
  if (values.secondaryResourceIds.length > 0) input.secondaryResourceIds = values.secondaryResourceIds;
  if (setupGroup !== "") input.setupGroup = setupGroup;
  if (setupMinutes !== undefined && setupMinutes !== null) input.setupMinutes = setupMinutes;
  if (queueBefore !== undefined && queueBefore !== null) input.queueBeforeMinutes = queueBefore;
  if (queueAfter !== undefined && queueAfter !== null) input.queueAfterMinutes = queueAfter;
  if (quantity !== "") input.quantity = quantity;
  if (overlap !== "") input.overlapQuantity = overlap;
  if (Object.keys(attributes).length > 0) input.attributes = attributes;
  if (values.networkId !== "") input.networkId = values.networkId;
  return { errors, input };
}

const fieldNames: Record<string, keyof Values> = {
  orderId: "orderId",
  operNumber: "operNumber",
  minutes: "minutes",
  priority: "priority",
  resourceIds: "resourceIds",
  secondaryResourceIds: "secondaryResourceIds",
  setupGroup: "setupGroup",
  setupMinutes: "setupMinutes",
  queueBeforeMinutes: "queueBeforeMinutes",
  queueAfterMinutes: "queueAfterMinutes",
  quantity: "quantity",
  overlapQuantity: "overlapQuantity",
  attributes: "attributes",
  networkId: "networkId",
};

/**
 * Add one operation of a production order with its candidate resources
 * (FR-23.2), and what the scheduler's constraints need: the setup group and
 * route setup for changeovers (FR-23.15), attributes for capacity blocks
 * (FR-23.17) and the resource network of the step into it (FR-23.13).
 */
export function NewOperationPage() {
  const navigate = useNavigate();
  const setup = useLoad(async (signal) => {
    const [resources, networks] = await Promise.all([listResources(signal), listNetworks(signal)]);
    return { resources, networks };
  }, "operation-setup");
  const resources = { ...setup, data: setup.data?.resources };
  const networks = setup.data?.networks ?? [];
  const [values, setValues] = useState<Values>(emptyOperation);
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
    const has = values.resourceIds.includes(id);
    update({
      resourceIds: has ? values.resourceIds.filter((r) => r !== id) : [...values.resourceIds, id],
      secondaryResourceIds: values.secondaryResourceIds.filter((r) => r !== id),
    });
  }

  function toggleSecondary(id: string) {
    const has = values.secondaryResourceIds.includes(id);
    update({ secondaryResourceIds: has ? values.secondaryResourceIds.filter((r) => r !== id) : [...values.secondaryResourceIds, id] });
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
      if (problem.code === "SCHED-0109") fieldErrors.networkId = "This resource network no longer exists.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const field = (name: "orderId" | "operNumber" | "minutes" | "priority" | "setupGroup" | "setupMinutes" | "queueBeforeMinutes" | "queueAfterMinutes" | "quantity" | "overlapQuantity", props: { inputMode?: "numeric" | "decimal" } = {}) =>
    function Control(control: { id: string }) {
      return <Input {...control} {...props} value={values[name]} onChange={(event) => update({ [name]: event.target.value })} invalid={Boolean(errors[name])} />;
    };

  return (
    <div className={styles.stack}>
      <PageHeader title="New operation" description="Production orders will feed operations automatically; until then, add them here." />
      <Card>
        {resources.error ? (
          <Alert tone="danger" title="Could not load resources and networks" live="alert" action={<Button size="sm" onClick={resources.reload}>Try again</Button>}>
            <ProblemMessage problem={resources.error} />
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
            <FormField id="operation-order" label="Production order" hint="The production order this operation belongs to." error={errors.orderId}>
              {(control) => <OrderSelect {...control} value={values.orderId} onChange={(v) => update({ orderId: v })} />}
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
            <fieldset className={styles.groups} aria-describedby={errors.secondaryResourceIds ? "operation-secondary-error" : "operation-secondary-hint"}>
              <legend className={styles.groupsLegend}>Secondary resources (optional)</legend>
              <p id="operation-secondary-hint" className={styles.muted}>
                Tools or crews booked for the whole job alongside the machine. The scheduler skips a slot when one of them is busy or closed.
              </p>
              {(resources.data ?? []).map((r) => {
                const candidate = values.resourceIds.includes(r.id);
                return (
                  <span key={r.id} className={styles.check}>
                    <Input
                      type="checkbox"
                      id={`operation-secondary-${r.id}`}
                      checked={values.secondaryResourceIds.includes(r.id)}
                      disabled={candidate}
                      onChange={() => toggleSecondary(r.id)}
                    />
                    <label htmlFor={`operation-secondary-${r.id}`}>{candidate ? `${r.code} (candidate)` : `${r.code} (secondary)`}</label>
                  </span>
                );
              })}
              {errors.secondaryResourceIds ? (
                <p id="operation-secondary-error" role="alert" className={styles.fieldError}>
                  {errors.secondaryResourceIds}
                </p>
              ) : null}
            </fieldset>
            <FormField
              id="operation-setup-group"
              label="Setup group"
              optional
              hint="Changeovers between setup groups come from the resource's setup matrix."
              error={errors.setupGroup}
            >
              {field("setupGroup")}
            </FormField>
            <FormField id="operation-setup-minutes" label="Route setup (minutes)" optional hint="Used when the matrix has no entry for a changeover." error={errors.setupMinutes}>
              {field("setupMinutes", { inputMode: "numeric" })}
            </FormField>
            <FormField id="operation-queue-before" label="Queue before (minutes)" optional hint="Elapsed wait in front of the operation; no resource is booked." error={errors.queueBeforeMinutes}>
              {field("queueBeforeMinutes", { inputMode: "numeric" })}
            </FormField>
            <FormField id="operation-queue-after" label="Queue after (minutes)" optional hint="Elapsed wait behind the operation, before the next one may start." error={errors.queueAfterMinutes}>
              {field("queueAfterMinutes", { inputMode: "numeric" })}
            </FormField>
            <FormField id="operation-quantity" label="Quantity" optional hint="The order quantity this operation works on; needed for an overlap." error={errors.quantity}>
              {field("quantity", { inputMode: "decimal" })}
            </FormField>
            <FormField
              id="operation-overlap"
              label="Overlap quantity"
              optional
              hint="The next operation may start once this part of the quantity is through, instead of at the end."
              error={errors.overlapQuantity}
            >
              {field("overlapQuantity", { inputMode: "decimal" })}
            </FormField>
            <FormField
              id="operation-network"
              label="Resource network"
              optional
              hint="Limits which resources this operation may follow the previous one on, with transfer and maximum wait."
              error={errors.networkId}
            >
              {(control) => (
                <Select {...control} value={values.networkId} onChange={(event) => update({ networkId: event.target.value })} invalid={Boolean(errors.networkId)}>
                  <option value="">No network</option>
                  {networks.map((n) => (
                    <option key={n.id} value={n.id}>
                      {`${n.code} · ${n.name}`}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField
              id="operation-attributes"
              label="Attributes"
              optional
              hint="One per line as name = value, for example color = white. Capacity blocks match on these; campaignKey = a formula family groups the order in campaign proposals."
              error={errors.attributes}
            >
              {(control) => (
                <Textarea {...control} rows={3} mono value={values.attributes} onChange={(event) => update({ attributes: event.target.value })} invalid={Boolean(errors.attributes)} />
              )}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not add the operation" live="alert">
                  <ProblemMessage problem={failure} />
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
