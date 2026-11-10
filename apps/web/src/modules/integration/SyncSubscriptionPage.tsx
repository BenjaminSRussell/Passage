import { useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, ConfirmDialog, FormField, Input, PageHeader, Skeleton, StatusPill, Textarea } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  DEFAULT_GRACE_SECONDS,
  DUTY_HUB_WRITE,
  getSubscription,
  MAX_FILTER_LENGTH,
  MAX_GRACE_SECONDS,
  resumeSubscription,
  rotateSubscriptionSecret,
  testSend,
  updateSubscriptionFilter,
  type Subscription,
  type SubscriptionWithSecret,
  type TestSendResult,
} from "./api/hubClient";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { SecretReveal } from "./NewWebhookSubscriptionPage";
import { ProblemAlert } from "./parts";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** A sales.order.confirmed sample (services/sales/events/asyncapi.yaml), edited as JSON like the map tester takes raw X12. */
export const SAMPLE_EVENT = {
  type: "dev.passage.sales.order.confirmed.v1",
  subject: "order-1",
  data: {
    order_id: "0195f0a0-0000-7000-8000-0000000000a1",
    customer_id: "0195f0a0-0000-7000-8000-0000000000c1",
    currency: "USD",
    lines: [
      {
        line_id: "0195f0a0-0000-7000-8000-0000000000b1",
        product_id: "0195f0a0-0000-7000-8000-0000000000d1",
        quantity: "12",
        site_id: "0195f0a0-0000-7000-8000-0000000000e1",
        warehouse_id: "0195f0a0-0000-7000-8000-0000000000f1",
        confirmed_ship_date: "2026-12-01",
      },
    ],
    net_total: "1200.00",
    version: 1,
  },
};

function Term({ term, children, mono }: { term: string; children: React.ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

const STAGES: Record<TestSendResult["stage"], string> = {
  render: "The outbound mapping could not render the event.",
  url: "The endpoint's placeholders could not be filled from the rendered body.",
  token: "The OAuth token request failed.",
  built: "Rendered. Nothing was sent.",
  sent: "Sent to the ERP once. No delivery or cross-reference was recorded.",
};

function TestSendCard({ subscription }: { subscription: Subscription }) {
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_WRITE);
  const [text, setText] = useState(JSON.stringify(SAMPLE_EVENT, null, 2));
  const [send, setSend] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<TestSendResult | null>(null);
  const [error, setError] = useState<Problem | null>(null);
  const [syntax, setSyntax] = useState<string | null>(null);

  async function run() {
    setError(null);
    setResult(null);
    let event: unknown;
    try {
      event = JSON.parse(text);
    } catch {
      setSyntax("The sample event is not valid JSON.");
      return;
    }
    setSyntax(null);
    setBusy(true);
    try {
      setResult(await testSend(subscription.id, event, send));
    } catch (failure) {
      setError(describeProblem(failure));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Test send">
      <div className={styles.stack}>
        <p className={styles.note}>Render a sample event through the mapping and see the request. Tick the box to send it to the ERP once; it will create a record there.</p>
        <FormField id="sync-test-event" label="Sample event" error={syntax ?? undefined}>
          {(control) => <Textarea {...control} rows={10} mono value={text} onChange={(event) => setText(event.target.value)} invalid={Boolean(syntax)} spellCheck={false} />}
        </FormField>
        <div>
          <input id="sync-test-send" type="checkbox" checked={send} onChange={(event) => setSend(event.target.checked)} />{" "}
          <label htmlFor="sync-test-send">Also send it to the ERP</label>
        </div>
        <div className={styles.formActions}>
          <Button onClick={run} disabled={busy || !canWrite} loading={busy}>
            {send ? "Render and send" : "Render"}
          </Button>
          {!canWrite ? <span className={styles.note}>Needs the duty {DUTY_HUB_WRITE}.</span> : null}
        </div>
        {error ? <ProblemAlert problem={error} title="Test failed" /> : null}
        {result ? (
          <Alert tone={result.stage === "built" || (result.stage === "sent" && result.status !== null && result.status !== undefined && result.status < 300) ? "success" : "warning"} title={STAGES[result.stage]} live="status">
            <pre className={styles.payload} aria-label="Test send result">
              {JSON.stringify(result, null, 2)}
            </pre>
          </Alert>
        ) : null}
      </div>
    </Card>
  );
}

/** The CESQL condition that narrows the events a subscription takes; saved against the version the page read. */
function FilterCard({ subscription, onSaved }: { subscription: Subscription; onSaved(): void }) {
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_WRITE);
  const saved = subscription.filter ?? "";
  const [text, setText] = useState(saved);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Problem | null>(null);
  const [invalid, setInvalid] = useState<string | null>(null);

  async function save(next: string | null) {
    setError(null);
    setInvalid(null);
    if (next !== null && next.length > MAX_FILTER_LENGTH) {
      setInvalid(`The filter may be at most ${MAX_FILTER_LENGTH} characters.`);
      return;
    }
    setBusy(true);
    try {
      await updateSubscriptionFilter(subscription.id, subscription.version, next);
      if (next === null) setText("");
      onSaved();
    } catch (failure) {
      const problem = describeProblem(failure);
      if (problem.code === "HUB-0400" && /^filter:/.test(problem.message)) setInvalid(problem.message.replace(/^filter:\s*/, ""));
      else setError(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Event filter">
      <div className={styles.stack}>
        <p className={styles.note}>
          An event is delivered when its type matches an event type above and this CESQL condition holds. Attributes: id, type, source, subject, time, tenantid, actor, legalentityid; the payload is data.&lt;field&gt;. An event the condition cannot be evaluated for is not delivered.
        </p>
        <FormField id="sync-filter" label="Filter" optional error={invalid ?? undefined}>
          {(control) => <Textarea {...control} rows={3} mono value={text} onChange={(event) => setText(event.target.value)} invalid={Boolean(invalid)} spellCheck={false} disabled={!canWrite} />}
        </FormField>
        <div className={styles.formActions}>
          <Button onClick={() => save(text.trim() === "" ? null : text.trim())} disabled={busy || !canWrite || text.trim() === saved} loading={busy}>
            Save filter
          </Button>
          {saved !== "" ? (
            <Button variant="secondary" onClick={() => save(null)} disabled={busy || !canWrite}>
              Remove filter
            </Button>
          ) : null}
          {!canWrite ? <span className={styles.note}>Needs the duty {DUTY_HUB_WRITE}.</span> : null}
        </div>
        {error ? <ProblemAlert problem={error} title="Could not save the filter" /> : null}
      </div>
    </Card>
  );
}

/** Webhook signing secret: rotate it, keeping the old one valid for a grace period so the receiver can switch over. */
function SecretCard({ subscription, onRotated }: { subscription: Subscription; onRotated(): void }) {
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_WRITE);
  const [grace, setGrace] = useState(String(DEFAULT_GRACE_SECONDS / 3600));
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Problem | null>(null);
  const [invalid, setInvalid] = useState<string | null>(null);
  const [fresh, setFresh] = useState<SubscriptionWithSecret | null>(null);
  const hours = Number(grace);
  const valid = grace.trim() !== "" && Number.isInteger(hours) && hours >= 0 && hours * 3600 <= MAX_GRACE_SECONDS;
  const previousUntil = subscription.previousSecretExpiresAt ? Date.parse(subscription.previousSecretExpiresAt) : null;

  function ask() {
    if (!valid) {
      setInvalid(`Enter whole hours from 0 to ${MAX_GRACE_SECONDS / 3600}; 0 ends the old secret at once.`);
      return;
    }
    setInvalid(null);
    setConfirming(true);
  }

  async function rotate() {
    setConfirming(false);
    setBusy(true);
    setError(null);
    try {
      setFresh(await rotateSubscriptionSecret(subscription.id, { version: subscription.version, graceSeconds: hours * 3600 }));
      onRotated();
    } catch (failure) {
      setError(describeProblem(failure));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Signing secret">
      <div className={styles.stack}>
        <dl className={styles.details}>
          <Term term="Last rotated">{subscription.secretRotatedAt ? formatDateTime(subscription.secretRotatedAt) : "Never"}</Term>
          <Term term="Previous secret">
            {previousUntil !== null && previousUntil > Date.now() ? `Still signs until ${formatDateTime(subscription.previousSecretExpiresAt)}` : dash}
          </Term>
        </dl>
        <p className={styles.note}>
          Rotating makes a new secret, shown once. During the grace period each delivery carries a signature for the new and the old secret, so the receiver can switch without losing events.
        </p>
        {fresh ? <SecretReveal secret={fresh.secret} title="New signing secret" /> : null}
        <FormField id="sync-grace" label="Keep the old secret for (hours)" hint={`0 to ${MAX_GRACE_SECONDS / 3600}; the default is ${DEFAULT_GRACE_SECONDS / 3600}.`} error={invalid ?? undefined}>
          {(control) => <Input {...control} inputMode="numeric" value={grace} onChange={(event) => setGrace(event.target.value)} invalid={Boolean(invalid)} disabled={!canWrite} />}
        </FormField>
        <div className={styles.formActions}>
          <Button onClick={ask} disabled={busy || !canWrite} loading={busy}>
            Rotate secret
          </Button>
          {!canWrite ? <span className={styles.note}>Needs the duty {DUTY_HUB_WRITE}.</span> : null}
        </div>
        {error ? <ProblemAlert problem={error} title="Could not rotate the secret" /> : null}
      </div>
      {confirming ? (
        <ConfirmDialog
          title="Rotate the signing secret?"
          description={`${hours === 0 ? "The old secret stops signing at once, so the receiver must have the new one before the next delivery." : `The old secret keeps signing for ${hours} hour${hours === 1 ? "" : "s"}.`} The new secret is shown once.`}
          confirmLabel="Rotate secret"
          cancelLabel="Keep current secret"
          onConfirm={rotate}
          onCancel={() => setConfirming(false)}
        />
      ) : null}
    </Card>
  );
}

/** One subscription: its settings (credentials are never shown), pause state, and a test send. */
export function SyncSubscriptionPage() {
  const { id = "" } = useParams();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_WRITE);
  const state = useLoad((signal) => getSubscription(id, signal), `sync-subscription:${id}`);
  const [busy, setBusy] = useState(false);
  const [resumeError, setResumeError] = useState<Problem | null>(null);
  const resuming = useRef(false);

  async function resume() {
    if (resuming.current) return;
    resuming.current = true;
    setBusy(true);
    setResumeError(null);
    try {
      await resumeSubscription(id);
      state.reload();
    } catch (failure) {
      setResumeError(describeProblem(failure));
    } finally {
      resuming.current = false;
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Subscription" />
        <Alert tone="danger" title="Could not load the subscription" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  const sub = state.data;
  if (!sub) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Subscription" />
        <Skeleton />
      </div>
    );
  }
  const rest = sub.rest;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={sub.name}
        description={sub.kind === "rest-push" ? "REST push into the existing ERP." : "Signed webhook."}
        actions={
          <Link className={buttonClassName("secondary")} to={`/integration/sync/deliveries?subscriptionId=${sub.id}`}>
            Deliveries
          </Link>
        }
      />
      {sub.status === "paused" ? (
        <Alert
          tone="warning"
          title="Paused: the receiver answered 410 Gone"
          live="status"
          action={canWrite ? <Button size="sm" onClick={resume} disabled={busy}>Resume</Button> : undefined}
        >
          Events are held and sent in order once the subscription is resumed.
        </Alert>
      ) : null}
      {resumeError ? <ProblemAlert problem={resumeError} title="Could not resume" /> : null}
      <Card title="Settings">
        <dl className={styles.details}>
          <Term term="Status">
            <StatusPill status={sub.status} />
          </Term>
          <Term term="Kind">{sub.kind === "rest-push" ? "REST push" : "Webhook"}</Term>
          <Term term="Endpoint" mono>
            {sub.url}
          </Term>
          <Term term="Event types">{sub.patterns.join(", ")}</Term>
          <Term term="Filter" mono={Boolean(sub.filter)}>
            {sub.filter || dash}
          </Term>
          {rest ? (
            <>
              <Term term="Method">{rest.method}</Term>
              <Term term="Outbound mapping" mono>
                {rest.outboundMappingId}
              </Term>
              <Term term="Legacy key in the response" mono>
                {rest.responseKeyPath || dash}
              </Term>
              <Term term="Line keys in the response" mono>
                {rest.lineKeyPath || dash}
              </Term>
              {rest.lineCorrelation ? (
                <Term term="Lines matched by" mono>
                  {rest.lineCorrelation.bodyPath} = {rest.lineCorrelation.responsePath}
                </Term>
              ) : null}
              <Term term="When the ERP refuses (4xx)">{rest.onClientError === "retry" ? "Retry on the schedule" : "Dead-letter at once"}</Term>
              <Term term="Authentication">
                {rest.auth.type}
                {rest.auth.username ? ` as ${rest.auth.username}` : ""}
                {rest.auth.headerName ? ` in ${rest.auth.headerName}` : ""} <Badge tone="success">Credentials stored</Badge>
              </Term>
            </>
          ) : null}
          <Term term="Created">{formatDateTime(sub.createdAt)}</Term>
          <Term term="Updated">{formatDateTime(sub.updatedAt)}</Term>
        </dl>
      </Card>
      <FilterCard key={`filter-${sub.version}`} subscription={sub} onSaved={state.reload} />
      {/* Not keyed by version: the rotation bumps the version, and a remount would drop the new secret before it is copied. */}
      {sub.kind === "webhook" ? <SecretCard subscription={sub} onRotated={state.reload} /> : null}
      {sub.kind === "rest-push" ? <TestSendCard subscription={sub} /> : null}
    </div>
  );
}
