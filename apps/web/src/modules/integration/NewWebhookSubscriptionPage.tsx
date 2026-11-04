import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Textarea } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { createWebhookSubscription, DUTY_HUB_WRITE, MAX_FILTER_LENGTH, newKey, type NewWebhookSubscription, type SubscriptionWithSecret } from "./api/hubClient";
import styles from "./pages.module.css";
import { ProblemAlert } from "./parts";

export interface WebhookValues {
  name: string;
  url: string;
  /** One event type pattern per line or comma separated. */
  patterns: string;
  /** Optional CESQL condition over the event. */
  filter: string;
  /** Optional: the hub generates one when empty. */
  secret: string;
}

type Errors = Partial<Record<keyof WebhookValues, string>>;

export const EMPTY_WEBHOOK: WebhookValues = { name: "", url: "", patterns: "dev.passage.sales.order.confirmed.v1", filter: "", secret: "" };

const SEGMENT = /^[a-z0-9]+$/;

/** Mirrors the hub's event type pattern rule (src/filter.ts validatePattern); null when valid. */
export function patternProblem(pattern: string): string | null {
  const segments = pattern.split(".");
  for (let i = 0; i < segments.length; i += 1) {
    const seg = segments[i] as string;
    if (seg === "**") {
      if (i === 0) return "'**' cannot be the first segment";
      if (i !== segments.length - 1) return "'**' is only allowed as the last segment";
    } else if (seg !== "*" && !SEGMENT.test(seg)) {
      return `segment '${seg}' must be lowercase letters or digits`;
    }
  }
  return null;
}

export function parsePatterns(text: string): string[] {
  return [...new Set(text.split(/[\n,]+/).map((p) => p.trim()).filter((p) => p !== ""))];
}

/** Checks that mirror the hub (src/server.ts), and the request the form sends. The hub parses the CESQL filter itself and answers 400 with the position. */
export function validateWebhook(v: WebhookValues): { errors: Errors; input?: NewWebhookSubscription } {
  const errors: Errors = {};
  const name = v.name.trim();
  if (name === "" || name.length > 200) errors.name = "Enter a name, up to 200 characters.";
  const url = v.url.trim();
  if (!/^https:\/\/[^/?#\s]+/i.test(url) || url.length > 2048) errors.url = "Enter a full https address of the receiver, such as https://hooks.example.com/passage.";
  else if (/^https:\/\/[^/?#]*@/i.test(url)) errors.url = "The address must not contain a user name or password.";
  const patterns = parsePatterns(v.patterns);
  const badPattern = patterns.map((p) => ({ p, why: patternProblem(p) })).find((x) => x.why !== null);
  if (patterns.length < 1 || patterns.length > 50) errors.patterns = "Enter 1 to 50 event types, one per line. Use * for one segment and a trailing ** for the rest.";
  else if (badPattern) errors.patterns = `"${badPattern.p}": ${badPattern.why}.`;
  const filter = v.filter.trim();
  if (filter.length > MAX_FILTER_LENGTH) errors.filter = `The filter may be at most ${MAX_FILTER_LENGTH} characters.`;
  const secret = v.secret.trim();
  if (secret !== "" && (secret.length < 16 || secret.length > 256)) errors.secret = "A secret you choose must be 16 to 256 characters; leave it empty to have one generated.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewWebhookSubscription = { kind: "webhook", name, url, patterns };
  if (filter !== "") input.filter = filter;
  if (secret !== "") input.secret = secret;
  return { errors, input };
}

/** The new signing secret, shown once: the hub never returns it again. */
export function SecretReveal({ secret, title }: { secret: string; title: string }) {
  return (
    <Alert tone="warning" title={title} live="status">
      <p className={styles.note}>Copy it now and store it where the receiver can read it. It is not shown again.</p>
      <pre className={styles.payload} aria-label="Signing secret">
        {secret}
      </pre>
    </Alert>
  );
}

/** Create a signed webhook subscription (FR-35.9). */
export function NewWebhookSubscriptionPage() {
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_WRITE);
  const [values, setValues] = useState<WebhookValues>(EMPTY_WEBHOOK);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<SubscriptionWithSecret | null>(null);
  const key = useRef(newKey());

  function update(patch: Partial<WebhookValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateWebhook(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setCreated(await createWebhookSubscription(input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "HUB-0422") setErrors({ url: problem.message });
      else if (problem.code === "HUB-0400" && /^filter:/.test(problem.message)) setErrors({ filter: problem.message.replace(/^filter:\s*/, "") });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (id: keyof WebhookValues, label: string, hint?: string, optional?: boolean, extra?: Record<string, unknown>) => (
    <FormField id={`webhook-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => <Input {...control} {...extra} value={values[id]} onChange={(event) => update({ [id]: event.target.value } as Partial<WebhookValues>)} invalid={Boolean(errors[id])} />}
    </FormField>
  );

  if (!canWrite) {
    return (
      <div className={styles.stack}>
        <PageHeader title="New webhook" />
        <Alert tone="warning" title="You cannot create webhooks">
          Creating a subscription needs the duty {DUTY_HUB_WRITE}.
        </Alert>
      </div>
    );
  }

  if (created) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Webhook created" description={`${created.name} will receive the events it matches, signed with the secret below.`} />
        {typeof created.secret === "string" && created.secret !== "" ? (
          <SecretReveal secret={created.secret} title="Signing secret" />
        ) : (
          <Alert tone="info" title="The signing secret is not shown again" live="status">
            An earlier attempt already created this webhook, and its secret was shown on that response only. Rotate the secret on the subscription page to get a new one.
          </Alert>
        )}
        <div className={styles.formActions}>
          <Link className={buttonClassName("primary")} to={`/integration/sync/${created.id}`}>
            Open the subscription
          </Link>
          <Link className={buttonClassName("secondary")} to="/integration/sync">
            Back to outbound sync
          </Link>
        </div>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New webhook"
        description="Send events to an https endpoint as signed CloudEvents. The receiver verifies the Passage-Signature header with the signing secret; the hub retries failures on a schedule and dead-letters what it cannot deliver."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("name", "Name", undefined, false, { maxLength: 200 })}
          <div className={styles.wide}>{text("url", "Endpoint", "The receiver's https address. Private and internal addresses are refused.", false, { spellCheck: false, inputMode: "url" })}</div>
          <FormField id="webhook-patterns" label="Event types" hint="One per line, such as dev.passage.sales.order.confirmed.v1. * matches one segment and a trailing ** the rest." error={errors.patterns}>
            {(control) => <Textarea {...control} rows={3} mono value={values.patterns} onChange={(event) => update({ patterns: event.target.value })} invalid={Boolean(errors.patterns)} spellCheck={false} />}
          </FormField>
          <FormField
            id="webhook-filter"
            label="Filter"
            hint="Optional CESQL condition on the matching events, such as data.currency = 'USD' AND EXISTS legalentityid. Leave empty to take every matching event."
            optional
            error={errors.filter}
          >
            {(control) => <Textarea {...control} rows={2} mono value={values.filter} onChange={(event) => update({ filter: event.target.value })} invalid={Boolean(errors.filter)} spellCheck={false} />}
          </FormField>
          {text("secret", "Signing secret", "Leave empty to have one generated. You will see it once, after the subscription is created.", true, { autoComplete: "off", spellCheck: false })}
          {failure ? (
            <div className={styles.wide}>
              <ProblemAlert problem={failure} title="Could not create the webhook" />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" disabled={busy}>
              {busy ? "Creating..." : "Create webhook"}
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
