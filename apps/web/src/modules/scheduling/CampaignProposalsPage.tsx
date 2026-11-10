import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
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
  type DataTableColumn,
} from "../../shell/ui";
import {
  createCampaign,
  describeProblem,
  listResources,
  newKey,
  PROPOSAL_WINDOWS,
  proposeCampaigns,
  SEQUENCE_RULE_LABELS,
  SEQUENCE_RULES,
  type CampaignProposals,
  type NewCampaign,
  type Problem,
  type ProposedCampaign,
  type ProposedCampaignOrder,
  type Resource,
  type SequenceRule,
} from "./api/client";
import { dayStart, formatDateTime, formatMinutes, shortId, today } from "./format";
import { decimalText, positive } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/**
 * Turns a proposal and the quantities typed for its orders into the campaign
 * request. Every order needs a quantity above zero with up to 6 decimals; the
 * rest comes from the scheduled operation.
 */
export function proposalToCampaign(
  proposal: ProposedCampaign,
  quantities: Record<string, string>,
  sequenceRule: SequenceRule,
): { errors: Record<string, string>; input?: NewCampaign } {
  const errors: Record<string, string> = {};
  const orders = proposal.orders.map((o) => {
    const q = decimalText(quantities[o.orderId] ?? "", 6);
    if (q === null || !positive(q)) errors[o.orderId] = "Enter a quantity above zero with up to 6 decimals.";
    const line: NewCampaign["orders"][number] = {
      orderId: o.orderId,
      setupGroup: o.setupGroup,
      quantity: q ?? "",
      runMinutes: o.runMinutes,
      routeSetupMinutes: o.routeSetupMinutes,
    };
    if (o.dueAt) line.dueAt = o.dueAt;
    return line;
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: { resourceId: proposal.resourceId, campaignKey: proposal.campaignKey, plannedStart: proposal.plannedStart, sequenceRule, orders },
  };
}

/** The service's orders[i].quantity errors, keyed by order id. */
function orderFieldErrors(fields: Record<string, string>, orders: ProposedCampaignOrder[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [name, message] of Object.entries(fields)) {
    const m = /^orders\[(\d+)\]\./.exec(name);
    const order = m ? orders[Number(m[1])] : undefined;
    if (order) out[order.orderId] = message;
  }
  return out;
}

function ProposalCard({ proposal, code }: { proposal: ProposedCampaign; code: string }) {
  const navigate = useNavigate();
  const [quantities, setQuantities] = useState<Record<string, string>>({});
  const [rule, setRule] = useState<SequenceRule>("nearest-neighbor");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const id = `${proposal.campaignKey}-${proposal.resourceId}-${proposal.plannedStart}`.replace(/[^A-Za-z0-9_-]/g, "");

  function setQuantity(orderId: string, value: string) {
    setQuantities({ ...quantities, [orderId]: value });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = proposalToCampaign(proposal, quantities, rule);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const campaign = await createCampaign(input, key.current);
      navigate(`/scheduling/campaigns/${campaign.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = orderFieldErrors(problem.fields, proposal.orders);
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else if (problem.code === "SCHED-0114") setFailure({ ...problem, message: "An order in this proposal joined another campaign. Refresh the proposals." });
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<ProposedCampaignOrder>[] = [
    { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
    { key: "operNumber", header: "Operation", align: "end" },
    { key: "setupGroup", header: "Setup group", mono: true },
    { key: "start", header: "Scheduled start", render: (row) => formatDateTime(row.start) },
    { key: "dueAt", header: "Due", render: (row) => formatDateTime(row.dueAt) },
    { key: "runMinutes", header: "Run", align: "end", render: (row) => formatMinutes(row.runMinutes) },
    { key: "firmed", header: "Job", render: (row) => (row.firmed ? <Badge tone="info">Firmed</Badge> : <Badge tone="neutral">Planned</Badge>) },
    {
      key: "quantity",
      header: "Quantity",
      render: (row) => (
        <>
          <Input
            aria-label={`Quantity for order ${shortId(row.orderId)}`}
            inputMode="decimal"
            value={quantities[row.orderId] ?? ""}
            onChange={(event) => setQuantity(row.orderId, event.target.value)}
            invalid={Boolean(errors[row.orderId])}
          />
          {errors[row.orderId] ? <p className={styles.fieldError}>{errors[row.orderId]}</p> : null}
        </>
      ),
    },
  ];

  return (
    <Card title={`${proposal.campaignKey} on ${code}`}>
      <form className={styles.stack} onSubmit={submit} noValidate aria-label={`Campaign ${proposal.campaignKey} on ${code}`}>
        <p className={styles.note}>
          {`Starts ${formatDateTime(proposal.plannedStart)} · ${proposal.orders.length} orders · ${formatMinutes(proposal.runMinutes)} run time`}
        </p>
        <DataTable label={`Orders proposed for ${proposal.campaignKey} on ${code}`} columns={columns} rows={proposal.orders} getRowKey={(row) => row.orderId} />
        <div className={styles.toolbar}>
          <FormField id={`proposal-rule-${id}`} label="Sequence rule">
            {(control) => (
              <Select {...control} value={rule} onChange={(event) => setRule(event.target.value as SequenceRule)}>
                {SEQUENCE_RULES.map((r) => (
                  <option key={r} value={r}>
                    {SEQUENCE_RULE_LABELS[r]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <Button type="submit" variant="primary" loading={busy}>
            Create campaign
          </Button>
        </div>
        {Object.keys(errors).length > 0 ? (
          <p role="alert" className={styles.fieldError}>
            Enter a quantity for every order.
          </p>
        ) : null}
        {failure ? (
          <Alert tone="danger" title="Could not create the campaign" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
      </form>
    </Card>
  );
}

interface View {
  proposals: CampaignProposals;
  resources: Resource[];
}

/**
 * Campaign proposals (FR-23.14): scheduled orders that share a campaign key on
 * one resource within the window, ready to be created as a campaign.
 */
export function CampaignProposalsPage() {
  const [from, setFrom] = useState(today());
  const [windowDays, setWindowDays] = useState<number>(14);
  const [resourceId, setResourceId] = useState("");
  const state = useLoad<View>(async (signal) => {
    const [proposals, resources] = await Promise.all([proposeCampaigns({ from: dayStart(from), windowDays, resourceId }, signal), listResources(signal)]);
    return { proposals, resources };
  }, `${from}|${windowDays}|${resourceId}`);
  const resources = state.data?.resources ?? [];
  const codes: Record<string, string> = Object.fromEntries(resources.map((r) => [r.id, `${r.name} (${r.code})`]));
  const items = state.data?.proposals.items ?? [];

  let body;
  if (state.error) {
    body = (
      <Alert tone="danger" title="Could not load campaign proposals" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        <ProblemMessage problem={state.error} />
      </Alert>
    );
  } else if (state.loading && !state.data) {
    body = (
      <div className={styles.loading} aria-busy="true">
        <span className="visually-hidden">Loading campaign proposals…</span>
        <Skeleton height="tile" />
        <Skeleton height="tile" />
      </div>
    );
  } else if (items.length === 0) {
    body = (
      <EmptyState
        title="No campaigns to propose"
        description={`No two scheduled orders outside a campaign share a campaign key on one resource in the ${windowDays} days from ${from}. Orders are keyed by their campaignKey attribute or setup group.`}
      />
    );
  } else {
    body = (
      <div className={styles.stack} aria-busy={state.loading}>
        {items.map((p) => (
          <ProposalCard key={`${p.campaignKey}|${p.resourceId}|${p.plannedStart}|${p.orders[0]?.orderId ?? ""}`} proposal={p} code={codes[p.resourceId] ?? shortId(p.resourceId)} />
        ))}
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Campaign proposals"
        description="Scheduled orders outside any campaign that share a campaign key on one resource within the window. Enter each order's quantity to create the campaign."
        actions={
          <Link className={buttonClassName("ghost")} to="/scheduling/campaigns">
            All campaigns
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="proposals-from" label="From (UTC)">
          {(control) => <Input {...control} type="date" value={from} onChange={(event) => event.target.value && setFrom(event.target.value)} />}
        </FormField>
        <FormField id="proposals-window" label="Window">
          {(control) => (
            <Select {...control} value={String(windowDays)} onChange={(event) => setWindowDays(Number(event.target.value))}>
              {PROPOSAL_WINDOWS.map((d) => (
                <option key={d} value={d}>
                  {`${d} days`}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="proposals-resource" label="Resource">
          {(control) => (
            <Select {...control} value={resourceId} onChange={(event) => setResourceId(event.target.value)}>
              <option value="">All resources</option>
              {resources.map((r) => (
                <option key={r.id} value={r.id}>
                  {`${r.code} · ${r.name}`}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {body}
    </div>
  );
}
