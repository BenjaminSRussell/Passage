import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
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
  Skeleton,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  applyChangeOrder,
  CHANGE_ORDER_STATUSES,
  CHANGE_ORDER_TYPES,
  changeOrderActions,
  createChangeOrder,
  decideChangeOrder,
  describeProblem,
  getChangeOrder,
  getWbs,
  listChangeOrders,
  listPlans,
  newKey,
  type ApplyResult,
  type ChangeOrder,
  type ChangeOrderAction,
  type ChangeOrderLine,
  type ChangeOrderStatus,
  type ChangeOrderType,
  type NewChangeOrder,
  type Problem,
  type WbsNode,
} from "./api/client";
import { changeOrderStatusLabel, changeOrderStatusTone, changeOrderTypeLabel, dash } from "./labels";
import plan from "./plans.module.css";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const projectPath = (id: string) => `/projects/${encodeURIComponent(id)}`;
const changeOrdersPath = (id: string) => `${projectPath(id)}/change-orders`;

export function ChangeOrderBadge({ status }: { status: ChangeOrderStatus }) {
  return <Badge tone={changeOrderStatusTone[status]}>{changeOrderStatusLabel[status]}</Badge>;
}

function NotFound({ title, text, back, backLabel }: { title: string; text: string; back: string; backLabel: string }) {
  return (
    <div className={styles.stack}>
      <PageHeader title={title} />
      <Card>
        <EmptyState
          icon="grid"
          title={`${title} not found`}
          description={text}
          action={
            <Link className={buttonClassName("secondary")} to={back}>
              {backLabel}
            </Link>
          }
        />
      </Card>
    </div>
  );
}

/** Change order log of a project (§8.41.14): number, type, status, cost and revenue impact, applied baseline. */
export function ChangeOrdersPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [type, setType] = useState<ChangeOrderType | "">("");
  const [status, setStatus] = useState<ChangeOrderStatus | "">("");
  const state = useLoad((signal) => listChangeOrders(id, { type, status }, signal), `${id}|${type}|${status}`);

  if (state.error?.status === 404) {
    return <NotFound title="Project" text={`There is no project ${id} in your legal entities.`} back="/projects/list" backLabel="Back to projects" />;
  }
  const items = state.data?.items ?? [];
  const filtered = type !== "" || status !== "";
  const columns: DataTableColumn<ChangeOrder>[] = [
    { key: "number", header: "Number", mono: true },
    { key: "title", header: "Title" },
    { key: "type", header: "Type", render: (c) => changeOrderTypeLabel[c.type] },
    { key: "status", header: "Status", render: (c) => <ChangeOrderBadge status={c.status} /> },
    { key: "costImpact", header: "Cost impact", align: "end", mono: true },
    { key: "revenueImpact", header: "Revenue impact", align: "end", mono: true },
    { key: "appliedBaselineVersionId", header: "Applied baseline", render: (c) => (c.appliedBaselineVersionId ? "Yes" : dash) },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Change orders for ${id}`}
        description="Scope changes and claims move from draft to submitted, then someone other than the author approves or rejects them. Applying an approved change order raises the contract value and creates a new baseline."
        actions={
          <>
            <Link className={buttonClassName("primary")} to={`${changeOrdersPath(id)}/new`}>
              New change order
            </Link>
            <Link className={buttonClassName("secondary")} to={projectPath(id)}>
              Back to project
            </Link>
          </>
        }
      />
      <div className={styles.toolbar} role="group" aria-label="Filters">
        <FormField id="co-filter-type" label="Type">
          {(control) => (
            <Select {...control} value={type} onChange={(e) => setType(e.target.value as ChangeOrderType | "")}>
              <option value="">All types</option>
              {CHANGE_ORDER_TYPES.map((t) => (
                <option key={t} value={t}>
                  {changeOrderTypeLabel[t]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="co-filter-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as ChangeOrderStatus | "")}>
              <option value="">All statuses</option>
              {CHANGE_ORDER_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {changeOrderStatusLabel[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert
          tone="danger"
          title="Could not load the change orders"
          live="alert"
          action={
            <Button size="sm" onClick={state.reload}>
              Try again
            </Button>
          }
        >
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : !state.loading && items.length === 0 && !filtered ? (
        <Card>
          <EmptyState
            icon="grid"
            title="No change orders yet"
            description="Record a scope change or claim with its cost and revenue impact on the WBS."
            action={
              <Link className={buttonClassName("primary")} to={`${changeOrdersPath(id)}/new`}>
                New change order
              </Link>
            }
          />
        </Card>
      ) : (
        <DataTable
          label="Change orders"
          columns={columns}
          rows={items}
          getRowKey={(c) => c.id}
          loading={state.loading}
          empty={{ title: "No change orders match", description: "Clear a filter to see more." }}
          onRowOpen={(c) => navigate(`${changeOrdersPath(id)}/${encodeURIComponent(c.id)}`)}
        />
      )}
    </div>
  );
}

export interface LineRow {
  key: string;
  wbsNodeId: string;
  costImpact: string;
  revenueImpact: string;
  effortHoursDelta: string;
  scheduleDaysDelta: string;
}

export interface ChangeOrderValues {
  type: ChangeOrderType;
  title: string;
  description: string;
  customerApprovalRef: string;
  vendorPartyId: string;
  lines: LineRow[];
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const AMOUNT = /^-?\d{1,14}(\.\d{1,6})?$/;
const EFFORT = /^-?\d{1,10}(\.\d{1,2})?$/;
const DAYS = /^-?\d{1,6}$/;

let lineSeq = 0;
export const emptyLine = (): LineRow => ({
  key: `line-${++lineSeq}`,
  wbsNodeId: "",
  costImpact: "0",
  revenueImpact: "0",
  effortHoursDelta: "0",
  scheduleDaysDelta: "0",
});

/**
 * Checks that mirror domain.ValidateChangeOrder; keys are the service's field
 * names, so server field errors land on the same inputs.
 */
export function changeOrderInput(projectId: string, v: ChangeOrderValues): { errors: Record<string, string>; input?: NewChangeOrder } {
  const errors: Record<string, string> = {};
  const title = v.title.trim();
  if (title === "" || title.length > 120) errors.title = "Enter a title of up to 120 characters.";
  if (v.description.length > 2000) errors.description = "Use at most 2000 characters.";
  const vendor = v.vendorPartyId.trim();
  if (v.type === "CLAIM_VENDOR" && !UUID.test(vendor)) errors.vendorPartyId = "A vendor claim needs the vendor's UUID.";
  const ref = v.customerApprovalRef.trim();
  if (ref.length > 200) errors.customerApprovalRef = "Use at most 200 characters.";
  if (v.lines.length === 0) errors.lines = "Add at least one impact line.";
  const lines = v.lines.map((l, i) => {
    const cost = l.costImpact.trim();
    const revenue = l.revenueImpact.trim();
    const effort = l.effortHoursDelta.trim() || "0";
    const days = l.scheduleDaysDelta.trim() || "0";
    if (!AMOUNT.test(cost)) errors[`lines[${i}].costImpact`] = "Enter an amount such as -1250.00.";
    if (!AMOUNT.test(revenue)) errors[`lines[${i}].revenueImpact`] = "Enter an amount such as 50000.00.";
    if (!EFFORT.test(effort)) errors[`lines[${i}].effortHoursDelta`] = "Enter hours with at most 2 decimals.";
    if (!DAYS.test(days)) errors[`lines[${i}].scheduleDaysDelta`] = "Enter whole working days.";
    const changesPlan = (EFFORT.test(effort) && Number(effort) !== 0) || (DAYS.test(days) && Number(days) !== 0);
    if (changesPlan && l.wbsNodeId === "") errors[`lines[${i}].wbsNodeId`] = "Pick the WBS task whose effort or schedule changes.";
    return {
      ...(l.wbsNodeId ? { wbsNodeId: l.wbsNodeId } : {}),
      costImpact: cost,
      revenueImpact: revenue,
      effortHoursDelta: effort,
      scheduleDaysDelta: Number(days),
    };
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      projectId,
      type: v.type,
      title,
      lines,
      ...(v.description.trim() ? { description: v.description } : {}),
      ...(ref ? { customerApprovalRef: ref } : {}),
      ...(v.type === "CLAIM_VENDOR" ? { vendorPartyId: vendor } : {}),
    },
  };
}

/** Loads the WBS the lines can name: the current baseline when there is one, else the working plan. */
async function loadTargetWbs(projectId: string, signal: AbortSignal): Promise<{ nodes: WbsNode[]; source: string }> {
  const plans = (await listPlans(projectId, signal)).items;
  const target = plans.find((p) => p.kind === "BASELINE" && !p.supersededByVersionId) ?? plans.find((p) => p.kind === "WORKING");
  if (!target) return { nodes: [], source: "" };
  const view = await getWbs(projectId, target.id, signal);
  return { nodes: view.nodes, source: target.kind === "BASELINE" ? "current baseline" : "working plan" };
}

const failureTitle: Record<string, string> = {
  "PROJ-NOT-FOUND": "The project was not found",
  "AUTH-0003": "You can't create change orders here",
  "AUTH-0001": "Sign in again to record change orders",
};

/** Change order entry (FR-41.18): header and impact lines on WBS tasks; saved as a draft. */
export function NewChangeOrderPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const wbs = useLoad((signal) => loadTargetWbs(id, signal), id);
  const [values, setValues] = useState<ChangeOrderValues>({
    type: "SCOPE_CHANGE",
    title: "",
    description: "",
    customerApprovalRef: "",
    vendorPartyId: "",
    lines: [emptyLine()],
  });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ChangeOrderValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }
  function editLine(lineKey: string, patch: Partial<LineRow>) {
    update({ lines: values.lines.map((l) => (l.key === lineKey ? { ...l, ...patch } : l)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = changeOrderInput(id, values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createChangeOrder(input, key.current);
      navigate(`${changeOrdersPath(id)}/${encodeURIComponent(created.id)}`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const nodes = wbs.data?.nodes ?? [];
  const lineError = (field: string) => (errors[field] ? <p className={plan.rowError}>{errors[field]}</p> : null);
  const amountCell = (l: LineRow, i: number, field: "costImpact" | "revenueImpact" | "effortHoursDelta" | "scheduleDaysDelta", label: string) => (
    <td className={plan.cell}>
      <Input
        aria-label={`${label} of line ${i + 1}`}
        className={`${plan.number} ${styles.mono}`}
        inputMode={field === "scheduleDaysDelta" ? "numeric" : "decimal"}
        value={l[field]}
        invalid={Boolean(errors[`lines[${i}].${field}`])}
        onChange={(e) => editLine(l.key, { [field]: e.target.value })}
      />
      {lineError(`lines[${i}].${field}`)}
    </td>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`New change order for ${id}`}
        description="The change order is saved as a draft. Submit it from its page when the impact is complete."
      />
      <form className={styles.stack} onSubmit={submit} noValidate aria-label="New change order">
        <Card title="Change order">
          <div className={styles.stack}>
            <div className={styles.form}>
              <FormField id="co-type" label="Type">
                {(control) => (
                  <Select {...control} value={values.type} onChange={(e) => update({ type: e.target.value as ChangeOrderType })}>
                    {CHANGE_ORDER_TYPES.map((t) => (
                      <option key={t} value={t}>
                        {changeOrderTypeLabel[t]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="co-title" label="Title" error={errors.title}>
                {(control) => (
                  <Input
                    {...control}
                    value={values.title}
                    maxLength={120}
                    invalid={Boolean(errors.title)}
                    onChange={(e) => update({ title: e.target.value })}
                  />
                )}
              </FormField>
              {values.type === "CLAIM_VENDOR" ? (
                <FormField id="co-vendor" label="Vendor" hint="UUID of the vendor party the claim is against." error={errors.vendorPartyId}>
                  {(control) => (
                    <Input
                      {...control}
                      className={styles.mono}
                      value={values.vendorPartyId}
                      invalid={Boolean(errors.vendorPartyId)}
                      onChange={(e) => update({ vendorPartyId: e.target.value })}
                    />
                  )}
                </FormField>
              ) : null}
              <FormField
                id="co-approval"
                label="Customer approval reference"
                hint={
                  values.type === "CLAIM_CUSTOMER"
                    ? "Needed before a customer claim can be approved. You can also add it when submitting or approving."
                    : "Signed letter, email or e-signature reference."
                }
                error={errors.customerApprovalRef}
                optional
              >
                {(control) => (
                  <Input
                    {...control}
                    value={values.customerApprovalRef}
                    invalid={Boolean(errors.customerApprovalRef)}
                    onChange={(e) => update({ customerApprovalRef: e.target.value })}
                  />
                )}
              </FormField>
            </div>
            <FormField id="co-description" label="Description" error={errors.description} optional>
              {(control) => (
                <Textarea
                  {...control}
                  rows={3}
                  value={values.description}
                  invalid={Boolean(errors.description)}
                  onChange={(e) => update({ description: e.target.value })}
                />
              )}
            </FormField>
          </div>
        </Card>
        <Card title="Impact lines">
          <div className={styles.stack}>
            {wbs.error ? (
              <Alert
                tone="warning"
                title="Could not load the WBS"
                live="alert"
                action={
                  <Button size="sm" onClick={wbs.reload}>
                    Try again
                  </Button>
                }
              >
                Lines can still be saved for the whole project. {wbs.error.message} ({wbs.error.code})
              </Alert>
            ) : wbs.loading ? (
              <Skeleton height="tile" />
            ) : nodes.length === 0 ? (
              <p className={styles.note}>The project has no WBS yet, so lines apply to the whole project and cannot change effort or schedule.</p>
            ) : (
              <p className={styles.note}>
                Tasks come from the {wbs.data?.source}. Applying the change order adds the effort and schedule deltas to the same task in a new baseline.
              </p>
            )}
            {errors.lines ? (
              <Alert tone="danger" live="alert">
                {errors.lines}
              </Alert>
            ) : null}
            <div className={plan.scroll}>
              <table className={plan.grid} aria-label="Impact lines">
                <thead>
                  <tr>
                    <th scope="col" className={plan.head}>
                      WBS task
                    </th>
                    <th scope="col" className={plan.head}>
                      Cost impact
                    </th>
                    <th scope="col" className={plan.head}>
                      Revenue impact
                    </th>
                    <th scope="col" className={plan.head}>
                      Effort (h)
                    </th>
                    <th scope="col" className={plan.head}>
                      Schedule (days)
                    </th>
                    <th scope="col" className={plan.head}>
                      <span className="visually-hidden">Remove</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {values.lines.map((l, i) => (
                    <tr key={l.key}>
                      <td className={plan.cell}>
                        <Select
                          aria-label={`WBS task of line ${i + 1}`}
                          value={l.wbsNodeId}
                          invalid={Boolean(errors[`lines[${i}].wbsNodeId`])}
                          onChange={(e) => editLine(l.key, { wbsNodeId: e.target.value })}
                        >
                          <option value="">Whole project</option>
                          {nodes.map((n) => (
                            <option key={n.id} value={n.id}>
                              {n.wbsCode} {n.name}
                            </option>
                          ))}
                        </Select>
                        {lineError(`lines[${i}].wbsNodeId`)}
                      </td>
                      {amountCell(l, i, "costImpact", "Cost impact")}
                      {amountCell(l, i, "revenueImpact", "Revenue impact")}
                      {amountCell(l, i, "effortHoursDelta", "Effort change")}
                      {amountCell(l, i, "scheduleDaysDelta", "Schedule change")}
                      <td className={plan.cell}>
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={values.lines.length === 1}
                          onClick={() => update({ lines: values.lines.filter((x) => x.key !== l.key) })}
                        >
                          Remove line {i + 1}
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className={plan.toolbar}>
              <Button variant="secondary" onClick={() => update({ lines: [...values.lines, emptyLine()] })}>
                Add line
              </Button>
            </div>
          </div>
        </Card>
        {failure ? (
          <Alert tone="danger" title={failureTitle[failure.code] ?? "Could not save the change order"} live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save draft
          </Button>
          <Link className={buttonClassName("ghost")} to={changeOrdersPath(id)}>
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

const actionText: Record<ChangeOrderAction, string> = {
  submit: "Submit for approval",
  approve: "Approve",
  reject: "Reject",
  apply: "Apply to project",
};

const actionDone: Record<ChangeOrderAction, string> = {
  submit: "submitted for approval",
  approve: "approved",
  reject: "rejected",
  apply: "applied",
};

const decisionTitle: Record<string, string> = {
  "PROJ-CO-001": "This change order is already applied",
  "PROJ-CO-002": "Customer approval is missing",
  "PROJ-CO-003": "That step is not possible now",
  "PROJ-CO-004": "Someone else must decide",
  "PROJ-CO-005": "The project has no baseline",
  "PROJ-CO-006": "A line cannot be applied",
  "AUTH-0003": "You don't have the duty for this step",
};

/** Change order detail: header, impact lines and the lifecycle actions (submit, approve, reject, apply). */
export function ChangeOrderDetailPage() {
  const { id = "", coId = "" } = useParams();
  const state = useLoad((signal) => getChangeOrder(coId, signal), coId);
  const [latest, setLatest] = useState<ChangeOrder | null>(null);
  const [busy, setBusy] = useState<ChangeOrderAction | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [applied, setApplied] = useState<ApplyResult | null>(null);
  const [approvalRef, setApprovalRef] = useState("");

  const co = latest ?? state.data;
  if (state.error && !co) {
    if (state.error.status === 404) {
      return (
        <NotFound
          title="Change order"
          text="There is no such change order in your legal entities."
          back={changeOrdersPath(id)}
          backLabel="Back to change orders"
        />
      );
    }
    return (
      <div className={styles.stack}>
        <PageHeader title="Change order" />
        <Alert
          tone="danger"
          title="Could not load the change order"
          live="alert"
          action={
            <Button size="sm" onClick={state.reload}>
              Try again
            </Button>
          }
        >
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!co) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Change order" />
        <span className="visually-hidden">Loading the change order…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const current = co;

  async function act(action: ChangeOrderAction) {
    setBusy(action);
    setFailure(null);
    setDone(null);
    try {
      if (action === "apply") {
        const result = await applyChangeOrder(current.id, current.version);
        setApplied(result);
        setLatest(result.changeOrder);
      } else {
        setLatest(await decideChangeOrder(current.id, action, current.version, approvalRef.trim() || undefined));
      }
      setDone(`Change order ${current.number} is ${actionDone[action]}.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  function reload() {
    setLatest(null);
    setFailure(null);
    state.reload();
  }

  const stale = failure?.code === "conflict_version";
  const actions = changeOrderActions[current.status];
  const canAttach = (current.status === "DRAFT" || current.status === "SUBMITTED") && !current.customerApprovalRef;
  const facts: [string, ReactNode, boolean?][] = [
    ["Type", changeOrderTypeLabel[current.type]],
    ["Project", <Link to={projectPath(current.projectId)}>{current.projectId}</Link>, true],
    ["Cost impact", current.costImpact, true],
    ["Revenue impact", current.revenueImpact, true],
    ["Customer approval", current.customerApprovalRef ?? dash],
    ["Vendor", current.vendorPartyId ?? dash, true],
    ["Created by", current.createdBy, true],
    ["Decided by", current.decidedBy ?? dash, true],
    ["Applied", current.appliedAt ? `${current.appliedAt.slice(0, 10)} by ${current.appliedBy ?? dash}` : dash],
    ["Version", current.version],
  ];
  const lineColumns: DataTableColumn<ChangeOrderLine>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "wbsCode", header: "WBS", mono: true, render: (l) => l.wbsCode ?? "Whole project" },
    { key: "costImpact", header: "Cost impact", align: "end", mono: true },
    { key: "revenueImpact", header: "Revenue impact", align: "end", mono: true },
    { key: "effortHoursDelta", header: "Effort (h)", align: "end", mono: true },
    { key: "scheduleDaysDelta", header: "Schedule (days)", align: "end", mono: true },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Change order ${current.number}`}
        description={current.title}
        meta={<ChangeOrderBadge status={current.status} />}
        actions={
          <Link className={buttonClassName("secondary")} to={changeOrdersPath(id || current.projectId)}>
            Back to change orders
          </Link>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {applied ? (
        <Alert tone="info" title="Applied to the project" live="status">
          The contract value is now {applied.effect.contractValue} ({applied.effect.contractValueDelta} added), the budget changes by{" "}
          {applied.effect.budgetDelta}, and baseline plan {applied.baseline.sequence} replaces the previous baseline.{" "}
          <Link to={`${projectPath(current.projectId)}/plans/${encodeURIComponent(applied.baseline.id)}`}>Open the new baseline</Link>
        </Alert>
      ) : null}
      {failure ? (
        <Alert
          tone="danger"
          title={stale ? "Someone else changed this change order" : (decisionTitle[failure.code] ?? "Could not complete the step")}
          live="alert"
          action={
            stale ? (
              <Button size="sm" onClick={reload}>
                Reload change order
              </Button>
            ) : undefined
          }
        >
          {stale ? "Reload to see the latest version, then try again." : <ProblemMessage problem={failure} />}
        </Alert>
      ) : null}
      <Card title="Header">
        <dl className={styles.details}>
          {facts.map(([term, value, mono]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
        {current.description ? <p className={styles.note}>{current.description}</p> : null}
      </Card>
      <DataTable label="Impact lines" columns={lineColumns} rows={current.lines} getRowKey={(l) => String(l.lineNo)} empty={{ title: "No impact lines" }} />
      <Card title="Next step">
        {actions.length === 0 ? (
          <p className={styles.note}>
            {current.status === "APPLIED"
              ? "This change order is applied and can no longer change."
              : "This change order was rejected and can no longer change."}
          </p>
        ) : (
          <div className={styles.stack}>
            <p className={styles.note}>
              {current.status === "APPROVED"
                ? "Applying adds the revenue impact to the contract value and creates a new baseline with the effort and schedule changes."
                : "The author submits; a project controller or account executive approves or rejects. The author cannot decide their own change order."}
            </p>
            {canAttach ? (
              <FormField
                id="co-approval-ref"
                label="Customer approval reference"
                hint={current.type === "CLAIM_CUSTOMER" ? "A customer claim needs it before approval." : "Attach the customer's approval evidence, if any."}
                optional={current.type !== "CLAIM_CUSTOMER"}
              >
                {(control) => <Input {...control} value={approvalRef} maxLength={200} onChange={(e) => setApprovalRef(e.target.value)} />}
              </FormField>
            ) : null}
            <div className={styles.actions} role="group" aria-label="Change order actions">
              {actions.map((a) => (
                <Button
                  key={a}
                  variant={a === "reject" ? "danger" : "primary"}
                  loading={busy === a}
                  disabled={busy !== null && busy !== a}
                  onClick={() => void act(a)}
                >
                  {actionText[a]}
                </Button>
              ))}
            </div>
          </div>
        )}
      </Card>
    </div>
  );
}
