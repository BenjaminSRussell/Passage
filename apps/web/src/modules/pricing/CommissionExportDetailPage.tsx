import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, ConfirmDialog, DataTable, FormField, PageHeader, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import {
  describeProblem,
  downloadCommissionSettlementExport,
  getCommissionSettlementExport,
  newKey,
  PricingApiError,
  voidCommissionSettlementExport,
  type Problem,
  type SettlementExportRecord,
  type SettlementRow,
} from "./api/client";
import { TARGET_LABELS } from "./CommissionSettlementPage";
import styles from "./pages.module.css";
import { describeTotals, ExportStatusBadge, saveCsv } from "./settlementFile";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const DUTY_MAINTAIN = "pricing.commission.maintain";
export const MAX_REASON = 500;

const columns: DataTableColumn<SettlementRow>[] = [
  { key: "repId", header: "Rep", mono: true },
  { key: "currency", header: "Currency", mono: true },
  { key: "transactions", header: "Transactions", mono: true, align: "end" },
  { key: "amount", header: "Commission", mono: true, align: "end", render: (r) => `${r.amount} ${r.currency}` },
];

export function validateVoidReason(reason: string): string {
  const n = reason.trim().length;
  if (n === 0) return "Say why the export is voided.";
  if (n > MAX_REASON) return `The reason is at most ${MAX_REASON} characters.`;
  return "";
}

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** One recorded settlement export: the frozen rows, the file, and voiding it so its period can be exported again. */
export function CommissionExportDetailPage() {
  const { id = "" } = useParams();
  const canVoid = holdsDuty(usePrincipalDuties(), DUTY_MAINTAIN);
  const state = useLoad((signal) => getCommissionSettlementExport(id, signal), id);
  const [changed, setChanged] = useState<SettlementExportRecord | null>(null);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [busy, setBusy] = useState<"void" | "file" | null>(null);
  const voidKey = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Commission export" />
        <Alert tone="danger" title="Could not load the export" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Commission export" />
        <span className="visually-hidden">Loading the export…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  // A record returned by the void belongs to this id only and is newer than the loaded one.
  const record = changed && changed.id === id && changed.version >= state.data.version ? changed : state.data;

  function askVoid(event: FormEvent) {
    event.preventDefault();
    const found = validateVoidReason(reason);
    setReasonError(found);
    setFailure(null);
    if (!found) setConfirming(true);
  }

  async function voidIt() {
    setConfirming(false);
    setBusy("void");
    try {
      setChanged(await voidCommissionSettlementExport(record.id, record.version, reason.trim(), voidKey.current));
      voidKey.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure({ title: "Could not void the export", problem });
      // A refused command is final; only a network or server failure may be retried with the same key.
      if (!(error instanceof PricingApiError) || error.status < 500) voidKey.current = newKey();
    } finally {
      setBusy(null);
    }
  }

  async function download() {
    setFailure(null);
    setBusy("file");
    try {
      const file = await downloadCommissionSettlementExport(record.id);
      saveCsv(file.csv, file.filename);
    } catch (error) {
      setFailure({ title: "Could not download the file", problem: describeProblem(error) });
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${TARGET_LABELS[record.target]}: ${record.from} to ${record.to}`}
        description={`Version ${record.version} · recorded by ${record.exportedBy || "unknown"}`}
        meta={<ExportStatusBadge status={record.status} />}
        actions={
          <div className={styles.actions}>
            <Button variant="secondary" icon="receipt" loading={busy === "file"} onClick={download}>
              Download CSV
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/commission/exports">
              All exports
            </Link>
          </div>
        }
      />
      {failure ? (
        <Alert tone="danger" title={failure.title} live="alert">
          <ProblemMessage problem={failure.problem} />
        </Alert>
      ) : null}
      {record.status === "VOIDED" ? (
        <Alert tone="warning" title="This export was voided" live="status">
          {record.voidReason} (by {record.voidedBy || "unknown"}). The file already handed over must be discarded by whoever received it; the period can be exported again.
        </Alert>
      ) : null}
      <Card title="Export">
        <dl className={styles.details}>
          <Detail term="Commission" mono>
            {describeTotals(record)}
          </Detail>
          <Detail term="Rows">{record.lineCount}</Detail>
          <Detail term="Legal entity" mono>
            {record.legalEntityId}
          </Detail>
          <Detail term="File digest (SHA-256)" mono>
            {record.csvSha256}
          </Detail>
          <Detail term="Export id" mono>
            {record.id}
          </Detail>
        </dl>
      </Card>
      <Card title="Rows as handed over">
        <DataTable
          label="Rows as handed over"
          columns={columns}
          rows={record.lines ?? []}
          getRowKey={(r) => `${r.repId}|${r.currency}`}
          empty={{ title: "No rows" }}
        />
      </Card>
      {record.status === "ISSUED" && canVoid ? (
        <Card title="Void">
          <form className={styles.form} onSubmit={askVoid} noValidate>
            <FormField id="ce-reason" label="Reason" hint="Recorded with the export and in its event." error={reasonError}>
              {(c) => <Textarea {...c} rows={3} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(reasonError)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="danger" loading={busy === "void"}>
                Void export
              </Button>
              <span className={styles.note}>Voiding frees the period for a new export. It does not recall the file.</span>
            </div>
          </form>
        </Card>
      ) : null}
      {confirming ? (
        <ConfirmDialog
          title="Void this export?"
          description={`The period ${record.from} to ${record.to} can then be exported to ${TARGET_LABELS[record.target]} again. The file already handed over is not recalled.`}
          confirmLabel="Void export"
          cancelLabel="Keep export"
          onConfirm={voidIt}
          onCancel={() => setConfirming(false)}
        />
      ) : null}
    </div>
  );
}
