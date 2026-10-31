import { useRef, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createCapacityOverride, describeProblem, newKey, type AffectedJob, type NewCapacityOverride, type OverrideKind, type Problem } from "./api/client";
import { formatDateTime, fromUtcInput, shortId, UUID } from "./format";
import { mapFields } from "./forms";
import styles from "./pages.module.css";
import { ResourceGate } from "./ResourceGate";

export interface OverrideValues {
  resourceId: string;
  kind: OverrideKind;
  start: string;
  end: string;
  reason: string;
}

type Errors = Partial<Record<keyof OverrideValues, string>>;

const MAX_SPAN_MS = 31 * 24 * 3_600_000;

/** Checks that mirror the service, and the request the form sends. Times are UTC. */
export function validateOverride(values: OverrideValues): { errors: Errors; input?: NewCapacityOverride } {
  const errors: Errors = {};
  if (!UUID.test(values.resourceId)) errors.resourceId = "Choose a resource.";
  const start = fromUtcInput(values.start);
  const end = fromUtcInput(values.end);
  if (!start) errors.start = "Enter the start date and time.";
  if (!end) errors.end = "Enter the end date and time.";
  if (start && end) {
    if (Date.parse(end) <= Date.parse(start)) errors.end = "The end must be after the start.";
    else if (Date.parse(end) - Date.parse(start) > MAX_SPAN_MS) errors.end = "An override lasts at most 31 days.";
  }
  if (values.reason.trim().length > 200) errors.reason = "Use at most 200 characters.";
  if (Object.keys(errors).length > 0 || !start || !end) return { errors };
  const reason = values.reason.trim();
  return { errors, input: { kind: values.kind, start, end, ...(reason ? { reason } : {}) } };
}

const fieldNames: Record<string, keyof OverrideValues> = { kind: "kind", start: "start", end: "end", reason: "reason" };

function Form({ resources, initial }: { resources: { id: string; code: string; name: string }[]; initial: string }) {
  const [values, setValues] = useState<OverrideValues>({ resourceId: initial, kind: "downtime", start: "", end: "", reason: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [affected, setAffected] = useState<{ kind: OverrideKind; jobs: AffectedJob[] } | null>(null);
  const key = useRef(newKey());

  function update(patch: Partial<OverrideValues>) {
    setValues({ ...values, ...patch });
    setAffected(null);
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateOverride(values);
    setErrors(found);
    setFailure(null);
    setAffected(null);
    if (!input) return;
    setBusy(true);
    try {
      const result = await createCapacityOverride(values.resourceId, input, key.current);
      setAffected({ kind: result.kind, jobs: result.affectedJobs });
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = mapFields(problem.fields, fieldNames);
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="override-resource" label="Resource" error={errors.resourceId}>
          {(control) => (
            <Select {...control} value={values.resourceId} onChange={(event) => update({ resourceId: event.target.value })} invalid={Boolean(errors.resourceId)}>
              {resources.map((r) => (
                <option key={r.id} value={r.id}>{`${r.code} · ${r.name}`}</option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="override-kind" label="Kind" hint="A downtime wins over an extra shift that overlaps it.">
          {(control) => (
            <Select {...control} value={values.kind} onChange={(event) => update({ kind: event.target.value as OverrideKind })}>
              <option value="downtime">Downtime (close the resource)</option>
              <option value="extra">Extra shift (open the resource)</option>
            </Select>
          )}
        </FormField>
        <FormField id="override-start" label="From (UTC)" error={errors.start}>
          {(control) => <Input {...control} type="datetime-local" value={values.start} onChange={(event) => update({ start: event.target.value })} invalid={Boolean(errors.start)} />}
        </FormField>
        <FormField id="override-end" label="To (UTC)" error={errors.end}>
          {(control) => <Input {...control} type="datetime-local" value={values.end} onChange={(event) => update({ end: event.target.value })} invalid={Boolean(errors.end)} />}
        </FormField>
        <FormField id="override-reason" label="Reason" optional error={errors.reason}>
          {(control) => <Input {...control} maxLength={200} value={values.reason} onChange={(event) => update({ reason: event.target.value })} invalid={Boolean(errors.reason)} />}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the override" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save override
          </Button>
          <Link className={buttonClassName("ghost")} to={`/scheduling/capacity-overrides?resourceId=${encodeURIComponent(values.resourceId)}`}>
            Back to the list
          </Link>
        </div>
      </form>
      <Alert tone="success" live="status">
        {affected ? "The override was saved." : ""}
      </Alert>
      {affected && affected.kind === "downtime" ? (
        affected.jobs.length === 0 ? (
          <p className={styles.note}>No booked job overlaps this downtime.</p>
        ) : (
          <div className={styles.stack}>
            <Alert tone="warning" title={`${affected.jobs.length} booked job(s) overlap the downtime`}>
              They stay where they are and show as outside open capacity on the workspace. Move them there or run the schedule again; a firmed job
              must be unfirmed first.
            </Alert>
            <DataTable
              label="Jobs in the downtime"
              columns={[
                { key: "orderId", header: "Order", mono: true, render: (j: AffectedJob) => <span title={j.orderId}>{shortId(j.orderId)}</span> },
                { key: "operNumber", header: "Operation", align: "end" },
                { key: "start", header: "From", render: (j: AffectedJob) => formatDateTime(j.start) },
                { key: "end", header: "To", render: (j: AffectedJob) => formatDateTime(j.end) },
                { key: "firmed", header: "Firmed", render: (j: AffectedJob) => (j.firmed ? "Yes" : "No") },
              ]}
              rows={affected.jobs}
              getRowKey={(j) => j.jobId}
            />
          </div>
        )
      ) : null}
    </>
  );
}

/** Add a downtime or an extra shift to a resource calendar (FR-23.1, AC-23.7). */
export function NewCapacityOverridePage() {
  const [params] = useSearchParams();
  return (
    <div className={styles.stack}>
      <PageHeader title="New capacity override" description="Times are UTC. The weekly calendar itself does not change." />
      <Card>
        <ResourceGate need="Overrides change the calendar of a resource.">
          {(resources) => <Form resources={resources} initial={resources.find((r) => r.id === params.get("resourceId"))?.id ?? resources[0].id} />}
        </ResourceGate>
      </Card>
    </div>
  );
}
