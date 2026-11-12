import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getContract,
  modifyContract,
  MODIFICATION_TYPES,
  type ContractObligation,
  type Modification,
  type ModificationLine,
  type ModificationType,
  type ModifyObligationInput,
  type Problem,
} from "./api/client";
import { DAY, DECIMAL, METHOD_LABELS, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

const SIGNED = /^-?\d{1,14}(\.\d{1,6})?$/;
const SCOPE = /^(0(\.\d{1,6})?|1(\.0{1,6})?)$/;

const TYPE_LABELS: Record<ModificationType, string> = {
  PROSPECTIVE: "Prospective: the remaining services are distinct",
  CATCH_UP: "Cumulative catch-up: part of one partly delivered service",
};

interface Revision {
  ssp: string;
  scope: string;
}

/** Why a modification failed, in words the revenue accountant can act on. */
function failureTitle(code: string): string {
  switch (code) {
    case "COM-0428":
    case "REV-0103":
      return "The contract changed since you opened it";
    case "REV-0401":
      return "The effective date is before the contract date";
    case "REV-0402":
      return "The new price is below what the delivered goods were billed at";
    case "REV-0404":
      return "There is nothing left to modify";
    default:
      return "The contract was not modified";
  }
}

/** Obligations a modification re-allocates: point-in-time and usage revenue stays as billed. */
function modifiable(o: ContractObligation): boolean {
  return o.method === "RATABLE" || o.method === "MILESTONE";
}

/**
 * Contract modification (#29, ASC 606-10-25-10 to 25-13): change the price, revise standalone selling prices, and reduce or
 * remove obligations, prospectively or with a cumulative catch-up. The result shows each obligation's new allocation, the
 * catch-up recognized and the revenue taken back, and whether billing was asked to invoice or credit the change.
 */
export function ModifyRevenueContractPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getContract(id, signal), id);
  const [type, setType] = useState<ModificationType>("PROSPECTIVE");
  const [effectiveDate, setEffectiveDate] = useState(today());
  const [change, setChange] = useState("0");
  const [reason, setReason] = useState("");
  const [revisions, setRevisions] = useState<Record<string, Revision>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Modification | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Modify revenue contract" />
        <Alert tone="danger" title="Could not load the revenue contract" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  const contract = state.data;
  if (!contract) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Modify revenue contract" />
        <span className="visually-hidden">Loading the revenue contract…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const currency = contract.currency;
  const obligations = contract.obligations.filter(modifiable);
  const lineOf = (obligationId: string) => contract.obligations.find((o) => o.id === obligationId);

  function revise(obligationId: string, patch: Partial<Revision>) {
    setRevisions((prev) => {
      const current: Revision = prev[obligationId] ?? { ssp: "", scope: "" };
      return { ...prev, [obligationId]: { ...current, ...patch } };
    });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!contract) return;
    const found: Record<string, string> = {};
    if (!DAY.test(effectiveDate)) found.effectiveDate = "Enter the date the change takes effect as YYYY-MM-DD.";
    if (!SIGNED.test(change.trim())) found.transactionPriceChange = "Enter the price change as a number, negative for a reduction.";
    const lines: ModifyObligationInput[] = [];
    obligations.forEach((o, i) => {
      const r = revisions[o.id];
      if (!r || (r.ssp.trim() === "" && r.scope.trim() === "")) return;
      if (r.ssp.trim() !== "" && !DECIMAL.test(r.ssp.trim())) found[`obligations[${i}].ssp`] = "Enter a positive amount.";
      if (r.scope.trim() !== "" && !SCOPE.test(r.scope.trim())) found[`obligations[${i}].remainingScope`] = "Enter a share from 0 (remove) to 1 (keep all).";
      lines.push({ obligationId: o.id, ...(r.ssp.trim() ? { ssp: r.ssp.trim() } : {}), ...(r.scope.trim() ? { remainingScope: r.scope.trim() } : {}) });
    });
    setErrors(found);
    setFailure(null);
    setResult(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const out = await modifyContract(
        contract.id,
        { type, effectiveDate, transactionPriceChange: change.trim(), obligations: lines, ...(reason.trim() ? { reason: reason.trim() } : {}) },
        contract.version,
      );
      setResult(out.modification);
      setRevisions({});
      setChange("0");
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      const indexed: Record<string, string> = {};
      for (const [field, message] of Object.entries(problem.fields)) {
        const m = /^obligations\[(\d+)\]\.(.+)$/.exec(field);
        const at = m ? obligations.findIndex((o) => o.id === lines[Number(m[1])]?.obligationId) : -1;
        indexed[m && at >= 0 ? `obligations[${at}].${m[2]}` : field] = message;
      }
      if (Object.keys(indexed).length > 0) setErrors(indexed);
      else setFailure(problem);
      if (problem.code === "REV-0103") state.reload();
    } finally {
      setBusy(false);
    }
  }

  const resultColumns: DataTableColumn<ModificationLine>[] = [
    { key: "obligationId", header: "Obligation", render: (l) => <EntityName entity="product" id={lineOf(l.obligationId)?.productId ?? ""} /> },
    { key: "allocatedBefore", header: "Allocated before", mono: true, align: "end", render: (l) => `${l.allocatedBefore} ${currency}` },
    { key: "allocatedAfter", header: "Allocated after", mono: true, align: "end", render: (l) => `${l.allocatedAfter} ${currency}` },
    { key: "catchUp", header: "Catch-up", mono: true, align: "end", render: (l) => `${l.catchUp} ${currency}` },
    { key: "reversal", header: "Revenue taken back", mono: true, align: "end", render: (l) => `${l.reversal} ${currency}` },
    { key: "removed", header: "Outcome", render: (l) => (l.removed ? "Removed" : "Re-allocated") },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Modify revenue contract"
        description={`${contract.transactionPrice} ${currency} · contract date ${contract.contractDate} · ${contract.recognized} ${currency} recognized`}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("ghost")} to={`/billing/revenue-contracts/${contract.id}`}>
              Back to the contract
            </Link>
          </div>
        }
      />
      {failure ? (
        <Alert tone="danger" title={failureTitle(failure.code)} live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {result ? (
        <Alert tone="success" title="Contract modified" live="status">
          {`Price ${result.priceBefore} to ${result.priceAfter} ${currency} from ${result.effectiveDate}. Catch-up ${result.catchUp} ${currency}, revenue taken back ${result.reversal} ${currency}. `}
          {result.billingStatus === "PENDING" ? "Billing will propose the invoice or credit note for the price change." : "No price change to bill."}
        </Alert>
      ) : null}
      {result ? (
        <Card title="Outcome per obligation">
          <DataTable label="New allocation of each obligation" columns={resultColumns} rows={result.obligations} getRowKey={(l) => l.obligationId} />
        </Card>
      ) : null}
      <Card title="Change">
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="mod-type" label="Accounting" hint="Prospective spreads what is left to recognize over the remaining services; catch-up restates revenue to date.">
            {(c) => (
              <Select {...c} value={type} onChange={(e) => setType(e.target.value as ModificationType)}>
                {MODIFICATION_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {TYPE_LABELS[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="mod-date" label="Effective date" error={errors.effectiveDate}>
            {(c) => <Input {...c} type="date" className={styles.mono} value={effectiveDate} onChange={(e) => setEffectiveDate(e.target.value)} invalid={Boolean(errors.effectiveDate)} />}
          </FormField>
          <FormField id="mod-change" label={`Price change (${currency})`} hint="Negative to lower the price. A price below recognized revenue takes the difference back out of revenue." error={errors.transactionPriceChange}>
            {(c) => (
              <Input {...c} className={styles.mono} inputMode="decimal" value={change} onChange={(e) => setChange(e.target.value)} invalid={Boolean(errors.transactionPriceChange)} />
            )}
          </FormField>
          {obligations.map((o, i) => (
            <fieldset key={o.id} className={styles.lineSet}>
              <legend className={styles.groupsLegend}>
                <EntityName entity="product" id={o.productId} /> · {METHOD_LABELS[o.method] ?? o.method} · allocated {o.allocated} {currency}
              </legend>
              <FormField id={`mod-ob-${i}-ssp`} label="New standalone selling price" hint={`Now ${o.ssp} ${currency}. Leave empty to keep it.`} error={errors[`obligations[${i}].ssp`]}>
                {(c) => (
                  <Input
                    {...c}
                    className={styles.mono}
                    inputMode="decimal"
                    value={revisions[o.id]?.ssp ?? ""}
                    onChange={(e) => revise(o.id, { ssp: e.target.value })}
                    invalid={Boolean(errors[`obligations[${i}].ssp`])}
                  />
                )}
              </FormField>
              <FormField
                id={`mod-ob-${i}-scope`}
                label="Remaining scope kept"
                hint="Share of what is not yet delivered that stays in the contract: 0 removes the obligation, 0.5 halves it. Leave empty to keep all."
                error={errors[`obligations[${i}].remainingScope`]}
              >
                {(c) => (
                  <Input
                    {...c}
                    className={styles.mono}
                    inputMode="decimal"
                    value={revisions[o.id]?.scope ?? ""}
                    onChange={(e) => revise(o.id, { scope: e.target.value })}
                    invalid={Boolean(errors[`obligations[${i}].remainingScope`])}
                  />
                )}
              </FormField>
            </fieldset>
          ))}
          <FormField id="mod-reason" label="Reason" hint="Shown on the billing documents for the price change." error={errors.reason}>
            {(c) => <Input {...c} value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} invalid={Boolean(errors.reason)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" disabled={busy || obligations.length === 0}>
              {busy ? "Modifying…" : "Modify contract"}
            </Button>
          </div>
          {obligations.length === 0 ? <p>This contract has no ratable or milestone obligation to modify.</p> : null}
        </form>
      </Card>
    </div>
  );
}
