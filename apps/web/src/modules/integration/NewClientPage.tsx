import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Textarea } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  BURST_MAX,
  BURST_MIN,
  DEFAULT_BURST,
  DEFAULT_QUOTA_PER_MIN,
  DUTY_HUB_CLIENT_WRITE,
  newKey,
  QUOTA_MAX,
  QUOTA_MIN,
  registerClient,
  type NewApiClient,
} from "./api/hubClient";
import { UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemAlert } from "./parts";

export interface ClientValues {
  clientId: string;
  name: string;
  /** One duty name per line (or comma separated); names may contain spaces, such as "Maintain users". */
  scopes: string;
  quotaPerMin: string;
  burst: string;
  sandboxTenantId: string;
  ownerContact: string;
}

type Errors = Partial<Record<keyof ClientValues, string>>;

export const EMPTY_CLIENT: ClientValues = { clientId: "", name: "", scopes: "", quotaPerMin: String(DEFAULT_QUOTA_PER_MIN), burst: String(DEFAULT_BURST), sandboxTenantId: "", ownerContact: "" };

const CLIENT_ID = /^[a-z0-9][a-z0-9._-]{2,63}$/;
/** A duty name in any catalogue style (sales.order.read, VendorInvoicePost, Maintain users); mirrors the hub. */
const SCOPE = /^[A-Za-z](?:[A-Za-z0-9 ._-]{0,98}[A-Za-z0-9._-])?$/;

export function parseScopes(text: string): string[] {
  return [...new Set(text.split(/[\n,]+/).map((s) => s.trim()).filter((s) => s !== ""))];
}

/** The integer in a quota field, or null when it is not a whole number in range. */
export function parseCount(text: string, min: number, max: number): number | null {
  const n = Number(text.trim());
  return text.trim() !== "" && Number.isInteger(n) && n >= min && n <= max ? n : null;
}

/** Checks that mirror the hub (src/clients.ts, src/server.ts), and the request the form sends. */
export function validateClient(v: ClientValues): { errors: Errors; input?: NewApiClient } {
  const errors: Errors = {};
  const clientId = v.clientId.trim();
  if (!CLIENT_ID.test(clientId)) errors.clientId = "Use 3 to 64 characters: a-z, 0-9, dot, underscore or hyphen, starting with a letter or digit.";
  const name = v.name.trim();
  if (name === "" || name.length > 200) errors.name = "Enter a name of at most 200 characters.";
  const scopes = parseScopes(v.scopes);
  const badScope = scopes.find((s) => !SCOPE.test(s));
  if (scopes.length < 1 || scopes.length > 50) errors.scopes = "List 1 to 50 duties, one per line.";
  else if (badScope !== undefined) errors.scopes = `"${badScope}" is not a duty name such as sales.order.read.`;
  const quotaPerMin = parseCount(v.quotaPerMin, QUOTA_MIN, QUOTA_MAX);
  if (quotaPerMin === null) errors.quotaPerMin = `Enter a whole number from ${QUOTA_MIN} to ${QUOTA_MAX}.`;
  const burst = parseCount(v.burst, BURST_MIN, BURST_MAX);
  if (burst === null) errors.burst = `Enter a whole number from ${BURST_MIN} to ${BURST_MAX}.`;
  const sandbox = v.sandboxTenantId.trim().toLowerCase();
  if (sandbox !== "" && !UUID.test(sandbox)) errors.sandboxTenantId = "Enter the sandbox tenant id (a UUID) or leave it empty.";
  const owner = v.ownerContact.trim();
  if (!/^[^\s@]+@[^\s@]+$/.test(owner) || owner.length > 320) errors.ownerContact = "Enter the owner's e-mail address.";
  if (Object.keys(errors).length > 0 || quotaPerMin === null || burst === null) return { errors };
  const input: NewApiClient = { clientId, name, scopes, quotaPerMin, burst, ownerContact: owner };
  if (sandbox !== "") input.sandboxTenantId = sandbox;
  return { errors, input };
}

/** Register an API client (FR-35.8). The Keycloak client itself is provisioned by the platform. */
export function NewClientPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_CLIENT_WRITE);
  const [values, setValues] = useState<ClientValues>(EMPTY_CLIENT);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ClientValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateClient(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await registerClient(input, key.current);
      navigate(`/integration/clients/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "HUB-0409") setErrors({ clientId: "A client with this id is already registered." });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (id: keyof ClientValues, label: string, hint?: string, optional?: boolean, extra?: Record<string, unknown>) => (
    <FormField id={`client-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => <Input {...control} {...extra} value={values[id]} onChange={(event) => update({ [id]: event.target.value } as Partial<ClientValues>)} invalid={Boolean(errors[id])} />}
    </FormField>
  );

  if (!canWrite) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Register API client" />
        <Alert tone="warning" title="You cannot register API clients">
          Registering a client needs the duty {DUTY_HUB_CLIENT_WRITE}.
        </Alert>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Register API client" description="Record a Keycloak client with the duties it may use and its request quota. The secret stays in Keycloak." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("clientId", "Client id", "The Keycloak client id the gateway sees in the token, such as acme-erp.", false, { spellCheck: false, maxLength: 64 })}
          {text("name", "Name", undefined, false, { maxLength: 200 })}
          <FormField id="client-scopes" label="Scopes" hint="Duties the client may use, one per line, such as sales.order.read. You can grant only duties you hold yourself." error={errors.scopes}>
            {(control) => <Textarea {...control} rows={4} mono value={values.scopes} onChange={(event) => update({ scopes: event.target.value })} invalid={Boolean(errors.scopes)} spellCheck={false} />}
          </FormField>
          {text("quotaPerMin", "Requests per minute", `${QUOTA_MIN} to ${QUOTA_MAX}; the default is ${DEFAULT_QUOTA_PER_MIN}.`, false, { inputMode: "numeric" })}
          {text("burst", "Burst", `Requests that may arrive at once, ${BURST_MIN} to ${BURST_MAX}; the default is ${DEFAULT_BURST}.`, false, { inputMode: "numeric" })}
          {text("sandboxTenantId", "Sandbox tenant id", "The tenant the developer portal's test console may use. It must not be your own tenant.", true, { spellCheck: false })}
          {text("ownerContact", "Owner e-mail", undefined, false, { type: "email", autoComplete: "off" })}
          {failure ? (
            <div className={styles.wide}>
              <ProblemAlert problem={failure} title="Could not register the client" />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" disabled={busy}>
              {busy ? "Registering..." : "Register client"}
            </Button>
            <Link className={buttonClassName("secondary")} to="/integration/clients">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
