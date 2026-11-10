import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  cancelReceipt,
  correctReceipt,
  fetchLocations,
  fetchReceipt,
  INVENTORY_STATUSES,
  postReceipt,
  registerReceipt,
  type Receipt,
  type ReceiptLine,
  type RegisterLineInput,
} from "./api/client";
import { DECIMAL, SIGNED_DECIMAL, UUID } from "../../shell/api/http";
import { Named, Detail, PurchaseOrderRef, StateBadge, useAction } from "./parts";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import styles from "../../shell/pageLayout.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

const lineColumns: DataTableColumn<ReceiptLine>[] = [
  { key: "lineNo", header: "Line", align: "end" },
  { key: "productId", header: "Product", render: (l) => <Named entity="product" id={l.productId} /> },
  { key: "qty", header: "Quantity", align: "end", render: (l) => `${l.qty} ${l.unit}` },
  { key: "unitPrice", header: "Unit price", align: "end" },
  { key: "amount", header: "Amount", align: "end", render: (l) => l.amount ?? "" },
  { key: "batch", header: "Batch" },
  { key: "serials", header: "Serials", render: (l) => l.serials.join(", ") },
  { key: "inventoryStatus", header: "Inventory status" },
];

export interface RegisterDraft {
  qty: string;
  batch: string;
  serials: string;
  locationId: string;
  /** Only narrows the location choices; the service takes the location. */
  warehouseId?: string;
  inventoryStatus: string;
}

export function validateRegister(receipt: Receipt, drafts: RegisterDraft[]): Errors {
  const errors: Errors = {};
  receipt.lines.forEach((line, i) => {
    const d = drafts[i];
    const at = (field: string) => `lines[${i}].${field}`;
    if (!DECIMAL.test(d.qty) || Number(d.qty) <= 0) errors[at("qty")] = "Enter a quantity above zero.";
    if (!UUID.test(d.locationId)) errors[at("locationId")] = "Choose where the goods go.";
    if (line.batchTracked && d.batch.trim() === "") errors[at("batch")] = "Enter the batch.";
    if (line.serialTracked) {
      const serials = splitSerials(d.serials);
      if (!/^\d+$/.test(d.qty) || serials.length !== Number(d.qty)) errors[at("serials")] = "Enter one serial per unit, one per line.";
    }
  });
  return errors;
}

const splitSerials = (text: string) => text.split("\n").map((s) => s.trim()).filter(Boolean);

export function toRegisterInput(receipt: Receipt, drafts: RegisterDraft[]): RegisterLineInput[] {
  return receipt.lines.map((line, i) => ({
    lineNo: line.lineNo,
    qty: drafts[i].qty.trim(),
    ...(drafts[i].batch.trim() ? { batch: drafts[i].batch.trim() } : {}),
    ...(line.serialTracked ? { serials: splitSerials(drafts[i].serials) } : {}),
    locationId: drafts[i].locationId,
    inventoryStatus: drafts[i].inventoryStatus,
  }));
}

function ConfirmStep({ confirmLabel, keepLabel, variant = "primary", busy, onConfirm, onKeep }: { confirmLabel: string; keepLabel: string; variant?: "primary" | "danger"; busy: boolean; onConfirm: () => void; onKeep: () => void }) {
  const confirm = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    confirm.current?.focus();
  }, []);
  return (
    <div className={styles.formActions}>
      <Button ref={confirm} variant={variant} loading={busy} onClick={onConfirm}>
        {confirmLabel}
      </Button>
      <Button variant="ghost" onClick={onKeep}>
        {keepLabel}
      </Button>
    </div>
  );
}

function FailureAlert({ title, failure, onReload }: { title: string; failure: { error: unknown; stale: boolean } | null; onReload: () => void }) {
  return failure ? (
    <Alert tone="danger" title={title} live="alert" action={failure.stale ? <Button size="sm" onClick={onReload}>Reload receipt</Button> : undefined}>
      <ProblemMessage problem={failure.error} />
    </Alert>
  ) : null;
}

/** A warehouse, then one of its bin locations by name. */
function LocationPicker({ n, i, draft, update, error }: { n: number; i: number; draft: RegisterDraft; update: (patch: Partial<RegisterDraft>) => void; error?: string }) {
  const warehouseId = draft.warehouseId ?? "";
  const locations = useQuery({ queryKey: ["receiving", "locations", warehouseId], queryFn: () => fetchLocations(warehouseId), enabled: warehouseId !== "" });
  return (
    <>
      <FormField id={`reg-${i}-warehouse`} label={`Line ${n} warehouse`} hint="Where the goods are put away.">
        {(c) => <EntityLookup entity="warehouse" {...c} value={warehouseId} onChange={(e) => update({ warehouseId: e.target.value, locationId: "" })} />}
      </FormField>
      <FormField id={`reg-${i}-location`} label={`Line ${n} location`} error={error} hint={warehouseId === "" ? "Choose a warehouse first." : locations.isError ? "Could not load the locations." : undefined}>
        {(c) => (
          <Select {...c} value={draft.locationId} disabled={warehouseId === ""} onChange={(e) => update({ locationId: e.target.value })} invalid={Boolean(error)}>
            <option value="">Choose a location</option>
            {(locations.data ?? []).map((l) => (
              <option key={l.id} value={l.id}>
                {l.zone ? `${l.name} · ${l.zone}` : l.name}
              </option>
            ))}
          </Select>
        )}
      </FormField>
    </>
  );
}

function RegisterForm({ receipt, reload }: { receipt: Receipt; reload: () => void }) {
  const action = useAction(receipt.id);
  const [drafts, setDrafts] = useState<RegisterDraft[]>(() =>
    receipt.lines.map((l) => ({ qty: l.orderedRemaining, batch: "", serials: "", locationId: "", inventoryStatus: "AVAILABLE" })),
  );
  const [errors, setErrors] = useState<Errors>({});
  const update = (i: number, patch: Partial<RegisterDraft>) => setDrafts((prev) => prev.map((d, j) => (j === i ? { ...d, ...patch } : d)));
  const shown = { ...errors, ...(action.failure?.fields ?? {}) };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateRegister(receipt, drafts);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    await action.run((key) => registerReceipt(receipt.id, receipt.version, toRegisterInput(receipt, drafts), key));
  }

  return (
    <Card title="Register received goods">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Register receipt">
        {receipt.lines.map((line, i) => {
          const n = line.lineNo;
          const at = (field: string) => `lines[${i}].${field}`;
          return (
            <fieldset key={line.lineNo} className={styles.group}>
              <legend className={styles.legend}>{`Line ${n}, ${line.orderedRemaining} ${line.unit} expected`}</legend>
              <FormField id={`reg-${i}-qty`} label={`Line ${n} quantity received`} error={shown[at("qty")]}>
                {(c) => <Input {...c} inputMode="decimal" value={drafts[i].qty} onChange={(e) => update(i, { qty: e.target.value })} invalid={Boolean(shown[at("qty")])} />}
              </FormField>
              <LocationPicker n={n} i={i} draft={drafts[i]} update={(patch) => update(i, patch)} error={shown[at("locationId")]} />
              <FormField id={`reg-${i}-status`} label={`Line ${n} inventory status`}>
                {(c) => (
                  <Select {...c} value={drafts[i].inventoryStatus} onChange={(e) => update(i, { inventoryStatus: e.target.value })}>
                    {INVENTORY_STATUSES.map((s) => (
                      <option key={s} value={s}>
                        {s.charAt(0) + s.slice(1).toLowerCase()}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              {line.batchTracked ? (
                <FormField id={`reg-${i}-batch`} label={`Line ${n} batch`} error={shown[at("batch")]}>
                  {(c) => <Input {...c} value={drafts[i].batch} onChange={(e) => update(i, { batch: e.target.value })} invalid={Boolean(shown[at("batch")])} />}
                </FormField>
              ) : null}
              {line.serialTracked ? (
                <FormField id={`reg-${i}-serials`} label={`Line ${n} serials`} hint="One serial per unit, one per line." error={shown[at("serials")]}>
                  {(c) => <Textarea {...c} className={styles.mono} rows={4} value={drafts[i].serials} onChange={(e) => update(i, { serials: e.target.value })} invalid={Boolean(shown[at("serials")])} />}
                </FormField>
              ) : null}
            </fieldset>
          );
        })}
        <div className={styles.formActions}>
          <FailureAlert title="Could not register the receipt" failure={action.failure} onReload={reload} />
        </div>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={action.busy}>
            Register receipt
          </Button>
        </div>
      </form>
    </Card>
  );
}

function PostPanel({ receipt, reload }: { receipt: Receipt; reload: () => void }) {
  const action = useAction(receipt.id);
  const [confirming, setConfirming] = useState(false);
  return (
    <Card title="Post to the ledger">
      <div className={styles.stack}>
        <p className={styles.note}>Posting debits inventory and credits GR/IR for every line, opens the GR/IR items for invoicing and tells inventory and payables.</p>
        <FailureAlert title="Could not post the receipt" failure={action.failure} onReload={reload} />
        {confirming ? (
          <ConfirmStep
            confirmLabel="Confirm post"
            keepLabel="Not yet"
            busy={action.busy}
            onConfirm={() => void action.run((key) => postReceipt(receipt.id, receipt.version, key))}
            onKeep={() => setConfirming(false)}
          />
        ) : (
          <div>
            <Button variant="primary" onClick={() => setConfirming(true)}>
              Post receipt
            </Button>
          </div>
        )}
      </div>
    </Card>
  );
}

export function validatePostingDate(receipt: Receipt, postingDate: string): string | undefined {
  if (postingDate === "") return undefined;
  if (!/^\d{4}-\d{2}-\d{2}$/.test(postingDate)) return "Enter a date.";
  return postingDate < receipt.receiptDate ? `Use ${receipt.receiptDate} or later.` : undefined;
}

export function validateCorrection(receipt: Receipt, deltas: string[], postingDate = ""): Errors {
  const errors: Errors = {};
  const dateError = validatePostingDate(receipt, postingDate);
  if (dateError) errors.postingDate = dateError;
  let changed = 0;
  receipt.lines.forEach((_, i) => {
    const d = deltas[i].trim();
    if (d === "") return;
    changed += 1;
    if (!SIGNED_DECIMAL.test(d) || Number(d) === 0) errors[`lines[${i}].qtyDelta`] = "Enter a quantity change other than zero, such as -2 or 1.5.";
  });
  if (changed === 0) errors.lines = "Enter a change for at least one line.";
  return errors;
}

function CorrectForm({ receipt, reload }: { receipt: Receipt; reload: () => void }) {
  const action = useAction(receipt.id);
  const [deltas, setDeltas] = useState<string[]>(() => receipt.lines.map(() => ""));
  const [postingDate, setPostingDate] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const [confirming, setConfirming] = useState(false);
  const shown = { ...errors, ...(action.failure?.fields ?? {}) };

  function review(event: FormEvent) {
    event.preventDefault();
    const found = validateCorrection(receipt, deltas, postingDate);
    setErrors(found);
    setConfirming(Object.keys(found).length === 0);
  }

  async function submit() {
    const lines = receipt.lines.flatMap((l, i) => (deltas[i].trim() === "" ? [] : [{ lineNo: l.lineNo, qtyDelta: deltas[i].trim() }]));
    await action.run((key) => correctReceipt(receipt.id, receipt.version, lines, key, postingDate));
  }

  return (
    <Card title="Correct quantities">
      <form className={styles.form} onSubmit={review} noValidate aria-label="Correct receipt">
        <p className={`${styles.note} ${styles.formActions}`}>A correction posts the difference to the ledger. A line can't drop below what is already invoiced or to zero; cancel the receipt instead.</p>
        {receipt.lines.map((line, i) => (
          <FormField key={line.lineNo} id={`cor-${i}`} label={`Line ${line.lineNo} quantity change`} hint={`Received ${line.qty} ${line.unit}.`} optional error={shown[`lines[${i}].qtyDelta`]}>
            {(c) => <Input {...c} inputMode="decimal" value={deltas[i]} onChange={(e) => setDeltas((prev) => prev.map((d, j) => (j === i ? e.target.value : d)))} invalid={Boolean(shown[`lines[${i}].qtyDelta`])} />}
          </FormField>
        ))}
        <FormField id="cor-date" label="Posting date" hint={`Defaults to the receipt date, ${receipt.receiptDate}.`} optional error={shown.postingDate}>
          {(c) => <Input {...c} type="date" value={postingDate} onChange={(e) => setPostingDate(e.target.value)} invalid={Boolean(shown.postingDate)} />}
        </FormField>
        {shown.lines ? (
          <div className={styles.formActions}>
            <Alert tone="danger" live="alert">
              {shown.lines}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <FailureAlert title="Could not correct the receipt" failure={action.failure} onReload={reload} />
        </div>
        {confirming ? (
          <ConfirmStep confirmLabel="Confirm correction" keepLabel="Keep editing" busy={action.busy} onConfirm={() => void submit()} onKeep={() => setConfirming(false)} />
        ) : (
          <div className={styles.formActions}>
            <Button type="submit">Post correction</Button>
          </div>
        )}
      </form>
    </Card>
  );
}

function CancelPanel({ receipt, reload }: { receipt: Receipt; reload: () => void }) {
  const action = useAction(receipt.id);
  const [confirming, setConfirming] = useState(false);
  const [postingDate, setPostingDate] = useState("");
  const [dateError, setDateError] = useState<string | undefined>();
  const posted = receipt.state === "POSTED";
  return (
    <Card title="Cancel receipt">
      <div className={styles.stack}>
        <p className={styles.note}>
          {posted
            ? "Cancelling reverses the ledger entry and closes the GR/IR items. It is refused once any line has been invoiced."
            : "Cancelling releases any serials recorded on the receipt. The packing slip stays used."}
        </p>
        {posted ? (
          <FormField id="cancel-date" label="Posting date" hint={`Defaults to the receipt date, ${receipt.receiptDate}.`} optional error={dateError ?? action.failure?.fields.postingDate}>
            {(c) => <Input {...c} type="date" value={postingDate} onChange={(e) => setPostingDate(e.target.value)} invalid={Boolean(dateError)} />}
          </FormField>
        ) : null}
        <FailureAlert title="Could not cancel the receipt" failure={action.failure} onReload={reload} />
        {confirming ? (
          <ConfirmStep
            confirmLabel="Confirm cancel"
            keepLabel="Keep receipt"
            variant="danger"
            busy={action.busy}
            onConfirm={() => {
              const invalid = validatePostingDate(receipt, postingDate);
              setDateError(invalid);
              if (!invalid) void action.run((key) => cancelReceipt(receipt.id, receipt.version, key, postingDate));
            }}
            onKeep={() => setConfirming(false)}
          />
        ) : (
          <div>
            <Button onClick={() => setConfirming(true)}>Cancel receipt</Button>
          </div>
        )}
      </div>
    </Card>
  );
}

function Actions({ receipt, reload }: { receipt: Receipt; reload: () => void }) {
  switch (receipt.state) {
    case "ARRIVED":
      return (
        <>
          <RegisterForm key={receipt.version} receipt={receipt} reload={reload} />
          <CancelPanel key={`cancel-${receipt.version}`} receipt={receipt} reload={reload} />
        </>
      );
    case "REGISTERED":
      return (
        <>
          <PostPanel key={receipt.version} receipt={receipt} reload={reload} />
          <CancelPanel key={`cancel-${receipt.version}`} receipt={receipt} reload={reload} />
        </>
      );
    case "POSTED":
      return (
        <>
          <CorrectForm key={receipt.version} receipt={receipt} reload={reload} />
          <CancelPanel key={`cancel-${receipt.version}`} receipt={receipt} reload={reload} />
        </>
      );
    default:
      return (
        <Alert tone="info" title={receipt.state === "CANCELLED" ? "This receipt is cancelled" : "This receipt is closed"}>
          No further changes are possible.
        </Alert>
      );
  }
}

/** One receipt with the actions its state allows: register, post, correct, cancel. */
export function ReceiptPage() {
  const { id = "" } = useParams();
  const result = useQuery({ queryKey: ["receiving", "receipt", id], queryFn: () => fetchReceipt(id) });
  const reload = () => void result.refetch();

  return (
    <div className={styles.stack}>
      <PageHeader
        title={result.data ? `Receipt ${result.data.receiptNumber}` : "Receipt"}
        description="Register what was received, post it to the ledger, correct it while nothing is invoiced, or cancel it."
        meta={result.data ? <StateBadge state={result.data.state} /> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/receiving/receipts">
            Back to receipts
          </Link>
        }
      />
      {result.isError ? (
        <Alert tone="danger" title="Could not load the receipt" live="alert" action={<Button size="sm" onClick={reload}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : result.isPending ? (
        <Card>
          <div aria-busy="true">
            <span className="visually-hidden">Loading the receipt…</span>
            <Skeleton height="control" />
            <Skeleton height="control" width="1/2" />
          </div>
        </Card>
      ) : (
        <>
          <Card title="Details">
            <dl className={styles.details}>
              <Detail term="Packing slip">{result.data.packingSlip}</Detail>
              <Detail term="Vendor"><Named entity="vendor" id={result.data.vendorId} /></Detail>
              <Detail term="Purchase order"><PurchaseOrderRef id={result.data.purchaseOrderId} /></Detail>
              <Detail term="Legal entity"><Named entity="legalEntity" id={result.data.legalEntityId} /></Detail>
              <Detail term="Receipt date">{result.data.receiptDate}</Detail>
              <Detail term="Currency">{`${result.data.currency} at ${result.data.exchangeRate}`}</Detail>
              {result.data.carrier ? <Detail term="Carrier">{result.data.carrier}</Detail> : null}
              {result.data.postingDate ? <Detail term="Posting date">{result.data.postingDate}</Detail> : null}
              {result.data.ledgerEntryId ? <Detail term="Ledger entry">Posted to the general ledger</Detail> : null}
            </dl>
          </Card>
          <DataTable label="Receipt lines" columns={lineColumns} rows={result.data.lines} getRowKey={(l) => String(l.lineNo)} />
          <Actions receipt={result.data} reload={reload} />
        </>
      )}
    </div>
  );
}
