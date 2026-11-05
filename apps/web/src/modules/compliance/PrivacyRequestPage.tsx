import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, FormField, Input, PageHeader, Select, Skeleton, Textarea } from "../../shell/ui";
import {
  actOnSubjectRequest,
  describeProblem,
  getSubjectRequest,
  previewErasure,
  type ErasurePreview,
  type PrivacyAction,
  type PrivacyState,
  type Problem,
  type RequestActionInput,
  type SubjectRequest,
  type Versioned,
} from "./api/client";
import { dash, formatDateTime, sentence } from "./format";
import styles from "./pages.module.css";
import { DaysLeft, privacyStateTone } from "./PrivacyRequestsPage";
import { useLoad } from "./useLoad";

export const actionLabel: Record<PrivacyAction, string> = {
  VERIFY: "Verify identity",
  DISCOVER: "Record discovery",
  RELEASE_HOLD: "Release from hold",
  APPROVE: "Approve",
  DELIVER: "Mark delivered",
  CLOSE: "Close",
  REJECT: "Reject",
};

/** The steps the service allows from each state (domain.ApplyPrivacyTransition). */
export const nextActions: Record<PrivacyState, PrivacyAction[]> = {
  RECEIVED: ["VERIFY", "REJECT"],
  VERIFIED: ["DISCOVER", "REJECT"],
  HELD: ["RELEASE_HOLD", "REJECT"],
  DISCOVERED: ["APPROVE", "REJECT"],
  APPROVED: ["DELIVER", "REJECT"],
  DELIVERED: ["CLOSE", "REJECT"],
  CLOSED: [],
  REJECTED: [],
};

export interface ActionValues {
  action: PrivacyAction;
  attributes: string;
  emailLink: boolean;
  holds: string;
  justification: string;
}

type Errors = Partial<Record<"attributes" | "emailLink" | "justification", string>>;

const list = (text: string) => [...new Set(text.split(/[\n,]/).map((s) => s.trim()).filter(Boolean))];

/** Checks the action form and builds the body; mirrors the service's rules. */
export function toActionInput(v: ActionValues, request: SubjectRequest, now: Date = new Date()): { input?: RequestActionInput; errors: Errors } {
  const errors: Errors = {};
  const input: RequestActionInput = { action: v.action };
  const justification = v.justification.trim();
  switch (v.action) {
    case "VERIFY":
      input.matchedAttributes = list(v.attributes);
      input.emailLinkVerified = v.emailLink;
      if (input.matchedAttributes.length < 2) errors.attributes = "Two different attributes must match the record, for example e-mail and postal code.";
      if (!v.emailLink) errors.emailLink = "The requester must confirm the e-mail link first.";
      break;
    case "DISCOVER":
    case "RELEASE_HOLD":
      input.holdIds = list(v.holds);
      break;
    case "CLOSE":
      if (new Date(request.deadlineAt).getTime() < now.getTime() && justification.length < 10) {
        errors.justification = "The deadline has passed: explain the late close in at least 10 characters.";
      }
      break;
    case "REJECT":
      if (justification.length < 10) errors.justification = "Give the reason for rejecting in at least 10 characters.";
      break;
  }
  if (justification) input.justification = justification;
  return Object.keys(errors).length > 0 ? { errors } : { input, errors };
}

/** "email=ada@example.com" lines into a record. */
export function parseRecord(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const at = line.indexOf("=");
    if (at > 0) out[line.slice(0, at).trim()] = line.slice(at + 1).trim();
  }
  return out;
}

/**
 * One subject request (PRD §9.48.6 Privacy requests, duty compliance.privacy-request.process):
 * its deadline, state and holds, the next step and, for an erasure, a preview
 * of what pseudonymization does to a record (BR-48.5).
 */
export function PrivacyRequestPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getSubjectRequest(id, signal), id);
  const [latest, setLatest] = useState<Versioned<SubjectRequest> | null>(null);
  const [values, setValues] = useState<ActionValues | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [record, setRecord] = useState("");
  const [preview, setPreview] = useState<ErasurePreview | null>(null);
  const [previewFailure, setPreviewFailure] = useState<Problem | null>(null);

  const current = latest && state.data && latest.value.requestId === state.data.value.requestId && latest.value.version >= state.data.value.version ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Privacy request" />
        <Alert tone="danger" title="Could not load the request" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link to="/compliance/privacy/requests">Back to privacy requests</Link>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Privacy request" />
        <span className="visually-hidden">Loading the request…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const req = current.value;
  const actions = nextActions[req.state];
  const form: ActionValues = values && actions.includes(values.action) ? values : { action: actions[0] ?? "REJECT", attributes: "", emailLink: false, holds: req.holdIds.join(", "), justification: "" };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { input, errors: found } = toActionInput(form, req);
    setErrors(found);
    if (!input || !current) return;
    setBusy(true);
    setFailure(null);
    setDone(null);
    try {
      const next = await actOnSubjectRequest(req.requestId, current.etag, input);
      setLatest(next);
      setValues(null);
      setDone(`${actionLabel[input.action]}: the request is now ${sentence(next.value.state).toLowerCase()}.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  async function runPreview(event: FormEvent) {
    event.preventDefault();
    setPreviewFailure(null);
    try {
      setPreview(await previewErasure(req.requestId, parseRecord(record)));
    } catch (error) {
      setPreview(null);
      setPreviewFailure(describeProblem(error));
    }
  }

  const facts: [string, ReactNode][] = [
    ["Subject", <span className={styles.mono}>{req.subjectRef}</span>],
    ["Type", sentence(req.type)],
    ["Regulation", req.regulation],
    ["Received", formatDateTime(req.receivedAt)],
    ["Deadline", <>{formatDateTime(req.deadlineAt)} <DaysLeft request={req} /></>],
    ["Legal holds", req.holdIds.length ? req.holdIds.join(", ") : dash],
    ["Identity verified", formatDateTime(req.verifiedAt)],
    ["Approved by", req.privacyOfficerId ? <span className={styles.mono}>{req.privacyOfficerId}</span> : dash],
    ["Delivered", formatDateTime(req.deliveredAt)],
    ["Closed", formatDateTime(req.closedAt)],
    ["Justification", req.justification ?? dash],
  ];
  const set = (patch: Partial<ActionValues>) => setValues({ ...form, ...patch });
  const canPreview = req.type === "ERASURE" && ["DISCOVERED", "APPROVED", "HELD"].includes(req.state);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${sentence(req.type)} request for ${req.subjectRef}`}
        description={`${req.regulation} request received ${formatDateTime(req.receivedAt)}.`}
        meta={<Badge tone={privacyStateTone[req.state]}>{sentence(req.state)}</Badge>}
        actions={<Link to="/compliance/privacy/requests">Back to privacy requests</Link>}
      />
      <Card title="Request">
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Alert tone="danger" title={failure ? "The step was refused" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {actions.length > 0 ? (
        <Card title="Next step">
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Next step">
            <FormField id="prv-action" label="Step">
              {(control) => (
                <Select {...control} value={form.action} onChange={(e) => setValues({ ...form, action: e.target.value as PrivacyAction })}>
                  {actions.map((a) => (
                    <option key={a} value={a}>
                      {actionLabel[a]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {form.action === "VERIFY" ? (
              <>
                <FormField id="prv-attributes" label="Matched attributes" hint="Separate with commas, for example email, postal_code." error={errors.attributes}>
                  {(control) => <Input {...control} value={form.attributes} onChange={(e) => set({ attributes: e.target.value })} invalid={Boolean(errors.attributes)} />}
                </FormField>
                <span className={`${styles.check} ${styles.wide}`}>
                  <Input id="prv-email-link" type="checkbox" checked={form.emailLink} onChange={(e) => set({ emailLink: e.target.checked })} aria-describedby={errors.emailLink ? "prv-email-link-error" : undefined} />
                  <label htmlFor="prv-email-link">The requester confirmed the e-mail link</label>
                </span>
                {errors.emailLink ? (
                  <p id="prv-email-link-error" className={`${styles.fieldError} ${styles.wide}`}>
                    {errors.emailLink}
                  </p>
                ) : null}
              </>
            ) : null}
            {form.action === "DISCOVER" || form.action === "RELEASE_HOLD" ? (
              <FormField
                id="prv-holds"
                label={form.action === "DISCOVER" ? "Legal holds found" : "Holds still standing"}
                hint={form.action === "DISCOVER" ? "An erasure under any hold waits until it is released." : "Clear the list once every hold is released."}
                optional
              >
                {(control) => <Input {...control} className={styles.mono} value={form.holds} onChange={(e) => set({ holds: e.target.value })} />}
              </FormField>
            ) : null}
            {form.action === "CLOSE" || form.action === "REJECT" ? (
              <div className={styles.wide}>
                <FormField id="prv-justification" label={form.action === "REJECT" ? "Reason" : "Late-close justification"} error={errors.justification} optional={form.action === "CLOSE"}>
                  {(control) => <Textarea {...control} rows={3} value={form.justification} onChange={(e) => set({ justification: e.target.value })} invalid={Boolean(errors.justification)} />}
                </FormField>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant={form.action === "REJECT" ? "danger" : "primary"} loading={busy}>
                {actionLabel[form.action]}
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {canPreview ? (
        <Card title="Erasure preview">
          <form className={styles.form} onSubmit={runPreview} noValidate aria-label="Erasure preview">
            <div className={styles.wide}>
              <FormField id="prv-record" label="Record" hint="One field per line, for example email=ada@example.com. Nothing is changed.">
                {(control) => <Textarea {...control} rows={5} className={styles.mono} value={record} onChange={(e) => setRecord(e.target.value)} />}
              </FormField>
            </div>
            <div className={styles.formActions}>
              <Button type="submit">Preview</Button>
            </div>
          </form>
          <Alert tone="danger" title={previewFailure ? "No preview" : undefined} live="alert">
            {previewFailure ? `${previewFailure.message} (${previewFailure.code})` : null}
          </Alert>
          {preview ? (
            <dl className={styles.details} aria-label="Record after erasure">
              {Object.entries(preview.record)
                .sort(([a], [b]) => a.localeCompare(b))
                .map(([field, value]) => (
                  <div key={field} className={styles.entry}>
                    <dt className={styles.term}>{field}</dt>
                    <dd className={`${styles.value} ${styles.mono}`}>
                      {value} {preview.pseudonymized.includes(field) ? <Badge tone="warning">Pseudonymized</Badge> : null}
                    </dd>
                  </div>
                ))}
            </dl>
          ) : null}
        </Card>
      ) : null}
    </div>
  );
}
