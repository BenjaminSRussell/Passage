import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile } from "../../shell/ui";
import { listVendorClaims, type ClaimStatus, type VendorClaimFilter } from "./api/client";
import { claimColumns } from "./claimColumns";
import { sumDecimals, UUID } from "./format";
import styles from "./pages.module.css";
import { CLAIM_STATUS_LABELS } from "./rebateFormat";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const PERIOD = /^\d{4}(-(0[1-9]|1[0-2])|-Q[1-4])?$/;

type Draft = { vendorId: string; period: string; status: ClaimStatus | "" };
type Errors = Partial<Record<keyof Draft, string>>;

/** Client checks of the inquiry filters; the service has the last word. */
export function validateClaimFilter(v: Draft): Errors {
  const errors: Errors = {};
  if (v.vendorId.trim() !== "" && !UUID.test(v.vendorId.trim())) errors.vendorId = "Enter the vendor id (a UUID).";
  if (v.period.trim() !== "" && !PERIOD.test(v.period.trim())) errors.period = "Use a period such as 2027-03, 2027-Q1 or 2027.";
  return errors;
}

/** Exact sum of claim amounts per currency, such as "750 USD · -20 EUR". */
export function claimTotals(rows: { amount: string; currency: string }[]): string {
  const byCurrency = new Map<string, string[]>();
  for (const r of rows) byCurrency.set(r.currency, [...(byCurrency.get(r.currency) ?? []), r.amount]);
  return [...byCurrency.entries()].map(([currency, amounts]) => `${sumDecimals(amounts)} ${currency}`).join(" · ");
}

/** Vendor rebate claims inquiry (§5.26.14): claims by vendor, period and status, newest first. */
export function VendorClaimsPage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<Draft>({ vendorId: "", period: "", status: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [filter, setFilter] = useState<VendorClaimFilter>({});
  const state = useLoad((signal) => listVendorClaims(filter, signal), JSON.stringify(filter));

  function apply(event: FormEvent) {
    event.preventDefault();
    const found = validateClaimFilter(draft);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setFilter({ vendorId: draft.vendorId.trim(), period: draft.period.trim(), status: draft.status });
  }

  const rows = state.data ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Vendor rebate claims"
        description="Period claims on vendor rebate agreements. Approved claims are settled through accounts payable by credit note, payment deduction or vendor invoice."
        actions={
          <Link className={buttonClassName("ghost")} to="/pricing/rebates">
            Rebate agreements
          </Link>
        }
      />
      <form className={styles.toolbar} onSubmit={apply} noValidate>
        <FormField id="vcl-vendor" label="Vendor" optional error={errors.vendorId}>
          {(c) => <EntityLookup entity="vendor" {...c} value={draft.vendorId} onChange={(e) => setDraft({ ...draft, vendorId: e.target.value })} invalid={Boolean(errors.vendorId)} />}
        </FormField>
        <FormField id="vcl-period" label="Period" optional error={errors.period}>
          {(c) => <Input {...c} className={styles.mono} value={draft.period} onChange={(e) => setDraft({ ...draft, period: e.target.value })} invalid={Boolean(errors.period)} />}
        </FormField>
        <FormField id="vcl-status" label="Status">
          {(c) => (
            <Select {...c} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as ClaimStatus | "" })}>
              <option value="">Any status</option>
              <option value="OPEN">{CLAIM_STATUS_LABELS.OPEN}</option>
              <option value="APPROVED">{CLAIM_STATUS_LABELS.APPROVED}</option>
            </Select>
          )}
        </FormField>
        <Button type="submit">Show claims</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load the vendor rebate claims" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <>
          <StatGroup label="Claim totals">
            <StatTile label="Claims" value={state.data ? rows.length : "…"} />
            <StatTile label="Claimed" value={state.data ? claimTotals(rows) || "0" : "…"} />
            <StatTile label="Waiting for approval" value={state.data ? rows.filter((r) => r.status === "OPEN").length : "…"} />
          </StatGroup>
          <DataTable
            label="Vendor rebate claims"
            columns={claimColumns}
            rows={rows}
            getRowKey={(c) => c.id}
            loading={state.loading}
            empty={{ title: "No vendor rebate claims", description: "Claims are created from a vendor rebate agreement, one period at a time." }}
            onRowOpen={(c) => navigate(`/pricing/rebates/claims/${c.id}`)}
          />
          {rows.length >= 100 ? <p className={styles.note}>Showing the newest 100 claims; the totals cover those rows only. Narrow the filters to see more.</p> : null}
        </>
      )}
    </div>
  );
}
