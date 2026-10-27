import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, Select, StatGroup, StatTile } from "../../shell/ui";
import {
  createVendorClaim,
  describeProblem,
  listVendorClaims,
  newKey,
  previewVendorClaim,
  PricingApiError,
  type Problem,
  type RebateAgreement,
  type VendorClaim,
  type VendorClaimPreview,
} from "./api/client";
import { claimColumns } from "./claimColumns";
import styles from "./pages.module.css";
import { SETTLEMENT_LABELS } from "./rebateFormat";
import { useLoad } from "./useLoad";

const PERIOD = /^\d{4}(-(0[1-9]|1[0-2])|-Q[1-4])?$/;

type ChosenMode = "CREDIT_NOTE" | "PAYMENT_DEDUCTION";

/**
 * Period claims of a VENDOR rebate agreement (FR-26.15, BR-26.11): preview what a period
 * would claim (its accruals less earlier claims), create the RC- claim, and list the claims.
 */
export function VendorClaimsCard({ agreement }: { agreement: RebateAgreement }) {
  const navigate = useNavigate();
  const claims = useLoad((signal) => listVendorClaims({ agreementId: agreement.id }, signal), agreement.id);
  const [period, setPeriod] = useState("");
  const [periodError, setPeriodError] = useState("");
  const [mode, setMode] = useState<ChosenMode>("CREDIT_NOTE");
  const [preview, setPreview] = useState<VendorClaimPreview | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [created, setCreated] = useState<VendorClaim | null>(null);
  const [busy, setBusy] = useState<"" | "preview" | "claim">("");
  const claimKey = useRef(newKey());

  async function runPreview(event: FormEvent) {
    event.preventDefault();
    const value = period.trim();
    setFailure(null);
    setCreated(null);
    if (!PERIOD.test(value)) {
      setPeriodError(`Use a ${agreement.period === "MONTH" ? "month such as 2027-03" : agreement.period === "QUARTER" ? "quarter such as 2027-Q1" : "year such as 2027"}.`);
      return;
    }
    setPeriodError("");
    setBusy("preview");
    try {
      setPreview(await previewVendorClaim(agreement.id, value));
      claimKey.current = newKey();
    } catch (error) {
      setPreview(null);
      setFailure({ title: "Could not preview the claim", problem: describeProblem(error) });
    } finally {
      setBusy("");
    }
  }

  async function claim() {
    if (!preview) return;
    setFailure(null);
    setBusy("claim");
    try {
      const made = await createVendorClaim({ agreementId: agreement.id, period: preview.periodId, settlementMode: mode }, claimKey.current);
      setCreated(made);
      claimKey.current = newKey();
      setPreview(await previewVendorClaim(agreement.id, preview.periodId).catch(() => null));
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
  const negative = preview !== null && Number(preview.amount) < 0;
  return (
    <Card title="Vendor claims">
      <div className={styles.stack}>
        <p className={styles.note}>
          A claim settles a period&apos;s accruals less what earlier claims of the period already claimed. Positive claims are settled by the vendor through accounts payable; an over-accrued period produces a vendor invoice instead.
        </p>
        <form className={styles.toolbar} onSubmit={runPreview} noValidate>
          <FormField id="vc-period" label="Period" hint={`The agreement accrues ${agreement.period.toLowerCase()}ly.`} error={periodError || undefined}>
            {(c) => <Input {...c} className={styles.mono} value={period} onChange={(e) => setPeriod(e.target.value)} invalid={Boolean(periodError)} />}
          </FormField>
          <Button type="submit" loading={busy === "preview"}>
            Preview claim
          </Button>
        </form>
        {failure ? (
          <Alert tone="danger" title={failure.title} live="alert">
            {failure.problem.message} ({failure.problem.code})
          </Alert>
        ) : null}
        {created ? (
          <Alert tone="success" live="status" title={`Claim ${created.number} created`}>
            {`${created.amount} ${created.currency} for ${created.periodId}, settled by ${SETTLEMENT_LABELS[created.settlementMode].toLowerCase()}. `}
            <Link to={`/pricing/rebates/claims/${created.id}`}>Open the claim to approve it</Link>
          </Alert>
        ) : null}
        {preview ? (
          <div className={styles.stack}>
            <StatGroup label={`Claim preview for ${preview.periodId}`}>
              <StatTile label="Accrued in the period" value={`${preview.accrued} ${preview.currency}`} />
              <StatTile label="Claimed before" value={`${preview.priorClaimed} ${preview.currency}`} />
              <StatTile label="To claim now" value={`${preview.amount} ${preview.currency}`} />
            </StatGroup>
            {nothingToClaim ? (
              <p className={styles.note}>{preview.claims > 0 ? "The period is fully claimed." : "Nothing has accrued in this period yet."}</p>
            ) : (
              <div className={styles.toolbar}>
                {negative ? (
                  <p className={styles.note}>The period was over-claimed: this claim creates a vendor invoice for the difference.</p>
                ) : (
                  <FormField id="vc-mode" label="Settle by">
                    {(c) => (
                      <Select {...c} value={mode} onChange={(e) => { setMode(e.target.value as ChosenMode); claimKey.current = newKey(); }}>
                        <option value="CREDIT_NOTE">{SETTLEMENT_LABELS.CREDIT_NOTE}</option>
                        <option value="PAYMENT_DEDUCTION">{SETTLEMENT_LABELS.PAYMENT_DEDUCTION}</option>
                      </Select>
                    )}
                  </FormField>
                )}
                <Button variant="primary" loading={busy === "claim"} onClick={claim}>
                  {`Create claim for ${preview.amount} ${preview.currency}`}
                </Button>
              </div>
            )}
          </div>
        ) : null}
        {claims.error ? (
          <Alert tone="danger" title="Could not load the claims" live="alert" action={<Button size="sm" onClick={claims.reload}>Try again</Button>}>
            {claims.error.message} ({claims.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Vendor claims of this agreement"
            columns={claimColumns}
            rows={claims.data ?? []}
            getRowKey={(c) => c.id}
            loading={claims.loading}
            empty={{ title: "No claims yet", description: "Preview a period to claim what it accrued." }}
            onRowOpen={(c) => navigate(`/pricing/rebates/claims/${c.id}`)}
          />
        )}
      </div>
    </Card>
  );
}
