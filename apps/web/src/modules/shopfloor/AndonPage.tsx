import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, DataTable, FormField, PageHeader, StatGroup, StatTile, Textarea, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { DUTY_CHECK_OVERRIDE, clearAndon, describeProblem, listAndon, newKey, type Andon, type AndonKind, type Problem } from "./api/client";
import { dash, durationMinutes, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const SEVERITY: Record<Andon["severity"], { label: string; tone: BadgeTone }> = {
  info: { label: "Info", tone: "info" },
  warning: { label: "Warning", tone: "warning" },
  error: { label: "Error", tone: "danger" },
};

const CONDITION: Record<AndonKind, string> = {
  downtime: "Machine down over 10 min",
  "scrap-rate": "Scrap over 5 % in a bucket",
  "quality-fail": "Quality check failed",
};

/** Open andon alerts across the plant (FR-25.11, BR-25.10, FR-25.14 supervisor view). */
export function AndonPage() {
  const state = useLoad((signal) => listAndon({}, signal), "andon");
  const duties = usePrincipalDuties();
  const canClear = holdsDuty(duties, DUTY_CHECK_OVERRIDE);
  const [clearing, setClearing] = useState<Andon | null>(null);
  const [cleared, setCleared] = useState<string | null>(null);
  const rows = state.data ?? [];

  const columns: DataTableColumn<Andon>[] = [
    { key: "resourceId", header: "Resource", mono: true },
    { key: "kind", header: "Condition", render: (row) => CONDITION[row.kind] ?? row.kind },
    { key: "detail", header: "Detail", render: (row) => row.detail ?? dash },
    { key: "severity", header: "Severity", render: (row) => <Badge tone={SEVERITY[row.severity].tone}>{SEVERITY[row.severity].label}</Badge> },
    { key: "startedAt", header: "Since", render: (row) => formatDateTime(row.startedAt) },
    { key: "open", header: "Open for", align: "end", render: (row) => durationMinutes(row.startedAt) },
    {
      key: "action",
      header: "Next step",
      render: (row) => {
        if (row.downtimeId) {
          return <Link to={`/shopfloor/downtimes?downtime=${encodeURIComponent(row.downtimeId)}&open=false`}>Code reason</Link>;
        }
        if (row.kind === "quality-fail") {
          return (
            <span>
              {row.jobId ? <Link to={`/shopfloor/jobs/${encodeURIComponent(row.jobId)}`}>Open job</Link> : null}
              {canClear ? (
                <>
                  {row.jobId ? " " : null}
                  <Button size="sm" onClick={() => setClearing(row)}>
                    Clear
                  </Button>
                </>
              ) : null}
            </span>
          );
        }
        return dash;
      },
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Andon board"
        description="One open alert per resource and condition. A downtime or scrap-rate alert clears by itself once its condition has been clear for 5 minutes; coding the downtime reason does not clear it. A quality-fail alert clears when a later report passes the check, or a supervisor clears it."
        actions={
          <Button onClick={state.reload} loading={state.loading}>
            Refresh
          </Button>
        }
      />
      {cleared ? (
        <Alert tone="success" title="Alert cleared" live="status">
          {cleared}
        </Alert>
      ) : null}
      {state.data ? (
        <StatGroup label="Andon summary">
          <StatTile label="Open alerts" value={rows.length} tone="danger" />
          <StatTile label="Resources affected" value={new Set(rows.map((a) => a.resourceId)).size} tone="warning" />
        </StatGroup>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load andon alerts" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Open andon alerts"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No open alerts", description: "Every machine is running or was stopped for less than 10 minutes, scrap is within 5 % and no check has failed." }}
        />
      )}
      {clearing ? (
        <ClearForm
          key={clearing.id}
          alert={clearing}
          onCancel={() => setClearing(null)}
          onDone={() => {
            setCleared(`The quality-fail alert on resource ${clearing.resourceId} is cleared.`);
            setClearing(null);
            state.reload();
          }}
        />
      ) : null}
    </div>
  );
}

interface ClearFormProps {
  alert: Andon;
  onCancel(): void;
  onDone(): void;
}

function ClearForm({ alert, onCancel, onDone }: ClearFormProps) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per form: a resend after a lost response returns the stored outcome.
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const text = reason.trim();
    if (text.length < 1 || text.length > 500) {
      setError("Say why the alert is cleared, up to 500 characters.");
      return;
    }
    setError(null);
    setFailure(null);
    setBusy(true);
    try {
      await clearAndon(alert.id, text, key.current);
      onDone();
    } catch (failed) {
      const problem = describeProblem(failed);
      if (problem.fields.reason) setError(problem.fields.reason);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Clear quality-fail alert" headingLevel={2}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <div className={styles.formActions}>
          <Alert tone="info" title={alert.detail ?? "Quality check failed"}>
            The alert clears now. If the job fails a check again, a new alert is raised.
          </Alert>
        </div>
        <FormField id="andon-clear-reason" label="Reason" hint="Kept on the alert, e.g. the part was reworked and re-checked." error={error ?? undefined}>
          {(control) => <Textarea {...control} value={reason} maxLength={500} rows={3} invalid={Boolean(error)} onChange={(e) => setReason(e.target.value)} />}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not clear the alert" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy} disabled={busy}>
            Clear alert
          </Button>
          <Button onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
        </div>
      </form>
    </Card>
  );
}
