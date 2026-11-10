import { useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, generateCoA, listCoAs, newKey, type CoA, type CoALine, type NewCoA, type OrderView, type Problem } from "./api/client";
import { dash, formatDateTime, pickFields, qty, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** True when the order's decision released quantity, so a CoA can be issued (FR-33.9). */
export function coaReleasable(order: OrderView): boolean {
  if (order.status !== "CLOSED" || !order.decision) return false;
  const positive = (value: string) => /[1-9]/.test(value);
  return positive(order.decision.split.acceptQty) || positive(order.decision.split.deviationQty);
}

export interface CoAValues {
  shipmentId: string;
  customerId: string;
  reason: string;
}

type CoAErrors = Partial<Record<keyof CoAValues, string>>;

/**
 * Checks that mirror the service's ValidateCoARequest and BR-33.10: a revision after
 * the first of the same (order, shipment) chain needs the reason for the amendment.
 */
export function validateCoA(orderId: string, values: CoAValues, existing: CoA[]): { errors: CoAErrors; input?: NewCoA } {
  const errors: CoAErrors = {};
  const shipmentId = values.shipmentId.trim();
  const customerId = values.customerId.trim();
  const reason = values.reason.trim();
  if (shipmentId && !UUID.test(shipmentId)) errors.shipmentId = "Enter the shipment id (a UUID), or leave it empty for the batch certificate.";
  if (customerId && !UUID.test(customerId)) errors.customerId = "Enter the customer id (a UUID).";
  const chainExists = existing.some((c) => (c.shipmentId ?? "") === shipmentId);
  if (chainExists && reason === "") errors.reason = "A certificate already exists; give the reason for the new revision.";
  if (reason.length > 500) errors.reason = "Keep the reason to 500 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewCoA = { qualityOrderId: orderId };
  if (shipmentId) input.shipmentId = shipmentId;
  if (customerId) input.customerId = customerId;
  if (reason) input.reason = reason;
  return { errors, input };
}

/** "6.5 – 7.5", "≥ 355", "passing outcome". */
export function specification(line: CoALine): string {
  if (line.kind === "ATTRIBUTE") return "Passing outcome";
  if (line.min !== undefined && line.max !== undefined) return `${qty(line.min)} – ${qty(line.max)}`;
  if (line.min !== undefined) return `≥ ${qty(line.min)}`;
  if (line.max !== undefined) return `≤ ${qty(line.max)}`;
  return dash;
}

const revisionColumns = (onView: (doc: CoA) => void, selected?: string): DataTableColumn<CoA>[] => [
  { key: "revision", header: "Revision", align: "end", render: (row) => String(row.revision) },
  { key: "shipmentId", header: "Shipment", mono: true, render: (row) => (row.shipmentId ? shortId(row.shipmentId) : "Batch certificate") },
  { key: "customerId", header: "Customer", mono: true, render: (row) => (row.customerId ? shortId(row.customerId) : dash) },
  { key: "supersedes", header: "Supersedes", mono: true, render: (row) => (row.supersedes ? shortId(row.supersedes) : dash) },
  { key: "reason", header: "Reason", render: (row) => row.reason || dash },
  { key: "signedBy", header: "Signed by", render: (row) => row.signedBy || dash },
  { key: "issuedAt", header: "Issued", render: (row) => `${row.issuedBy || dash}, ${formatDateTime(row.issuedAt)}` },
  {
    key: "view",
    header: "Document",
    render: (row) => (
      <Button size="sm" variant={row.id === selected ? "primary" : "secondary"} aria-label={`View revision ${row.revision}${row.shipmentId ? ` of shipment ${shortId(row.shipmentId)}` : ""}`} onClick={() => onView(row)}>
        View
      </Button>
    ),
  },
];

const lineColumns: DataTableColumn<CoALine>[] = [
  { key: "testName", header: "Test", render: (row) => row.testName || shortId(row.testId) },
  { key: "sampleNo", header: "Sample", align: "end", render: (row) => String(row.sampleNo) },
  { key: "spec", header: "Specification", mono: true, render: (row) => specification(row) },
  { key: "value", header: "Result", mono: true, render: (row) => (row.value !== undefined ? qty(row.value) : shortId(row.outcomeId)) },
  { key: "passed", header: "Conforms", render: (row) => <span className={row.passed ? styles.pass : styles.fail}>{row.passed ? "Pass" : "Fail"}</span> },
  { key: "instrumentId", header: "Instrument", mono: true, render: (row) => (row.instrumentId ? shortId(row.instrumentId) : dash) },
  { key: "recordedBy", header: "Tested by", render: (row) => row.recordedBy || dash },
];

/** The CoA as issued: header, the decision's signature block and one line per test and sample. */
export function CoADocument({ doc }: { doc: CoA }) {
  const entry = (term: string, value: string, mono = false) => (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
    </div>
  );
  return (
    <section className={styles.stack} aria-label={`Certificate of analysis revision ${doc.revision}`}>
      <dl className={styles.details}>
        {entry("Item", doc.itemId, true)}
        {entry("Batch", doc.batch || dash, true)}
        {entry("Lot", `${qty(doc.lotQty)} ${doc.uom}`)}
        {entry("Accepted", `${qty(doc.acceptQty)} ${doc.uom}`)}
        {entry("Accepted with deviation", `${qty(doc.deviationQty)} ${doc.uom}${doc.deviations.length ? ` (${doc.deviations.join("; ")})` : ""}`)}
        {entry("Specification", `${doc.testGroupCode} v${doc.testGroupVersion}, ${doc.specificationSet === "DEFAULT" ? "default limits" : doc.specificationSet}`)}
        {entry("Revision", doc.supersedes ? `${doc.revision}, supersedes ${shortId(doc.supersedes)}` : String(doc.revision))}
        {entry("Signed", `${doc.signedBy || dash}, ${formatDateTime(doc.signedAt)} (usage decision)`)}
      </dl>
      <DataTable label={`Results on revision ${doc.revision}`} columns={lineColumns} rows={doc.lines} getRowKey={(row) => `${row.testId}|${row.sampleNo}`} empty={{ title: "No results on this certificate" }} />
    </section>
  );
}

/**
 * Certificates of analysis of a closed order (FR-33.9, BR-33.10): the revisions of
 * the batch certificate and of each shipment, a preview of each, and the form that
 * issues the next revision.
 */
export function CoAPanel({ order }: { order: OrderView }) {
  const [refresh, setRefresh] = useState(0);
  const [selected, setSelected] = useState<CoA | null>(null);
  const state = useLoad((signal) => listCoAs(order.id, signal), `coas|${order.id}|${refresh}`);
  const releasable = coaReleasable(order);
  const docs = state.data ?? [];

  return (
    <Card title="Certificates of analysis" actions={<Badge tone={docs.length > 0 ? "success" : "neutral"}>{docs.length > 0 ? `${docs.length} issued` : "None issued"}</Badge>}>
      <div className={styles.stack}>
        <p className={styles.note}>
          {releasable
            ? "A certificate prints the results this decision was taken on, with the test group limits, signed by the decision user. Issue one for the batch, or one per shipment; a correction is a new revision that supersedes the last."
            : "The usage decision released no quantity, so no certificate of analysis can be issued for this lot."}
        </p>
        {state.error ? (
          <Alert tone="danger" title="Could not load the certificates of analysis" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        ) : (
          <DataTable
            label="Certificate of analysis revisions"
            columns={revisionColumns(setSelected, selected?.id)}
            rows={docs}
            getRowKey={(row) => row.id}
            loading={state.loading && !state.data}
            empty={{ title: "No certificate issued yet", description: releasable ? "Issue the batch certificate, or one for a shipment." : undefined }}
          />
        )}
        {selected ? <CoADocument doc={selected} /> : null}
        {releasable && !state.error ? (
          <IssueCoAForm
            order={order}
            existing={docs}
            onIssued={(doc) => {
              setSelected(doc);
              setRefresh((n) => n + 1);
            }}
          />
        ) : null}
      </div>
    </Card>
  );
}

function IssueCoAForm({ order, existing, onIssued }: { order: OrderView; existing: CoA[]; onIssued(doc: CoA): void }) {
  const [values, setValues] = useState<CoAValues>({ shipmentId: "", customerId: "", reason: "" });
  const [errors, setErrors] = useState<CoAErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const chainExists = existing.some((c) => (c.shipmentId ?? "") === values.shipmentId.trim());

  function update(patch: Partial<CoAValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setNotice(null);
    const checked = validateCoA(order.id, values, existing);
    setErrors(checked.errors);
    if (!checked.input) return;
    setBusy(true);
    try {
      const doc = await generateCoA(checked.input, key.current);
      key.current = newKey();
      setValues({ shipmentId: "", customerId: "", reason: "" });
      setNotice(`Revision ${doc.revision} issued${doc.shipmentId ? ` for shipment ${shortId(doc.shipmentId)}` : " for the batch"}.`);
      onIssued(doc);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = pickFields(problem.fields, ["shipmentId", "customerId", "reason"] as const);
      if (problem.code === "QM-0187") fieldErrors.reason = problem.message;
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Issue a certificate of analysis">
      <FormField id="coa-shipmentId" label="Shipment" optional hint="Leave empty for the batch certificate." error={errors.shipmentId}>
        {(control) => <Input {...control} value={values.shipmentId} invalid={Boolean(errors.shipmentId)} onChange={(event) => update({ shipmentId: event.target.value })} />}
      </FormField>
      <FormField id="coa-customerId" label="Customer" optional error={errors.customerId}>
        {(control) => <Input {...control} value={values.customerId} invalid={Boolean(errors.customerId)} onChange={(event) => update({ customerId: event.target.value })} />}
      </FormField>
      <FormField
        id="coa-reason"
        label="Reason for a new revision"
        optional={!chainExists}
        hint={chainExists ? "A certificate exists for this batch or shipment; the new revision supersedes it." : "Only needed for a later revision."}
        error={errors.reason}
      >
        {(control) => <Textarea {...control} rows={2} value={values.reason} invalid={Boolean(errors.reason)} onChange={(event) => update({ reason: event.target.value })} />}
      </FormField>
      {failure || notice ? (
        <div className={styles.formActions}>
          {failure ? (
            <Alert tone="danger" title="Could not issue the certificate" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          ) : null}
          {notice ? (
            <Alert tone="success" live="status">
              {notice}
            </Alert>
          ) : null}
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Issue certificate of analysis
        </Button>
      </div>
    </form>
  );
}
