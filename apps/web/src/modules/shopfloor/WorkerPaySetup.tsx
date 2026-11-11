import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Select, Skeleton } from "../../shell/ui";
import { assignWorkerSetup, describeProblem, listPayAgreements, listTimeProfiles, newKey, type PayAgreement, type Problem, type TimeProfile, type Worker } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const DEFAULT_OPTION = "";

/**
 * Links a worker to a time profile and a pay agreement (FR-25.7). Only active ones can be chosen;
 * "Defaults" clears the link. Days already calculated keep their pay lines until recalculated.
 */
export function WorkerPaySetup({ worker, onSaved }: { worker: Worker; onSaved(): void }) {
  const options = useLoad<{ profiles: TimeProfile[]; agreements: PayAgreement[] }>(
    async (signal) => {
      const [profiles, agreements] = await Promise.all([listTimeProfiles({ active: true }, signal), listPayAgreements({ active: true }, signal)]);
      return { profiles, agreements };
    },
    "pay-setup-options",
  );
  const [profileId, setProfileId] = useState(worker.profileId ?? DEFAULT_OPTION);
  const [agreementId, setAgreementId] = useState(worker.payAgreementId ?? DEFAULT_OPTION);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const key = useRef(newKey());

  const changed = profileId !== (worker.profileId ?? DEFAULT_OPTION) || agreementId !== (worker.payAgreementId ?? DEFAULT_OPTION);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setSaved(false);
    setBusy(true);
    try {
      await assignWorkerSetup(worker.id, worker.version, { profileId: profileId || null, payAgreementId: agreementId || null }, key.current);
      setSaved(true);
      key.current = newKey();
      onSaved();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      // A stale version or a refused link is a different request next time.
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (options.error) {
    return (
      <Card title="Pay setup">
        <Alert tone="danger" title="Could not load profiles and agreements" live="alert" action={<Button size="sm" onClick={options.reload}>Try again</Button>}>
          <ProblemMessage problem={options.error} />
        </Alert>
      </Card>
    );
  }
  if (!options.data) {
    return (
      <Card title="Pay setup">
        <div aria-busy="true">
          <span className="visually-hidden">Loading profiles and agreements…</span>
          <Skeleton height="tile" />
        </div>
      </Card>
    );
  }
  const { profiles, agreements } = options.data;
  const stale = (list: { id: string }[], id: string | null) => id !== null && !list.some((item) => item.id === id);

  return (
    <Card title="Pay setup">
      <form className={styles.form} onSubmit={submit}>
        <FormField id="worker-profile" label="Time profile" hint={profiles.length === 0 ? "No active time profiles; the defaults apply." : "Shift windows, breaks and rounding."}>
          {(control) => (
            <Select {...control} value={profileId} onChange={(event) => setProfileId(event.target.value)}>
              <option value={DEFAULT_OPTION}>Defaults</option>
              {stale(profiles, worker.profileId) ? <option value={worker.profileId ?? ""}>Current profile (inactive)</option> : null}
              {profiles.map((p) => (
                <option key={p.id} value={p.id}>{`${p.code} - ${p.name}`}</option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="worker-agreement" label="Pay agreement" hint={agreements.length === 0 ? "No active pay agreements; the defaults apply." : "Overtime thresholds and multipliers."}>
          {(control) => (
            <Select {...control} value={agreementId} onChange={(event) => setAgreementId(event.target.value)}>
              <option value={DEFAULT_OPTION}>Defaults</option>
              {stale(agreements, worker.payAgreementId) ? <option value={worker.payAgreementId ?? ""}>Current agreement (inactive)</option> : null}
              {agreements.map((a) => (
                <option key={a.id} value={a.id}>{`${a.code} - ${a.name}`}</option>
              ))}
            </Select>
          )}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the pay setup" live="alert">
              <ProblemMessage problem={failure} message={failure.code === "SF-0143" ? "The worker was changed by someone else. Reload the page and try again." : undefined} />
            </Alert>
          </div>
        ) : null}
        {saved ? (
          <div className={styles.formActions}>
            <Alert tone="success" title="Pay setup saved" live="status">
              New calculations use it. Days already calculated keep their pay lines until you calculate them again.
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy} disabled={!changed}>
            Save pay setup
          </Button>
        </div>
      </form>
    </Card>
  );
}
