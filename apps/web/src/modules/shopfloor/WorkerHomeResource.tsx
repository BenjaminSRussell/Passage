import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Select, Skeleton } from "../../shell/ui";
import { assignWorkerResource, describeProblem, listJobs, listMachines, newKey, type Job, type Machine, type Problem, type Worker } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const NONE = "";

/**
 * Sets the worker's home resource (D2a'): the machine or work centre whose jobs make up the
 * worker's own dispatch list on the kiosk. "None" clears it, and the worker then sees every job.
 * The choices are the resources the shop floor knows, as the service requires (SF-0156): those of
 * the registered machines (named by machine) and those of the jobs on the dispatch list (a work
 * centre without a machine, named by its id), each listed once.
 */
export function WorkerHomeResource({ worker, onSaved }: { worker: Worker; onSaved(): void }) {
  const machines = useLoad<[Machine[], Job[]]>(
    // The dispatch list needs the Register time duty, which a setup maintainer may not hold:
    // without it the choices are the machine resources only.
    (signal) => Promise.all([listMachines(signal), listJobs({}, signal).catch((): Job[] => [])]),
    "home-resource-choices",
  );
  const [resourceId, setResourceId] = useState(worker.defaultResourceId ?? NONE);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const key = useRef(newKey());

  const changed = resourceId !== (worker.defaultResourceId ?? NONE);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setSaved(false);
    setBusy(true);
    try {
      await assignWorkerResource(worker.id, worker.version, resourceId || null, key.current);
      setSaved(true);
      key.current = newKey();
      onSaved();
    } catch (error) {
      setFailure(describeProblem(error));
      // A stale version is a different request next time.
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (machines.error) {
    return (
      <Card title="Home resource">
        <Alert tone="danger" title="Could not load the resources" live="alert" action={<Button size="sm" onClick={machines.reload}>Try again</Button>}>
          <ProblemMessage problem={machines.error} />
        </Alert>
      </Card>
    );
  }
  if (!machines.data) {
    return (
      <Card title="Home resource">
        <div aria-busy="true">
          <span className="visually-hidden">Loading resources…</span>
          <Skeleton height="tile" />
        </div>
      </Card>
    );
  }
  const [machineList, jobList] = machines.data;
  const byResource = new Map<string, string[]>();
  for (const m of machineList) byResource.set(m.resourceId, [...(byResource.get(m.resourceId) ?? []), m.machineId]);
  for (const j of jobList) if (!byResource.has(j.resourceId)) byResource.set(j.resourceId, []);
  const current = worker.defaultResourceId;

  return (
    <Card title="Home resource">
      <form className={styles.form} onSubmit={submit}>
        <FormField
          id="worker-home-resource"
          label="Home resource"
          hint={byResource.size === 0 ? "No machines are registered and no jobs are dispatched yet; the worker sees every job." : "The kiosk lists this resource's jobs for the worker. None shows every job."}
        >
          {(control) => (
            <Select {...control} value={resourceId} onChange={(event) => setResourceId(event.target.value)}>
              <option value={NONE}>None (every job)</option>
              {current && !byResource.has(current) ? <option value={current}>{`Current resource ${current}`}</option> : null}
              {[...byResource].map(([id, names]) => (
                <option key={id} value={id}>{names.length > 0 ? `${names.join(", ")} (${id.slice(0, 8)})` : `Resource ${id}`}</option>
              ))}
            </Select>
          )}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the home resource" live="alert">
              {failure.code === "SF-0143"
                ? "The worker was changed by someone else. Reload the page and try again."
                : failure.code === "SF-0156"
                  ? "The shop floor does not know this resource: it has no registered machine and no dispatched jobs."
                  : failure.message}{" "}
              ({failure.code})
            </Alert>
          </div>
        ) : null}
        {saved ? (
          <div className={styles.formActions}>
            <Alert tone="success" title="Home resource saved" live="status">
              The kiosk now lists this resource's jobs for the worker.
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy} disabled={!changed}>
            Save home resource
          </Button>
        </div>
      </form>
    </Card>
  );
}
