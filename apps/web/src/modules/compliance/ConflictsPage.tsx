import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Badge, Button, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { CONFLICT_STATES, listConflicts, SEVERITIES, type Conflict, type ConflictFilter, type ConflictState, type Severity } from "./api/client";
import { ageDays, dash, daysUntil, formatDateTime, sentence, severityTone, shortId, stateTone, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** "Maint, Pay" for the roles that grant each side of a conflict. */
export function rolesText(c: Pick<Conflict, "rolesA" | "rolesB">): string {
  const all = [...new Set([...c.rolesA, ...c.rolesB])];
  return all.length > 0 ? all.join(", ") : dash;
}

/** "In 12 days", "Expired" or a dash: the mitigation expiry of a MITIGATED conflict. */
export function expiryText(c: Pick<Conflict, "state" | "expiresAt">, now: Date = new Date()): string {
  if (c.state !== "MITIGATED" || !c.expiresAt) return dash;
  const days = daysUntil(c.expiresAt, now);
  if (days <= 0) return "Expired";
  return days === 1 ? "In 1 day" : `In ${days} days`;
}

const columns: DataTableColumn<Conflict>[] = [
  { key: "ruleId", header: "Rule", mono: true },
  { key: "userId", header: "User", mono: true, render: (c) => <span title={c.userId}>{shortId(c.userId)}</span> },
  { key: "severity", header: "Severity", render: (c) => <Badge tone={severityTone[c.severity]}>{sentence(c.severity)}</Badge> },
  { key: "duties", header: "Duties", render: (c) => `${c.dutyA} and ${c.dutyB}` },
  { key: "roles", header: "Roles", render: rolesText },
  { key: "state", header: "State", render: (c) => <Badge tone={stateTone[c.state]}>{sentence(c.state)}</Badge> },
  { key: "age", header: "Age", align: "end", render: (c) => <span title={formatDateTime(c.detectedAt)}>{ageDays(c.detectedAt)} d</span> },
  { key: "expires", header: "Mitigation expires", render: (c) => expiryText(c) },
];

/**
 * SoD conflicts (PRD §9.48.6): filter by state and severity, open a conflict
 * to mitigate it or accept its risk. Defaults to OPEN, the review queue.
 */
export function ConflictsPage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<{ state: ConflictState | ""; severity: Severity | ""; userId: string }>({ state: "OPEN", severity: "", userId: "" });
  const [filter, setFilter] = useState<ConflictFilter>({ state: "OPEN" });
  const [extra, setExtra] = useState<{ items: Conflict[]; cursor?: string; loading: boolean; error?: string }>({ items: [], loading: false });
  const userError = draft.userId.trim() && !UUID.test(draft.userId.trim()) ? "Enter the user's UUID." : undefined;
  const state = useLoad((signal) => listConflicts(filter, signal), JSON.stringify(filter));
  const rows = [...(state.data?.items ?? []), ...extra.items];
  const cursor = extra.cursor ?? state.data?.nextCursor;

  async function more() {
    if (!cursor) return;
    setExtra((e) => ({ ...e, loading: true, error: undefined }));
    try {
      const page = await listConflicts({ ...filter, cursor });
      setExtra((e) => ({ items: [...e.items, ...page.items], cursor: page.nextCursor ?? "", loading: false }));
    } catch (error) {
      setExtra((e) => ({ ...e, loading: false, error: error instanceof Error ? error.message : "Request failed" }));
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="SoD conflicts"
        description="Users whose roles combine duties that must stay apart. Open a conflict to approve a compensating control or, for low and medium risks, accept the risk."
      />
      <form
        className={styles.toolbar}
        aria-label="Filter conflicts"
        onSubmit={(e) => {
          e.preventDefault();
          if (userError) return;
          setExtra({ items: [], loading: false });
          setFilter({ state: draft.state, severity: draft.severity, userId: draft.userId.trim().toLowerCase() });
        }}
      >
        <FormField id="conflict-state" label="State">
          {(control) => (
            <Select {...control} value={draft.state} onChange={(e) => setDraft({ ...draft, state: e.target.value as ConflictState | "" })}>
              <option value="">Any state</option>
              {CONFLICT_STATES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="conflict-severity" label="Severity">
          {(control) => (
            <Select {...control} value={draft.severity} onChange={(e) => setDraft({ ...draft, severity: e.target.value as Severity | "" })}>
              <option value="">Any severity</option>
              {SEVERITIES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="conflict-user" label="User" optional error={userError}>
          {(control) => <Input {...control} className={styles.mono} value={draft.userId} onChange={(e) => setDraft({ ...draft, userId: e.target.value })} invalid={Boolean(userError)} />}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load conflicts" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="SoD conflicts"
          columns={columns}
          rows={rows}
          getRowKey={(c) => c.conflictId}
          loading={state.loading}
          onRowOpen={(c) => navigate(`/compliance/sod/conflicts/${c.conflictId}`)}
          empty={{ title: "No conflicts match", description: "Conflicts appear here when a role assignment combines duties a rule keeps apart." }}
        />
      )}
      {extra.error ? (
        <Alert tone="danger" live="alert">
          {extra.error}
        </Alert>
      ) : null}
      {cursor && !state.loading && !state.error ? (
        <div className={styles.more}>
          <Button loading={extra.loading} onClick={() => void more()}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}
