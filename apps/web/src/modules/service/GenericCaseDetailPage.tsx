import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, Select, Skeleton, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  advanceCaseStage,
  ASSOCIABLE_TYPES,
  ASSOCIATION_ROLES,
  associateCase,
  describeProblem,
  getGenericCase,
  listCaseCategories,
  listCaseProcesses,
  newKey,
  reopenGenericCase,
  transitionGenericCase,
  type AssociableType,
  type AssociationRole,
  type CaseLogEntry,
  type CaseStage,
  type GenericCase,
  type GenericTransition,
  type Problem,
  type Versioned,
} from "./api/client";
import { ActivityPanel, RecordLink } from "./ActivitiesPage";
import { PriorityBadge } from "./badges";
import { Details } from "./Details";
import { dash, formatDateTime, sentence, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const logColumns: DataTableColumn<CaseLogEntry & { id: string }>[] = [
  { key: "at", header: "When", mono: true, render: (row) => formatDateTime(row.at) },
  { key: "kind", header: "Kind", render: (row) => sentence(row.kind) },
  { key: "text", header: "Entry" },
  { key: "actor", header: "By", mono: true, render: (row) => (row.actor ? shortId(row.actor) : dash) },
];

type Mode = "back" | "resolve" | "reopen" | null;

/** The stages around the current one: the next stage to enter and the one a move back returns to. */
export function stageNeighbours(stages: CaseStage[], current: string): { index: number; next?: CaseStage; previous?: CaseStage } {
  const index = stages.findIndex((s) => s.code === current);
  return { index, next: index >= 0 ? stages[index + 1] : undefined, previous: index > 0 ? stages[index - 1] : undefined };
}

/** One case in any category: its stages and their required activities, resolution, associations, log and activities (FR-45.12, BR-45.9). */
export function GenericCaseDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getGenericCase(id, signal), id);
  const categories = useLoad((signal) => listCaseCategories(signal), "categories");
  const processes = useLoad((signal) => listCaseProcesses(signal), "processes");
  const [latest, setLatest] = useState<Versioned<GenericCase> | null>(null);
  const [mode, setMode] = useState<Mode>(null);
  const [confirming, setConfirming] = useState<"CLOSED" | "CANCELED" | null>(null);
  const [reason, setReason] = useState("");
  const [resolutionCode, setResolutionCode] = useState("");
  const [resolutionText, setResolutionText] = useState("");
  const [assoc, setAssoc] = useState<{ entityType: AssociableType | ""; entityId: string; role: AssociationRole }>({ entityType: "", entityId: "", role: "RELATED" });
  const [fieldError, setFieldError] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  // One key per intent: a retry of the same move replays instead of failing on the version.
  const moveKey = useRef(newKey());

  const current = latest && latest.value.id === id && (!state.data || latest.value.version >= state.data.value.version) ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Case" />
        <Alert
          tone="danger"
          title={state.error.code === "CASE-SEC-001" ? "You do not have access to this case" : "Could not load the case"}
          live="alert"
          action={<Button size="sm" onClick={state.reload}>Try again</Button>}
        >
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Case" />
        <span className="visually-hidden">Loading the case…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: c, etag } = current;
  const category = categories.data?.items.find((k) => k.id === c.categoryId);
  const stages = processes.data?.items.find((p) => p.id === category?.processId)?.stages ?? [];
  const { index, next, previous } = stageNeighbours(stages, c.stageCode);
  const required = index >= 0 ? stages[index].requiredActivities : [];
  const open = c.status === "OPEN";

  async function run(action: () => Promise<{ value: GenericCase; etag: string }>, done: (next: GenericCase) => string) {
    setBusy(true);
    setFailure(null);
    setNotice("");
    try {
      const next = await action();
      moveKey.current = newKey();
      setLatest(next);
      setMode(null);
      setReason("");
      setNotice(done(next.value));
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (Object.keys(problem.fields).length > 0) setFieldError(problem.fields);
      if (problem.code === "conflict_version" || problem.code === "idempotency_key_reuse") {
        moveKey.current = newKey();
      }
      if (problem.code === "conflict_version") {
        setLatest(null);
        state.reload();
      }
    } finally {
      setBusy(false);
    }
  }

  const advance = (to: string, why: string) =>
    run(
      () => advanceCaseStage(c.id, etag, to, why, moveKey.current),
      (n) => `Case moved to stage ${n.stageCode}.`,
    );

  const transition = (move: GenericTransition) =>
    run(
      () => transitionGenericCase(c.id, etag, move, moveKey.current),
      (n) => `Case is now ${sentence(n.status).toLowerCase()}.`,
    );

  function submitBack(event: FormEvent) {
    event.preventDefault();
    if (!previous) return;
    if (!reason.trim()) {
      setFieldError({ reason: "Say why the case goes back a stage." });
      return;
    }
    setFieldError({});
    void advance(previous.code, reason.trim());
  }

  function submitResolve(event: FormEvent) {
    event.preventDefault();
    if (!resolutionCode.trim()) {
      setFieldError({ resolutionCode: "Choose a resolution code." });
      return;
    }
    setFieldError({});
    void transition({ to: "RESOLVED", resolutionCode: resolutionCode.trim().toUpperCase(), ...(resolutionText.trim() ? { resolutionText: resolutionText.trim() } : {}) });
  }

  async function submitReopen(event: FormEvent) {
    event.preventDefault();
    if (!reason.trim()) {
      setFieldError({ reason: "Say why the case is reopened." });
      return;
    }
    setFieldError({});
    setBusy(true);
    setFailure(null);
    setNotice("");
    try {
      const result = await reopenGenericCase(c.id, etag, reason.trim(), moveKey.current);
      moveKey.current = newKey();
      setMode(null);
      setReason("");
      if (result.created) {
        navigate(`/service/case-management/${result.case.id}`);
      } else {
        setLatest({ value: result.case, etag: `"${result.case.version}"` });
        setNotice("Case reopened.");
      }
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "conflict_version") {
        moveKey.current = newKey();
        setLatest(null);
        state.reload();
      }
    } finally {
      setBusy(false);
    }
  }

  function submitAssociation(event: FormEvent) {
    event.preventDefault();
    const errors: Record<string, string> = {};
    if (!assoc.entityType) errors.entityType = "Choose the record type.";
    if (!UUID.test(assoc.entityId.trim())) errors.entityId = "Enter the record's UUID.";
    setFieldError(errors);
    if (Object.keys(errors).length > 0 || !assoc.entityType) return;
    const association = { entityType: assoc.entityType, entityId: assoc.entityId.trim(), role: assoc.role };
    void run(
      () => associateCase(c.id, association, moveKey.current),
      () => {
        setAssoc({ entityType: "", entityId: "", role: "RELATED" });
        return `${sentence(association.entityType)} associated.`;
      },
    );
  }

  const failureTitle =
    failure?.code === "conflict_version"
      ? "Someone else changed this case; it was reloaded"
      : failure?.code === "CASE-STG-001"
        ? "The stage's required activities are not done"
        : "The change failed";

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Case ${c.number}`}
        description={c.subject}
        meta={<StatusPill status={c.status} />}
        actions={
          <div className={styles.actions}>
            {open && next ? (
              <Button variant="primary" loading={busy && mode === null} disabled={busy} onClick={() => void advance(next.code, "")}>
                {`Advance to ${next.code}`}
              </Button>
            ) : null}
            {open && previous ? (
              <Button variant="secondary" disabled={busy} onClick={() => setMode("back")}>
                {`Back to ${previous.code}`}
              </Button>
            ) : null}
            {open ? (
              <Button variant="secondary" disabled={busy} onClick={() => setMode("resolve")}>
                Resolve
              </Button>
            ) : null}
            {c.status === "RESOLVED" ? (
              <Button variant="secondary" disabled={busy} onClick={() => setConfirming("CLOSED")}>
                Close
              </Button>
            ) : null}
            {c.status === "RESOLVED" || c.status === "CLOSED" ? (
              <Button variant="secondary" disabled={busy} onClick={() => setMode("reopen")}>
                Reopen
              </Button>
            ) : null}
            {open ? (
              <Button variant="danger" disabled={busy} onClick={() => setConfirming("CANCELED")}>
                Cancel case
              </Button>
            ) : null}
          </div>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failureTitle} live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}

      {mode === "back" && previous ? (
        <Card title={`Move back to ${previous.code}`} headingLevel={3}>
          <form className={styles.form} onSubmit={submitBack} noValidate aria-label="Move back a stage">
            <div className={styles.wide}>
              <FormField id="gcase-back-reason" label="Reason" error={fieldError.reason}>
                {(control) => <Input {...control} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(fieldError.reason)} />}
              </FormField>
            </div>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Move back
              </Button>
              <Button variant="ghost" onClick={() => setMode(null)}>
                Keep the stage
              </Button>
            </div>
          </form>
        </Card>
      ) : null}

      {mode === "resolve" ? (
        <Card title="Resolve the case" headingLevel={3}>
          <form className={styles.form} onSubmit={submitResolve} noValidate aria-label="Resolve the case">
            <FormField id="gcase-resolution-code" label="Resolution code" error={fieldError.resolutionCode}>
              {(control) =>
                category && category.resolutionCodes.length > 0 ? (
                  <Select {...control} value={resolutionCode} onChange={(e) => setResolutionCode(e.target.value)} invalid={Boolean(fieldError.resolutionCode)}>
                    <option value="">Choose a code</option>
                    {category.resolutionCodes.map((code) => (
                      <option key={code} value={code}>
                        {code}
                      </option>
                    ))}
                  </Select>
                ) : (
                  <Input {...control} className={styles.mono} value={resolutionCode} onChange={(e) => setResolutionCode(e.target.value)} invalid={Boolean(fieldError.resolutionCode)} />
                )
              }
            </FormField>
            <div className={styles.wide}>
              <FormField id="gcase-resolution-text" label="Resolution" optional>
                {(control) => <Textarea {...control} value={resolutionText} onChange={(e) => setResolutionText(e.target.value)} />}
              </FormField>
            </div>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Resolve
              </Button>
              <Button variant="ghost" onClick={() => setMode(null)}>
                Keep it open
              </Button>
            </div>
          </form>
        </Card>
      ) : null}

      {mode === "reopen" ? (
        <Card title="Reopen the case" headingLevel={3}>
          <p className={styles.note}>
            {`Within ${category?.reopenWindowDays ?? 14} days of its resolution the case itself reopens; after that a new case is opened and linked to this one.`}
          </p>
          <form className={styles.form} onSubmit={submitReopen} noValidate aria-label="Reopen the case">
            <div className={styles.wide}>
              <FormField id="gcase-reopen-reason" label="Reason" error={fieldError.reason}>
                {(control) => <Input {...control} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(fieldError.reason)} />}
              </FormField>
            </div>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Reopen
              </Button>
              <Button variant="ghost" onClick={() => setMode(null)}>
                Keep it as is
              </Button>
            </div>
          </form>
        </Card>
      ) : null}

      <Card title="Stages" headingLevel={3}>
        {processes.error || categories.error ? (
          <Alert tone="warning" title="The process could not be loaded" live="alert">
            {(processes.error ?? categories.error)?.message}
          </Alert>
        ) : stages.length === 0 ? (
          <p className={styles.note}>{processes.loading || categories.loading ? "Loading the process…" : "This category has no case process."}</p>
        ) : (
          <ol className={styles.trace} aria-label="Case stages">
            {stages.map((s, i) => (
              <li key={s.code}>
                <span className={styles.mono}>{s.code}</span>{" "}
                {i === index ? <Badge tone="info">Current</Badge> : i < index ? <Badge tone="success">Done</Badge> : null}
                {s.requiredActivities.length > 0 ? ` — requires ${s.requiredActivities.join(", ")}` : ""}
              </li>
            ))}
          </ol>
        )}
        {open && required.length > 0 ? (
          <div className={styles.actions}>
            {required.map((subject) => (
              <Link
                key={subject}
                className={buttonClassName("secondary", "sm")}
                to={`/service/activities/new?entityType=case&entityId=${c.id}&subject=${encodeURIComponent(subject)}`}
              >
                {`Add ${subject}`}
              </Link>
            ))}
          </div>
        ) : null}
      </Card>

      <Card title="Case" headingLevel={3}>
        <Details
          label="Case"
          items={[
            ["Category", `${c.categoryCode} (${sentence(c.categoryType)})`],
            ["Stage", c.stageCode || dash, true],
            ["Priority", <PriorityBadge key="p" priority={c.priority} />],
            ["Owner", c.ownerId || dash, true],
            ["Legal entity", c.legalEntityId, true],
            ["Customer", c.customerPartyId ?? dash, true],
            ["Opened", formatDateTime(c.openedAt), true],
            ["Response due", formatDateTime(c.slaResponseDue), true],
            ["Resolution due", formatDateTime(c.slaResolutionDue), true],
            ["Resolution", c.resolutionCode ? `${c.resolutionCode}${c.resolutionText ? `: ${c.resolutionText}` : ""}` : dash],
            ["Resolved", formatDateTime(c.resolvedAt), true],
            ["Closed", formatDateTime(c.closedAt), true],
            ["Reopened from", c.reopenedFrom ? <Link key="r" to={`/service/case-management/${c.reopenedFrom}`}>{shortId(c.reopenedFrom)}</Link> : dash],
          ]}
        />
        {c.description ? <p className={styles.note}>{c.description}</p> : null}
      </Card>

      <Card title="Associated records" headingLevel={3}>
        {c.associations.length === 0 ? (
          <p className={styles.note}>No records are associated with this case.</p>
        ) : (
          <ul className={styles.trace} aria-label="Associated records">
            {c.associations.map((a) => (
              <li key={`${a.entityType}-${a.entityId}`}>
                <RecordLink type={a.entityType} id={a.entityId} /> <Badge tone={a.role === "SUBJECT" ? "info" : "neutral"}>{sentence(a.role)}</Badge>
              </li>
            ))}
          </ul>
        )}
        {c.status !== "CLOSED" && c.status !== "CANCELED" ? (
          <form className={styles.form} onSubmit={submitAssociation} noValidate aria-label="Associate a record">
            <FormField id="gcase-assoc-type" label="Record type" error={fieldError.entityType}>
              {(control) => (
                <Select {...control} value={assoc.entityType} onChange={(e) => setAssoc((v) => ({ ...v, entityType: e.target.value as AssociableType | "" }))} invalid={Boolean(fieldError.entityType)}>
                  <option value="">Choose a type</option>
                  {ASSOCIABLE_TYPES.map((t) => (
                    <option key={t} value={t}>
                      {sentence(t)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="gcase-assoc-id" label="Record" hint="UUID of the record." error={fieldError.entityId}>
              {(control) => (
                <Input {...control} className={styles.mono} value={assoc.entityId} onChange={(e) => setAssoc((v) => ({ ...v, entityId: e.target.value }))} invalid={Boolean(fieldError.entityId)} />
              )}
            </FormField>
            <FormField id="gcase-assoc-role" label="Role">
              {(control) => (
                <Select {...control} value={assoc.role} onChange={(e) => setAssoc((v) => ({ ...v, role: e.target.value as AssociationRole }))}>
                  {ASSOCIATION_ROLES.map((r) => (
                    <option key={r} value={r}>
                      {sentence(r)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" loading={busy && mode === null}>
                Associate record
              </Button>
            </div>
          </form>
        ) : null}
      </Card>

      <Card title="Case log" headingLevel={3}>
        <DataTable
          label="Case log"
          columns={logColumns}
          rows={c.log.map((e, i) => ({ ...e, id: String(i) }))}
          getRowKey={(row) => row.id}
          empty={{ title: "No log entries" }}
        />
      </Card>

      <ActivityPanel entityType="case" entityId={c.id} />

      {confirming ? (
        <ConfirmDialog
          title={confirming === "CLOSED" ? "Close this case?" : "Cancel this case?"}
          description={
            confirming === "CLOSED"
              ? "A closed case can only be reopened with a reason; after the category's reopen window that opens a new case."
              : "A canceled case is final and cannot be reopened."
          }
          confirmLabel={confirming === "CLOSED" ? "Close" : "Cancel case"}
          cancelLabel="Keep the case"
          onConfirm={() => {
            const to = confirming;
            setConfirming(null);
            void transition({ to });
          }}
          onCancel={() => setConfirming(null)}
        />
      ) : null}
    </div>
  );
}
