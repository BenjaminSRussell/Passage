import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, PageHeader, Skeleton } from "../../shell/ui";
import { getLibraryEntry, libraryContentUrl } from "./api/client";
import { CURRENCY_TYPE_LABELS, formatBytes, periodLabel } from "./format";
import styles from "./pages.module.css";
import { ShareLinks } from "./ShareLinks";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** One stored statement file (FR-15.8): what it is, its SHA-256 and retention, and the download. */
export function LibraryEntryPage() {
  const { id = "" } = useParams();
  const canShare = holdsDuty(usePrincipalDuties(), "reporting.library.share");
  const state = useLoad((signal) => getLibraryEntry(id, signal), id);
  const title = "Library file";
  if (state.error) {
    const missing = state.error.status === 404;
    return (
      <div className={styles.stack}>
        <PageHeader title={title} />
        <Alert
          tone="danger"
          title={missing ? "This file is not in the library" : "This file could not be loaded"}
          live="alert"
          action={missing ? undefined : <Button size="sm" onClick={state.reload}>Try again</Button>}
        >
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link to="/finreport/library">Back to the library</Link>
      </div>
    );
  }
  const e = state.data;
  if (!e) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title={title} />
        <p className={styles.note}>Loading file…</p>
        <Skeleton width="full" height="tile" />
      </div>
    );
  }
  const rows: [string, string][] = [
    ["Statement", `${e.definitionCode} version ${e.definitionVersion}`],
    ["Period", periodLabel(e.periodYear, e.periodNo)],
    ["Currency type", CURRENCY_TYPE_LABELS[e.currencyType] ?? e.currencyType],
    ["Ledger", e.ledgerId],
    ["Legal entity", e.legalEntityId ?? "None"],
    ["Schedule", e.scheduleCode],
    ["Format", e.format.toUpperCase()],
    ["Size", formatBytes(e.sizeBytes)],
    ["SHA-256", e.sha256],
    ["Generated", e.createdAt],
    ["Kept until", e.onHold ? `${e.retainUntil} (kept past this date: legal hold)` : e.retainUntil],
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title={e.fileName}
        description={`${e.definitionCode} for ${periodLabel(e.periodYear, e.periodNo)}, stored by schedule ${e.scheduleCode}.`}
        actions={
          <>
            <a className={buttonClassName("primary")} href={libraryContentUrl(e.id)} download={e.fileName}>
              Download {e.format.toUpperCase()}
            </a>
            <Link className={buttonClassName("secondary")} to={`/finreport/runs/${e.runId}`}>
              Open the statement
            </Link>
          </>
        }
      />
      <Card title="Details">
        <dl className={styles.details}>
          {rows.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      {canShare ? <ShareLinks libraryId={e.id} /> : null}
      <p className={styles.note}>
        The download is exactly the stored file. To check it, compute its SHA-256 and compare it with the value above; the download also carries it in the X-Passage-Content-Sha256 header.
      </p>
      <Link to="/finreport/library">Back to the library</Link>
    </div>
  );
}
