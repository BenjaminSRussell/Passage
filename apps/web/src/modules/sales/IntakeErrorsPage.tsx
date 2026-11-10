import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, DataTable, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { describeProblem, INTAKE_ERROR_STATUSES, listIntakeErrors, replayIntakeErrors, type IntakeReplayBatch, type Problem, type IntakeError, type IntakeErrorFilter, type IntakeErrorStatus } from "./api/client";
import { formatDateTime, sentence, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** Replaying needs this duty (SalesOrderMaintain); the service enforces it. */
const DUTY_ORDER_WRITE = "sales.order.write";

const tone: Record<IntakeErrorStatus, BadgeTone> = { OPEN: "warning", RESOLVED: "success" };

export function IntakeErrorStatusBadge({ status }: { status: IntakeErrorStatus }) {
  return <Badge tone={tone[status]}>{sentence(status)}</Badge>;
}

const columns: DataTableColumn<IntakeError>[] = [
  { key: "channel", header: "Channel", render: (e) => <Link to={`/sales/intake-errors/${e.id}`}>{e.channel}</Link> },
  { key: "externalRef", header: "External reference", mono: true, render: (e) => e.externalRef ?? "—" },
  { key: "errorCode", header: "Error", mono: true, render: (e) => e.errorCode },
  { key: "message", header: "What was wrong", render: (e) => e.message },
  { key: "attempts", header: "Attempts", align: "end", render: (e) => String(e.attempts) },
  { key: "lastSeenAt", header: "Last seen", render: (e) => formatDateTime(e.lastSeenAt) },
  { key: "status", header: "Status", render: (e) => <IntakeErrorStatusBadge status={e.status} /> },
];

/**
 * Intake errors (PRD §5.27.14): channel orders (EDI, portal, API clients) that
 * the service refused, most recently seen first. A sender that retries the same
 * payload raises the attempts instead of adding a row.
 */
export function IntakeErrorsPage() {
  const [draft, setDraft] = useState({ legalEntityId: "", channel: "", errorCode: "", status: "OPEN" as IntakeErrorStatus | "" });
  const [filter, setFilter] = useState<IntakeErrorFilter>({ status: "OPEN" });
  const invalid = draft.legalEntityId.trim() && !UUID.test(draft.legalEntityId.trim()) ? "Enter a legal entity UUID." : undefined;
  const state = useLoad((signal) => listIntakeErrors(filter, signal), JSON.stringify(filter));
  const canReplay = holdsDuty(usePrincipalDuties(), DUTY_ORDER_WRITE);
  const [replaying, setReplaying] = useState(false);
  const [batch, setBatch] = useState<IntakeReplayBatch | null>(null);
  const [replayProblem, setReplayProblem] = useState<Problem | null>(null);

  async function replayAll() {
    setBatch(null);
    setReplayProblem(null);
    setReplaying(true);
    try {
      setBatch(await replayIntakeErrors({ legalEntityId: filter.legalEntityId, channel: filter.channel, errorCode: filter.errorCode }));
      state.reload();
    } catch (failure) {
      setReplayProblem(describeProblem(failure));
    } finally {
      setReplaying(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Intake errors"
        description="Channel orders the service refused, with the error and how often the sender tried again. Fix the sender or the master data, then close the error with a note."
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (invalid) return;
          setFilter({
            legalEntityId: draft.legalEntityId.trim() || undefined,
            channel: draft.channel.trim() || undefined,
            errorCode: draft.errorCode.trim() || undefined,
            status: draft.status || undefined,
          });
        }}
      >
        <FormField id="ie-le" label="Legal entity" error={invalid} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid)} />}
        </FormField>
        <FormField id="ie-channel" label="Channel" optional>
          {(control) => <Input {...control} value={draft.channel} onChange={(e) => setDraft({ ...draft, channel: e.target.value })} />}
        </FormField>
        <FormField id="ie-code" label="Error code" optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.errorCode} onChange={(e) => setDraft({ ...draft, errorCode: e.target.value })} />}
        </FormField>
        <FormField id="ie-status" label="Status">
          {(control) => (
            <Select {...control} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as IntakeErrorStatus | "" })}>
              <option value="">Any status</option>
              {INTAKE_ERROR_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Filter</Button>
        {canReplay ? (
          <Button variant="secondary" loading={replaying} onClick={() => void replayAll()}>
            Replay open errors
          </Button>
        ) : null}
      </form>
      {replayProblem ? (
        <Alert tone="danger" title="The replay could not run" live="alert">
          <ProblemMessage problem={replayProblem} />
        </Alert>
      ) : null}
      <Alert tone={batch && batch.failed > 0 ? "warning" : "success"} live="status">
        {batch
          ? `Replayed ${batch.replayed} of ${batch.attempted} orders; ${batch.failed} refused again; ${batch.skipped} dead letters left for an operator (10 or more attempts).`
          : null}
      </Alert>
      {state.error ? (
        <Alert tone="danger" title="Could not load the intake errors" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Intake errors"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(e) => e.id}
          loading={state.loading}
          empty={{ title: "No intake errors", description: "A channel order that the service refuses appears here with its error code." }}
        />
      )}
    </div>
  );
}
