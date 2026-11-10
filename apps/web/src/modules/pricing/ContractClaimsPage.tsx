import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { listContractClaims, type ClaimStatus, type ContractClaim, type ContractClaimFilter, type ContractKind } from "./api/client";
import { ClaimStatusBadge } from "./claimColumns";
import { KIND_LABELS } from "./contractFormat";
import { sumDecimals, UUID } from "./format";
import styles from "./pages.module.css";
import { CLAIM_STATUS_LABELS } from "./rebateFormat";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

const PERIOD = /^\d{4}(-(0[1-9]|1[0-2])|-Q[1-4])?$/;

type Draft = { kind: ContractKind | ""; vendorId: string; period: string; status: ClaimStatus | "" };
type Errors = Partial<Record<keyof Draft, string>>;

/** Client checks of the inquiry filters; the service has the last word. */
export function validateContractClaimFilter(v: Draft): Errors {
  const errors: Errors = {};
  if (v.vendorId.trim() !== "" && !UUID.test(v.vendorId.trim())) errors.vendorId = "Enter the vendor id (a UUID).";
  if (v.period.trim() !== "" && !PERIOD.test(v.period.trim())) errors.period = "Use a period such as 2027-03, 2027-Q1 or 2027.";
  return errors;
}

/** Exact sum of claim amounts per currency, such as "750 USD · -20 EUR". */
export function contractClaimTotals(rows: { amount: string; currency: string }[]): string {
  const byCurrency = new Map<string, string[]>();
  for (const r of rows) byCurrency.set(r.currency, [...(byCurrency.get(r.currency) ?? []), r.amount]);
  return [...byCurrency.entries()].map(([currency, amounts]) => `${sumDecimals(amounts)} ${currency}`).join(" · ");
}

const columns: DataTableColumn<ContractClaim>[] = [
  { key: "number", header: "Claim", mono: true },
  { key: "contractKind", header: "Type", render: (c) => <Badge tone={c.contractKind === "ROYALTY" ? "info" : "neutral"}>{KIND_LABELS[c.contractKind]}</Badge> },
  { key: "vendorId", header: "Vendor", render: (row) => <EntityName entity="vendor" id={row.vendorId} /> },
  { key: "periodId", header: "Period", mono: true },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (c) => `${c.amount} ${c.currency}` },
  { key: "status", header: "Status", render: (c) => <ClaimStatusBadge status={c.status} /> },
  { key: "createdAt", header: "Created", mono: true, render: (c) => c.createdAt.slice(0, 10) },
];

/** Broker and royalty claims inquiry (§5.26.14): claims by type, vendor, period and status, newest first. */
export function ContractClaimsPage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<Draft>({ kind: "", vendorId: "", period: "", status: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [filter, setFilter] = useState<ContractClaimFilter>({});
  const state = useLoad((signal) => listContractClaims(filter, signal), JSON.stringify(filter));

  function apply(event: FormEvent) {
    event.preventDefault();
    const found = validateContractClaimFilter(draft);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setFilter({ contractKind: draft.kind, vendorId: draft.vendorId.trim(), period: draft.period.trim(), status: draft.status });
  }

  const rows = state.data ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Broker and royalty claims"
        description="Period claims on broker contracts and royalty agreements. Approved claims are paid to the vendor through accounts payable."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/pricing/brokers">
              Broker contracts
            </Link>
            <Link className={buttonClassName("secondary")} to="/pricing/royalties">
              Royalty agreements
            </Link>
          </div>
        }
      />
      <form className={styles.toolbar} onSubmit={apply} noValidate>
        <FormField id="ccl-kind" label="Type">
          {(c) => (
            <Select {...c} value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value as ContractKind | "" })}>
              <option value="">Broker and royalty</option>
              <option value="BROKER">{KIND_LABELS.BROKER}</option>
              <option value="ROYALTY">{KIND_LABELS.ROYALTY}</option>
            </Select>
          )}
        </FormField>
        <FormField id="ccl-vendor" label="Vendor" optional error={errors.vendorId}>
          {(c) => <EntityLookup entity="vendor" {...c} value={draft.vendorId} onChange={(e) => setDraft({ ...draft, vendorId: e.target.value })} invalid={Boolean(errors.vendorId)} />}
        </FormField>
        <FormField id="ccl-period" label="Period" optional error={errors.period}>
          {(c) => <Input {...c} className={styles.mono} value={draft.period} onChange={(e) => setDraft({ ...draft, period: e.target.value })} invalid={Boolean(errors.period)} />}
        </FormField>
        <FormField id="ccl-status" label="Status">
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
        <Alert tone="danger" title="Could not load the claims" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <>
          <StatGroup label="Claim totals">
            <StatTile label="Claims" value={state.data ? rows.length : "…"} />
            <StatTile label="Claimed" value={state.data ? contractClaimTotals(rows) || "0" : "…"} />
            <StatTile label="Waiting for approval" value={state.data ? rows.filter((r) => r.status === "OPEN").length : "…"} />
          </StatGroup>
          <DataTable
            label="Broker and royalty claims"
            columns={columns}
            rows={rows}
            getRowKey={(c) => c.id}
            loading={state.loading}
            empty={{ title: "No broker or royalty claims", description: "Claims are created from a broker contract or a royalty agreement, one period at a time." }}
            onRowOpen={(c) => navigate(`/pricing/contract-claims/${c.id}`)}
          />
          {rows.length >= 100 ? <p className={styles.note}>Showing the newest 100 claims; the totals cover those rows only. Narrow the filters to see more.</p> : null}
        </>
      )}
    </div>
  );
}
