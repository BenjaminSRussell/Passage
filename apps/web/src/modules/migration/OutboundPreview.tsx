import { useState } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Textarea, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type OutboundPreview, type Problem, type UnresolvedRef } from "./api/client";
import { parseEventText, STARTER_EVENT, toJsonText } from "./outboundDomain";
import styles from "./pages.module.css";

const unresolvedColumns: DataTableColumn<UnresolvedRef>[] = [
  { key: "name", header: "Lookup", mono: true },
  { key: "entity", header: "Entity" },
  { key: "id", header: "Passage id", mono: true, render: (row) => row.id ?? "" },
  { key: "reason", header: "Why" },
];

const STATUS: Record<OutboundPreview["status"], { label: string; tone: BadgeTone }> = {
  ok: { label: "Rendered", tone: "success" },
  unresolved: { label: "Unresolved references", tone: "warning" },
  rejected: { label: "Rejected by the mapping", tone: "danger" },
  invalid: { label: "Not valid", tone: "danger" },
};

/**
 * Render a sample CloudEvent through a mapping and show the request body the legacy ERP would receive, or the
 * references that have no legacy key yet. Nothing is sent and nothing is recorded.
 */
export function OutboundPreviewPanel({ run, disabledReason }: { run(event: Record<string, unknown>): Promise<OutboundPreview>; disabledReason?: string }) {
  const [text, setText] = useState(toJsonText(STARTER_EVENT));
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<OutboundPreview | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function submit() {
    setFailure(null);
    setResult(null);
    const parsed = parseEventText(text);
    setError(parsed.error);
    if (!parsed.value) return;
    setBusy(true);
    try {
      setResult(await run(parsed.value));
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Preview">
      <p className={styles.note}>Paste a sample event as the integration hub receives it. The preview looks up legacy keys in the cross-reference and shows the body that would be pushed. Nothing is sent.</p>
      <FormField id="outbound-event" label="Sample event" error={error} hint="A CloudEvent as JSON with at least an id and a type.">
        {(control) => <Textarea {...control} mono rows={12} value={text} invalid={Boolean(error)} onChange={(event) => setText(event.target.value)} />}
      </FormField>
      <div className={styles.formActions}>
        <Button variant="primary" loading={busy} disabled={Boolean(disabledReason)} onClick={submit}>
          Preview body
        </Button>
        <Button onClick={() => setText(toJsonText(STARTER_EVENT))}>Reset sample</Button>
      </div>
      {disabledReason ? <p className={styles.note}>{disabledReason}</p> : null}
      {busy ? (
        <p className={styles.note} role="status" aria-busy="true">
          Rendering...
        </p>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="The preview could not run" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {result ? (
        <div className={styles.stack}>
          <p>
            <Badge tone={STATUS[result.status].tone}>{STATUS[result.status].label}</Badge>
          </p>
          {result.status === "ok" ? (
            <>
              <h3>Request body</h3>
              <pre className={styles.code} tabIndex={0} aria-label="Rendered request body">{toJsonText(result.body)}</pre>
              <dl className={styles.details}>
                <div className={styles.entry}>
                  <dt className={styles.term}>Idempotency key</dt>
                  <dd className={`${styles.value} ${styles.mono}`}>{result.idempotencyKey}</dd>
                </div>
                <div className={styles.entry}>
                  <dt className={styles.term}>Legacy key recorded against</dt>
                  <dd className={`${styles.value} ${styles.mono}`}>{(result.refs ?? []).map((r) => `${r.entity} ${r.id}`).join(", ") || "Nothing: the mapping has no subject."}</dd>
                </div>
              </dl>
              {(result.warnings ?? []).length > 0 ? <p className={styles.note}>{(result.warnings ?? []).map((w) => `${w.field}: ${w.detail} (${w.code})`).join("; ")}</p> : null}
            </>
          ) : null}
          {result.status === "unresolved" ? (
            <>
              <Alert tone="warning" title="Some references have no legacy key yet">
                The hub would answer 422 {result.code} and dead-letter the delivery. Load the missing records from the legacy system, or mark the lookup optional.
              </Alert>
              <DataTable label="Unresolved references" columns={unresolvedColumns} rows={result.unresolved ?? []} getRowKey={(row) => `${row.name}|${row.id ?? ""}`} />
            </>
          ) : null}
          {result.status === "rejected" ? (
            <Alert tone="danger" title="The mapping rejected this event">
              {(result.rejects ?? []).map((r) => `${r.field}: ${r.detail || r.code} (${r.code})`).join("; ")}
            </Alert>
          ) : null}
          {result.status === "invalid" ? (
            <Alert tone="danger" title="Not valid">
              {result.detail} ({result.code})
            </Alert>
          ) : null}
        </div>
      ) : null}
    </Card>
  );
}
