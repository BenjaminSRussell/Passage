import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  closeRoyaltyPeriod,
  createContractClaim,
  describeProblem,
  listContractClaims,
  newKey,
  previewContractClaim,
  PricingApiError,
  type ContractClaim,
  type ContractClaimPreview,
  type ContractKind,
  type Problem,
  type RoyaltyPeriodClose,
} from "./api/client";
import { ClaimStatusBadge } from "./claimColumns";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const PERIOD = /^\d{4}(-(0[1-9]|1[0-2])|-Q[1-4])?$/;

const claimColumns: DataTableColumn<ContractClaim>[] = [
  { key: "number", header: "Claim", mono: true },
  { key: "periodId", header: "Period", mono: true },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (c) => `${c.amount} ${c.currency}` },
  { key: "status", header: "Status", render: (c) => <ClaimStatusBadge status={c.status} /> },
  { key: "createdAt", header: "Created", mono: true, render: (c) => c.createdAt.slice(0, 10) },
];

/**
 * Period claims of a broker contract or royalty agreement (FR-26.16, FR-26.17): preview what a
 * period would claim (its accruals and any minimum royalty shortfall, less earlier claims),
 * close a royalty period, create the BC- or RY- claim, and list the claims.
 */
export function ContractClaimCard({ kind, contractId, closed }: { kind: ContractKind; contractId: string; closed: boolean }) {
  const navigate = useNavigate();
  const claims = useLoad((signal) => listContractClaims({ contractKind: kind, contractId }, signal), `${kind}-${contractId}`);
  const [period, setPeriod] = useState("");
  const [periodError, setPeriodError] = useState("");
  const [preview, setPreview] = useState<ContractClaimPreview | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [created, setCreated] = useState<ContractClaim | null>(null);
  const [closeResult, setCloseResult] = useState<RoyaltyPeriodClose | null>(null);
  const [busy, setBusy] = useState<"" | "preview" | "close" | "claim">("");
  const claimKey = useRef(newKey());
  const royalty = kind === "ROYALTY";

  function validPeriod(): string | null {
    const value = period.trim();
    if (!PERIOD.test(value)) {
      setPeriodError("Use a period such as 2027-03, 2027-Q1 or 2027.");
      return null;
    }
    setPeriodError("");
    return value;
  }

  async function runPreview(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setCreated(null);
    setCloseResult(null);
    const value = validPeriod();
    if (!value) return;
    setBusy("preview");
    try {
      setPreview(await previewContractClaim(kind, contractId, value));
      claimKey.current = newKey();
    } catch (error) {
      setPreview(null);
      setFailure({ title: "Could not preview the claim", problem: describeProblem(error) });
    } finally {
      setBusy("");
    }
  }

  async function closePeriod() {
    const value = validPeriod();
    if (!value) return;
    setFailure(null);
    setBusy("close");
    try {
      setCloseResult(await closeRoyaltyPeriod(contractId, value));
      setPreview(await previewContractClaim(kind, contractId, value).catch(() => null));
    } catch (error) {
      setFailure({ title: "Could not close the period", problem: describeProblem(error) });
    } finally {
      setBusy("");
    }
  }

  async function claim() {
    if (!preview) return;
    setFailure(null);
    setBusy("claim");
    try {
      const made = await createContractClaim({ contractKind: kind, contractId, period: preview.periodId }, claimKey.current);
      setCreated(made);
      claimKey.current = newKey();
      setPreview(await previewContractClaim(kind, contractId, preview.periodId).catch(() => null));
      claims.reload();
    } catch (error) {
      setFailure({ title: "Could not create the claim", problem: describeProblem(error) });
      // A refused claim is final; only a network or server failure may be retried with the same key.
      if (!(error instanceof PricingApiError) || error.status < 500) claimKey.current = newKey();
    } finally {
      setBusy("");
    }
  }

  const nothingToClaim = preview !== null && Number(preview.amount) === 0;
  const needsClose = royalty && preview !== null && !preview.closed;
  return (
    <Card title="Period claims">
      <div className={styles.stack}>
        <p className={styles.note}>
          {royalty
            ? "A royalty period is closed first: if it is below the minimum royalty, the shortfall is posted. The claim then settles the period's royalty less earlier claims through accounts payable."
            : "A claim settles a period's accruals less what earlier claims of the period already claimed, through accounts payable."}
          {closed ? " The contract is closed; earlier periods can still be claimed." : ""}
        </p>
        <form className={styles.toolbar} onSubmit={runPreview} noValidate>
          <FormField id={`cc-period-${kind}`} label="Period" hint="Such as 2027-03, 2027-Q1 or 2027." error={periodError || undefined}>
            {(c) => <Input {...c} className={styles.mono} value={period} onChange={(e) => setPeriod(e.target.value)} invalid={Boolean(periodError)} />}
          </FormField>
          <Button type="submit" loading={busy === "preview"}>
            Preview claim
          </Button>
          {royalty ? (
            <Button variant="secondary" loading={busy === "close"} onClick={closePeriod}>
              Close period
            </Button>
          ) : null}
        </form>
        {failure ? (
          <Alert tone="danger" title={failure.title} live="alert">
            <ProblemMessage problem={failure.problem} />
          </Alert>
        ) : null}
        {closeResult ? (
          <Alert tone={closeResult.notice ? "warning" : "success"} live="status" title={closeResult.alreadyClosed ? `Period ${closeResult.periodId} was already closed` : `Period ${closeResult.periodId} closed`}>
            {closeResult.notice ?? `The royalty of ${closeResult.accrued} ${closeResult.currency} meets the minimum of ${closeResult.minimumRoyalty} ${closeResult.currency}.`}
          </Alert>
        ) : null}
        {created ? (
          <Alert tone="success" live="status" title={`Claim ${created.number} created`}>
            {`${created.amount} ${created.currency} for ${created.periodId}. `}
            <Link to={`/pricing/contract-claims/${created.id}`}>Open the claim to approve it</Link>
          </Alert>
        ) : null}
        {preview ? (
          <div className={styles.stack}>
            <StatGroup label={`Claim preview for ${preview.periodId}`}>
              <StatTile label="Accrued in the period" value={`${preview.accrued} ${preview.currency}`} />
              {royalty ? <StatTile label="Minimum royalty shortfall" value={`${preview.shortfall} ${preview.currency}`} /> : null}
              <StatTile label="Claimed before" value={`${preview.priorClaimed} ${preview.currency}`} />
              <StatTile label="To claim now" value={`${preview.amount} ${preview.currency}`} />
            </StatGroup>
            {needsClose ? (
              <p className={styles.note}>Close this period before claiming it, so the minimum royalty is settled.</p>
            ) : nothingToClaim ? (
              <p className={styles.note}>{preview.claims > 0 ? "The period is fully claimed." : "Nothing has accrued in this period yet."}</p>
            ) : (
              <div className={styles.toolbar}>
                <Button variant="primary" loading={busy === "claim"} onClick={claim}>
                  {`Create claim for ${preview.amount} ${preview.currency}`}
                </Button>
              </div>
            )}
          </div>
        ) : null}
        {claims.error ? (
          <Alert tone="danger" title="Could not load the claims" live="alert" action={<Button size="sm" onClick={claims.reload}>Try again</Button>}>
            <ProblemMessage problem={claims.error} />
          </Alert>
        ) : (
          <DataTable
            label="Claims of this contract"
            columns={claimColumns}
            rows={claims.data ?? []}
            getRowKey={(c) => c.id}
            loading={claims.loading}
            empty={{ title: "No claims yet", description: "Preview a period to claim what it accrued." }}
            onRowOpen={(c) => navigate(`/pricing/contract-claims/${c.id}`)}
          />
        )}
      </div>
    </Card>
  );
}
