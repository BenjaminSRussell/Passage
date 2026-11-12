import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import { EntityName } from "../../shell/lookup/EntityName";
import { ProblemMessage } from "../../shell/errors";
import {
  describeProblem,
  facturXPdfPath,
  getEInvoice,
  recordEInvoiceResponse,
  submitEInvoice,
  validateEInvoice,
  type EInvoice,
  type EInvoiceRuleCheck,
  type Problem,
} from "./api/client";
import { apiFetch } from "../../shell/api/http";
import { formatDateTime } from "./format";
import { EINVOICE_STATUS_LABELS, MANDATE_LABELS } from "./EInvoicesPage";
import { IssueList } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

function Entry({ term, children, mono }: { term: string; children: React.ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** Checks the answer form: a rejection needs its reason. */
export function validateAnswer(outcome: string, reason: string): Record<string, string> {
  const errors: Record<string, string> = {};
  if (outcome !== "ACCEPTED" && outcome !== "REJECTED") errors.outcome = "Choose whether the network accepted or rejected the document.";
  if (outcome === "REJECTED" && reason.trim() === "") errors.reason = "Enter the reason the network gave.";
  if (reason.length > 1000) errors.reason = "Use at most 1000 characters.";
  return errors;
}

/** One e-invoice: its status, blocking issues and document, with submit, the network's answer and the rule check (35-2). */
export function EInvoicePage() {
  const { id = "" } = useParams();
  const state = useLoad<EInvoice>((signal) => getEInvoice(id, signal), `einvoice:${id}`);
  const [busy, setBusy] = useState<"submit" | "validate" | "answer" | "pdf" | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [check, setCheck] = useState<EInvoiceRuleCheck | null>(null);
  const [outcome, setOutcome] = useState("ACCEPTED");
  const [reference, setReference] = useState("");
  const [reason, setReason] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const submitting = useRef(false);

  async function run<T>(kind: "submit" | "validate" | "answer" | "pdf", action: () => Promise<T>, done: (value: T) => void) {
    if (submitting.current) return;
    submitting.current = true;
    setBusy(kind);
    setFailure(null);
    try {
      done(await action());
    } catch (error) {
      setFailure(describeProblem(error));
      // A refusal means another caller moved it; show what it is now.
      state.reload();
    } finally {
      submitting.current = false;
      setBusy(null);
    }
  }

  async function downloadPdf(e: EInvoice) {
    await run(
      "pdf",
      async () => {
        const res = await apiFetch(facturXPdfPath(e.id), { credentials: "same-origin" });
        if (!res.ok) throw new Error(`The PDF could not be built (HTTP ${res.status})`);
        return res.blob();
      },
      (blob) => {
        const url = URL.createObjectURL(blob);
        const link = document.createElement("a");
        link.href = url;
        link.download = `${e.number}.pdf`;
        link.click();
        URL.revokeObjectURL(url);
      },
    );
  }

  function answer(event: FormEvent) {
    event.preventDefault();
    const found = validateAnswer(outcome, reason);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void run(
      "answer",
      () => recordEInvoiceResponse(id, { outcome: outcome as "ACCEPTED" | "REJECTED", reference: reference.trim() || undefined, reason: reason.trim() || undefined }),
      () => {
        setReason("");
        setReference("");
        state.reload();
      },
    );
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="E-invoice" />
        <Alert tone="danger" title="Could not load the e-invoice" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="E-invoice" />
        <span className="visually-hidden">Loading the e-invoice…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const e = state.data;
  const status = EINVOICE_STATUS_LABELS[e.status] ?? { label: e.status, tone: "neutral" as const };
  const canSubmit = e.status === "VALID" || e.status === "REJECTED";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${e.documentType === "CREDIT_NOTE" ? "Credit note" : "Invoice"} ${e.number}`}
        description={`${MANDATE_LABELS[e.mandate] ?? e.mandate}, built ${formatDateTime(e.createdAt)}`}
        meta={<Badge tone={status.tone}>{status.label}</Badge>}
        actions={
          <>
            {canSubmit ? (
              <Button variant="primary" onClick={() => void run("submit", () => submitEInvoice(id), () => state.reload())} loading={busy === "submit"} disabled={busy !== null}>
                {e.status === "REJECTED" ? "Submit again" : "Submit to the network"}
              </Button>
            ) : null}
            {e.mandate === "FACTURX" && e.xml ? (
              <Button onClick={() => void downloadPdf(e)} loading={busy === "pdf"} disabled={busy !== null}>
                Download Factur-X PDF
              </Button>
            ) : null}
            {e.mandate === "PEPPOL" && e.xml ? (
              <Button onClick={() => void run("validate", () => validateEInvoice(id), setCheck)} loading={busy === "validate"} disabled={busy !== null}>
                Check the Peppol rules
              </Button>
            ) : null}
            <Link className={buttonClassName("ghost")} to="/integration/einvoices">
              E-invoices
            </Link>
          </>
        }
      />
      {failure ? (
        <Alert tone="danger" title="That did not work" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {e.status === "SUBMITTING" ? (
        <Alert tone="warning" title="Being handed to the network" live="status">
          Another submit is at the access point. This page shows the result when it finishes; reload in a moment.
        </Alert>
      ) : null}
      {e.status === "INVALID" ? (
        <Alert tone="danger" title="Blocking issues">
          Fix the cause (a party profile, the invoice) and build the e-invoice again. It cannot be submitted as it is.
        </Alert>
      ) : null}
      {e.status === "REJECTED" && e.responseText ? (
        <Alert tone="danger" title="The network rejected it">
          {e.responseText}
        </Alert>
      ) : null}
      {check ? (
        <Alert tone={check.valid ? "success" : "danger"} title={check.valid ? "The stored document passes the Peppol rules" : "The stored document breaks Peppol rules"} live="status">
          {check.valid ? "The Go subset of the Peppol BIS Billing 3.0 rules found nothing." : <IssueList issues={check.issues} label="Failed Peppol rules" />}
        </Alert>
      ) : null}
      <Card title="Details">
        <dl className={styles.details}>
          <Entry term="Legal entity">
            <EntityName entity="legalEntity" id={e.legalEntityId} />
          </Entry>
          <Entry term="Format">{e.format}</Entry>
          <Entry term="Network reference" mono>
            {e.reference ?? "—"}
          </Entry>
          <Entry term="Last change">{formatDateTime(e.updatedAt)}</Entry>
        </dl>
      </Card>
      {e.issues.length > 0 ? (
        <Card title="Issues">
          <IssueList issues={e.issues} label="E-invoice issues" />
        </Card>
      ) : null}
      {e.status === "SUBMITTED" ? (
        <Card title="Record the network's answer">
          <form className={styles.form} onSubmit={answer} noValidate>
            <FormField id="answer-outcome" label="Answer" error={errors.outcome}>
              {(control) => (
                <Select {...control} value={outcome} onChange={(event) => setOutcome(event.target.value)}>
                  <option value="ACCEPTED">Accepted</option>
                  <option value="REJECTED">Rejected</option>
                </Select>
              )}
            </FormField>
            <FormField id="answer-reference" label="Network reference" hint="The reference of the answer, when the network gave one.">
              {(control) => <Input {...control} value={reference} maxLength={200} onChange={(event) => setReference(event.target.value)} />}
            </FormField>
            <FormField id="answer-reason" label="Reason" error={errors.reason} hint="Required when the network rejected the document.">
              {(control) => <Input {...control} value={reason} maxLength={1000} onChange={(event) => setReason(event.target.value)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "answer"} disabled={busy !== null}>
                Record the answer
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {e.xml ? (
        <Card title="Document">
          <pre className={styles.payload} aria-label="E-invoice document">
            {e.xml}
          </pre>
        </Card>
      ) : null}
    </div>
  );
}
