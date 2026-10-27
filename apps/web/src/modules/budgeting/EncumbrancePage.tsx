import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { describeError, getEncumbrance, relieveEncumbrance, type EncumbranceHistory, type ReliefOutcome } from "./api/client";
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

/** One encumbrance with its history and invoice relief (FR-38.5, BR-38.4). */
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
            <Button type="submit" variant="primary" loading={busy} disabled={enc.status === "Relieved"}>
              Relieve encumbrance
            </Button>
          </div>
        </form>
      </Card>
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
