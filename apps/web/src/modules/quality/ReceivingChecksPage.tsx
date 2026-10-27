import { useRef, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { checkReceipt, describeProblem, listReceiptChecks, newKey, type NewReceiptCheck, type Problem, type ReceiptCheck } from "./api/client";
import { DECIMAL, dash, formatDateTime, pickFields, qty, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<ReceiptCheck>[] = [
  { key: "receiptLineId", header: "Receipt line", mono: true, render: (row) => shortId(row.receiptLineId) },
  { key: "poLineId", header: "PO line", mono: true, render: (row) => shortId(row.poLineId) },
  { key: "batch", header: "Batch", mono: true },
  { key: "receivedQty", header: "Received", align: "end", mono: true, render: (row) => qty(row.receivedQty) },
  { key: "coveredQty", header: "Source-inspected", align: "end", mono: true, render: (row) => qty(row.coveredQty) },
  { key: "normalQty", header: "To inspect", align: "end", mono: true, render: (row) => qty(row.normalQty) },
  {
    key: "skip",
    header: "Receiving inspection",
    render: (row) => (row.skip ? <Badge tone="success">Skipped (QM-0128)</Badge> : <Badge tone="warning">Quality order needed</Badge>),
  },
  { key: "checkedAt", header: "Checked", render: (row) => `${row.checkedBy || dash}, ${formatDateTime(row.checkedAt)}` },
];

export interface ReceiptValues {
  poLineId: string;
  batch: string;
  receiptLineId: string;
  receivedQty: string;
}

type ReceiptErrors = Partial<Record<keyof ReceiptValues, string>>;

/** Checks that mirror the service's ValidateReceiptCheck, and the request the form sends. */
export function validateReceipt(values: ReceiptValues): { errors: ReceiptErrors; input?: NewReceiptCheck } {
  const errors: ReceiptErrors = {};
  const poLineId = values.poLineId.trim();
  const receiptLineId = values.receiptLineId.trim();
  const batch = values.batch.trim();
  const receivedQty = values.receivedQty.trim();
  if (!UUID.test(poLineId)) errors.poLineId = "Enter the PO line id (a UUID).";
  if (!UUID.test(receiptLineId)) errors.receiptLineId = "Enter the receipt line id (a UUID).";
  if (batch === "" || batch.length > 60) errors.batch = "Enter the batch, up to 60 characters.";
  if (!DECIMAL.test(receivedQty) || /^0+(\.0+)?$/.test(receivedQty)) errors.receivedQty = "Enter a quantity above 0, up to 6 decimals.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { poLineId, batch, receiptLineId, receivedQty } };
}

/**
 * Receipts against source inspection (FR-33.19, BR-33.16): receiving checks each
 * receipt line of a PO line and batch; a receipt the accepted source inspection
 * covers skips receiving inspection, and any excess needs a normal quality order.
 */
export function ReceivingChecksPage() {
  const [params, setParams] = useSearchParams();
  const poLineId = params.get("poLineId") ?? "";
  const filterValid = poLineId === "" || UUID.test(poLineId);
  const state = useLoad((signal) => listReceiptChecks({ poLineId: filterValid ? poLineId : undefined }, signal), `receipt-checks|${filterValid ? poLineId : ""}`);
  const [filter, setFilter] = useState(poLineId);

  function applyFilter(event: FormEvent) {
    event.preventDefault();
    const next = new URLSearchParams(params);
    if (filter.trim()) next.set("poLineId", filter.trim());
    else next.delete("poLineId");
    setParams(next);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Receipts and source inspection"
        description="A batch accepted at the vendor by a source inspection skips receiving inspection when it arrives, up to the accepted quantity. Each receipt uses up what it covers; any excess, and any other batch, needs a normal quality order."
      />
      <ReceiptCheckForm onChecked={state.reload} />
      <Card title="Checked receipt lines">
        <div className={styles.stack}>
          <form className={styles.toolbar} onSubmit={applyFilter} aria-label="Filter receipt checks">
            <FormField id="receipt-filter-po" label="PO line" optional error={filterValid ? undefined : "Enter the PO line id (a UUID)."}>
              {(control) => <Input {...control} value={filter} invalid={!filterValid} onChange={(event) => setFilter(event.target.value)} />}
            </FormField>
            <Button type="submit" size="sm">
              Filter
            </Button>
          </form>
          {state.error ? (
            <Alert tone="danger" title="Could not load the receipt checks" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
              {state.error.message} ({state.error.code})
            </Alert>
          ) : (
            <DataTable
              label="Checked receipt lines"
              columns={columns}
              rows={state.data ?? []}
              getRowKey={(row) => row.id}
              loading={state.loading}
              empty={
                poLineId
                  ? { title: "No receipt of this PO line was checked yet" }
                  : { title: "No receipt lines checked yet", description: "Receiving checks each receipt line here before it creates a receiving quality order." }
              }
            />
          )}
        </div>
      </Card>
    </div>
  );
}

function ReceiptCheckForm({ onChecked }: { onChecked(): void }) {
  const empty: ReceiptValues = { poLineId: "", batch: "", receiptLineId: "", receivedQty: "" };
  const [values, setValues] = useState<ReceiptValues>(empty);
  const [errors, setErrors] = useState<ReceiptErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<ReceiptCheck | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ReceiptValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setResult(null);
    const checked = validateReceipt(values);
    setErrors(checked.errors);
    if (!checked.input) return;
    setBusy(true);
    try {
      const saved = await checkReceipt(checked.input, key.current);
      key.current = newKey();
      setResult(saved);
      setValues(empty);
      onChecked();
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = pickFields(problem.fields, ["poLineId", "batch", "receiptLineId", "receivedQty"] as const);
      if (problem.code === "QM-0185") fieldErrors.receiptLineId = "This receipt line was already checked with another PO line, batch or quantity.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (id: keyof ReceiptValues, label: string, hint?: string, extra?: Record<string, unknown>) => (
    <FormField id={`receipt-${id}`} label={label} hint={hint} error={errors[id]}>
      {(control) => <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value })} />}
    </FormField>
  );

  return (
    <Card title="Check a receipt line">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Check a receipt line">
        {field("poLineId", "PO line", "The purchase order line the source inspection was made for.")}
        {field("batch", "Batch", undefined, { maxLength: 60 })}
        {field("receiptLineId", "Receipt line", "Each receipt line is checked once.")}
        {field("receivedQty", "Received quantity", undefined, { inputMode: "decimal" })}
        {failure || result ? (
          <div className={styles.formActions}>
            {failure ? (
              <Alert tone="danger" title="Could not check the receipt" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            ) : null}
            {result ? (
              <Alert tone={result.skip ? "success" : "warning"} title={result.skip ? "Receiving inspection skipped" : "Create a receiving quality order"} live="status">
                {`${result.message} Accepted at source ${qty(result.acceptedQty)}, used by earlier receipts ${qty(result.coveredBefore)}.`}
              </Alert>
            ) : null}
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Check receipt
          </Button>
        </div>
      </form>
    </Card>
  );
}
