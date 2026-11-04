import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  AUTH_TYPES,
  createRestSubscription,
  DUTY_HUB_WRITE,
  newKey,
  SYNC_METHODS,
  type AuthInput,
  type AuthType,
  type ClientErrorPolicy,
  type NewRestSubscription,
  type SyncMethod,
} from "./api/hubClient";
import { UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemAlert } from "./parts";

export interface SyncValues {
  name: string;
  /** One event type pattern per line or comma separated. */
  patterns: string;
  outboundMappingId: string;
  method: SyncMethod;
  url: string;
  responseKeyPath: string;
  /** Path with one [*] to each line's key in the response, such as $.data.lines[*].id. */
  lineKeyPath: string;
  /** Optional: the field of each pushed line (Lines[*].LineNum) and of each response line that carry the same value. */
  lineBodyPath: string;
  lineResponsePath: string;
  onClientError: ClientErrorPolicy;
  authType: AuthType;
  username: string;
  password: string;
  token: string;
  headerName: string;
  value: string;
  tokenUrl: string;
  clientId: string;
  clientSecret: string;
  scope: string;
}

type Errors = Partial<Record<keyof SyncValues, string>>;

export const EMPTY_SYNC: SyncValues = {
  name: "",
  patterns: "dev.passage.sales.order.confirmed.v1",
  outboundMappingId: "",
  method: "POST",
  url: "",
  responseKeyPath: "",
  lineKeyPath: "",
  lineBodyPath: "",
  lineResponsePath: "",
  onClientError: "dead_letter",
  authType: "basic",
  username: "",
  password: "",
  token: "",
  headerName: "X-Api-Key",
  value: "",
  tokenUrl: "",
  clientId: "",
  clientSecret: "",
  scope: "",
};

// A path with exactly one [*]: lines[*].id, $.data.lines[*].id, $[*].
const ELEMENT_PATH = /^(\$|\$?\.?[A-Za-z_][A-Za-z0-9_-]*(?:(?:\.[A-Za-z_][A-Za-z0-9_-]*|\[\d+\]))*)\[\*\]((?:\.[A-Za-z_][A-Za-z0-9_-]*|\[\d+\])*)$/;
const PATH = /^(\$)?(\.?[A-Za-z_][A-Za-z0-9_-]*|\[\d+\])(\.[A-Za-z_][A-Za-z0-9_-]*|\[\d+\])*$/;

/** Checks that mirror the hub (src/restPush.ts parseRestInput), and the request the form sends. */
export function validateSync(v: SyncValues): { errors: Errors; input?: NewRestSubscription } {
  const errors: Errors = {};
  const name = v.name.trim();
  if (name.length < 1 || name.length > 200) errors.name = "Enter a name, up to 200 characters.";
  const patterns = [...new Set(v.patterns.split(/[\n,]+/).map((p) => p.trim()).filter((p) => p !== ""))];
  if (patterns.length < 1 || patterns.length > 50) errors.patterns = "Enter 1 to 50 event types, one per line. Use * for one segment and a trailing ** for the rest.";
  if (!UUID.test(v.outboundMappingId.trim())) errors.outboundMappingId = "Enter the outbound mapping id (a UUID from the migration workspace).";
  const url = v.url.trim();
  const origin = /^https?:\/\/[^/?#]*/i.exec(url)?.[0];
  if (!origin) errors.url = "Enter a full http(s) address, such as https://erp.example.com/api/salesorders.";
  else if (/[{}]/.test(origin)) errors.url = "Placeholders such as {CustAccount} are allowed in the path and query, not in the host.";
  else if (url.includes("#")) errors.url = "The address must not contain a fragment (#).";
  const path = v.responseKeyPath.trim();
  if (path !== "" && !PATH.test(path)) errors.responseKeyPath = "Use a path such as $.data.id or orders[0].id.";
  const lineKeyPath = v.lineKeyPath.trim();
  const lineBodyPath = v.lineBodyPath.trim();
  const lineResponsePath = v.lineResponsePath.trim();
  if (lineKeyPath !== "") {
    if (path === "") errors.lineKeyPath = "Line keys are recorded with the header's key: fill in the legacy key in the response first.";
    else if (!ELEMENT_PATH.test(lineKeyPath)) errors.lineKeyPath = "Use a path with one [*], such as $.data.lines[*].id.";
  }
  if (lineBodyPath !== "" || lineResponsePath !== "") {
    if (lineKeyPath === "") errors.lineKeyPath = errors.lineKeyPath ?? "Matching lines by a field needs the line key path.";
    if (!ELEMENT_PATH.test(lineBodyPath)) errors.lineBodyPath = "Use a path with one [*], such as Lines[*].LineNum, or clear both fields to match by position.";
    if (!ELEMENT_PATH.test(lineResponsePath)) errors.lineResponsePath = "Use a path with one [*], such as $.data.lines[*].lineNo, or clear both fields to match by position.";
  }
  const need = (field: keyof SyncValues, message: string) => {
    if (v[field].trim() === "") errors[field] = message;
  };
  const auth: AuthInput = { type: v.authType };
  switch (v.authType) {
    case "basic":
      need("username", "Enter the user name.");
      need("password", "Enter the password.");
      auth.username = v.username.trim();
      auth.password = v.password;
      break;
    case "bearer":
      need("token", "Enter the token.");
      auth.token = v.token.trim();
      break;
    case "api-key":
      need("headerName", "Enter the header name.");
      need("value", "Enter the key.");
      auth.headerName = v.headerName.trim();
      auth.value = v.value;
      break;
    case "oauth2-client-credentials":
      need("tokenUrl", "Enter the token endpoint.");
      need("clientId", "Enter the client id.");
      need("clientSecret", "Enter the client secret.");
      auth.tokenUrl = v.tokenUrl.trim();
      auth.clientId = v.clientId.trim();
      auth.clientSecret = v.clientSecret;
      if (v.scope.trim() !== "") auth.scope = v.scope.trim();
      break;
  }
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewRestSubscription = {
    kind: "rest-push",
    name,
    url,
    patterns,
    outboundMappingId: v.outboundMappingId.trim().toLowerCase(),
    method: v.method,
    onClientError: v.onClientError,
    auth,
  };
  if (path !== "") input.responseKeyPath = path;
  if (lineKeyPath !== "") input.lineKeyPath = lineKeyPath;
  if (lineKeyPath !== "" && lineBodyPath !== "") input.lineCorrelation = { bodyPath: lineBodyPath, responsePath: lineResponsePath };
  return { errors, input };
}

/** Create a REST push subscription (FR-35.9, integration track S7). Credentials are write-only. */
export function NewSyncSubscriptionPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_WRITE);
  const [values, setValues] = useState<SyncValues>(EMPTY_SYNC);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<SyncValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateSync(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createRestSubscription(input, key.current);
      navigate(`/integration/sync/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "HUB-0422") setErrors({ url: problem.message });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (id: Exclude<keyof SyncValues, "method" | "onClientError" | "authType" | "patterns">, label: string, hint?: string, optional?: boolean, extra?: Record<string, unknown>) => (
    <FormField id={`sync-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => <Input {...control} {...extra} value={values[id]} onChange={(event) => update({ [id]: event.target.value } as Partial<SyncValues>)} invalid={Boolean(errors[id])} />}
    </FormField>
  );
  const secret = (id: "password" | "token" | "value" | "clientSecret", label: string) =>
    text(id, label, "Stored sealed. It cannot be read back; enter a new one to change it.", false, { type: "password", autoComplete: "new-password" });

  if (!canWrite) {
    return (
      <div className={styles.stack}>
        <PageHeader title="New REST push" />
        <Alert tone="warning" title="You cannot create outbound sync">
          Creating a subscription needs the duty {DUTY_HUB_WRITE}.
        </Alert>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New REST push"
        description="Send events into the existing ERP. The outbound mapping turns the event into the ERP's record; the hub posts it, and keeps the key the ERP returns."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("name", "Name", undefined, false, { maxLength: 200 })}
          <FormField id="sync-patterns" label="Event types" hint="One per line, such as dev.passage.sales.order.confirmed.v1." error={errors.patterns}>
            {(control) => <Textarea {...control} rows={3} mono value={values.patterns} onChange={(event) => update({ patterns: event.target.value })} invalid={Boolean(errors.patterns)} />}
          </FormField>
          {text("outboundMappingId", "Outbound mapping id", "The mapping that renders the event into the ERP's body.", false, { spellCheck: false })}
          <FormField id="sync-method" label="Method">
            {(control) => (
              <Select {...control} value={values.method} onChange={(event) => update({ method: event.target.value as SyncMethod })}>
                {SYNC_METHODS.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.wide}>
            {text("url", "Endpoint", "Placeholders from the rendered body go in the path or query, for example /api/customers/{CustAccount}/orders. Values are escaped.", false, { spellCheck: false })}
          </div>
          {text("responseKeyPath", "Legacy key in the response", "Where the ERP returns the new record's key, such as $.data.id. Leave empty if it returns none.", true, { spellCheck: false })}
          {text("lineKeyPath", "Line keys in the response", "When one push creates a header and its lines, where the ERP returns each line's key, such as $.data.lines[*].id. Leave empty to record the header's key only.", true, { spellCheck: false })}
          {text("lineBodyPath", "Line field sent (optional)", "To match lines by a field instead of by position, the field each pushed line carries, such as Lines[*].LineNum. Needed when the mapping sends more than one list.", true, { spellCheck: false })}
          {text("lineResponsePath", "Same field in the response (optional)", "Where the ERP echoes that field for each line, such as $.data.lines[*].lineNo.", true, { spellCheck: false })}
          <FormField id="sync-onClientError" label="When the ERP refuses (4xx)" hint="Refusals other than 410 and 429 normally need a fix, so they go to dead letters at once.">
            {(control) => (
              <Select {...control} value={values.onClientError} onChange={(event) => update({ onClientError: event.target.value as ClientErrorPolicy })}>
                <option value="dead_letter">Dead-letter at once</option>
                <option value="retry">Retry on the schedule</option>
              </Select>
            )}
          </FormField>
          <FormField id="sync-authType" label="Authentication">
            {(control) => (
              <Select {...control} value={values.authType} onChange={(event) => update({ authType: event.target.value as AuthType })}>
                {AUTH_TYPES.map((a) => (
                  <option key={a.value} value={a.value}>
                    {a.label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.authType === "basic" ? (
            <>
              {text("username", "User name", undefined, false, { autoComplete: "off" })}
              {secret("password", "Password")}
            </>
          ) : null}
          {values.authType === "bearer" ? secret("token", "Token") : null}
          {values.authType === "api-key" ? (
            <>
              {text("headerName", "Header name")}
              {secret("value", "Key")}
            </>
          ) : null}
          {values.authType === "oauth2-client-credentials" ? (
            <>
              {text("tokenUrl", "Token endpoint", undefined, false, { spellCheck: false })}
              {text("clientId", "Client id")}
              {secret("clientSecret", "Client secret")}
              {text("scope", "Scope", undefined, true)}
            </>
          ) : null}
          {failure ? (
            <div className={styles.wide}>
              <ProblemAlert problem={failure} title="Could not create the subscription" />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" disabled={busy}>
              {busy ? "Creating..." : "Create subscription"}
            </Button>
            <Link className={buttonClassName("secondary")} to="/integration/sync">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
