import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import {
  classifyRead,
  describeProblem,
  evaluateExportPolicy,
  EXPORT_CHANNELS,
  EXPORT_CLASSES,
  type ExportChannel,
  type ExportClassification,
  type ExportDecision,
  type Problem,
  type ReadClassification,
} from "./api/client";
import { sentence } from "./format";
import styles from "./pages.module.css";

export const channelLabel: Record<ExportChannel, string> = {
  GRID: "Grid export",
  EXPLORER: "Explorer export",
  REPORT: "Report download",
  API_BULK: "API bulk read",
  MCP_BULK: "MCP tool bulk read",
};

/** What the user sees about the download. */
export function decisionText(d: ExportDecision): string {
  const parts = [d.allowed ? "The download may go ahead." : "The download waits for approval."];
  if (d.watermark) parts.push("The file is watermarked.");
  if (d.requiresReason) parts.push("The reason is recorded.");
  parts.push("The export is logged.");
  return parts.join(" ");
}

/**
 * Export policy check (FR-48.24, FR-48.25, duty ComplianceView): what the
 * policy for a data classification does to one download, and which fields of
 * a read are written to the read-access log. Nothing is logged yet; the
 * checks show the policy the API layer will apply.
 */
export function ExportPolicyPage() {
  const [classification, setClassification] = useState<ExportClassification>("PII");
  const [channel, setChannel] = useState<ExportChannel>("GRID");
  const [rows, setRows] = useState("");
  const [reason, setReason] = useState("");
  const [rowsError, setRowsError] = useState<string | undefined>();
  const [decision, setDecision] = useState<ExportDecision | null>(null);
  const [exportFailure, setExportFailure] = useState<Problem | null>(null);
  const [fields, setFields] = useState("");
  const [fieldsError, setFieldsError] = useState<string | undefined>();
  const [read, setRead] = useState<ReadClassification | null>(null);
  const [readFailure, setReadFailure] = useState<Problem | null>(null);

  async function checkExport(event: FormEvent) {
    event.preventDefault();
    const n = Number(rows.trim());
    if (!rows.trim() || !Number.isInteger(n) || n < 0) {
      setRowsError("Enter the number of rows, 0 or more.");
      return;
    }
    setRowsError(undefined);
    setExportFailure(null);
    try {
      setDecision(await evaluateExportPolicy({ classification, channel, rows: n, ...(reason.trim() ? { reason: reason.trim() } : {}) }));
    } catch (error) {
      setDecision(null);
      setExportFailure(describeProblem(error));
    }
  }

  async function checkRead(event: FormEvent) {
    event.preventDefault();
    const list = [...new Set(fields.split(/[\n,]/).map((f) => f.trim()).filter(Boolean))];
    if (list.length === 0) {
      setFieldsError("Enter at least one field, for example hr.worker.salary.");
      return;
    }
    setFieldsError(undefined);
    setReadFailure(null);
    try {
      setRead(await classifyRead(list));
    } catch (error) {
      setRead(null);
      setReadFailure(describeProblem(error));
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Export policy check"
        description="Downloads of personal, financial and confidential data follow the policy for their classification: audit, watermark, a recorded reason, approval or denial. Bulk API and MCP reads above 10,000 rows wait for approval."
      />
      <Card title="Download">
        <form className={styles.form} onSubmit={checkExport} noValidate aria-label="Check a download">
          <FormField id="exp-class" label="Classification">
            {(control) => (
              <Select {...control} value={classification} onChange={(e) => setClassification(e.target.value as ExportClassification)}>
                {EXPORT_CLASSES.map((c) => (
                  <option key={c} value={c}>
                    {c === "PII" ? "Personal data (PII)" : sentence(c)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="exp-channel" label="Channel">
            {(control) => (
              <Select {...control} value={channel} onChange={(e) => setChannel(e.target.value as ExportChannel)}>
                {EXPORT_CHANNELS.map((c) => (
                  <option key={c} value={c}>
                    {channelLabel[c]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="exp-rows" label="Rows" error={rowsError}>
            {(control) => <Input {...control} inputMode="numeric" value={rows} onChange={(e) => setRows(e.target.value)} invalid={Boolean(rowsError)} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="exp-reason" label="Reason" hint="Personal data needs a reason of at least 10 characters." optional>
              {(control) => <Textarea {...control} rows={2} value={reason} onChange={(e) => setReason(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Check download
            </Button>
          </div>
        </form>
        <Alert tone="danger" title={exportFailure ? "The download is refused" : undefined} live="alert">
          {exportFailure ? `${exportFailure.message} (${exportFailure.code})` : null}
        </Alert>
        <Alert tone={decision?.allowed ? "success" : "warning"} live="status">
          {decision ? decisionText(decision) : null}
        </Alert>
        {decision ? (
          <ul className={styles.badges} aria-label="Policy actions">
            {decision.actions.map((a) => (
              <li key={a}>
                <Badge tone={a === "APPROVAL" ? "warning" : "info"}>{sentence(a)}</Badge>
              </li>
            ))}
          </ul>
        ) : null}
      </Card>
      <Card title="Read-access log">
        <form className={styles.form} onSubmit={checkRead} noValidate aria-label="Check a read">
          <div className={styles.wide}>
            <FormField id="read-fields" label="Fields read" hint="Separate with commas or lines. Only field names are logged, never values." error={fieldsError}>
              {(control) => <Textarea {...control} rows={3} className={styles.mono} value={fields} onChange={(e) => setFields(e.target.value)} invalid={Boolean(fieldsError)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit">Check read</Button>
          </div>
        </form>
        <Alert tone="danger" title={readFailure ? "The read could not be checked" : undefined} live="alert">
          {readFailure ? `${readFailure.message} (${readFailure.code})` : null}
        </Alert>
        <Alert tone="info" live="status">
          {read ? (read.logged ? `Logged and kept ${read.retentionDays} days: ${read.fields.join(", ")}.` : "None of these fields is logged.") : null}
        </Alert>
      </Card>
    </div>
  );
}
