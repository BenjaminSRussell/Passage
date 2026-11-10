import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import { fetchClearing, newKey, REASON_CODE, resetClearing, type OpenItem } from "./api/client";
import { ClearingStatusBadge } from "./ClearingsPage";
import { formatDimensions } from "./LedgerLinesPage";
import { ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText, ItemStatusBadge } from "./OpenItemsPage";
import styles from "./pages.module.css";

const MAX_COMMENT = 255;

function Detail({ term, value }: { term: string; value: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{value}</dd>
    </div>
  );
}

const itemColumns: DataTableColumn<OpenItem>[] = [
  { key: "postingDate", header: "Posting date", mono: true },
  { key: "voucher", header: "Voucher", mono: true, render: (r) => (r.entryId ? <Link to={`/ledger/entries/${r.entryId}`}>{r.voucher}</Link> : "Residual") },
  { key: "assignment", header: "Assignment", mono: true },
  { key: "dimensions", header: "Dimensions", mono: true, render: (r) => formatDimensions(r.dimensions) },
  { key: "debit", header: "Debit", align: "end", mono: true, render: (r) => (r.debitCredit === "D" ? r.amount : "") },
  { key: "credit", header: "Credit", align: "end", mono: true, render: (r) => (r.debitCredit === "C" ? r.amount : "") },
  { key: "status", header: "Status", render: (r) => <ItemStatusBadge status={r.status} /> },
];

function ResetForm({ id, hasDifference, onReset }: { id: string; hasDifference: boolean; onReset: () => void }) {
  const [reasonCode, setReasonCode] = useState("");
  const [comment, setComment] = useState("");
  const [errors, setErrors] = useState<{ reasonCode?: string; comment?: string }>({});
  const [failure, setFailure] = useState<ReactNode>(null);
  const [busy, setBusy] = useState(false);
  const attempt = useRef<{ signature: string; key: string }>({ signature: "", key: "" });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: typeof errors = {};
    if (!REASON_CODE.test(reasonCode)) found.reasonCode = "Enter the reason code: 1 to 10 capital letters, digits or underscores.";
    if (comment.length > MAX_COMMENT) found.comment = `Use at most ${MAX_COMMENT} characters.`;
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const input = { reasonCode, ...(comment.trim() ? { comment: comment.trim() } : {}) };
    const signature = JSON.stringify(input);
    if (attempt.current.signature !== signature) attempt.current = { signature, key: newKey() };
    setBusy(true);
    try {
      await resetClearing(id, input, attempt.current.key);
      onReset();
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="Reset this clearing">
        <FormField
          id="cl-reason"
          label="Reason code"
          hint={hasDifference ? "Required. Also the reason of the reversal of the difference entry." : "Required. A shared reason code that applies to the ledger."}
          error={errors.reasonCode}
        >
          {(c) => <Input {...c} className={styles.mono} maxLength={10} value={reasonCode} onChange={(e) => setReasonCode(e.target.value.toUpperCase())} invalid={Boolean(errors.reasonCode)} />}
        </FormField>
        <FormField id="cl-comment" label="Comment" error={errors.comment}>
          {(c) => <Textarea {...c} value={comment} maxLength={MAX_COMMENT} onChange={(e) => setComment(e.target.value)} invalid={Boolean(errors.comment)} />}
        </FormField>
        {failure ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Could not reset the clearing" live="alert">{failure}</Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="danger" loading={busy}>Reset clearing</Button>
          <span className={styles.note}>The whole document is reset; its items become open again.</span>
        </div>
      </form>
    </Card>
  );
}

/** One clearing document with its items and, while cleared, the reset action. */
export function ClearingPage() {
  const { id = "" } = useParams();
  const queryClient = useQueryClient();
  const [ledgerId] = useLedgerParam();
  const doc = useQuery({ queryKey: ["ledger", "clearings", "doc", id], queryFn: () => fetchClearing(id) });
  const query = ledgerQuery(ledgerId);

  function refresh() {
    void queryClient.invalidateQueries({ queryKey: ["ledger", "clearings"] });
    void queryClient.invalidateQueries({ queryKey: ["ledger", "open-items"] });
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Clearing document" description={doc.data ? `Account ${doc.data.account}, cleared on ${doc.data.clearingDate}.` : undefined} />
      {doc.isPending ? (
        <Skeleton height="tile" />
      ) : doc.isError ? (
        <Alert tone="danger" title="Could not load the clearing document" live="alert" action={<Button size="sm" onClick={() => void doc.refetch()}>Try again</Button>}>
          {errorText(doc.error)}
        </Alert>
      ) : (
        <>
          <Card>
            <dl className={styles.details} aria-label="Clearing">
              <Detail term="Status" value={<ClearingStatusBadge status={doc.data.status} />} />
              <Detail term="Items" value={doc.data.itemIds.length} />
              <Detail term="Written off" value={doc.data.differenceAmount} />
              <Detail term="Tolerance group" value={doc.data.toleranceGroup ?? "None"} />
              <Detail term="Difference entry" value={doc.data.differenceEntryId ? <Link to={`/ledger/entries/${doc.data.differenceEntryId}`}>Open the entry</Link> : "None"} />
              <Detail term="Residual item" value={doc.data.residualItemId ? "Created" : "None"} />
              <Detail term="Cleared by" value={`${doc.data.createdBy}, ${doc.data.createdAt.slice(0, 10)}`} />
              {doc.data.status === "reset" ? (
                <Detail term="Reset" value={`${doc.data.resetBy ?? ""}${doc.data.resetReasonCode ? `, reason ${doc.data.resetReasonCode}` : ""}${doc.data.resetComment ? `: ${doc.data.resetComment}` : ""}`} />
              ) : null}
            </dl>
          </Card>
          <DataTable
            label="Cleared items"
            columns={itemColumns}
            rows={doc.data.items ?? []}
            getRowKey={(r) => r.id}
            empty={{ title: "No items", description: "This document holds no items." }}
          />
          {doc.data.status === "cleared" ? <ResetForm id={id} hasDifference={Boolean(doc.data.differenceEntryId)} onReset={refresh} /> : null}
          <p className={styles.note}>
            <Link to={`/ledger/clearings${query}`}>All clearing documents</Link> · <Link to={`/ledger/open-items${query}`}>Open items</Link>
          </p>
        </>
      )}
    </div>
  );
}
