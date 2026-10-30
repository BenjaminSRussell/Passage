import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  CLAIM_SOURCES,
  CLAIM_STATUSES,
  createSupplierClaim,
  describeProblem,
  listSupplierClaims,
  newKey,
  type ClaimInput,
  type ClaimSource,
  type ClaimStatus,
  type Problem,
  type SupplierClaim,
} from "./api/client";
import { ClaimStatusBadge } from "./badges";
import { dash, DATE, DECIMAL, money, sentence, shortId, UUID } from "./format";
import { PagedTable } from "./PagedTable";
import styles from "./pages.module.css";

const columns: DataTableColumn<SupplierClaim>[] = [
  { key: "number", header: "Claim", mono: true },
  { key: "vendorPartyId", header: "Vendor", mono: true, render: (row) => shortId(row.vendorPartyId) },
  { key: "sourceKind", header: "Source", render: (row) => `${row.sourceKind === "SERVICE_WO" ? "Service" : "EAM"} (${row.sourceIds.length})` },
  { key: "claimedTotal", header: "Claimed", mono: true, render: (row) => money(row.claimedTotal, row.currency) },
  { key: "acceptedTotal", header: "Accepted", mono: true, render: (row) => money(row.acceptedTotal, row.currency) },
  { key: "recoveredAmount", header: "Recovered", mono: true, render: (row) => money(row.recoveredAmount, row.currency) },
  { key: "windowEndsOn", header: "Submit by", mono: true, render: (row) => (row.status === "DRAFT" ? row.windowEndsOn : dash) },
  { key: "status", header: "Status", render: (row) => <ClaimStatusBadge status={row.status} /> },
];

/** Supplier warranty claims (PRD 8.45.6): claim, vendor, claimed, accepted and recovered amounts, by status. */
export function SupplierClaimsPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [status, setStatus] = useState<ClaimStatus | "">((params.get("status") as ClaimStatus | null) ?? "");
  const [vendor, setVendor] = useState("");
  const vendorId = vendor.trim();
  const vendorValid = UUID.test(vendorId);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Supplier warranty claims"
        description="Recover warranty cost from vendors: claim the part cost and labor of warranty work, submit within the vendor's window, record the decision and match the credit note."
        actions={
          <Link className={buttonClassName("primary")} to="/service/supplier-claims/new">
            New claim
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="claims-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as ClaimStatus | "")}>
              <option value="">Any status</option>
              {CLAIM_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="claims-vendor" label="Vendor" hint="UUID of the vendor party." error={vendorId && !vendorValid ? "Enter a full UUID." : undefined}>
          {(control) => (
            <Input {...control} className={styles.mono} value={vendor} invalid={Boolean(vendorId && !vendorValid)} onChange={(e) => setVendor(e.target.value)} />
          )}
        </FormField>
      </div>
      <PagedTable
        label="Supplier claims"
        columns={columns}
        load={(cursor, signal) => listSupplierClaims({ status, ...(vendorValid ? { vendorPartyId: vendorId } : {}), cursor }, signal)}
        loadKey={`${status}|${vendorValid ? vendorId : ""}`}
        empty={{ title: "No supplier claims match", description: "Create a claim from posted warranty work orders, or change the filters." }}
        onRowOpen={(row) => navigate(`/service/supplier-claims/${row.id}`)}
      />
    </div>
  );
}

export interface ClaimValues {
  sourceKind: ClaimSource;
  vendorPartyId: string;
  /** One work order UUID per line. */
  sourceIds: string;
  laborRate: string;
  failureCode: string;
  windowDays: string;
  returnRequired: boolean;
  // EAM_WO: one work order and one line.
  legalEntityId: string;
  currency: string;
  completedOn: string;
  itemId: string;
  serial: string;
  partAmount: string;
  laborHours: string;
}

type Errors = Record<string, string>;

/** Checks that mirror the service (claims.go createClaim). */
export function validateClaim(v: ClaimValues): { errors: Errors; input?: ClaimInput } {
  const errors: Errors = {};
  if (!UUID.test(v.vendorPartyId.trim())) errors.vendorPartyId = "Enter the vendor party's UUID.";
  const sources = v.sourceIds
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
  const eam = v.sourceKind === "EAM_WO";
  if (sources.length < 1 || sources.length > (eam ? 1 : 50) || sources.some((s) => !UUID.test(s))) {
    errors.sourceIds = eam ? "Enter the UUID of the EAM work order." : "Enter 1 to 50 posted work order UUIDs, one per line.";
  } else if (new Set(sources).size !== sources.length) {
    errors.sourceIds = "A work order appears twice.";
  }
  const rate = v.laborRate.trim() || "0";
  if (!DECIMAL.test(rate)) errors.laborRate = "Enter the vendor's hourly labor rate, or 0 when labor is not covered.";
  if (v.failureCode.trim().length > 40) errors.failureCode = "Use at most 40 characters.";
  const window = v.windowDays.trim() === "" ? 30 : Number(v.windowDays);
  if (!Number.isInteger(window) || window < 1 || window > 365) errors.windowDays = "Enter 1 to 365 days.";
  if (eam) {
    if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
    if (!/^[A-Z]{3}$/.test(v.currency.trim().toUpperCase())) errors.currency = "Enter a three-letter currency code.";
    if (!DATE.test(v.completedOn)) errors.completedOn = "Enter the completion date.";
    if (!UUID.test(v.itemId.trim())) errors.itemId = "Enter the item's UUID.";
    if (!DECIMAL.test(v.partAmount.trim() || "0")) errors.partAmount = "Enter the part cost as a decimal.";
    if (!DECIMAL.test(v.laborHours.trim() || "0")) errors.laborHours = "Enter the labor hours as a decimal.";
    if ([...v.serial.trim()].length > 100) errors.serial = "Use at most 100 characters.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  const input: ClaimInput = {
    vendorPartyId: v.vendorPartyId.trim(),
    sourceKind: v.sourceKind,
    sourceIds: sources,
    laborRate: rate,
    windowDays: window,
    returnRequired: v.returnRequired,
    ...(v.failureCode.trim() ? { failureCode: v.failureCode.trim() } : {}),
  };
  if (eam) {
    input.legalEntityId = v.legalEntityId.trim();
    input.currency = v.currency.trim().toUpperCase();
    input.completedOn = v.completedOn;
    input.lines = [
      {
        itemId: v.itemId.trim(),
        ...(v.serial.trim() ? { serial: v.serial.trim() } : {}),
        partAmount: v.partAmount.trim() || "0",
        laborHours: v.laborHours.trim() || "0",
      },
    ];
  }
  return { errors, input };
}

/** Create a supplier claim from posted warranty work orders or an EAM work order (FR-45.15). */
export function NewSupplierClaimPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<ClaimValues>({
    sourceKind: "SERVICE_WO",
    vendorPartyId: "",
    sourceIds: params.get("workOrderId") ?? "",
    laborRate: "",
    failureCode: "",
    windowDays: "30",
    returnRequired: false,
    legalEntityId: "",
    currency: "",
    completedOn: "",
    itemId: "",
    serial: "",
    partAmount: "",
    laborHours: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const eam = values.sourceKind === "EAM_WO";

  function update(patch: Partial<ClaimValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateClaim(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createSupplierClaim(input, key.current);
      navigate(`/service/supplier-claims/${created.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: keyof ClaimValues & string, label: string, hint?: string, mono = true, optional = false) => (
    <FormField id={`claim-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(control) => (
        <Input
          {...control}
          className={mono ? styles.mono : undefined}
          value={String(values[name])}
          onChange={(e) => update({ [name]: e.target.value } as Partial<ClaimValues>)}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New supplier claim"
        description="A service claim takes one line per posted work order: the warranty part cost and warranty labor hours, with the service object's item and serial. Each line claims the part cost plus the hours at the vendor's labor rate."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New supplier claim">
          <FormField id="claim-sourceKind" label="Source">
            {(control) => (
              <Select {...control} value={values.sourceKind} onChange={(e) => update({ sourceKind: e.target.value as ClaimSource })}>
                {CLAIM_SOURCES.map((s) => (
                  <option key={s} value={s}>
                    {s === "SERVICE_WO" ? "Posted service work orders" : "EAM work order"}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("vendorPartyId", "Vendor", "UUID of the vendor party.")}
          {text("laborRate", "Vendor labor rate", "Per hour; 0 when the vendor does not cover labor.", true, true)}
          {text("windowDays", "Claim window (days)", "Days after completion to submit; default 30.")}
          {text("failureCode", "Failure code", undefined, true, true)}
          <div className={styles.wide}>
            <FormField
              id="claim-sourceIds"
              label={eam ? "EAM work order" : "Work orders"}
              hint={eam ? "UUID of the EAM work order." : "UUIDs of POSTED work orders with warranty lines, one per line."}
              error={errors.sourceIds}
            >
              {(control) => (
                <Textarea {...control} className={styles.mono} value={values.sourceIds} onChange={(e) => update({ sourceIds: e.target.value })} invalid={Boolean(errors.sourceIds)} />
              )}
            </FormField>
          </div>
          {eam ? (
            <>
              {text("legalEntityId", "Legal entity", "UUID of the legal entity.")}
              {text("currency", "Currency", "ISO 4217 code, such as USD.")}
              <FormField id="claim-completedOn" label="Completed on" error={errors.completedOn}>
                {(control) => <Input {...control} type="date" value={values.completedOn} onChange={(e) => update({ completedOn: e.target.value })} invalid={Boolean(errors.completedOn)} />}
              </FormField>
              {text("itemId", "Failed item", "UUID of the item.")}
              {text("serial", "Serial", undefined, true, true)}
              {text("partAmount", "Part cost", "Cost at issue, as a decimal.")}
              {text("laborHours", "Labor hours", undefined, true, true)}
            </>
          ) : null}
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="claim-returnRequired" type="checkbox" checked={values.returnRequired} onChange={(e) => update({ returnRequired: e.target.checked })} />
              <label htmlFor="claim-returnRequired">The vendor wants the failed part back before submission</label>
            </span>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the claim" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create claim
            </Button>
            <Link className={buttonClassName("ghost")} to="/service/supplier-claims">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
