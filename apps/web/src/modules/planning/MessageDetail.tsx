import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  EmptyState,
  FormField,
  PageHeader,
  Skeleton,
  Textarea,
  type BadgeTone,
} from "../../shell/ui";
import {
  APPLICABLE_ACTIONS,
  decideMessage,
  getMessage,
  type Message,
  type MessageStatus,
} from "./api/client";
import {
  describeError,
  useLoad,
  usePlanCode,
  withPlan,
  type LoadError,
} from "./hooks";
import {
  actionTone,
  dash,
  Details,
  LoadAlert,
  messageDelay,
  scopeLabel,
  sentence,
} from "./pages";
import styles from "./pages.module.css";

export const MAX_NOTE = 500;

export const statusTone: Record<MessageStatus, BadgeTone> = {
  open: "warning",
  applied: "success",
  dismissed: "neutral",
};

/** Whether applying the message changes a source order (FR-22.7); information messages can only be dismissed. */
export const isApplicable = (m: Pick<Message, "action">) =>
  (APPLICABLE_ACTIONS as readonly string[]).includes(m.action);

/** What applying each action asks of the order named by the message's supply reference. */
const applyEffect: Record<string, string> = {
  postpone: "move the receipt to the suggested date",
  advance: "bring the receipt forward to the suggested date",
  cancel: "cancel the receipt",
  decrease: "decrease the receipt by the quantity shown",
  increase: "increase the receipt by the quantity shown",
};

/** What applying a derived message asks, by the action of the parent supply it follows (FR-22.7). */
const derivedEffect: Record<string, string> = {
  cancel:
    "reduce the component receipt by the quantity shown, as the parent supply it serves is cancelled",
  decrease:
    "reduce the component receipt by the quantity shown, as the parent supply it serves is decreased",
  postpone:
    "move the quantity shown of the component receipt to the suggested date, as the parent supply it serves is postponed",
  advance:
    "bring the quantity shown of the component receipt forward to the suggested date, as the parent supply it serves is advanced",
};

/** What applying a message asks of the order named by its supply reference. */
export const applyEffectOf = (m: Pick<Message, "action" | "parentAction">) =>
  m.action === "derived"
    ? (m.parentAction && derivedEffect[m.parentAction]) ||
      "change the component receipt as its parent supply changes"
    : applyEffect[m.action];

/** The parent supply a derived message's dependent demand "<parent supply>/<component>" names. */
export const parentSupplyOf = (demandRef: string | null | undefined) => {
  const i = demandRef ? demandRef.lastIndexOf("/") : -1;
  return i > 0 ? demandRef!.slice(0, i) : "";
};

/** Checks the planner's note before it is sent; returns the error or null. */
export const noteError = (note: string) =>
  note.trim().length > MAX_NOTE
    ? `Keep the note to ${MAX_NOTE} characters.`
    : null;

function newKey() {
  const c = (globalThis as { crypto?: { randomUUID?: () => string } }).crypto;
  return c?.randomUUID
    ? c.randomUUID()
    : `msg-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

type Busy = "apply" | "dismiss" | null;

function DecisionCard({
  message,
  onDecided,
  onReload,
}: {
  message: Message;
  onDecided: (m: Message) => void;
  onReload: () => void;
}) {
  const [note, setNote] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [busy, setBusy] = useState<Busy>(null);
  // One key per message and decision, kept across retries so a retry after a lost response replays.
  const keys = useRef<Record<string, string>>({});

  if (message.status !== "open") {
    return (
      <Alert
        tone="info"
        title={`This message was ${message.status}`}
        live="off"
      >
        {message.decidedBy
          ? `By ${message.decidedBy}${message.decidedAt ? ` on ${message.decidedAt.slice(0, 10)}` : ""}. `
          : ""}
        {message.decisionNote ? `${message.decisionNote} ` : ""}
        {message.status === "applied"
          ? `The change was sent to the owner of ${message.supplyRef || "the supply"}; the order is changed there, not in planning. `
          : ""}
        {message.carriedFrom
          ? "Decided on an earlier plan version: this run raised the same message again, so the decision stands."
          : ""}
      </Alert>
    );
  }
  const applicable = isApplicable(message);
  const id = message.id ?? "";

  async function decide(decision: Exclude<Busy, null>, e?: FormEvent) {
    e?.preventDefault();
    if (busy) return;
    const found = noteError(note);
    setError(found);
    if (found) return;
    setBusy(decision);
    setFailure(null);
    const slot = `${id}:${message.version}:${decision}`;
    keys.current[slot] ??= newKey();
    try {
      onDecided(
        await decideMessage(
          id,
          message.version,
          decision,
          note,
          keys.current[slot],
        ),
      );
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(null);
    }
  }

  const stale =
    failure?.status === 412 || failure?.code === "PLAN-MESSAGE-DECIDED";
  return (
    <Card title="Decision">
      <form
        className={styles.stack}
        onSubmit={(e) => void decide(applicable ? "apply" : "dismiss", e)}
        noValidate
        aria-label="Decide message"
      >
        <Alert
          tone="danger"
          title={
            failure
              ? stale
                ? "This message was changed or decided by someone else"
                : "The decision was not saved"
              : undefined
          }
          live="alert"
          action={
            stale ? (
              <Button size="sm" onClick={onReload}>
                Reload message
              </Button>
            ) : undefined
          }
        >
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
        <p>
          {applicable
            ? `Applying asks the owner of ${message.supplyRef || "the supply"} to ${applyEffectOf(message)}; the order is changed there, not in planning. Dismiss it when no change is needed. Either decision stays with the message while later runs raise it unchanged.`
            : `A ${sentence(message.action).toLowerCase()} message changes no source order, so it can only be dismissed once you have dealt with it.`}
        </p>
        <FormField
          id="message-note"
          label="Note"
          optional
          hint={`Why you decided this, up to ${MAX_NOTE} characters.`}
          error={error ?? undefined}
        >
          {(a11y) => (
            <Textarea
              {...a11y}
              value={note}
              onChange={(e) => setNote(e.target.value)}
            />
          )}
        </FormField>
        <div className={styles.formActions}>
          {applicable ? (
            <Button
              type="submit"
              variant="primary"
              loading={busy === "apply"}
              disabled={busy === "dismiss"}
            >
              Apply message
            </Button>
          ) : null}
          <Button
            type={applicable ? "button" : "submit"}
            variant={applicable ? "secondary" : "primary"}
            loading={busy === "dismiss"}
            disabled={busy === "apply"}
            onClick={applicable ? () => void decide("dismiss") : undefined}
          >
            Dismiss message
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** One action message with its decision: apply it to the source order or dismiss it (FR-22.7). */
export function MessageDetailPage() {
  const { id = "" } = useParams();
  const [plan] = usePlanCode();
  const [reload, setReload] = useState(0);
  // The message as the last decision returned it; a reload goes back to the service.
  const [decided, setDecided] = useState<Message | null>(null);
  const state = useLoad((signal) => getMessage(id, { signal }), [id, reload]);
  const back = (
    <Link
      className={buttonClassName("secondary")}
      to={withPlan("/planning/messages", plan)}
    >
      Back to action messages
    </Link>
  );
  if (state.error?.status === 404) {
    return (
      <Card>
        <EmptyState
          icon="grid"
          title="Message not found"
          description="It may belong to a version that has been purged. Pick a message from the current plan."
          action={back}
        />
      </Card>
    );
  }
  const m = decided?.id === id ? decided : state.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={m ? `${sentence(m.action)} message` : "Action message"}
        meta={
          m ? (
            <Badge tone={statusTone[m.status]}>{sentence(m.status)}</Badge>
          ) : undefined
        }
        actions={back}
      />
      <LoadAlert what="the message" error={state.error} />
      <Card title="Message">
        {state.loading ? (
          <div aria-busy="true">
            <Skeleton width="3/4" />
            <span className="visually-hidden">Loading…</span>
          </div>
        ) : m ? (
          <Details
            entries={[
              [
                "Action",
                <Badge tone={actionTone[m.action]}>{sentence(m.action)}</Badge>,
              ],
              [
                "Message",
                m.text ? `${m.code ? `${m.code} ` : ""}${m.text}` : dash,
              ],
              ["Item", m.itemId, true],
              ["Scope", scopeLabel(m)],
              ["Supply", m.supplyRef || dash, true],
              ["Demand", m.demandRef || dash, true],
              ...(m.action === "derived"
                ? [
                    [
                      "Parent supply",
                      `${parentSupplyOf(m.demandRef) || dash} (${m.parentAction ? sentence(m.parentAction).toLowerCase() : "changed"})`,
                      true,
                    ] as [string, string, boolean],
                  ]
                : []),
              ["Suggested date", m.suggestedDate || dash],
              ["Delay", messageDelay(m) > 0 ? `${messageDelay(m)} days` : dash],
              ["Quantity", m.qty],
            ]}
          />
        ) : null}
      </Card>
      {m && !state.loading ? (
        <DecisionCard
          key={`${m.id}-${m.version}`}
          message={m}
          onDecided={setDecided}
          onReload={() => {
            setDecided(null);
            setReload((n) => n + 1);
          }}
        />
      ) : null}
    </div>
  );
}
