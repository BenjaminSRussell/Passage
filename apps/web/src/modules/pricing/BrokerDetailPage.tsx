import { useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, PageHeader, Skeleton, StatusPill } from "../../shell/ui";
import { closeBrokerContract, describeProblem, getBrokerContract, newKey, PricingApiError, type BrokerContract, type Problem } from "./api/client";
import { ContractAccrualsCard } from "./ContractAccrualsCard";
import { ContractClaimCard } from "./ContractClaimCard";
import { Detail } from "./ContractDetail";
import { describeBrokerRate, describeBrokerScope, RATE_KIND_LABELS } from "./contractFormat";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** One broker contract (FR-26.16): its terms, accruals, period claims, and closing it. */
export function BrokerDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getBrokerContract(id, signal), id);
  const [changed, setChanged] = useState<BrokerContract | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const closeKey = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Broker contract" />
        <Alert tone="danger" title="Could not load the contract" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Broker contract" />
        <span className="visually-hidden">Loading the contract…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const loaded = state.data;
  const contract = changed && changed.id === id && changed.version >= loaded.version ? changed : loaded;

  async function close() {
    setFailure(null);
    setBusy(true);
    try {
      setChanged(await closeBrokerContract(contract.id, contract.version, closeKey.current));
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
        title={contract.name}
        description={`${describeBrokerRate(contract)} · ${contract.validFrom} – ${contract.validTo} · version ${contract.version}`}
        meta={<StatusPill status={contract.status.toLowerCase()} />}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/pricing/contract-claims">
              Broker and royalty claims
            </Link>
            <Link className={buttonClassName("ghost")} to="/pricing/brokers">
              All broker contracts
            </Link>
          </div>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not close the contract" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {changed && changed.status === "CLOSED" ? (
        <Alert tone="success" live="status">
          The contract is closed. It takes no further accruals; earlier periods can still be claimed.
        </Alert>
      ) : null}
      <Card title="Terms">
        <dl className={styles.details}>
          <Detail term="Broker vendor" mono>
            {contract.brokerVendorId}
          </Detail>
          <Detail term="Rate">{`${RATE_KIND_LABELS[contract.rateKind]}: ${describeBrokerRate(contract)}`}</Detail>
          <Detail term="Earns on">{describeBrokerScope(contract)}</Detail>
          <Detail term="Currency" mono>
            {contract.currency}
          </Detail>
          <Detail term="Posting" mono>{`Dr ${contract.expenseAccount} / Cr ${contract.payableAccount}`}</Detail>
          <Detail term="Legal entity" mono>
            {contract.legalEntityId}
          </Detail>
        </dl>
      </Card>
      <ContractAccrualsCard kind="BROKER" contractId={contract.id} />
      <ContractClaimCard kind="BROKER" contractId={contract.id} closed={contract.status === "CLOSED"} />
      {contract.status === "ACTIVE" ? (
        <Card title="Close the contract">
          <div className={styles.stack}>
            <p className={styles.note}>A closed contract takes no further accruals. Claims of earlier periods stay open.</p>
            <div className={styles.actions}>
              <Button variant="secondary" loading={busy} onClick={close}>
                Close contract
              </Button>
            </div>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
