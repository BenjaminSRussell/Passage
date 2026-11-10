import { useState, type FormEvent } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, Card, FormField, Input, PageHeader, Skeleton } from "../../shell/ui";
import { describeProblem, getSettings, putSettings, type MigrationSettings, type Problem } from "./api/client";
import { formatDateTime } from "./domain";
import styles from "./pages.module.css";
import { DUTY_PROJECT_WRITE } from "./ProjectPages";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** Bounds of mig.recurring.min_interval_min; the service enforces the same ones. */
export const MIN_INTERVAL_FLOOR = 15;
export const MIN_INTERVAL_CEILING = 1440;

/** Parses the field: whole minutes within the bounds, or a message that says what is wrong. */
export function parseInterval(text: string): { value?: number; error?: string } {
  const trimmed = text.trim();
  if (!/^\d+$/.test(trimmed)) return { error: `Enter whole minutes between ${MIN_INTERVAL_FLOOR} and ${MIN_INTERVAL_CEILING}.` };
  const value = Number(trimmed);
  if (value < MIN_INTERVAL_FLOOR || value > MIN_INTERVAL_CEILING) return { error: `Must be between ${MIN_INTERVAL_FLOOR} and ${MIN_INTERVAL_CEILING} minutes.` };
  return { value };
}

/** Tenant-wide migration settings: today the shortest gap a scheduled project or binding may leave between runs. */
export function SettingsPage() {
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_PROJECT_WRITE);
  const state = useLoad((signal) => getSettings(signal), "settings");

  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Migration settings" />
        <Alert tone="danger" title="Could not load the settings" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Migration settings" description="Loading..." />
        <Skeleton />
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader title="Migration settings" description="Rules that apply to every migration project and binding of this tenant." />
      <IntervalCard settings={state.data} canEdit={canWrite} onSaved={state.set} onStale={state.reload} />
    </div>
  );
}

function IntervalCard({ settings, canEdit, onSaved, onStale }: { settings: MigrationSettings; canEdit: boolean; onSaved: (saved: MigrationSettings) => void; onStale: () => void }) {
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const parsed = parseInterval(text ?? "");
    setError(parsed.error ?? "");
    if (parsed.value === undefined) return;
    setBusy(true);
    try {
      onSaved(await putSettings(settings.version, parsed.value));
      setText(null);
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      if (problem.errors?.recurringMinIntervalMin) setError(problem.errors.recurringMinIntervalMin);
    } finally {
      setBusy(false);
    }
  }

  const note = settings.updatedAt ? `Saved ${formatDateTime(settings.updatedAt)}${settings.updatedBy ? ` by ${settings.updatedBy}` : ""}.` : "Not changed from the default.";

  if (text !== null && canEdit) {
    return (
      <Card title="Recurring runs">
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField
            id="min-interval"
            label="Shortest gap between scheduled runs (minutes)"
            error={error}
            hint={`Between ${MIN_INTERVAL_FLOOR} and ${MIN_INTERVAL_CEILING}. Applies to schedules saved from now on; existing schedules keep running as they are.`}
          >
            {(control) => <Input {...control} inputMode="numeric" value={text} invalid={error !== ""} onChange={(event) => setText(event.target.value)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert
                tone="danger"
                title="Could not save the setting"
                live="alert"
                action={failure.code === "MIG-VERSION-CONFLICT" ? <Button size="sm" onClick={() => { setText(null); onStale(); }}>Reload</Button> : undefined}
              >
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save setting
            </Button>
            <Button onClick={() => { setText(null); setError(""); setFailure(null); }}>Cancel</Button>
          </div>
        </form>
      </Card>
    );
  }
  return (
    <Card title="Recurring runs" actions={canEdit ? <Button size="sm" onClick={() => { setText(String(settings.recurringMinIntervalMin)); setError(""); setFailure(null); }}>Edit setting</Button> : undefined}>
      <dl className={styles.details}>
        <div className={styles.entry}>
          <dt className={styles.term}>Shortest gap between scheduled runs</dt>
          <dd className={styles.value}>{settings.recurringMinIntervalMin} minutes</dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Version</dt>
          <dd className={styles.value}>{settings.version}</dd>
        </div>
      </dl>
      <p className={styles.note}>{note}</p>
    </Card>
  );
}
