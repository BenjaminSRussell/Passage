import { useState } from "react";
import { Alert, Button } from "../../shell/ui";
import { LedgerApiError, type CsvExport } from "./api/client";
import styles from "./pages.module.css";

/** Hands a built CSV to the browser as a download. */
export function saveCsv(file: CsvExport) {
  const url = URL.createObjectURL(file.blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = file.filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  // Revoked on the next task: revoking in the same task can cancel the download in some browsers.
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

/**
 * Downloads the current result as CSV. The service builds the file (the whole result, not the page on
 * screen); a result past its row cap is cut and the button says so. Failures show in place.
 */
export function ExportButton({ label = "Export CSV", run }: { label?: string; run: () => Promise<CsvExport> }) {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [truncated, setTruncated] = useState(false);

  async function start() {
    setBusy(true);
    setFailure(null);
    setTruncated(false);
    try {
      const file = await run();
      setTruncated(file.truncated);
      saveCsv(file);
    } catch (error) {
      setFailure(error instanceof LedgerApiError ? `${error.message} (${error.code})` : (error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <div className={styles.formActions}>
        <Button loading={busy} onClick={() => void start()}>
          {label}
        </Button>
      </div>
      {truncated ? (
        <Alert tone="warning" title="The export is incomplete" live="status">
          The result is longer than 50,000 rows, so the file holds the first 50,000. Narrow the filter and export again.
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not export" live="alert">
          {failure}
        </Alert>
      ) : null}
    </div>
  );
}
