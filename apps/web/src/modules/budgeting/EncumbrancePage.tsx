import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  describeError,
  getEncumbrance,
  releaseEncumbrance,
  relieveEncumbrance,
  type EncumbranceHistory,
  type EncumbranceReleaseOutcome,
  type EncumbranceReleaseReason,
  type ReliefOutcome,
} from "./api/client";
import { formatAmount, isMoney, toUnits, UUID } from "./logic";
import styles from "./pages.module.css";
import { Detail, formatDateTime, LoadError, statusLabel } from "./parts";
import { useLoad } from "./useLoad";

const historyColumns: DataTableColumn<EncumbranceHistory>[] = [
  { key: "at", header: "When", render: (h) => formatDateTime(h.at) },
  { key: "type", header: "Movement" },
  { key: "amount", header: "Amount", align: "end", mono: true, render: (h) => formatAmount(h.amount) },
  { key: "documentId", header: "Document", mono: true },
];

interface ReliefForm {
  invoiceId: string;
  invoiceQuantity: string;
  poUnitPrice: string;
  invoiceAmount: string;
}

/**
 * Gives the open remainder back to available funds when the PO line is cancelled or the PO is closed (FR-38.5).
 * Release is final, so the form asks for the purchase order and a confirmation before it sends.
 */
function ReleasePanel({ id, documentId, remaining, onReleased }: { id: string; documentId: string; remaining: string; onReleased(): void }) {
  const [reason, setReason] = useState<EncumbranceReleaseReason>("Cancelled");
  const [po, setPo] = useState(documentId);
  const [error, setError] = useState<string | undefined>();
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [done, setDone] = useState<EncumbranceReleaseOutcome | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  function ask(e: FormEvent) {
    e.preventDefault();
    const found = UUID.test(po.trim()) ? undefined : "Enter the purchase order id (a UUID).";
    setError(found);
    setFailure(null);
    if (!found) setConfirming(true);
  }

  async function release() {
    const input = { documentId: po.trim(), reason };
    const body = JSON.stringify(input);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      setDone(await releaseEncumbrance(id, input, key.current.value));
      setConfirming(false);
      onReleased();
    } catch (err) {
      setFailure(describeError(err, "The encumbrance could not be released."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Release on PO cancellation or close" headingLevel={3}>
      <form className={styles.form} onSubmit={ask} noValidate>
        <FormField id="release-reason" label="Reason">
          {(c) => (
            <Select {...c} value={reason} onChange={(e) => setReason(e.target.value as EncumbranceReleaseReason)}>
              <option value="Cancelled">PO line cancelled</option>
              <option value="Closed">PO closed with a residual</option>
            </Select>
          )}
        </FormField>
        <FormField id="release-po" label="Purchase order id" error={error}>
          {(c) => <Input {...c} className={styles.mono} value={po} invalid={Boolean(error)} onChange={(e) => setPo(e.target.value)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="secondary">
            Release remaining funds
          </Button>
        </div>
      </form>
      {confirming ? (
        <Alert
          tone="warning"
          live="alert"
          title={`Release ${formatAmount(remaining)}?`}
          action={
            <div className={styles.formActions}>
              <Button size="sm" variant="primary" loading={busy} onClick={release}>
                Confirm release
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setConfirming(false)}>
                Keep reserved
              </Button>
            </div>
          }
        >
          The amount goes back to available funds and the line closes. It cannot take a new check afterwards.
        </Alert>
      ) : null}
      <Alert tone="danger" live="alert">
        {failure}
      </Alert>
      <Alert tone="success" live="status">
        {done ? `Released ${formatAmount(done.released)} back to available funds. The line is closed.` : null}
      </Alert>
    </Card>
  );
}

/** One encumbrance with its history, invoice relief (FR-38.5, BR-38.4) and release on PO cancellation or close. */
export function EncumbrancePage() {
  const { id = "" } = useParams();
  const [reload, setReload] = useState(0);
  const state = useLoad((signal) => getEncumbrance(id, signal), `${id}|${reload}`);
  const [form, setForm] = useState<ReliefForm>({ invoiceId: "", invoiceQuantity: "", poUnitPrice: "", invoiceAmount: "" });
  const [errors, setErrors] = useState<Partial<Record<keyof ReliefForm, string>>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [relief, setRelief] = useState<ReliefOutcome | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  async function submit(e: FormEvent) {
    e.preventDefault();
    const clean = { invoiceId: form.invoiceId.trim(), invoiceQuantity: form.invoiceQuantity.trim(), poUnitPrice: form.poUnitPrice.trim(), invoiceAmount: form.invoiceAmount.trim() };
    const found: typeof errors = {};
    if (!UUID.test(clean.invoiceId)) found.invoiceId = "Enter the invoice id (a UUID).";
    for (const f of ["invoiceQuantity", "poUnitPrice", "invoiceAmount"] as const) {
      if (!isMoney(clean[f]) || clean[f].startsWith("-")) found[f] = "Enter a positive decimal with at most 4 places.";
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const body = JSON.stringify(clean);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      setRelief(await relieveEncumbrance(id, clean, key.current.value));
      setReload((n) => n + 1);
    } catch (err) {
      setFailure(describeError(err, "The relief could not be applied."));
    } finally {
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Encumbrance" />
        <LoadError title="Could not load this encumbrance" error={state.error} onRetry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Encumbrance" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const enc = state.data;
  const field = (name: keyof ReliefForm, label: string, hint?: string) => (
    <FormField id={`relief-${name}`} label={label} hint={hint} error={errors[name]}>
      {(c) => (
        <Input
          {...c}
          className={styles.mono}
          inputMode={name === "invoiceId" ? undefined : "decimal"}
          value={form[name]}
          invalid={Boolean(errors[name])}
          onChange={(e) => setForm((f) => ({ ...f, [name]: e.target.value }))}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Encumbrance"
        description="Funds reserved for one document line. Invoices relieve it at the PO price; any price difference is checked as a new amount."
        meta={<StatusPill status={statusLabel(enc.status)} />}
        actions={<Link to={`/budgeting/rules/${enc.ruleVersionId}`}>Back to the rule</Link>}
      />
      <Card>
        <dl className={styles.details}>
          <Detail term="Document" mono>
            {enc.sourceDocumentId}
          </Detail>
          <Detail term="Source">{enc.sourceType}</Detail>
          <Detail term="Stage">{enc.stage === "pre" ? "Pre-encumbrance from a requisition, waiting for its purchase order" : "Encumbrance"}</Detail>
          <Detail term="Line" mono>
            {enc.sourceLineId}
          </Detail>
          <Detail term="Dimension" mono>
            {enc.controlDimensionKey} · {enc.timeSpanKey}
          </Detail>
          <Detail term="Reserved" mono>
            {formatAmount(enc.reserved)}
          </Detail>
          <Detail term="Relieved" mono>
            {formatAmount(enc.relieved)}
          </Detail>
          <Detail term="Released" mono>
            {formatAmount(enc.released)}
          </Detail>
          <Detail term="Remaining" mono>
            {formatAmount(enc.remaining)}
          </Detail>
        </dl>
      </Card>
      <DataTable label="Encumbrance history" columns={historyColumns} rows={enc.history} getRowKey={(h, i) => `${h.at}-${i}`} />
      <Card title="Relieve from an invoice" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("invoiceId", "Invoice id")}
          {field("invoiceQuantity", "Invoiced quantity")}
          {field("poUnitPrice", "PO unit price", "Relief is quantity × PO price, capped at the remaining reservation.")}
          {field("invoiceAmount", "Invoice amount")}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy} disabled={enc.status === "Relieved" || enc.status === "Released" || enc.stage === "pre"}>
              Relieve encumbrance
            </Button>
          </div>
        </form>
      </Card>
      {toUnits(enc.remaining) > 0n ? (
        <ReleasePanel id={id} documentId={enc.sourceDocumentId} remaining={enc.remaining} onReleased={() => setReload((n) => n + 1)} />
      ) : enc.status === "Released" ? (
        <Alert tone="info" title="Released">
          The open remainder went back to available funds. An invoice against this line now relieves nothing and is checked as a new amount.
        </Alert>
      ) : null}
      <Alert tone="danger" live="alert">
        {failure}
      </Alert>
      <Alert tone={relief && toUnits(relief.excess) !== 0n ? "warning" : "success"} live="status">
        {relief
          ? `Relieved ${formatAmount(relief.relieved)}; ${formatAmount(relief.remaining)} remains reserved.` +
            (toUnits(relief.excess) !== 0n ? ` The invoice exceeds the reservation by ${formatAmount(relief.excess)}; run a budget check for that amount.` : "")
          : null}
      </Alert>
    </div>
  );
}
