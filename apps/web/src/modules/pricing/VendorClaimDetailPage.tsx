import { useRef, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, PageHeader, Skeleton } from "../../shell/ui";
import { approveVendorClaim, describeProblem, getVendorClaim, newKey, PricingApiError, type Problem, type VendorClaim } from "./api/client";
import { ClaimStatusBadge } from "./claimColumns";
import styles from "./pages.module.css";
import { SETTLEMENT_LABELS } from "./rebateFormat";
import { useLoad } from "./useLoad";

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** One vendor rebate claim (FR-26.15): its amount and how it settles, and approval for AP (VendorRebateClaimApprove). */
export function VendorClaimDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getVendorClaim(id, signal), id);
  const [changed, setChanged] = useState<VendorClaim | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const approveKey = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Vendor rebate claim" />
        <Alert tone="danger" title="Could not load the claim" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Vendor rebate claim" />
        <span className="visually-hidden">Loading the claim…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const loaded = state.data;
  const claim = changed && changed.id === id && changed.version >= loaded.version ? changed : loaded;

  async function approve() {
    setFailure(null);
    setBusy(true);
    try {
      setChanged(await approveVendorClaim(claim.id, claim.version, approveKey.current));
      approveKey.current = newKey();
    } catch (error) {
      setFailure(describeProblem(error));
      // A refused approval is final; only a network or server failure may be retried with the same key.
      if (!(error instanceof PricingApiError) || error.status < 500) approveKey.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const negative = claim.amount.startsWith("-");
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Claim ${claim.number}`}
        description={`${claim.periodId} · ${claim.amount} ${claim.currency} · version ${claim.version}`}
        meta={<ClaimStatusBadge status={claim.status} />}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to={`/pricing/rebates/${claim.agreementId}`}>
              Rebate agreement
            </Link>
            <Link className={buttonClassName("ghost")} to="/pricing/rebates/claims">
              All claims
            </Link>
          </div>
        }
      />
      {changed && changed.status === "APPROVED" ? (
        <Alert tone="success" live="status">
          The claim is approved. Accounts payable settles it by {SETTLEMENT_LABELS[claim.settlementMode].toLowerCase()}.
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not approve the claim" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <Card title="Claim">
        <dl className={styles.details}>
          <Detail term="Vendor" mono>
            {claim.vendorId}
          </Detail>
          <Detail term="Period" mono>
            {claim.periodId}
          </Detail>
          <Detail term="Accrued in the period" mono>{`${claim.accruedTotal} ${claim.currency}`}</Detail>
          <Detail term="Claimed before" mono>{`${claim.priorClaimed} ${claim.currency}`}</Detail>
          <Detail term="This claim" mono>{`${claim.amount} ${claim.currency}`}</Detail>
          <Detail term="Settles by">{SETTLEMENT_LABELS[claim.settlementMode]}</Detail>
          <Detail term="Created" mono>{`${claim.createdAt.slice(0, 10)}${claim.createdBy ? ` by ${claim.createdBy}` : ""}`}</Detail>
          {claim.approvedAt ? <Detail term="Approved" mono>{`${claim.approvedAt.slice(0, 10)}${claim.approvedBy ? ` by ${claim.approvedBy}` : ""}`}</Detail> : null}
          <Detail term="Legal entity" mono>
            {claim.legalEntityId}
          </Detail>
        </dl>
      </Card>
      {claim.status === "OPEN" ? (
        <Card title="Approval">
          <div className={styles.stack}>
            <p className={styles.note}>
              {negative
                ? "The period was over-claimed. Approving sends a vendor invoice for the difference to accounts payable."
                : "Approving sends the claim to accounts payable for settlement. The person who created the claim cannot approve it."}
            </p>
            <div className={styles.actions}>
              <Button variant="primary" loading={busy} onClick={approve}>
                Approve claim
              </Button>
            </div>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
