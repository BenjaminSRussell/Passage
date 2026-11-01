import { useRef, useState, type FormEvent, type ReactNode } from "react";
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
  Skeleton,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  addCampaignOrder,
  allocateCampaignCost,
  cancelCampaign,
  completeCampaign,
  ALLOCATION_BASES,
  describeProblem,
  getCampaign,
  listResources,
  newKey,
  removeCampaignOrder,
  SEQUENCE_RULE_LABELS,
  sequenceCampaign,
  startCampaign,
  type Allocation,
  type AllocationBasis,
  type Campaign,
  type CampaignOrder,
  type Changeover,
  type CleanoutCounter,
  type NewCampaignOrder,
  type PlannedCleanout,
  type Problem,
} from "./api/client";
import { campaignTone } from "./CampaignsPage";
import { dash, formatDateTime, formatMinutes, fromUtcInput, sentence, shortId, UUID } from "./format";
import { CODE, CURRENCY, decimalText, mapFields, positive, wholeNumber, WEEK_MINUTES } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface Detail {
  campaign: Campaign;
  codes: Record<string, string>;
}

/** Campaigns accept orders and resequencing until they start (FR-23.14). */
export function editable(c: Campaign): boolean {
  return c.status === "proposed" || c.status === "planned";
}

export interface OrderValues {
  orderId: string;
  setupGroup: string;
  quantity: string;
  runMinutes: string;
  routeSetupMinutes: string;
  dueAt: string;
}

type OrderErrors = Partial<Record<keyof OrderValues, string>>;

const emptyOrder: OrderValues = { orderId: "", setupGroup: "", quantity: "", runMinutes: "", routeSetupMinutes: "0", dueAt: "" };

/** Checks that mirror the service for one campaign order. */
export function validateCampaignOrder(values: OrderValues): { errors: OrderErrors; input?: NewCampaignOrder } {
  const errors: OrderErrors = {};
  const orderId = values.orderId.trim().toLowerCase();
  if (!UUID.test(orderId)) errors.orderId = "Enter the production order id (a UUID).";
  const group = values.setupGroup.trim();
  if (!CODE.test(group)) errors.setupGroup = "Use 1 to 32 capital letters, digits, _ or -.";
  const quantity = decimalText(values.quantity, 6);
  if (quantity === null || !positive(quantity)) errors.quantity = "Enter a quantity above zero with up to 6 decimals.";
  const runMinutes = wholeNumber(values.runMinutes, 0, 100_000);
  if (runMinutes === null) errors.runMinutes = "Enter 0 to 100,000 minutes.";
  const routeSetup = wholeNumber(values.routeSetupMinutes, 0, WEEK_MINUTES);
  if (routeSetup === null) errors.routeSetupMinutes = `Enter 0 to ${WEEK_MINUTES.toLocaleString("en-US")} minutes.`;
  const dueAt = values.dueAt === "" ? undefined : fromUtcInput(values.dueAt);
  if (dueAt === null) errors.dueAt = "Enter a date and time.";
  if (Object.keys(errors).length > 0 || quantity === null || runMinutes === null || routeSetup === null) return { errors };
  const input: NewCampaignOrder = { orderId, setupGroup: group, quantity, runMinutes, routeSetupMinutes: routeSetup };
  if (dueAt) input.dueAt = dueAt;
  return { errors, input };
}

const orderFields: Record<string, keyof OrderValues> = {
  orderId: "orderId",
  setupGroup: "setupGroup",
  quantity: "quantity",
  runMinutes: "runMinutes",
  routeSetupMinutes: "routeSetupMinutes",
};

const cleanoutReasons: Record<PlannedCleanout["reason"], string> = {
  batches: "Batch limit reached",
  "run-time": "Run-time limit reached",
  "campaign-end": "Campaign end",
};

/** One campaign: its orders, the sequenced plan, commands and cost allocation. */
export function CampaignDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad<Detail>(async (signal) => {
    const [campaign, resources] = await Promise.all([getCampaign(id, signal), listResources(signal)]);
    return { campaign, codes: Object.fromEntries(resources.map((r) => [r.id, r.code])) };
  }, id);
  const [updated, setUpdated] = useState<Campaign | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [cancelling, setCancelling] = useState(false);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Campaign" />
        <Alert tone="danger" title="Could not load the campaign" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Campaign" />
        <span className="visually-hidden">Loading the campaign…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  const campaign = updated && updated.id === state.data.campaign.id && updated.version >= state.data.campaign.version ? updated : state.data.campaign;
  const codes = state.data.codes;
  const orders = campaign.orders ?? [];
  const open = editable(campaign);
  const cancellable = campaign.status !== "completed" && campaign.status !== "cancelled";

  /** Runs a campaign command; resolves to the problem, or null when it succeeded. */
  async function run(name: string, command: () => Promise<Campaign>, done: string): Promise<Problem | null> {
    setBusy(name);
    setFailure(null);
    setNotice(null);
    try {
      setUpdated(await command());
      setNotice(done);
      return null;
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      return problem;
    } finally {
      setBusy(null);
    }
  }

  function reload() {
    setUpdated(null);
    setFailure(null);
    state.reload();
  }

  const orderColumns: DataTableColumn<CampaignOrder>[] = [
    { key: "sequence", header: "Run order", align: "end", render: (o) => (o.sequence === null ? dash : String(o.sequence)) },
    { key: "orderId", header: "Order", mono: true, render: (o) => shortId(o.orderId) },
    { key: "setupGroup", header: "Setup group", mono: true },
    { key: "quantity", header: "Quantity", align: "end" },
    { key: "runMinutes", header: "Run time", align: "end", render: (o) => formatMinutes(o.runMinutes) },
    { key: "routeSetupMinutes", header: "Route setup", align: "end", render: (o) => formatMinutes(o.routeSetupMinutes) },
    { key: "dueAt", header: "Due", render: (o) => formatDateTime(o.dueAt) },
  ];
  if (open) {
    orderColumns.push({
      key: "remove",
      header: "Actions",
      render: (o) => (
        <Button
          size="sm"
          variant="ghost"
          loading={busy === `remove-${o.orderId}`}
          disabled={busy !== null}
          aria-label={`Remove order ${o.orderId}`}
          onClick={() => run(`remove-${o.orderId}`, () => removeCampaignOrder(campaign.id, campaign.version, o.orderId), "Order removed. Sequence the campaign again.")}
        >
          Remove
        </Button>
      ),
    });
  }

  const limits = [
    campaign.maxOrders !== null ? `${campaign.maxOrders} orders` : null,
    campaign.maxQty !== null ? `${campaign.maxQty} units` : null,
    campaign.maxMinutes !== null ? formatMinutes(campaign.maxMinutes) : null,
  ].filter(Boolean);
  const facts: [string, ReactNode][] = [
    ["Resource", codes[campaign.resourceId] ?? shortId(campaign.resourceId)],
    ["Planned start", formatDateTime(campaign.plannedStart)],
    ["Sequence rule", SEQUENCE_RULE_LABELS[campaign.sequenceRule] ?? sentence(campaign.sequenceRule)],
    ["Current setup group", campaign.currentSetupGroup ?? dash],
    ["Limits", limits.length > 0 ? limits.join(" · ") : "None"],
    ["Run time", formatMinutes(campaign.runMinutes)],
    ["Changeover", formatMinutes(campaign.changeoverMinutes)],
    ["Cleanout", formatMinutes(campaign.cleanoutMinutes)],
    ["Sequenced", formatDateTime(campaign.sequencedAt)],
    ["Started", formatDateTime(campaign.startedAt)],
    ...(campaign.status === "completed" ? ([["Completed", formatDateTime(campaign.completedAt ?? null)]] as [string, ReactNode][]) : []),
    ...(campaign.status === "cancelled"
      ? ([
          ["Cancelled", formatDateTime(campaign.cancelledAt ?? null)],
          ["Cancel reason", campaign.cancelReason ?? dash],
        ] as [string, ReactNode][])
      : []),
    ["Version", String(campaign.version)],
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={campaign.campaignKey}
        description="Orders run back to back on the campaign resource, with changeovers between setup groups and cleanouts where the rules demand them."
        meta={<Badge tone={campaignTone[campaign.status] ?? "neutral"}>{sentence(campaign.status)}</Badge>}
        actions={
          <div className={styles.actions}>
            {open ? (
              <Button
                variant={campaign.status === "proposed" ? "primary" : "secondary"}
                loading={busy === "sequence"}
                disabled={busy !== null || orders.length === 0}
                onClick={() => run("sequence", () => sequenceCampaign(campaign.id, campaign.version), "Campaign sequenced.")}
              >
                Sequence
              </Button>
            ) : null}
            {campaign.status === "planned" ? (
              <Button
                variant="primary"
                loading={busy === "start"}
                disabled={busy !== null}
                onClick={() => run("start", () => startCampaign(campaign.id, campaign.version), "Campaign started. New orders go to the next campaign.")}
              >
                Start
              </Button>
            ) : null}
            {campaign.status === "started" ? (
              <Button
                variant="primary"
                loading={busy === "complete"}
                disabled={busy !== null}
                onClick={() => run("complete", () => completeCampaign(campaign.id, campaign.version), "Campaign completed.")}
              >
                Complete
              </Button>
            ) : null}
            {cancellable && !cancelling ? (
              <Button variant="danger" disabled={busy !== null} onClick={() => setCancelling(true)}>
                Cancel campaign
              </Button>
            ) : null}
            <Link className={buttonClassName("ghost")} to="/scheduling/campaigns">
              All campaigns
            </Link>
          </div>
        }
      />
      {failure ? (
        <Alert
          tone="danger"
          title={failureTitles[failure.code] ?? "The command failed"}
          live="alert"
          action={failure.code === "SCHED-0115" ? <Button size="sm" onClick={reload}>Reload</Button> : undefined}
        >
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {notice ? (
        <Alert tone="success" title={notice} live="status">
          {`Version ${campaign.version}.`}
        </Alert>
      ) : null}
      {cancellable && cancelling ? (
        <CancelCampaignForm
          started={campaign.status === "started"}
          busy={busy === "cancel"}
          onKeep={() => setCancelling(false)}
          onCancel={async (reason) => {
            const problem = await run("cancel", () => cancelCampaign(campaign.id, campaign.version, reason), "Campaign cancelled. Its orders are free again.");
            if (problem === null) setCancelling(false);
            return problem;
          }}
        />
      ) : null}
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card title="Orders">
        <div className={styles.stack}>
          {!open ? (
            <p className={styles.note}>
              {campaign.status === "cancelled"
                ? "The campaign is cancelled; its orders were released and can join another campaign."
                : `The campaign is ${campaign.status}; orders can no longer join or leave it (SCHED-0011).`}
            </p>
          ) : null}
          <DataTable
            label="Campaign orders"
            columns={orderColumns}
            rows={orders}
            getRowKey={(o) => o.orderId}
            empty={
              campaign.status === "cancelled"
                ? { title: "No orders", description: "Cancelling released this campaign's orders." }
                : { title: "No orders yet", description: "Add the production orders that share this campaign's setup family." }
            }
          />
          {open ? (
            <AddOrderForm
              disabled={busy !== null}
              onAdd={(order, key) => run("add", () => addCampaignOrder(campaign.id, campaign.version, order, key), "Order added. Sequence the campaign again.")}
            />
          ) : null}
        </div>
      </Card>
      <PlanCard campaign={campaign} />
      <AllocationCard campaign={campaign} />
    </div>
  );
}

function AddOrderForm({ disabled, onAdd }: { disabled: boolean; onAdd(order: NewCampaignOrder, key: string): Promise<Problem | null> }) {
  const [values, setValues] = useState<OrderValues>(emptyOrder);
  const [errors, setErrors] = useState<OrderErrors>({});
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<OrderValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCampaignOrder(values);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    try {
      const problem = await onAdd(input, key.current);
      if (problem === null) {
        setValues(emptyOrder);
        key.current = newKey();
      } else {
        setErrors(mapFields(problem.fields, orderFields));
        if (problem.code === "SCHED-0114") setErrors({ orderId: "This order already belongs to a campaign." });
        if (problem.code === "idempotency_key_reuse") key.current = newKey();
      }
    } finally {
      setBusy(false);
    }
  }

  const text = (name: Exclude<keyof OrderValues, "dueAt">, props: { inputMode?: "numeric" | "decimal"; upper?: boolean } = {}) =>
    function Control(control: { id: string }) {
      return (
        <Input
          {...control}
          inputMode={props.inputMode}
          value={values[name]}
          onChange={(event) => update({ [name]: props.upper ? event.target.value.toUpperCase() : event.target.value })}
          invalid={Boolean(errors[name])}
        />
      );
    };

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Add an order">
      <FormField id="campaign-order-id" label="Order id" error={errors.orderId}>
        {text("orderId")}
      </FormField>
      <FormField id="campaign-order-group" label="Setup group" hint="From the product or formula." error={errors.setupGroup}>
        {text("setupGroup", { upper: true })}
      </FormField>
      <FormField id="campaign-order-qty" label="Quantity" error={errors.quantity}>
        {text("quantity", { inputMode: "decimal" })}
      </FormField>
      <FormField id="campaign-order-run" label="Run minutes" error={errors.runMinutes}>
        {text("runMinutes", { inputMode: "numeric" })}
      </FormField>
      <FormField id="campaign-order-setup" label="Route setup minutes" hint="Used when the matrix has no entry for the pair (SCHED-0012)." error={errors.routeSetupMinutes}>
        {text("routeSetupMinutes", { inputMode: "numeric" })}
      </FormField>
      <FormField id="campaign-order-due" label="Due (UTC)" optional hint="Breaks changeover ties." error={errors.dueAt}>
        {(control) => <Input {...control} type="datetime-local" value={values.dueAt} onChange={(event) => update({ dueAt: event.target.value })} invalid={Boolean(errors.dueAt)} />}
      </FormField>
      <div className={styles.formActions}>
        <Button type="submit" variant="secondary" loading={busy} disabled={disabled && !busy}>
          Add order
        </Button>
      </div>
    </form>
  );
}

/** The longest cancellation reason the service keeps. */
export const CANCEL_REASON_MAX = 500;

interface CancelCampaignFormProps {
  started: boolean;
  busy: boolean;
  onCancel(reason: string): Promise<Problem | null>;
  onKeep(): void;
}

/** The confirmation step of a cancellation, with an optional reason kept on the campaign. */
function CancelCampaignForm({ started, busy, onCancel, onKeep }: CancelCampaignFormProps) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | undefined>();

  async function submit(event: FormEvent) {
    event.preventDefault();
    const text = reason.trim();
    if ([...text].length > CANCEL_REASON_MAX) {
      setError(`Keep the reason to ${CANCEL_REASON_MAX} characters.`);
      return;
    }
    setError(undefined);
    const problem = await onCancel(text);
    if (problem?.fields.reason) setError(problem.fields.reason);
  }

  return (
    <Card title="Cancel this campaign">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Cancel the campaign">
        <p className={styles.note}>
          {started
            ? "Its orders are released and can join another campaign. The resource's cleanout counter goes back to where it was before this campaign if no later campaign has used it."
            : "Its orders are released and can join another campaign."}
        </p>
        <FormField id="campaign-cancel-reason" label="Reason" optional hint="Kept on the campaign and sent with the cancellation." error={error}>
          {(control) => (
            <Textarea
              {...control}
              autoFocus
              rows={2}
              maxLength={CANCEL_REASON_MAX}
              value={reason}
              onChange={(event) => setReason(event.target.value)}
              invalid={Boolean(error)}
            />
          )}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="danger" loading={busy}>
            Cancel campaign
          </Button>
          <Button variant="ghost" disabled={busy} onClick={onKeep}>
            Keep campaign
          </Button>
        </div>
      </form>
    </Card>
  );
}

const failureTitles: Record<string, string> = {
  "SCHED-0115": "Someone else changed this campaign",
  "SCHED-0123": "Sequence the campaign again before you start it",
  "SCHED-0125": "Start the campaign before you complete it",
};

/** SCHED-0006 reports the optimizer outcome of a cp-sat campaign. */
const SOLVER_CODE = "SCHED-0006";

export function counterText(c: CleanoutCounter): string {
  return `${c.batches} ${c.batches === 1 ? "batch" : "batches"} and ${formatMinutes(c.minutes)}`;
}

function PlanCard({ campaign }: { campaign: Campaign }) {
  const plan = campaign.plan;
  if (!plan) {
    return (
      <Card title="Sequence">
        <EmptyState compact headingLevel={3} title="Not sequenced yet" description="Sequence the campaign to order it by the setup matrix and insert the cleanouts its rules demand." />
      </Card>
    );
  }
  const changeoverColumns: DataTableColumn<Changeover>[] = [
    { key: "kind", header: "Step", render: (c) => (c.beforeOrderId ? "Changeover" : "Leading setup") },
    { key: "fromGroup", header: "From", mono: true },
    { key: "toGroup", header: "To", mono: true },
    { key: "afterOrderId", header: "Before order", mono: true, render: (c) => shortId(c.afterOrderId) },
    { key: "minutes", header: "Minutes", align: "end" },
    { key: "cost", header: "Cost", align: "end" },
    { key: "cleaningSetId", header: "Cleaning set", render: (c) => c.cleaningSetId ?? dash },
    { key: "missing", header: "Source", render: (c) => (c.missing ? <Badge tone="warning">Route setup</Badge> : <Badge tone="neutral">Matrix</Badge>) },
  ];
  const cleanoutColumns: DataTableColumn<PlannedCleanout>[] = [
    { key: "afterOrderId", header: "After order", mono: true, render: (c) => shortId(c.afterOrderId) },
    { key: "setupGroup", header: "Setup group", mono: true },
    { key: "minutes", header: "Duration", align: "end", render: (c) => formatMinutes(c.minutes) },
    { key: "reason", header: "Reason", render: (c) => cleanoutReasons[c.reason] ?? c.reason },
    { key: "cleaningSetId", header: "Cleaning set", render: (c) => c.cleaningSetId ?? dash },
  ];
  const solver = plan.warnings.filter((w) => w.code === SOLVER_CODE);
  const missing = plan.warnings.filter((w) => w.code !== SOLVER_CODE);
  return (
    <Card title="Sequence">
      <div className={styles.stack}>
        {solver.length > 0 ? (
          <Alert tone={plan.optimized ? "success" : "info"} title={plan.optimized ? "Optimizer sequence kept" : "Nearest-neighbour sequence kept"} live="off">
            {solver.map((w) => (
              <p key={w.detail}>{`${w.code}: ${w.detail}`}</p>
            ))}
          </Alert>
        ) : null}
        {missing.length > 0 ? (
          <Alert tone="warning" title="Changeovers without a matrix entry" live="off">
            <ul className={styles.messages}>
              {missing.map((w) => (
                <li key={w.detail}>{`${w.code}: ${w.detail}`}</li>
              ))}
            </ul>
          </Alert>
        ) : null}
        {plan.carryIn && plan.carryOut ? (
          <p className={styles.note}>
            {`Cleanout counter: the resource starts ${counterText(plan.carryIn)} after its last cleanout and is left at ${counterText(plan.carryOut)} when this campaign starts.`}
          </p>
        ) : null}
        <p className={styles.note}>
          {plan.matrixId ? `Priced with setup matrix ${shortId(plan.matrixId)}.` : "No setup matrix is in force on the planned start; every changeover uses the route setup time."}
        </p>
        <DataTable
          label="Changeovers"
          columns={changeoverColumns}
          rows={plan.changeovers}
          getRowKey={(c, i) => `${c.afterOrderId}-${i}`}
          empty={{ title: "No changeovers", description: "Every order shares the resource's current setup group." }}
        />
        <DataTable
          label="Cleanouts"
          columns={cleanoutColumns}
          rows={plan.cleanouts}
          getRowKey={(c, i) => `${c.afterOrderId}-${i}`}
          empty={{ title: "No cleanouts", description: "No cleanout rule of these setup groups is triggered." }}
        />
      </div>
    </Card>
  );
}

interface AllocationValues {
  amount: string;
  currency: string;
  basis: AllocationBasis;
}

/** Checks for the cost allocation form; the amount stays decimal text. */
export function validateAllocation(values: AllocationValues): { errors: Partial<Record<keyof AllocationValues, string>>; ok: boolean } {
  const errors: Partial<Record<keyof AllocationValues, string>> = {};
  if (decimalText(values.amount, 6, 14) === null) errors.amount = "Enter an amount such as 900.00.";
  if (!CURRENCY.test(values.currency)) errors.currency = "Enter a three-letter currency code.";
  return { errors, ok: Object.keys(errors).length === 0 };
}

const basisLabels: Record<AllocationBasis, string> = {
  quantity: "By quantity",
  equal: "Equally",
  "run-minutes": "By run minutes",
};

function AllocationCard({ campaign }: { campaign: Campaign }) {
  const [values, setValues] = useState<AllocationValues>({ amount: "", currency: "USD", basis: "quantity" });
  const [errors, setErrors] = useState<Partial<Record<keyof AllocationValues, string>>>({});
  const [result, setResult] = useState<Allocation | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, ok } = validateAllocation(values);
    setErrors(found);
    setFailure(null);
    if (!ok) return;
    setBusy(true);
    try {
      setResult(await allocateCampaignCost(campaign.id, { amount: values.amount.trim(), currency: values.currency, basis: values.basis }));
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = mapFields(problem.fields, { amount: "amount", currency: "currency", basis: "basis" } as const);
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  if (campaign.status === "cancelled" || (campaign.orders ?? []).length === 0) {
    return null;
  }
  return (
    <Card title="Changeover cost allocation">
      <div className={styles.stack}>
        <p className={styles.note}>
          Split the actual cost of the leading setup and cleanout orders over the campaign's orders (FR-23.16). Rounding goes to the largest order. Nothing is posted yet.
        </p>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Allocate changeover cost">
          <FormField id="allocation-amount" label="Cost to allocate" error={errors.amount}>
            {(control) => (
              <Input {...control} inputMode="decimal" value={values.amount} onChange={(event) => setValues({ ...values, amount: event.target.value })} invalid={Boolean(errors.amount)} />
            )}
          </FormField>
          <FormField id="allocation-currency" label="Currency" error={errors.currency}>
            {(control) => (
              <Input
                {...control}
                maxLength={3}
                value={values.currency}
                onChange={(event) => setValues({ ...values, currency: event.target.value.toUpperCase() })}
                invalid={Boolean(errors.currency)}
              />
            )}
          </FormField>
          <FormField id="allocation-basis" label="Basis" error={errors.basis}>
            {(control) => (
              <Select {...control} value={values.basis} onChange={(event) => setValues({ ...values, basis: event.target.value as AllocationBasis })}>
                {ALLOCATION_BASES.map((b) => (
                  <option key={b} value={b}>
                    {basisLabels[b]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="secondary" loading={busy}>
              Allocate
            </Button>
          </div>
        </form>
        {failure ? (
          <Alert tone="danger" title="Could not allocate the cost" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        {result ? (
          <DataTable
            label="Allocation"
            columns={[
              { key: "orderId", header: "Order", mono: true, render: (l: Allocation["lines"][number]) => shortId(l.orderId) },
              { key: "weight", header: "Weight", align: "end" },
              { key: "amount", header: `Amount (${result.currency})`, align: "end", mono: true },
            ]}
            rows={result.lines}
            getRowKey={(l) => l.orderId}
          />
        ) : null}
      </div>
    </Card>
  );
}
