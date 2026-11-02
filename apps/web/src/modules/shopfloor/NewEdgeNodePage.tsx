import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createEdgeNode, describeProblem, DUTY_EDGE_CONFIGURE, newKey, type EdgePlatform, type EdgeRing, type NewEdgeNode, type Problem } from "./api/client";
import styles from "./pages.module.css";

interface Values {
  name: string;
  platform: string;
  ring: string;
  connectorSubject: string;
}

type Errors = Partial<Record<keyof Values, string>>;

/** Checks that mirror the service, and the request the form sends. */
export function validateEdgeNode(values: Values): { errors: Errors; input?: NewEdgeNode } {
  const errors: Errors = {};
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!["k3s", "windows", "linux"].includes(values.platform)) errors.platform = "Choose a platform.";
  if (!["canary", "ring1", "all"].includes(values.ring)) errors.ring = "Choose a ring.";
  const subject = values.connectorSubject.trim();
  if (subject.length > 200) errors.connectorSubject = "Enter an identity of up to 200 characters.";
  else if (subject.startsWith("service:")) errors.connectorSubject = "Enter the connector's own identity, not a service actor.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { name, platform: values.platform as EdgePlatform, ring: values.ring as EdgeRing, connectorSubject: subject === "" ? null : subject } };
}

/** Register an edge node (FR-25.19). */
export function NewEdgeNodePage() {
  const navigate = useNavigate();
  const canConfigure = holdsDuty(usePrincipalDuties(), DUTY_EDGE_CONFIGURE);
  const [values, setValues] = useState<Values>({ name: "", platform: "k3s", ring: "all", connectorSubject: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateEdgeNode(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const node = await createEdgeNode(input, key.current);
      navigate(`/shopfloor/edge-nodes/${node.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["name", "platform", "ring", "connectorSubject"] as const) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "SF-0153") fieldErrors.name = "An edge node with this name or connector identity already exists.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (!canConfigure) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Register edge node" />
        <Alert tone="warning" title="You cannot register edge nodes">
          Registering connectors needs the edge fleet duty ({DUTY_EDGE_CONFIGURE}). <Link to="/shopfloor/edge-nodes">Back to the edge fleet</Link>
        </Alert>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Register edge node"
        description="New connectors join ring All unless you pick Canary or Ring 1. Publish a configuration on the node's page; only the connector identity you bind here can pull it and report heartbeats."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="edge-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} maxLength={120} value={values.name} onChange={(event) => update({ name: event.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="edge-platform" label="Platform" error={errors.platform}>
            {(control) => (
              <Select {...control} value={values.platform} onChange={(event) => update({ platform: event.target.value })} invalid={Boolean(errors.platform)}>
                <option value="k3s">k3s</option>
                <option value="windows">Windows</option>
                <option value="linux">Linux</option>
              </Select>
            )}
          </FormField>
          <FormField id="edge-new-ring" label="Rollout ring" error={errors.ring}>
            {(control) => (
              <Select {...control} value={values.ring} onChange={(event) => update({ ring: event.target.value })} invalid={Boolean(errors.ring)}>
                <option value="canary">Canary</option>
                <option value="ring1">Ring 1</option>
                <option value="all">All</option>
              </Select>
            )}
          </FormField>
          <FormField
            id="edge-subject"
            label="Connector identity"
            optional
            hint="The identity the connector signs in as. Leave empty to bind it later; an unbound node refuses pulls and heartbeats."
            error={errors.connectorSubject}
          >
            {(control) => (
              <Input {...control} maxLength={200} value={values.connectorSubject} onChange={(event) => update({ connectorSubject: event.target.value })} invalid={Boolean(errors.connectorSubject)} />
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not register the edge node" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Register edge node
            </Button>
            <Link className={buttonClassName("ghost")} to="/shopfloor/edge-nodes">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
