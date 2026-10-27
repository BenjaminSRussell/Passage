import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createNetwork, describeProblem, newKey, type NetworkEdge, type NewNetwork, type Problem, type Resource } from "./api/client";
import { today } from "./format";
import { CODE, DATE, mapFields, optionalWhole, wholeNumber, WEEK_MINUTES } from "./forms";
import styles from "./pages.module.css";
import { ResourceGate } from "./ResourceGate";

export interface EdgeValues {
  from: string;
  to: string;
  transfer: string;
  maxWait: string;
  secondary: string;
}

export interface NetworkValues {
  code: string;
  name: string;
  activeFrom: string;
  activeTo: string;
  edges: EdgeValues[];
}

type Errors = Partial<Record<"code" | "name" | "activeFrom" | "activeTo" | "edges", string>>;

const blankEdge: EdgeValues = { from: "", to: "", transfer: "0", maxWait: "", secondary: "" };

/** Checks that mirror the service (and the domain edge rules), and the request. */
export function validateNetwork(values: NetworkValues): { errors: Errors; input?: NewNetwork } {
  const errors: Errors = {};
  if (!CODE.test(values.code)) errors.code = "Use 1 to 32 capital letters, digits, _ or -.";
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!DATE.test(values.activeFrom)) errors.activeFrom = "Enter the first day the network applies.";
  if (values.activeTo !== "" && (!DATE.test(values.activeTo) || values.activeTo < values.activeFrom)) errors.activeTo = "Enter a day on or after Active from.";
  const edges: NetworkEdge[] = [];
  const seen = new Set<string>();
  for (const [i, e] of values.edges.entries()) {
    const at = `Transition ${i + 1}`;
    const transfer = wholeNumber(e.transfer, 0, WEEK_MINUTES);
    const maxWait = optionalWhole(e.maxWait, 0, WEEK_MINUTES);
    if (e.from === "" || e.to === "") errors.edges = `${at}: choose both resources.`;
    else if (e.from === e.to) errors.edges = `${at}: a resource cannot feed itself.`;
    else if (e.secondary !== "" && (e.secondary === e.from || e.secondary === e.to)) errors.edges = `${at}: the transfer resource must be a third resource.`;
    else if (transfer === null) errors.edges = `${at}: transfer minutes must be 0 to ${WEEK_MINUTES}.`;
    else if (maxWait === null) errors.edges = `${at}: maximum wait must be 0 to ${WEEK_MINUTES} minutes, or blank for none.`;
    else if (seen.has(`${e.from}>${e.to}`)) errors.edges = `${at}: this transition is listed twice.`;
    if (errors.edges) break;
    seen.add(`${e.from}>${e.to}`);
    const edge: NetworkEdge = { fromResourceId: e.from, toResourceId: e.to, transferMinutes: transfer ?? 0, maxWaitMinutes: maxWait ?? null };
    if (e.secondary !== "") edge.secondaryResourceId = e.secondary;
    edges.push(edge);
  }
  if (values.edges.length === 0) errors.edges = "Add at least one allowed transition.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewNetwork = { code: values.code, name, activeFrom: values.activeFrom, edges };
  if (values.activeTo !== "") input.activeTo = values.activeTo;
  return { errors, input };
}

const fieldNames: Record<string, keyof Errors> = { code: "code", name: "name", activeFrom: "activeFrom", activeTo: "activeTo", edges: "edges" };

function EdgeEditor({ index, edge, resources, onChange, onRemove }: { index: number; edge: EdgeValues; resources: Resource[]; onChange(e: EdgeValues): void; onRemove?: () => void }) {
  const options = (blank: string) => [
    <option key="" value="">
      {blank}
    </option>,
    ...resources.map((r) => (
      <option key={r.id} value={r.id}>
        {r.code}
      </option>
    )),
  ];
  const n = index + 1;
  return (
    <fieldset className={styles.edgeRow}>
      <legend className={styles.groupsLegend}>{`Transition ${n}`}</legend>
      <FormField id={`edge-${n}-from`} label="From resource">
        {(control) => (
          <Select {...control} value={edge.from} onChange={(event) => onChange({ ...edge, from: event.target.value })}>
            {options("Choose")}
          </Select>
        )}
      </FormField>
      <FormField id={`edge-${n}-to`} label="To resource">
        {(control) => (
          <Select {...control} value={edge.to} onChange={(event) => onChange({ ...edge, to: event.target.value })}>
            {options("Choose")}
          </Select>
        )}
      </FormField>
      <FormField id={`edge-${n}-transfer`} label="Transfer minutes">
        {(control) => <Input {...control} inputMode="numeric" value={edge.transfer} onChange={(event) => onChange({ ...edge, transfer: event.target.value })} />}
      </FormField>
      <FormField id={`edge-${n}-wait`} label="Maximum wait minutes" optional>
        {(control) => <Input {...control} inputMode="numeric" value={edge.maxWait} onChange={(event) => onChange({ ...edge, maxWait: event.target.value })} />}
      </FormField>
      <FormField id={`edge-${n}-secondary`} label="Transfer resource" optional>
        {(control) => (
          <Select {...control} value={edge.secondary} onChange={(event) => onChange({ ...edge, secondary: event.target.value })}>
            {options("None")}
          </Select>
        )}
      </FormField>
      {onRemove ? (
        <div className={styles.actions}>
          <Button size="sm" variant="ghost" onClick={onRemove} aria-label={`Remove transition ${n}`}>
            Remove
          </Button>
        </div>
      ) : null}
    </fieldset>
  );
}

/** Define a resource network and its allowed transitions. */
export function NewNetworkPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<NetworkValues>({ code: "", name: "", activeFrom: today(), activeTo: "", edges: [{ ...blankEdge }] });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<NetworkValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateNetwork(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const network = await createNetwork(input, key.current);
      navigate(`/scheduling/networks/${network.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = mapFields(problem.fields, fieldNames);
      if (problem.code === "SCHED-0108") fieldErrors.code = "A network with this code already exists.";
      if (problem.code === "SCHED-0107") fieldErrors.edges = "A resource in a transition no longer exists.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New resource network" description="When operation n runs on a resource, operation n+1 may only run on a resource it has a transition to." />
      <Card>
        <ResourceGate need="A network connects at least two resources.">
          {(resources) => (
            <form className={styles.form} onSubmit={submit} noValidate>
              <FormField id="network-code" label="Code" hint="For example MIX-FILL." error={errors.code}>
                {(control) => <Input {...control} maxLength={32} value={values.code} onChange={(event) => update({ code: event.target.value.toUpperCase() })} invalid={Boolean(errors.code)} />}
              </FormField>
              <FormField id="network-name" label="Name" error={errors.name}>
                {(control) => <Input {...control} maxLength={120} value={values.name} onChange={(event) => update({ name: event.target.value })} invalid={Boolean(errors.name)} />}
              </FormField>
              <FormField id="network-from" label="Active from" error={errors.activeFrom}>
                {(control) => <Input {...control} type="date" value={values.activeFrom} onChange={(event) => update({ activeFrom: event.target.value })} invalid={Boolean(errors.activeFrom)} />}
              </FormField>
              <FormField id="network-to" label="Active to" optional error={errors.activeTo}>
                {(control) => <Input {...control} type="date" value={values.activeTo} onChange={(event) => update({ activeTo: event.target.value })} invalid={Boolean(errors.activeTo)} />}
              </FormField>
              {values.edges.map((edge, i) => (
                <EdgeEditor
                  key={i}
                  index={i}
                  edge={edge}
                  resources={resources}
                  onChange={(next) => update({ edges: values.edges.map((e, j) => (j === i ? next : e)) })}
                  onRemove={values.edges.length > 1 ? () => update({ edges: values.edges.filter((_, j) => j !== i) }) : undefined}
                />
              ))}
              <div className={styles.formActions}>
                <Button variant="secondary" size="sm" onClick={() => update({ edges: [...values.edges, { ...blankEdge }] })}>
                  Add transition
                </Button>
                {errors.edges ? (
                  <p role="alert" className={styles.fieldError}>
                    {errors.edges}
                  </p>
                ) : null}
              </div>
              {failure ? (
                <div className={styles.formActions}>
                  <Alert tone="danger" title="Could not create the network" live="alert">
                    {failure.message} ({failure.code})
                  </Alert>
                </div>
              ) : null}
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy}>
                  Create network
                </Button>
                <Link className={buttonClassName("ghost")} to="/scheduling/networks">
                  Cancel
                </Link>
              </div>
            </form>
          )}
        </ResourceGate>
      </Card>
    </div>
  );
}
