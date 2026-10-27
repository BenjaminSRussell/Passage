import { useRef, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { codeDowntime, describeProblem, listDowntimes, newKey, type Downtime, type DowntimeCategory, type Problem } from "./api/client";
import { dash, durationMinutes, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const REASON = /^\S.{0,31}$/;

/** Machine downtimes with their reason codes; open downtimes are coded here (FR-25.11, FR-25.14). */
export function DowntimesPage() {
  const [params, setParams] = useSearchParams();
  const openOnly = params.get("open") !== "false";
  const selectedId = params.get("downtime") ?? "";
  const state = useLoad((signal) => listDowntimes({ open: openOnly || undefined }, signal), `downtimes|${openOnly}`);
  const rows = state.data ?? [];
  const selected = rows.find((d) => d.id === selectedId) ?? null;
  const [notice, setNotice] = useState<string | null>(null);

  function select(id: string, notice: string | null = null) {
    const next = new URLSearchParams(params);
    if (id) next.set("downtime", id);
    else next.delete("downtime");
    setParams(next);
    setNotice(notice);
  }

  const columns: DataTableColumn<Downtime>[] = [
    { key: "resourceId", header: "Resource", mono: true },
    { key: "startedAt", header: "Started", render: (row) => formatDateTime(row.startedAt) },
    { key: "duration", header: "Duration", align: "end", render: (row) => durationMinutes(row.startedAt, row.endedAt) },
    { key: "state", header: "State", render: (row) => (row.endedAt ? <Badge tone="neutral">Ended</Badge> : <Badge tone="danger">Down</Badge>) },
    { key: "reasonCode", header: "Reason", mono: true, render: (row) => row.reasonCode ?? <Badge tone="warning">Not coded</Badge> },
    { key: "category", header: "Category", render: (row) => (row.category === "planned" ? "Planned" : "Unplanned") },
    { key: "andonRaised", header: "Andon", render: (row) => (row.andonRaised ? <Badge tone="warning">Raised</Badge> : dash) },
    {
      key: "action",
      header: "Action",
      render: (row) => (
        <Button size="sm" aria-label={`Code reason for downtime on ${row.resourceId} from ${formatDateTime(row.startedAt)}`} onClick={() => select(row.id)}>
          {row.reasonCode ? "Recode" : "Code reason"}
        </Button>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Downtimes"
        description="A machine that reports DOWN opens a downtime; the next running or idle report ends it. Give every stop a reason code so losses can be analyzed."
      />
      <div className={styles.toolbar}>
        <FormField id="downtimes-filter" label="Show">
          {(control) => (
            <Select
              {...control}
              value={openOnly ? "open" : "all"}
              onChange={(event) => {
                const next = new URLSearchParams(params);
                next.set("open", event.target.value === "open" ? "true" : "false");
                setParams(next);
              }}
            >
              <option value="open">Open downtimes</option>
              <option value="all">All downtimes</option>
            </Select>
          )}
        </FormField>
      </div>
      {notice ? (
        <Alert tone="success" title="Reason saved" live="status">
          {notice}
        </Alert>
      ) : null}
      {selected ? (
        <CodeReasonForm
          key={selected.id}
          downtime={selected}
          onDone={(saved) => {
            select("", `Downtime on ${saved.resourceId} is coded ${saved.reasonCode ?? ""}. An open andon stays until the machine has run for 5 minutes.`);
            state.reload();
          }}
          onCancel={() => select("")}
        />
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load downtimes" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Downtimes"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: openOnly ? "No machine is down" : "No downtimes recorded",
            description: "Downtimes open when a registered machine reports DOWN through the edge connector.",
          }}
        />
      )}
    </div>
  );
}

interface CodeReasonFormProps {
  downtime: Downtime;
  onDone(saved: Downtime): void;
  onCancel(): void;
}

function CodeReasonForm({ downtime, onDone, onCancel }: CodeReasonFormProps) {
  const [reason, setReason] = useState(downtime.reasonCode ?? "");
  const [category, setCategory] = useState<DowntimeCategory>(downtime.category);
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const code = reason.trim();
    if (!REASON.test(code)) {
      setError("Enter a reason code of up to 32 characters.");
      return;
    }
    setError(null);
    setFailure(null);
    setBusy(true);
    try {
      onDone(await codeDowntime(downtime.id, { reasonCode: code, category }, key.current));
    } catch (failed) {
      const problem = describeProblem(failed);
      if (problem.fields.reasonCode) setError(problem.fields.reasonCode);
      else setFailure(problem);
      // Keep the key so a retry after a lost response replays the stored outcome.
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title={`Code downtime from ${formatDateTime(downtime.startedAt)}`} headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="downtime-reason" label="Reason code" hint="From your downtime reason list, e.g. JAM or NO-MATERIAL." error={error ?? undefined}>
          {(control) => (
            <Input
              {...control}
              value={reason}
              maxLength={32}
              invalid={Boolean(error)}
              onChange={(event) => {
                setReason(event.target.value);
                key.current = newKey();
              }}
            />
          )}
        </FormField>
        <FormField id="downtime-category" label="Category" hint="Planned stops do not lower availability.">
          {(control) => (
            <Select
              {...control}
              value={category}
              onChange={(event) => {
                setCategory(event.target.value as DowntimeCategory);
                key.current = newKey();
              }}
            >
              <option value="unplanned">Unplanned</option>
              <option value="planned">Planned</option>
            </Select>
          )}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the reason" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save reason
          </Button>
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      </form>
    </Card>
  );
}
