import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, Textarea } from "../../shell/ui";
import { describeProblem, getCapacityBlock, newKey, updateCapacityBlock, type AffectedJob, type CapacityBlock, type CapacityBlockPatch, type Problem } from "./api/client";
import { describeCondition, describeWindow } from "./CapacityBlocksPage";
import { formatDateTime, shortId } from "./format";
import { parseCondition } from "./NewCapacityBlockPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface BlockEditValues {
  priority: string;
  active: boolean;
  condition: string;
}

type Errors = Partial<Record<"priority" | "condition" | "form", string>>;

/** One predicate per line, the way the create form reads them. */
export function conditionText(block: CapacityBlock): string {
  return block.condition.map((p) => `${p.attribute} ${p.op} ${p.value}`).join("\n");
}

/** Checks that mirror the service, and the patch the form sends: only what changed. */
export function validateBlockEdit(values: BlockEditValues, current: CapacityBlock): { errors: Errors; patch?: CapacityBlockPatch } {
  const errors: Errors = {};
  const priority = Number(values.priority);
  if (values.priority.trim() === "" || !Number.isInteger(priority) || priority < 0 || priority > 1000) errors.priority = "Enter a whole number from 0 to 1000.";
  const parsed = parseCondition(values.condition);
  if (parsed.error) errors.condition = parsed.error;
  if (Object.keys(errors).length > 0) return { errors };
  const patch: CapacityBlockPatch = {};
  if (priority !== current.priority) patch.priority = priority;
  if (values.active !== current.active) patch.active = values.active;
  if (JSON.stringify(parsed.condition) !== JSON.stringify(current.condition)) patch.condition = parsed.condition;
  if (Object.keys(patch).length === 0) {
    errors.form = "Nothing changed.";
    return { errors };
  }
  return { errors, patch };
}

function Form({ initial, onReload }: { initial: CapacityBlock; onReload: () => void }) {
  const [block, setBlock] = useState<CapacityBlock>(initial);
  const [values, setValues] = useState<BlockEditValues>({ priority: String(initial.priority), active: initial.active, condition: conditionText(initial) });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<{ version: number; jobs: AffectedJob[] } | null>(null);
  const key = useRef(newKey());

  function update(patch: Partial<BlockEditValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, patch } = validateBlockEdit(values, block);
    setErrors(found);
    setFailure(null);
    setSaved(null);
    if (!patch) return;
    setBusy(true);
    try {
      const { affectedJobs, ...next } = await updateCapacityBlock(block.id, block.version, patch, key.current);
      setBlock(next);
      setSaved({ version: next.version, jobs: affectedJobs });
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["priority", "condition"] as const) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <dl className={styles.details}>
        <div className={styles.entry}>
          <dt className={styles.term}>When</dt>
          <dd className={styles.value}>{describeWindow(block)}</dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Condition now</dt>
          <dd className={styles.value}>{describeCondition(block.condition)}</dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Status</dt>
          <dd className={styles.value}>
            <Badge tone={block.active ? "success" : "neutral"}>{block.active ? "Active" : "Inactive"}</Badge>
          </dd>
        </div>
      </dl>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="block-priority" label="Priority" hint="0 to 1000. Higher priority wins where blocks overlap." error={errors.priority}>
          {(control) => <Input {...control} inputMode="numeric" value={values.priority} onChange={(event) => update({ priority: event.target.value })} invalid={Boolean(errors.priority)} />}
        </FormField>
        <FormField
          id="block-condition"
          label="Condition"
          hint="One predicate per line, such as color = white or grade >= 2. Leave blank to admit every job."
          error={errors.condition}
        >
          {(control) => <Textarea {...control} rows={4} value={values.condition} onChange={(event) => update({ condition: event.target.value })} invalid={Boolean(errors.condition)} />}
        </FormField>
        <div className={styles.formActions}>
          <span className={styles.check}>
            <Input id="block-active" type="checkbox" checked={values.active} onChange={(event) => update({ active: event.target.checked })} />
            <label htmlFor="block-active">Active</label>
          </span>
        </div>
        {errors.form ? (
          <p role="alert" className={styles.fieldError}>
            {errors.form}
          </p>
        ) : null}
        {failure ? (
          <div className={styles.formActions}>
            {failure.code === "SCHED-0127" ? (
              <Alert
                tone="danger"
                title="The block changed since you opened it"
                live="alert"
                action={
                  <Button size="sm" onClick={onReload}>
                    Load the latest version
                  </Button>
                }
              >
                <ProblemMessage problem={failure} /> Loading the latest version discards your changes on this screen.
              </Alert>
            ) : (
              <Alert tone="danger" title="Could not save the block" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            )}
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save changes
          </Button>
          <Link className={buttonClassName("ghost")} to="/scheduling/capacity-blocks">
            All capacity blocks
          </Link>
        </div>
      </form>
      <Alert tone="success" live="status">
        {saved ? `The block was saved (version ${saved.version}). Runs and moves from now on use it; jobs already placed stay where they are.` : ""}
      </Alert>
      {saved ? (
        saved.jobs.length === 0 ? (
          <p className={styles.note}>No booked job on this resource is kept out by its blocks.</p>
        ) : (
          <div className={styles.stack}>
            <Alert tone="warning" title={`${saved.jobs.length} booked job(s) now sit in a block they may not use`}>
              They stay where they are. Move them or run the schedule again to place them in unblocked capacity or a block they match; a firmed job must
              be unfirmed first.
            </Alert>
            <DataTable
              label="Jobs kept out by the blocks"
              columns={[
                { key: "orderId", header: "Order", mono: true, render: (j: AffectedJob) => <span title={j.orderId}>{shortId(j.orderId)}</span> },
                { key: "operNumber", header: "Operation", align: "end" },
                { key: "start", header: "From", render: (j: AffectedJob) => formatDateTime(j.start) },
                { key: "end", header: "To", render: (j: AffectedJob) => formatDateTime(j.end) },
                { key: "firmed", header: "Firmed", render: (j: AffectedJob) => (j.firmed ? "Yes" : "No") },
              ]}
              rows={saved.jobs}
              getRowKey={(j) => j.jobId}
            />
          </div>
        )
      ) : null}
    </>
  );
}

/** Change a capacity block's priority, condition or active flag (FR-23.17). */
export function CapacityBlockDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad<CapacityBlock>((signal) => getCapacityBlock(id, signal), id);
  return (
    <div className={styles.stack}>
      <PageHeader title={state.data ? state.data.code : "Capacity block"} description="Deactivate a block to retire it. The window and resource do not change; create a new block for those." />
      {state.error ? (
        <Alert tone="danger" title="Could not load the capacity block" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : !state.data ? (
        <div aria-busy="true">
          <span className="visually-hidden">Loading the capacity block…</span>
          <Skeleton height="tile" />
        </div>
      ) : (
        <Card title="Capacity block">
          <Form key={`${state.data.id}:${state.data.version}`} initial={state.data} onReload={state.reload} />
        </Card>
      )}
    </div>
  );
}
