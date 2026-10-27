import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, PageHeader, Skeleton, Textarea } from "../../shell/ui";
import { describeProblem, getScreeningHold, releaseScreeningHold, type Problem, type ScreeningHold } from "./api/client";
import { Details } from "./Details";
import { checkTone, dash, formatDateTime, stateTone, statusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const MAX_REASON = 500;

/** Checks that mirror ValidateHoldRelease in the service. */
export function validateRelease(reason: string): string | undefined {
  const trimmed = reason.trim();
  if (trimmed === "") return "Say why the parties may be released, such as what you checked.";
  if (trimmed.length > MAX_REASON) return `Keep the reason to ${MAX_REASON} characters.`;
  return undefined;
}

/** Release the hold with a reason (TRADE_COMPLIANCE_RELEASE). The reason and your user stay on the hold. */
function ReleaseCard({ hold, onDone }: { hold: ScreeningHold; onDone(h: ScreeningHold): void }) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateRelease(reason);
    setError(found);
    setFailure(null);
    if (found) return;
    setBusy(true);
    try {
      onDone(await releaseScreeningHold(hold.id, reason.trim()));
    } catch (e) {
      const problem = describeProblem(e);
      if (problem.fields.reason) setError(problem.fields.reason);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Release the hold" headingLevel={3}>
      <form className={styles.stack} onSubmit={submit} noValidate>
        <p className={styles.note}>Releasing lets the load be tendered. Only trade compliance officers can release a hold, and the reason is kept with your user.</p>
        <FormField id="release-reason" label="Reason" hint="Up to 500 characters." error={error}>
          {(control) => <Textarea {...control} rows={3} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(error)} />}
        </FormField>
        {failure ? (
          <Alert tone="danger" title="The hold was not released" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Release hold
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** One screening hold: the party results that hold the load and its release (BR-34.17, AC-34.16). */
export function ScreeningHoldDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getScreeningHold(id, signal), `hold:${id}`);
  const [updated, setUpdated] = useState<ScreeningHold | null>(null);
  const hold = updated && updated.id === state.data?.id ? updated : state.data;

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Screening hold" />
        <Alert tone="danger" title="Could not load the screening hold" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link to="/transport/screening-holds">Back to screening holds</Link>
      </div>
    );
  }
  if (!hold) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Screening hold" />
        <Card>
          <p className={styles.note}>Loading the screening hold…</p>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }

  const blocked = hold.parties.filter((p) => p.status === "BLOCKED");
  return (
    <div className={styles.stack}>
      <PageHeader title={`Screening hold ${hold.id.slice(0, 8)}`} meta={<Badge tone={stateTone(hold.status)}>{statusLabel(hold.status)}</Badge>} />
      {hold.status === "OPEN" ? (
        <Alert tone="danger" title="Load held for trade compliance (TMS-0133)">
          The load cannot be tendered while this hold is open. The hold is never cleared automatically.
        </Alert>
      ) : (
        <Alert tone="success" title="Released" live="status">
          Released by {hold.releasedBy} on {formatDateTime(hold.releasedAt)}. Tendering can proceed.
        </Alert>
      )}
      <Card>
        <Details
          entries={[
            { term: "Hold", value: hold.id, mono: true },
            { term: "Load", value: <Link to={`/transport/loads/${hold.loadId}`}>{hold.loadId}</Link>, mono: true },
            { term: "Held by", value: hold.createdBy },
            { term: "Held since", value: formatDateTime(hold.createdAt) },
            { term: "Released by", value: hold.releasedBy ?? dash },
            { term: "Released", value: formatDateTime(hold.releasedAt) },
            { term: "Reason", value: hold.releaseReason ?? dash },
          ]}
        />
      </Card>
      <Card title="Party results" headingLevel={3}>
        <DataTable
          label="Party results"
          columns={[
            { key: "role", header: "Role", render: (row) => statusLabel(row.role) },
            { key: "name", header: "Name" },
            { key: "status", header: "Result", render: (row) => <Badge tone={checkTone(row.status)}>{statusLabel(row.status)}</Badge> },
            { key: "code", header: "Code", mono: true, render: (row) => row.code ?? dash },
          ]}
          rows={hold.parties}
          getRowKey={(row, i) => `${row.role}-${i}`}
        />
      </Card>
      {hold.status === "OPEN" && blocked.length > 0 ? (
        <Alert tone="danger" title="Blocked by the screening list">
          {blocked.map((p) => p.name).join(", ")} {blocked.length === 1 ? "is" : "are"} on a denied-party or sanctions list. The hold stays until the list changes; it cannot be released here.
        </Alert>
      ) : null}
      {hold.status === "OPEN" && blocked.length === 0 ? <ReleaseCard key={hold.version} hold={hold} onDone={setUpdated} /> : null}
      <div className={styles.actions}>
        <Link to={`/transport/loads/${hold.loadId}`}>Open the load</Link>
        <Link to="/transport/screening-holds">Back to screening holds</Link>
      </div>
    </div>
  );
}
