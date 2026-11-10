import { useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, PageHeader, Skeleton, StatusPill } from "../../shell/ui";
import { closeRoyaltyAgreement, describeProblem, getRoyaltyAgreement, newKey, PricingApiError, type Problem, type RoyaltyAgreement } from "./api/client";
import { ContractAccrualsCard } from "./ContractAccrualsCard";
import { ContractClaimCard } from "./ContractClaimCard";
import { Detail } from "./ContractDetail";
import { describeRoyaltyProduct, describeRoyaltyTiers, ROYALTY_BASIS_LABELS } from "./contractFormat";
import styles from "./pages.module.css";
import { PERIOD_LABELS } from "./rebateFormat";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** One royalty agreement (FR-26.17): its terms, accruals, period close with the minimum royalty, claims, and closing it. */
export function RoyaltyDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getRoyaltyAgreement(id, signal), id);
  const [changed, setChanged] = useState<RoyaltyAgreement | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const closeKey = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Royalty agreement" />
        <Alert tone="danger" title="Could not load the agreement" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Royalty agreement" />
        <span className="visually-hidden">Loading the agreement…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const loaded = state.data;
  const agreement = changed && changed.id === id && changed.version >= loaded.version ? changed : loaded;

  async function close() {
    setFailure(null);
    setBusy(true);
    try {
      setChanged(await closeRoyaltyAgreement(agreement.id, agreement.version, closeKey.current));
      closeKey.current = newKey();
    } catch (error) {
      setFailure(describeProblem(error));
      if (!(error instanceof PricingApiError) || error.status < 500) closeKey.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={agreement.name}
        description={`${describeRoyaltyProduct(agreement)} · ${agreement.validFrom} – ${agreement.validTo} · version ${agreement.version}`}
        meta={<StatusPill status={agreement.status.toLowerCase()} />}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/pricing/contract-claims">
              Broker and royalty claims
            </Link>
            <Link className={buttonClassName("ghost")} to="/pricing/royalties">
              All royalty agreements
            </Link>
          </div>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not close the agreement" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {changed && changed.status === "CLOSED" ? (
        <Alert tone="success" live="status">
          The agreement is closed. It takes no further accruals; earlier periods can still be closed and claimed.
        </Alert>
      ) : null}
      <Card title="Terms">
        <dl className={styles.details}>
          <Detail term="Licensor vendor" mono>
            {agreement.licensorVendorId}
          </Detail>
          <Detail term="Covers">{describeRoyaltyProduct(agreement)}</Detail>
          <Detail term="Basis">{ROYALTY_BASIS_LABELS[agreement.basis]}</Detail>
          <Detail term="Period">{PERIOD_LABELS[agreement.period]}</Detail>
          <Detail term="Tiers" mono>
            {describeRoyaltyTiers(agreement)}
          </Detail>
          <Detail term="Minimum royalty per period" mono>{`${agreement.minimumRoyalty} ${agreement.currency}`}</Detail>
          <Detail term="Posting" mono>{`Dr ${agreement.expenseAccount} / Cr ${agreement.payableAccount}`}</Detail>
          <Detail term="Legal entity" mono>
            {agreement.legalEntityId}
          </Detail>
        </dl>
      </Card>
      <ContractAccrualsCard kind="ROYALTY" contractId={agreement.id} />
      <ContractClaimCard kind="ROYALTY" contractId={agreement.id} closed={agreement.status === "CLOSED"} />
      {agreement.status === "ACTIVE" ? (
        <Card title="Close the agreement">
          <div className={styles.stack}>
            <p className={styles.note}>A closed agreement takes no further accruals. Close its last periods and claim them first if they are not yet settled.</p>
            <div className={styles.actions}>
              <Button variant="secondary" loading={busy} onClick={close}>
                Close agreement
              </Button>
            </div>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
