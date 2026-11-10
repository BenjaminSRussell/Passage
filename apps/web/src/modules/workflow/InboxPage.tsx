import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { copy } from "../../shell/copy";
import { holdsDuty } from "../../shell/dutyGate";
import { ProblemMessage } from "../../shell/errors";
import { formatAge, formatDate, isPastDate } from "../../shell/format";
import { sourceList, type InboxItem } from "../../shell/inbox/inbox";
import { useInbox } from "../../shell/inbox/useInbox";
import { useWorkContext } from "../../shell/lookup/workContextState";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, ConfirmDialog, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { decidePendingChange } from "../party/api/client";
import { DUTY_APPROVE } from "../party/format";
import styles from "./pages.module.css";

type Decision = "approve" | "reject";
type Outcome = { tone: "success"; text: string } | { tone: "danger"; failure: unknown };

const ALL_KINDS = "";

function canDecide(item: InboxItem, duties: readonly string[]): boolean {
  return item.source === "party" && item.actions.includes("approve") && holdsDuty(duties, DUTY_APPROVE);
}

export function InboxPage() {
  const { legalEntityId } = useWorkContext();
  const duties = usePrincipalDuties();
  const inbox = useInbox(legalEntityId, true);
  const [kind, setKind] = useState(ALL_KINDS);
  const [confirming, setConfirming] = useState<{ item: InboxItem; action: Decision } | null>(null);
  const [busy, setBusy] = useState("");
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const statusRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (outcome) statusRef.current?.focus();
  }, [outcome]);

  const kinds = useMemo(() => [...new Set(inbox.items.map((i) => i.kind))].sort(), [inbox.items]);
  const rows = kind === ALL_KINDS ? inbox.items : inbox.items.filter((i) => i.kind === kind);

  async function decide(item: InboxItem, action: Decision) {
    setOutcome(null);
    setBusy(`${item.source}:${item.id}`);
    try {
      await decidePendingChange(item.id, action, "");
      inbox.remove(item);
      setOutcome({ tone: "success", text: `${action === "approve" ? "Approved" : "Rejected"}: ${item.number}.` });
    } catch (failure) {
      setOutcome({ tone: "danger", failure });
    } finally {
      setBusy("");
    }
  }

  const columns: DataTableColumn<InboxItem>[] = [
    {
      key: "work",
      header: "Work",
      render: (row) => (
        <>
          <strong>{row.kind}</strong>
          <br />
          {row.number}
        </>
      ),
    },
    { key: "reason", header: "Why it is here" },
    { key: "waiting", header: "Waiting", render: (row) => `${formatAge(row.createdAt)} (since ${formatDate(row.createdAt)})` },
    {
      key: "due",
      header: "Due",
      render: (row) =>
        row.dueDate ? (
          <>
            {formatDate(row.dueDate)} {isPastDate(row.dueDate) ? <Badge tone="warning">Overdue</Badge> : null}
          </>
        ) : (
          "—"
        ),
    },
    {
      key: "actions",
      header: "Actions",
      render: (row) => (
        <span className={styles.actions}>
          <Link className={buttonClassName("secondary", "sm")} to={row.link} aria-label={`Open ${row.kind} ${row.number}`}>
            Open
          </Link>
          {canDecide(row, duties) ? (
            <>
              <Button
                size="sm"
                variant="primary"
                aria-label={`Approve ${row.kind} ${row.number}`}
                loading={busy === `${row.source}:${row.id}`}
                disabled={busy !== "" && busy !== `${row.source}:${row.id}`}
                onClick={() => setConfirming({ item: row, action: "approve" })}
              >
                Approve
              </Button>
              <Button
                size="sm"
                variant="danger"
                aria-label={`Reject ${row.kind} ${row.number}`}
                disabled={busy !== ""}
                onClick={() => setConfirming({ item: row, action: "reject" })}
              >
                Reject
              </Button>
            </>
          ) : null}
        </span>
      ),
    },
  ];

  const loading = inbox.status === "loading";
  const failed = inbox.status === "error";

  return (
    <div className={styles.stack}>
      <PageHeader title={copy.myWork.title} description={copy.myWork.description} />
      <div role="status" aria-live="polite" ref={statusRef} tabIndex={-1}>
        {outcome?.tone === "success" ? <Alert tone="success">{outcome.text}</Alert> : null}
        {outcome?.tone === "danger" ? (
          <Alert tone="danger" title="The decision failed">
            <ProblemMessage problem={outcome.failure} />
          </Alert>
        ) : null}
      </div>
      {failed ? (
        <Alert
          tone="danger"
          title={copy.myWork.errorTitle}
          live="alert"
          action={<Button onClick={inbox.reload}>{copy.myWork.retry}</Button>}
        >
          {inbox.needsEntity ? copy.myWork.needEntity : <ProblemMessage problem={inbox.problem} />}
        </Alert>
      ) : null}
      {inbox.status === "ready" && inbox.unavailable.length > 0 ? (
        <Alert tone="warning" live="status" action={<Button onClick={inbox.reload}>{copy.myWork.retry}</Button>}>
          {copy.myWork.partial(sourceList(inbox.unavailable))}
        </Alert>
      ) : null}
      {inbox.status === "ready" && inbox.truncated.length > 0 ? <Alert tone="info">{copy.myWork.truncated(sourceList(inbox.truncated))}</Alert> : null}
      {inbox.sourcesChanged ? (
        <Alert tone="warning" live="alert" action={<Button onClick={inbox.reload}>{copy.myWork.reloadFromStart}</Button>}>
          {copy.myWork.changed}
        </Alert>
      ) : null}
      {inbox.status === "ready" && inbox.problem ? (
        <Alert tone="danger" live="alert" action={<Button onClick={inbox.loadMore}>{copy.myWork.retry}</Button>}>
          <ProblemMessage problem={inbox.problem} />
        </Alert>
      ) : null}
      {kinds.length > 1 ? (
        <div className={styles.filter}>
          <FormField id="inbox-kind" label="Type of work">
            {(control) => (
              <Select {...control} value={kind} onChange={(event) => setKind(event.target.value)}>
                <option value={ALL_KINDS}>All types</option>
                {kinds.map((k) => (
                  <option key={k} value={k}>
                    {k}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      ) : null}
      {failed ? null : (
        <DataTable
          label="Work waiting for you"
          columns={columns}
          rows={rows}
          loading={loading}
          getRowKey={(row) => `${row.source}:${row.id}`}
          empty={{
            title: copy.myWork.emptyTitle,
            description: kind === ALL_KINDS ? copy.myWork.emptyDescription : "Nothing of this type is waiting for you.",
          }}
        />
      )}
      {inbox.status === "ready" && inbox.hasMore ? (
        <div>
          <Button loading={inbox.loadingMore} onClick={inbox.loadMore}>
            {copy.myWork.loadMore}
          </Button>
        </div>
      ) : null}
      {confirming ? (
        <ConfirmDialog
          title={confirming.action === "approve" ? "Approve this change?" : "Reject this change?"}
          description={`${confirming.item.kind}: ${confirming.item.number}`}
          confirmLabel={confirming.action === "approve" ? "Confirm approval" : "Confirm rejection"}
          cancelLabel="Cancel"
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            const { item, action } = confirming;
            setConfirming(null);
            void decide(item, action);
          }}
        />
      ) : null}
    </div>
  );
}
