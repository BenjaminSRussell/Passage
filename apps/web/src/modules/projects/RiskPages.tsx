import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createRisk,
  describeProblem,
  listRisks,
  newKey,
  RISK_KINDS,
  RISK_STATUSES,
  riskMoves,
  updateRisk,
  type Problem,
  type RiskInput,
  type RiskIssue,
  type RiskKind,
  type RiskStatus,
} from "./api/client";
import { dash, riskKindLabel, riskScoreTone, riskStatusLabel, riskStatusTone } from "./labels";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface RiskValues {
  kind: RiskKind;
  title: string;
  description: string;
  probability: string;
  impact: string;
  ownerWorkerId: string;
  mitigation: string;
  dueDate: string;
  status: RiskStatus;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const SCALE = ["1", "2", "3", "4", "5"];

const blank: RiskValues = {
  kind: "RISK",
  title: "",
  description: "",
  probability: "3",
  impact: "3",
  ownerWorkerId: "",
  mitigation: "",
  dueDate: "",
  status: "OPEN",
};

function valuesOf(r: RiskIssue): RiskValues {
  return {
    kind: r.kind,
    title: r.title,
    description: r.description,
    probability: String(r.probability),
    impact: String(r.impact),
    ownerWorkerId: r.ownerWorkerId ?? "",
    mitigation: r.mitigation,
    dueDate: r.dueDate ?? "",
    status: r.status,
  };
}

/** Checks that mirror domain.ValidateRiskIssue; keys are the service's field names. */
export function riskInput(v: RiskValues): { errors: Record<string, string>; input?: RiskInput } {
  const errors: Record<string, string> = {};
  const title = v.title.trim();
  if (title === "" || title.length > 120) errors.title = "Enter a title of up to 120 characters.";
  if (!SCALE.includes(v.probability)) errors.probability = "Choose 1 (rare) to 5 (almost certain).";
  if (!SCALE.includes(v.impact)) errors.impact = "Choose 1 (minor) to 5 (severe).";
  const owner = v.ownerWorkerId.trim();
  if (owner !== "" && !UUID.test(owner)) errors.ownerWorkerId = "Enter the owner's worker UUID, or leave it empty.";
  if (v.dueDate !== "" && !/^\d{4}-\d{2}-\d{2}$/.test(v.dueDate)) errors.dueDate = "Choose a valid date, or leave it empty.";
  if (v.description.length > 2000) errors.description = "Use at most 2000 characters.";
  if (v.mitigation.length > 2000) errors.mitigation = "Use at most 2000 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      kind: v.kind,
      title,
      probability: Number(v.probability),
      impact: Number(v.impact),
      ...(v.description.trim() ? { description: v.description } : {}),
      ...(v.mitigation.trim() ? { mitigation: v.mitigation } : {}),
      ...(owner ? { ownerWorkerId: owner } : {}),
      ...(v.dueDate ? { dueDate: v.dueDate } : {}),
    },
  };
}

function RiskForm({
  values,
  errors,
  onChange,
  statuses,
  idPrefix,
}: {
  values: RiskValues;
  errors: Record<string, string>;
  onChange(patch: Partial<RiskValues>): void;
  statuses?: RiskStatus[];
  idPrefix: string;
}) {
  const id = (name: string) => `${idPrefix}-${name}`;
  return (
    <div className={styles.stack}>
      <div className={styles.form}>
        <FormField id={id("kind")} label="Kind">
          {(control) => (
            <Select {...control} value={values.kind} onChange={(e) => onChange({ kind: e.target.value as RiskKind })}>
              {RISK_KINDS.map((k) => (
                <option key={k} value={k}>
                  {riskKindLabel[k]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id={id("title")} label="Title" error={errors.title}>
          {(control) => (
            <Input {...control} value={values.title} maxLength={120} invalid={Boolean(errors.title)} onChange={(e) => onChange({ title: e.target.value })} />
          )}
        </FormField>
        <FormField id={id("probability")} label="Probability" hint="1 rare to 5 almost certain" error={errors.probability}>
          {(control) => (
            <Select {...control} value={values.probability} invalid={Boolean(errors.probability)} onChange={(e) => onChange({ probability: e.target.value })}>
              {SCALE.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id={id("impact")} label="Impact" hint="1 minor to 5 severe" error={errors.impact}>
          {(control) => (
            <Select {...control} value={values.impact} invalid={Boolean(errors.impact)} onChange={(e) => onChange({ impact: e.target.value })}>
              {SCALE.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id={id("due")} label="Due date" error={errors.dueDate} optional>
          {(control) => (
            <Input {...control} type="date" value={values.dueDate} invalid={Boolean(errors.dueDate)} onChange={(e) => onChange({ dueDate: e.target.value })} />
          )}
        </FormField>
        <FormField id={id("owner")} label="Owner" hint="Worker UUID until worker records exist." error={errors.ownerWorkerId} optional>
          {(control) => (
            <Input
              {...control}
              className={styles.mono}
              value={values.ownerWorkerId}
              invalid={Boolean(errors.ownerWorkerId)}
              onChange={(e) => onChange({ ownerWorkerId: e.target.value })}
            />
          )}
        </FormField>
        {statuses ? (
          <FormField id={id("status")} label="Status" error={errors.status}>
            {(control) => (
              <Select {...control} value={values.status} onChange={(e) => onChange({ status: e.target.value as RiskStatus })}>
                {statuses.map((s) => (
                  <option key={s} value={s}>
                    {riskStatusLabel[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        ) : null}
      </div>
      <FormField id={id("mitigation")} label="Mitigation" error={errors.mitigation} optional>
        {(control) => (
          <Textarea
            {...control}
            rows={2}
            value={values.mitigation}
            invalid={Boolean(errors.mitigation)}
            onChange={(e) => onChange({ mitigation: e.target.value })}
          />
        )}
      </FormField>
      <FormField id={id("description")} label="Description" error={errors.description} optional>
        {(control) => (
          <Textarea
            {...control}
            rows={2}
            value={values.description}
            invalid={Boolean(errors.description)}
            onChange={(e) => onChange({ description: e.target.value })}
          />
        )}
      </FormField>
    </div>
  );
}

const failureTitle: Record<string, string> = {
  "PROJ-RISK-001": "That status change is not possible",
  conflict_version: "Someone else changed this entry",
  "AUTH-0003": "You can't change the register",
};

/** Risk and issue register of a project (FR-41.20): list by score, add, edit and move status. */
export function RisksPage() {
  const { id = "" } = useParams();
  const [kind, setKind] = useState<RiskKind | "">("");
  const [status, setStatus] = useState<RiskStatus | "">("");
  const state = useLoad((signal) => listRisks(id, { kind, status }, signal), `${id}|${kind}|${status}`);
  const [values, setValues] = useState<RiskValues>(blank);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [editing, setEditing] = useState<RiskIssue | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  if (state.error?.status === 404) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Risks and issues" />
        <Card>
          <EmptyState
            icon="grid"
            title="Project not found"
            description={`There is no project ${id} in your legal entities.`}
            action={
              <Link className={buttonClassName("secondary")} to="/projects/list">
                Back to projects
              </Link>
            }
          />
        </Card>
      </div>
    );
  }
  const items = state.data?.items ?? [];

  function change(patch: Partial<RiskValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }
  function startEdit(r: RiskIssue) {
    setEditing(r);
    setValues(valuesOf(r));
    setErrors({});
    setFailure(null);
    setSaved(null);
  }
  function cancel() {
    setEditing(null);
    setValues(blank);
    setErrors({});
    setFailure(null);
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = riskInput(values);
    setErrors(found);
    setFailure(null);
    setSaved(null);
    if (!input) return;
    setBusy(true);
    try {
      if (editing) {
        const out = await updateRisk(id, editing.id, { ...input, status: values.status }, editing.version);
        setSaved(`${riskKindLabel[out.kind]} "${out.title}" is saved.`);
      } else {
        const out = await createRisk(id, input, key.current);
        setSaved(`${riskKindLabel[out.kind]} "${out.title}" is added to the register.`);
      }
      setEditing(null);
      setValues(blank);
      key.current = newKey();
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<RiskIssue>[] = [
    { key: "title", header: "Title" },
    { key: "kind", header: "Kind", render: (r) => riskKindLabel[r.kind] },
    { key: "probability", header: "Probability", align: "end" },
    { key: "impact", header: "Impact", align: "end" },
    { key: "score", header: "Score", align: "end", render: (r) => <Badge tone={riskScoreTone(r.score)}>{r.score}</Badge> },
    { key: "status", header: "Status", render: (r) => <Badge tone={riskStatusTone[r.status]}>{riskStatusLabel[r.status]}</Badge> },
    { key: "dueDate", header: "Due", mono: true, render: (r) => r.dueDate ?? dash },
    {
      key: "edit",
      header: "Actions",
      render: (r) =>
        r.status === "CLOSED" ? (
          dash
        ) : (
          <Button size="sm" variant="secondary" onClick={() => startEdit(r)}>
            Edit {r.title}
          </Button>
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Risks and issues for ${id}`}
        description="Score is probability times impact (1 to 25); the highest scores come first. Closed entries are final."
        actions={
          <Link className={buttonClassName("secondary")} to={`/projects/${encodeURIComponent(id)}`}>
            Back to project
          </Link>
        }
      />
      <Alert tone="success" live="status">
        {saved}
      </Alert>
      <div className={styles.toolbar} role="group" aria-label="Filters">
        <FormField id="risk-filter-kind" label="Kind">
          {(control) => (
            <Select {...control} value={kind} onChange={(e) => setKind(e.target.value as RiskKind | "")}>
              <option value="">Risks and issues</option>
              {RISK_KINDS.map((k) => (
                <option key={k} value={k}>
                  {riskKindLabel[k]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="risk-filter-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as RiskStatus | "")}>
              <option value="">All statuses</option>
              {RISK_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {riskStatusLabel[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert
          tone="danger"
          title="Could not load the register"
          live="alert"
          action={
            <Button size="sm" onClick={state.reload}>
              Try again
            </Button>
          }
        >
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Risks and issues"
          columns={columns}
          rows={items}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={
            kind || status
              ? { title: "No entries match", description: "Clear a filter to see more." }
              : { title: "No risks or issues yet", description: "Add a risk with its probability and impact to start the register." }
          }
        />
      )}
      <Card title={editing ? `Edit ${editing.title}` : "Add a risk or issue"}>
        <form className={styles.stack} onSubmit={save} noValidate aria-label={editing ? "Edit risk or issue" : "New risk or issue"}>
          <RiskForm
            idPrefix={editing ? "risk-edit" : "risk-new"}
            values={values}
            errors={errors}
            onChange={change}
            statuses={editing ? [editing.status, ...riskMoves[editing.status]] : undefined}
          />
          {failure ? (
            <Alert tone="danger" title={failureTitle[failure.code] ?? "Could not save the entry"} live="alert">
              {failure.code === "conflict_version" ? "Cancel and edit the entry again to see the latest version." : <ProblemMessage problem={failure} />}
            </Alert>
          ) : null}
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy}>
              {editing ? "Save changes" : "Add to register"}
            </Button>
            {editing ? (
              <Button variant="ghost" onClick={cancel}>
                Cancel
              </Button>
            ) : null}
          </div>
        </form>
      </Card>
    </div>
  );
}
