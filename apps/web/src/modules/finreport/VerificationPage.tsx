import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getVerification, type Verification, type VerificationDiff } from "./api/client";
import { VERIFICATION_LABELS, VERIFICATION_TONES } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const diffColumns: DataTableColumn<VerificationDiff>[] = [
  { key: "account", header: "Account", mono: true },
  { key: "readModel", header: "Read model", mono: true, align: "end" },
  { key: "ledger", header: "Ledger", mono: true, align: "end" },
  { key: "difference", header: "Difference", mono: true, align: "end" },
];

function summary(v: Verification): string {
  switch (v.status) {
    case "matched":
      return `All ${v.accountsCompared} accounts have the same net in the read model and in the ledger.`;
    case "inconclusive": {
      const differences = `${v.diffCount} difference${v.diffCount === 1 ? "" : "s"}`;
      if (v.hwmBefore !== v.hwmAfter) {
        return `The read model moved from sequence ${v.hwmBefore} to ${v.hwmAfter} while the ledger was read, so ${differences} may be postings in flight. Check the ledger again.`;
      }
      return `${v.entriesPending} ${v.entriesPending === 1 ? "entry was" : "entries were"} waiting ahead of sequence ${v.hwmAfter}, so the ledger is ahead of the read model and ${differences} may be those postings. Check again once the lag panel shows nothing waiting.`;
    }
    default:
      return `${v.diffCount} of ${v.accountsCompared} accounts differ while the read model was steady at sequence ${v.hwmAfter}. An entry the read model has not received yet also shows here: if the lag panel shows a delay, check again before investigating.`;
  }
}

/** One stored ledger check with the accounts that differ (FR-15.1). */
export function VerificationPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getVerification(id, signal), id);
  const title = "Ledger check";
  if (state.error) {
    const missing = state.error.status === 404;
    return (
      <div className={styles.stack}>
        <PageHeader title={title} />
        <Alert
          tone="danger"
          title={missing ? "This check does not exist" : "This check could not be loaded"}
          live="alert"
          action={missing ? undefined : <Button size="sm" onClick={state.reload}>Try again</Button>}
        >
          {state.error.message} ({state.error.code})
        </Alert>
        <Link to="/finreport/verifications">Back to ledger verification</Link>
      </div>
    );
  }
  const v = state.data;
  if (!v) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title={title} />
        <p className={styles.note}>Loading check…</p>
        <Skeleton width="full" height="tile" />
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader title={title} description={`Ledger ${v.ledgerId}`} meta={<Badge tone={VERIFICATION_TONES[v.status]}>{VERIFICATION_LABELS[v.status]}</Badge>} />
      <Alert tone={v.status === "matched" ? "success" : v.status === "differs" ? "danger" : "warning"} title={VERIFICATION_LABELS[v.status]}>
        {summary(v)}
      </Alert>
      <Card title="Check">
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Checked</dt>
            <dd className={styles.value}><time dateTime={v.verifiedAt}>{v.verifiedAt}</time> by {v.requestedBy || "system"}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Read model as of sequence</dt>
            <dd className={styles.value}>{v.hwmBefore === v.hwmAfter ? v.hwmAfter : `${v.hwmBefore} to ${v.hwmAfter}`}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Accounts compared</dt>
            <dd className={styles.value}>{v.accountsCompared}</dd>
          </div>
        </dl>
      </Card>
      {v.diffsWithheld ? (
        <Alert tone="info" title="Ledger amounts withheld">
          The differing accounts show the ledger's balances, which need the ledger duty to read balances.{" "}
          {v.diffCount === 1 ? "1 account differs." : `${v.diffCount} accounts differ.`}
        </Alert>
      ) : (
        <DataTable
          label="Differing accounts"
          columns={diffColumns}
          rows={v.diffs}
          getRowKey={(d) => d.account}
          empty={{ title: "No account differs", description: "Every account has the same net in the read model and in the ledger." }}
        />
      )}
      {!v.diffsWithheld && v.diffCount > v.diffs.length ? (
        <p className={styles.note}>Showing the first {v.diffs.length} of {v.diffCount} differing accounts.</p>
      ) : null}
      <Link to="/finreport/verifications">Back to ledger verification</Link>
    </div>
  );
}
