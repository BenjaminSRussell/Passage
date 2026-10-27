import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, DataTable, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { listJobs, type Job, type JobStatus } from "./api/client";
import { fixed, formatDateTime, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const STATUS: Record<JobStatus, { label: string; tone: BadgeTone }> = {
  released: { label: "Released", tone: "neutral" },
  started: { label: "Started", tone: "info" },
  finished: { label: "Finished", tone: "success" },
};

export const JOB_STATUSES = Object.keys(STATUS) as JobStatus[];

export function JobStatusBadge({ status }: { status: JobStatus }) {
  const s = STATUS[status] ?? { label: status, tone: "neutral" as const };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

/** Quantities are decimal strings; show them with up to 4 places, trailing zeros trimmed. */
export function qty(value: string): string {
  const s = fixed(value, 4);
  return s.includes(".") ? s.replace(/\.?0+$/, "") : s;
}

const columns: DataTableColumn<Job>[] = [
  { key: "sequenceStart", header: "Start", render: (row) => formatDateTime(row.sequenceStart) },
  { key: "orderRef", header: "Order", mono: true },
  { key: "operationRef", header: "Operation", mono: true },
  { key: "resourceId", header: "Resource", mono: true },
  { key: "plannedQuantity", header: "Planned", align: "end", mono: true, render: (row) => qty(row.plannedQuantity) },
  { key: "goodQuantity", header: "Good", align: "end", mono: true, render: (row) => qty(row.goodQuantity) },
  { key: "scrapQuantity", header: "Scrap", align: "end", mono: true, render: (row) => qty(row.scrapQuantity) },
  {
    key: "status",
    header: "Status",
    render: (row) => (
      <span className={styles.flags}>
        <JobStatusBadge status={row.status} />
        {row.checks.some((c) => c.mandatory) ? <Badge tone="warning">Mandatory checks</Badge> : null}
      </span>
    ),
  },
  {
    key: "open",
    header: "Details",
    render: (row) => <Link to={`/shopfloor/jobs/${row.id}`}>{`Open ${row.orderRef} ${row.operationRef}`}</Link>,
  },
];

/**
 * Dispatch list (FR-25.3): jobs from the schedule in start order, per resource. Finished
 * jobs are hidden unless the status filter asks for them. Each job opens to report
 * quantities, record checks and finish.
 */
export function JobsPage() {
  const [params, setParams] = useSearchParams();
  const resourceId = params.get("resourceId") ?? "";
  const status = (params.get("status") ?? "") as JobStatus | "";
  const [resourceDraft, setResourceDraft] = useState(resourceId);
  const [resourceError, setResourceError] = useState<string | null>(null);
  const state = useLoad((signal) => listJobs({ resourceId, status }, signal), `jobs|${resourceId}|${status}`);

  function setFilter(name: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(name, value);
    else next.delete(name);
    setParams(next);
  }

  function applyResource(event: FormEvent) {
    event.preventDefault();
    const value = resourceDraft.trim();
    if (value !== "" && !UUID.test(value)) {
      setResourceError("Enter the resource id (a UUID), or leave it empty for every resource.");
      return;
    }
    setResourceError(null);
    setFilter("resourceId", value);
  }

  const filtered = resourceId !== "" || status !== "";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Dispatch list"
        description="Jobs released by the schedule, in start order. Open a job to report good and scrap quantities, record its quality checks and finish it."
      />
      <div className={styles.toolbar}>
        <form className={styles.toolbar} onSubmit={applyResource} noValidate>
          <FormField id="jobs-resource" label="Resource" hint="Resource id; empty shows every resource." error={resourceError ?? undefined}>
            {(control) => (
              <Input {...control} value={resourceDraft} invalid={Boolean(resourceError)} onChange={(event) => setResourceDraft(event.target.value)} />
            )}
          </FormField>
          <Button type="submit">Show jobs</Button>
        </form>
        <FormField id="jobs-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setFilter("status", event.target.value)}>
              <option value="">Open jobs</option>
              {JOB_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {STATUS[s].label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the dispatch list" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Jobs"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={
            filtered
              ? { title: "No jobs match these filters", description: "Clear a filter to see more jobs." }
              : { title: "No open jobs", description: "Jobs appear here when the schedule releases operations to the floor." }
          }
        />
      )}
    </div>
  );
}
