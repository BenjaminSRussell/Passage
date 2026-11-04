import { useRef, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getCampaign,
  killCampaign,
  listConditionRecords,
  newKey,
  PricingApiError,
  restoreCampaign,
  type Campaign,
  type ConditionRecord,
  type Problem,
} from "./api/client";
import { CampaignEditCard } from "./CampaignEditCard";
import { CAMPAIGN_STATES, campaignState, describeTarget } from "./campaignFormat";
import { describeCustomer, describePercent, describeProduct, today, TYPE_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

const recordColumns: DataTableColumn<ConditionRecord>[] = [
  { key: "type", header: "Type", render: (row) => TYPE_LABELS[row.type] },
  { key: "customerKey", header: "Customer", render: describeCustomer },
  { key: "productKey", header: "Product", mono: true, render: describeProduct },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => (Number(row.amount) !== 0 ? `${row.amount} ${row.currency}` : describePercent(row)) },
  { key: "validTo", header: "Valid to", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
];

interface Loaded {
  campaign: Campaign;
  records: ConditionRecord[];
}

/** One campaign: its terms, its trade agreements, and the kill switch (FR-26.10). */
export function CampaignDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad<Loaded>(async (signal) => {
    const [campaign, records] = await Promise.all([getCampaign(id, signal), listConditionRecords({ campaignId: id }, signal)]);
    return { campaign, records };
  }, id);
  const [changed, setChanged] = useState<Campaign | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState(false);
  const commandKey = useRef(newKey());

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
  const loaded = state.data.campaign;
  // A campaign returned by kill or restore belongs to this id only and is newer than the loaded one.
  const campaign = changed && changed.id === id && changed.version >= loaded.version ? changed : loaded;
  const status = CAMPAIGN_STATES[campaignState(campaign, today())];

  async function setKilled(kill: boolean) {
    setFailure(null);
    setDone("");
    setBusy(true);
    try {
      const next = kill ? await killCampaign(campaign.id, campaign.version, commandKey.current) : await restoreCampaign(campaign.id, campaign.version, commandKey.current);
      setChanged(next);
      setDone(kill ? "The campaign is killed. Its agreements leave the next price resolution." : "The campaign is restored. Its agreements price again inside its validity.");
      commandKey.current = newKey();
    } catch (error) {
      setFailure({ title: kill ? "Could not kill the campaign" : "Could not restore the campaign", problem: describeProblem(error) });
      // A refused command is final; only a network or server failure may be retried with the same key.
      if (!(error instanceof PricingApiError) || error.status < 500) commandKey.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={campaign.name}
        description={`${campaign.validFrom} to ${campaign.validTo} · version ${campaign.version}`}
        meta={<Badge tone={status.tone}>{status.label}</Badge>}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to={`/pricing/agreements/new?campaign=${encodeURIComponent(campaign.id)}`}>
              Add agreement
            </Link>
            <Link className={buttonClassName("ghost")} to="/pricing/campaigns">
              All campaigns
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.title} live="alert">
          {failure.problem.message} ({failure.problem.code})
        </Alert>
      ) : null}
      <Card title="Campaign">
        <dl className={styles.details}>
          <Detail term="Customers">{describeTarget(campaign.targetCustomerGroup)}</Detail>
          <Detail term="Valid from" mono>
            {campaign.validFrom}
          </Detail>
          <Detail term="Valid to" mono>
            {campaign.validTo}
          </Detail>
          <Detail term="Budget" mono>{`${campaign.budget} ${campaign.currency}`}</Detail>
          <Detail term="Legal entity" mono>
            {campaign.legalEntityId}
          </Detail>
          <Detail term="Campaign id" mono>
            {campaign.id}
          </Detail>
        </dl>
      </Card>
      <Card title="Trade agreements in this campaign">
        <DataTable
          label="Campaign agreements"
          columns={recordColumns}
          rows={state.data.records}
          getRowKey={(row) => row.id}
          empty={{ title: "No agreements yet", description: "Add an agreement to price products for this campaign." }}
          onRowOpen={(row) => navigate(`/pricing/agreements/${row.id}`)}
        />
      </Card>
      <CampaignEditCard campaign={campaign} onChange={setChanged} />
      <Card title="Kill switch">
        <div className={styles.stack}>
          <p className={styles.note}>
            {campaign.killed
              ? "Restoring lets the campaign's agreements price again from the next resolution, inside the campaign validity."
              : "Killing the campaign removes its agreements from the next price resolution at once, whatever their validity. It can be restored."}
          </p>
          <div className={styles.actions}>
            {campaign.killed ? (
              <Button variant="secondary" loading={busy} onClick={() => setKilled(false)}>
                Restore campaign
              </Button>
            ) : (
              <Button variant="danger" loading={busy} onClick={() => setKilled(true)}>
                Kill campaign
              </Button>
            )}
          </div>
        </div>
      </Card>
    </div>
  );
}
