import { useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, PageHeader, Skeleton } from "../../shell/ui";
import { approveContractClaim, describeProblem, getContractClaim, newKey, PricingApiError, type ContractClaim, type Problem } from "./api/client";
import { ClaimStatusBadge } from "./claimColumns";
import { Detail } from "./ContractDetail";
import { contractLink, KIND_LABELS } from "./contractFormat";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** One broker or royalty claim (FR-26.16, FR-26.17): its amount, and approval for AP (VendorRebateClaimApprove). */
export function ContractClaimDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getContractClaim(id, signal), id);
  const [changed, setChanged] = useState<ContractClaim | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const approveKey = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Claim" />
        <Alert tone="danger" title="Could not load the claim" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Claim" />
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
      setChanged(await approveContractClaim(claim.id, claim.version, approveKey.current));
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
        description={`${KIND_LABELS[claim.contractKind]} · ${claim.periodId} · ${claim.amount} ${claim.currency} · version ${claim.version}`}
        meta={<ClaimStatusBadge status={claim.status} />}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to={contractLink(claim.contractKind, claim.contractId)}>
              {claim.contractKind === "BROKER" ? "Broker contract" : "Royalty agreement"}
            </Link>
            <Link className={buttonClassName("ghost")} to="/pricing/contract-claims">
              All claims
            </Link>
          </div>
        }
      />
      {changed && changed.status === "APPROVED" ? (
        <Alert tone="success" live="status">
          The claim is approved. Accounts payable settles it with the vendor.
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not approve the claim" live="alert">
          <ProblemMessage problem={failure} />
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
                ? "The period was over-claimed. Approving sends the difference to accounts payable to offset against the vendor."
                : "Approving sends the claim to accounts payable for payment. The person who created the claim cannot approve it."}
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
