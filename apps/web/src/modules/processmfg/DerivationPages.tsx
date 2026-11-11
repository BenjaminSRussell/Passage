import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import {
  deriveStoredBatch,
  describeProblem,
  getBatch,
  listDerivations,
  newKey,
  type Batch,
  type Derivation,
  type DerivedValue,
  type DeriveStoredResult,
  type Problem,
} from "./api/client";
import { dash, formatDateTime, QUANTITY, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

/** Duty that records batch attribute values, which a derivation does; enforced by the service (FR-24.15). */
export const DERIVE_DUTY = "production.batch-attribute.write";

interface SenderRow {
  batchId: string;
  qty: string;
}

const derivedColumns: DataTableColumn<DerivedValue>[] = [
  { key: "attribute", header: "Attribute" },
  { key: "aggregation", header: "Derived by" },
  { key: "value", header: "Value", render: (d) => (d.recorded ? (d.value ?? dash) : "Left as it was") },
  { key: "missing", header: "Senders without a value", align: "end", render: (d) => String(d.missing.length) },
];

/**
 * FR-24.15, AC-24.11: derives a stored batch's attributes from its sender
 * batches with the item's active rules. The senders' values come from their
 * quality records; the quantity consumed from each is typed, because the
 * register keeps none. Running it again re-derives.
 */
export function DeriveStoredPage() {
  const allowed = holdsDuty(usePrincipalDuties(), DERIVE_DUTY);
  const [receiverId, setReceiverId] = useState("");
  const [receiver, setReceiver] = useState<Batch | null>(null);
  const [rows, setRows] = useState<SenderRow[]>([{ batchId: "", qty: "" }]);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<DeriveStoredResult | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function touch() {
    key.current = newKey();
    setResult(null);
  }

  if (!allowed) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Derive batch attributes" />
        <Card title="Derivation">
          <p className={styles.note}>Deriving attributes needs the batch attribute duty. Ask your administrator if you should have it.</p>
        </Card>
      </div>
    );
  }

  function setRow(i: number, patch: Partial<SenderRow>) {
    touch();
    setRows((prev) => prev.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    const id = receiverId.trim();
    if (!UUID.test(id)) found.receiverId = "Enter the receiver batch id as a UUID.";
    const ids = rows.map((r) => r.batchId.trim().toLowerCase());
    if (rows.some((r) => !UUID.test(r.batchId.trim()))) found.senders = "Enter each sender batch id as a UUID.";
    else if (new Set(ids).size !== ids.length) found.senders = "A sender is listed twice.";
    else if (ids.includes(id.toLowerCase())) found.senders = "A batch cannot be its own sender.";
    else if (rows.some((r) => !QUANTITY.test(r.qty.trim()))) found.senders = "Enter the quantity consumed from each sender, 0 or more, at most 6 decimals.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      // The version is read just before the command, so the answer is the receiver as it stands now.
      const current = await getBatch(id);
      setReceiver(current);
      const res = await deriveStoredBatch(id, current.version, rows.map((r) => ({ batchId: r.batchId.trim(), qty: r.qty.trim() })), key.current);
      setResult(res);
      setReceiver(res.batch);
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Derive batch attributes" description="Fill a batch's attributes from the batches it was made from, with the inheritance rules of its item. Running it again derives again from the senders you list." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <LookupFormField id="derive-receiver" label="Receiver batch" entity="batch" value={receiverId} error={errors.receiverId} onChange={(id) => { touch(); setReceiverId(id); }} />
          {rows.map((row, i) => (
            <div className={styles.lineRow} key={i}>
              <LookupFormField id={`derive-sender-${i}`} label={`Sender ${i + 1} batch`} entity="batch" value={row.batchId} onChange={(id) => setRow(i, { batchId: id })} />
              <FormField id={`derive-qty-${i}`} label={`Sender ${i + 1} quantity`}>
                {(c) => <Input {...c} inputMode="decimal" value={row.qty} onChange={(e) => setRow(i, { qty: e.target.value })} invalid={Boolean(errors.senders)} />}
              </FormField>
              {rows.length > 1 ? (
                <Button size="sm" variant="ghost" onClick={() => { touch(); setRows((prev) => prev.filter((_, j) => j !== i)); }}>
                  Remove sender {i + 1}
                </Button>
              ) : null}
            </div>
          ))}
          {errors.senders ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Check the senders" live="alert">{errors.senders}</Alert>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not derive the attributes" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button onClick={() => { touch(); setRows((prev) => [...prev, { batchId: "", qty: "" }]); }} disabled={rows.length >= 50}>
              Add sender
            </Button>
            <Button type="submit" variant="primary" loading={busy}>
              Derive attributes
            </Button>
          </div>
        </form>
      </Card>
      {result && receiver ? (
        <>
          <StatGroup label="Derivation">
            <StatTile label="Batch" value={receiver.batchNumber} />
            <StatTile label="Version" value={String(receiver.version)} />
            <StatTile label="Rules applied" value={String(result.derived.length)} />
            <StatTile label="Expiry" value={receiver.expiryDate ?? dash} />
          </StatGroup>
          {result.warnings.length > 0 ? (
            <Alert tone="warning" title="Senders without a value" live="status">
              {result.warnings.join(" ")}
            </Alert>
          ) : null}
          <DataTable label="Derived attributes" columns={derivedColumns} rows={result.derived} getRowKey={(d) => d.ruleId} empty={{ title: "No rules applied" }} />
          <Link to={`/processmfg/batches/${receiver.id}`}>Open the batch</Link>
        </>
      ) : null}
    </div>
  );
}

const logColumns: DataTableColumn<Derivation>[] = [
  { key: "runAt", header: "When", render: (d) => formatDateTime(d.runAt) },
  { key: "batchNumber", header: "Receiver", mono: true, render: (d) => <Link to={`/processmfg/batches/${d.receiverBatchId}`}>{d.batchNumber}</Link> },
  { key: "attribute", header: "Attribute" },
  { key: "aggregation", header: "Derived by" },
  { key: "valueBefore", header: "Before", render: (d) => d.valueBefore ?? dash },
  { key: "value", header: "After", render: (d) => (d.recorded ? (d.value ?? dash) : "Left as it was") },
  { key: "senders", header: "Senders", align: "end", render: (d) => String(d.senders.length) },
  { key: "missing", header: "Without a value", align: "end", render: (d) => (d.missing.length === 0 ? "0" : d.missing.map(shortId).join(", ")) },
  { key: "runBy", header: "By" },
];

/** FR-24.15: the derivation monitor, the log of every derivation with the senders that had no value. */
export function DerivationMonitorPage() {
  const [batchId, setBatchId] = useState("");
  const [itemId, setItemId] = useState("");
  const [withMissing, setWithMissing] = useState(false);
  const batch = batchId.trim();
  const item = itemId.trim();
  const bad = (batch !== "" && !UUID.test(batch)) || (item !== "" && !UUID.test(item));
  const state = useLoad(
    (signal) => (bad ? Promise.resolve([]) : listDerivations({ batchId: batch, itemId: item, withMissing }, signal)),
    `derivations|${batch}|${item}|${withMissing}|${bad}`,
  );
  return (
    <div className={styles.stack}>
      <PageHeader title="Derivation monitor" description="Every derivation of an inheritance rule on a batch, newest first: the value before and after, the senders used and the senders that had no value." />
      <div className={styles.toolbar}>
        <LookupFormField id="mon-batch" label="Receiver batch" entity="batch" optional value={batchId} error={batch !== "" && !UUID.test(batch) ? "Choose the batch." : undefined} onChange={setBatchId} />
        <FormField id="mon-item" label="Item" error={item !== "" && !UUID.test(item) ? "Enter the item id as a UUID." : undefined}>
          {(c) => <EntityLookup entity="product" {...c} value={itemId} onChange={(e) => setItemId(e.target.value)} invalid={item !== "" && !UUID.test(item)} />}
        </FormField>
        <div className={styles.lineChecks}>
          <label htmlFor="mon-missing">
            <input id="mon-missing" type="checkbox" checked={withMissing} onChange={(e) => setWithMissing(e.target.checked)} /> Only derivations with senders lacking a value
          </label>
        </div>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the derivations" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Derivations"
          columns={logColumns}
          rows={state.data ?? []}
          getRowKey={(d) => d.id}
          loading={state.loading}
          empty={
            batch || item || withMissing
              ? { title: "No derivations match", description: "Clear the filters to see every derivation." }
              : { title: "Nothing derived yet", description: "A derivation is logged when a batch's attributes are derived from its senders." }
          }
        />
      )}
    </div>
  );
}
