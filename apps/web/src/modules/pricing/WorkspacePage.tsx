import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, StatGroup, StatTile, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  expireConditionRecords,
  getPricingWorkspace,
  listConditionRecords,
  type AccrualTotal,
  type ConditionRecord,
  type ExpiryResult,
  type PricingWorkspace,
  type Problem,
} from "./api/client";
import { addDays, DAY, describeCustomer, describeProduct, today, TYPE_LABELS, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const EXPIRY_WINDOW_DAYS = 30;
const PAGE = 100;

interface Overview {
  tiles: PricingWorkspace;
  /** ACTIVE records ending within the window, soonest first (at most one page). */
  expiring: ConditionRecord[];
}

/** "200.00 USD", or each currency in turn; "0" when nothing accrued. */
export function describeAccruals(totals: AccrualTotal[], role: AccrualTotal["partyRole"]): string {
  const mine = totals.filter((t) => t.partyRole === role);
  return mine.length === 0 ? "0" : mine.map((t) => `${t.amount} ${t.currency}`).join(" · ");
}

const expiringColumns: DataTableColumn<ConditionRecord>[] = [
  { key: "type", header: "Type", render: (row) => TYPE_LABELS[row.type] },
  { key: "customerKey", header: "Customer", render: describeCustomer },
  { key: "productKey", header: "Product", mono: true, render: describeProduct },
  { key: "validTo", header: "Valid to", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
];

/** Runs the nightly record expiry job (BR-26.9) on demand for one legal entity. */
function ExpiryCard({ onExpired }: { onExpired(): void }) {
  const [legalEntityId, setLegalEntityId] = useState("");
  const [asOf, setAsOf] = useState(today());
  const [errors, setErrors] = useState<{ legalEntityId?: string; asOf?: string }>({});
  const [result, setResult] = useState<ExpiryResult | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function run(event: FormEvent) {
    event.preventDefault();
    const found: typeof errors = {};
    if (!UUID.test(legalEntityId.trim())) found.legalEntityId = "Enter the legal entity id (a UUID).";
    if (!DAY.test(asOf)) found.asOf = "Enter the day the expiry runs for.";
    else if (asOf > addDays(today(), 1)) found.asOf = "Choose today or an earlier day.";
    setErrors(found);
    setFailure(null);
    setResult(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const out = await expireConditionRecords(legalEntityId.trim(), asOf);
      setResult(out);
      if (out.expired > 0) onExpired();
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.fields.legalEntityId || problem.fields.asOf) setErrors({ legalEntityId: problem.fields.legalEntityId, asOf: problem.fields.asOf });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Record expiry">
      <form className={styles.form} onSubmit={run} noValidate>
        <FormField id="ex-legal-entity" label="Legal entity" error={errors.legalEntityId}>
          {(c) => <EntityLookup entity="legalEntity" {...c} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} invalid={Boolean(errors.legalEntityId)} />}
        </FormField>
        <FormField id="ex-as-of" label="Expire validity ended before" error={errors.asOf}>
          {(c) => <Input {...c} type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} invalid={Boolean(errors.asOf)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Expire ended agreements
          </Button>
          <span className={styles.note}>The nightly job does this at 00:10. Active agreements whose last valid day has passed become expired; running it again changes nothing.</span>
        </div>
        {result ? (
          <div className={styles.formActions}>
            <Alert tone={result.expired > 0 ? "success" : "info"} live="status">
              {result.expired === 0 ? "No agreements ended before that day." : `${result.expired} ${result.expired === 1 ? "agreement" : "agreements"} expired.`}
            </Alert>
          </div>
        ) : null}
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not run the expiry" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
      </form>
    </Card>
  );
}

/** Pricing workspace (PRD §5.26.6): what needs attention in the trade agreements. */
export function WorkspacePage() {
  const navigate = useNavigate();
  const day = today();
  const state = useLoad<Overview>(async (signal) => {
    const [tiles, expiring] = await Promise.all([
      getPricingWorkspace({ asOf: day }, signal),
      listConditionRecords({ status: "ACTIVE", validToFrom: day, validToTo: addDays(day, EXPIRY_WINDOW_DAYS), limit: PAGE }, signal),
    ]);
    return { tiles, expiring };
  }, day);
  const tiles = state.data?.tiles;
  const expiring = state.data?.expiring ?? [];
  const value = (n: number | undefined) => (n === undefined ? "…" : n);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Pricing workspace"
        description="Trade agreements that are live, waiting for activation or about to expire, price journals waiting to post, campaigns running today and the rebate accrued this month."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("primary")} to="/pricing/agreements/new">
              New agreement
            </Link>
            <Link className={buttonClassName("secondary")} to="/pricing/journals/new">
              New price journal
            </Link>
            <Link className={buttonClassName("secondary")} to="/pricing/simulate">
              Price simulation
            </Link>
          </div>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the pricing workspace" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          <StatGroup label="Pricing tiles">
            <StatTile label="Active agreements" value={value(tiles?.activeRecords)} />
            <StatTile label="Drafts to activate" value={value(tiles?.draftRecords)} tone="warning" />
            <StatTile label={`Expiring in ${EXPIRY_WINDOW_DAYS} days`} value={value(tiles?.expiringIn30Days)} tone="warning" />
            <StatTile label="Journals to post" value={value(tiles?.journalsToPost)} />
            <StatTile label="Live campaigns" value={value(tiles?.liveCampaigns)} />
            <StatTile label="Vendor claims to approve" value={value(tiles?.claimsAwaitingApproval)} tone="warning" />
            <StatTile label="Customer rebates accrued this month" value={tiles ? describeAccruals(tiles.accrualsThisMonth, "CUSTOMER") : "…"} />
            <StatTile label="Vendor rebates accrued this month" value={tiles ? describeAccruals(tiles.accrualsThisMonth, "VENDOR") : "…"} />
          </StatGroup>
          <Card title="Expiring soon">
            <DataTable
              label="Expiring agreements"
              columns={expiringColumns}
              rows={expiring}
              getRowKey={(row) => row.id}
              loading={state.loading}
              empty={{ title: "Nothing expires in the next 30 days", description: "Active agreements whose validity ends soon appear here." }}
              onRowOpen={(row) => navigate(`/pricing/agreements/${row.id}`)}
            />
            {tiles && tiles.expiringIn30Days > expiring.length && expiring.length === PAGE ? (
              <p className={styles.note}>
                The list shows the {PAGE} agreements that end soonest; the tile counts all {tiles.expiringIn30Days}. Filter the trade agreements to see the rest.
              </p>
            ) : null}
          </Card>
        </>
      )}
      <ExpiryCard onExpired={state.reload} />
    </div>
  );
}
