import { useState, type FormEvent } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  closeAgreement,
  describeProblem,
  listAgreementVersions,
  repriceAgreementOrders,
  reviseAgreement,
  type Agreement,
  type AgreementRepriceResult,
  type AgreementRevisionInput,
  type AgreementVersion,
  type Problem,
} from "./api/client";
import { diffColumns } from "./ChangeOrderPage";
import { commitmentText, DECIMAL, formatDateTime, money, PERCENT, subtractDecimal } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Revising and closing an agreement needs this duty; the service enforces it. */
export const DUTY_AGREEMENT_MAINTAIN = "sales.agreement.maintain";
const REASON_MAX = 500;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** True for an empty tolerance or 0 in any spelling. */
function isZeroPercent(v: string): boolean {
  return v === "" || /^0+(\.0+)?$/.test(v);
}

/** Exact comparison of decimal strings (never parsed to float): -1, 0 or 1. */
function compare(a: string, b: string): number {
  const d = subtractDecimal(a, b);
  return d === "0" ? 0 : d.startsWith("-") ? -1 : 1;
}

export interface RevisionValues {
  commitmentQty: string;
  unitPrice: string;
  cumTolerance: string;
  overdeliveryPct: string;
  validFrom: string;
  validTo: string;
  reason: string;
}

/** Starting values of the revise form: what the agreement has now. */
export function revisionValues(a: Agreement): RevisionValues {
  return {
    commitmentQty: a.commitmentQty,
    unitPrice: a.unitPrice,
    cumTolerance: a.cumTolerance ?? "0",
    overdeliveryPct: a.overdeliveryPct ?? "0",
    validFrom: a.validFrom ?? "",
    validTo: a.validTo ?? "",
    reason: "",
  };
}

/**
 * Checks that mirror the service (api/agreement_lifecycle.go buildRevision) and
 * sends only the fields that differ from the agreement, so an unchanged form is
 * refused here instead of creating no version.
 */
export function validateRevision(values: RevisionValues, current: Agreement): { errors: Record<string, string>; input?: AgreementRevisionInput } {
  const errors: Record<string, string> = {};
  const input: AgreementRevisionInput = {};
  const byAmount = current.commitmentType === "AMOUNT";
  const commitmentOk = byAmount ? /^\d+(\.\d{1,4})?$/.test(values.commitmentQty) : DECIMAL.test(values.commitmentQty);
  if (!commitmentOk || /^0+(\.0+)?$/.test(values.commitmentQty)) errors.commitmentQty = byAmount ? "Enter an amount above 0 with up to 4 decimals." : "Enter a quantity above 0 with up to 6 decimals.";
  else if (isZeroPercent(values.overdeliveryPct) && isZeroPercent(current.overdeliveryPct ?? "0") && compare(values.commitmentQty, current.releasedQty) < 0) errors.commitmentQty = `Cannot be below the ${current.releasedQty} already released.`;
  else if (compare(values.commitmentQty, current.commitmentQty) !== 0) input[byAmount ? "commitmentAmount" : "commitmentQty"] = values.commitmentQty;
  if (byAmount && /^0+(\.0+)?$/.test(values.unitPrice)) errors.unitPrice = "An amount commitment needs a price above 0.";
  if (!/^\d+(\.\d{1,4})?$/.test(values.unitPrice)) errors.unitPrice = "Enter a price of at least 0 with up to 4 decimals.";
  else if (compare(values.unitPrice, current.unitPrice) !== 0) input.unitPrice = values.unitPrice;
  if (!PERCENT.test(values.overdeliveryPct)) errors.overdeliveryPct = "Enter a percent from 0 to 100 with up to 2 decimals.";
  else if (compare(values.overdeliveryPct, current.overdeliveryPct ?? "0") !== 0) input.overdeliveryPct = values.overdeliveryPct;
  if (!DECIMAL.test(values.cumTolerance)) errors.cumTolerance = "Enter a tolerance of at least 0.";
  else if (compare(values.cumTolerance, current.cumTolerance ?? "0") !== 0) input.cumTolerance = values.cumTolerance;
  for (const name of ["validFrom", "validTo"] as const) {
    if (values[name] !== "" && !DATE.test(values[name])) errors[name] = "Choose a date, or leave empty for open-ended.";
    else if (values[name] !== (current[name] ?? "")) input[name] = values[name];
  }
  if (!errors.validTo && values.validFrom && values.validTo && values.validFrom > values.validTo) errors.validTo = "Must not be before the start date.";
  if (Object.keys(errors).length === 0 && Object.keys(input).length === 0) errors.form = "Change at least one value.";
  if (Object.keys(errors).length > 0) return { errors };
  if (values.reason.trim()) input.reason = values.reason.trim();
  return { errors, input };
}

const kindLabel: Record<AgreementVersion["kind"], string> = { REVISED: "Revised", CLOSED: "Closed", RETURNED: "Quantity returned", DRAWN: "Quantity drawn", REPRICED: "Orders repriced" };

const versionColumns: DataTableColumn<AgreementVersion>[] = [
  { key: "version", header: "Version", align: "end" },
  { key: "kind", header: "What", render: (v) => kindLabel[v.kind] ?? v.kind },
  { key: "changedAt", header: "When", render: (v) => `${formatDateTime(v.changedAt)} by ${v.actor}` },
  { key: "reason", header: "Reason", render: (v) => v.reason || "—" },
  { key: "fields", header: "Fields", align: "end", render: (v) => String(v.diff.length) },
];

const problemTitle: Record<string, string> = {
  "SLS-0006": "The agreement was changed by someone else",
  "SLS-0052": "The agreement is closed",
  "SLS-0053": "Commitment is below the released quantity",
  "SLS-0008": "Not enough commitment left",
};

const repricedColumns = (currency: string): DataTableColumn<AgreementRepriceResult["repriced"][number]>[] => [
  { key: "orderNumber", header: "Order" },
  { key: "oldPrice", header: "Price before", align: "end", render: (r) => money(r.oldPrice, currency) },
  { key: "newPrice", header: "Price now", align: "end", render: (r) => money(r.newPrice, currency) },
  { key: "netTotal", header: "Order total", align: "end", render: (r) => money(r.netTotal, currency) },
];

const skippedColumns: DataTableColumn<AgreementRepriceResult["skipped"][number]>[] = [
  { key: "orderNumber", header: "Order" },
  { key: "reason", header: "Why it was left" },
];

/** What a reprice run did: the orders it repriced, the ones it left, and whether to run it again. */
function RepriceOutcome({ result }: { result: AgreementRepriceResult }) {
  const currency = result.agreement.currency;
  return (
    <div className={styles.stack}>
      <Alert tone={result.repriced.length > 0 ? "success" : "info"} live="status">
        {result.repriced.length > 0 ? `${result.repriced.length} released order(s) repriced; the agreement is at version ${result.agreement.version}.` : "No released order needed repricing."}
      </Alert>
      {result.repriced.length > 0 ? <DataTable label="Repriced orders" columns={repricedColumns(currency)} rows={result.repriced} getRowKey={(r) => r.orderId} /> : null}
      {result.skipped.length > 0 ? (
        <DataTable label="Orders left at their price" columns={skippedColumns} rows={result.skipped} getRowKey={(r) => r.orderId} />
      ) : null}
      {result.more ? <Alert tone="warning">More released orders are waiting. Run it again to reprice the rest.</Alert> : null}
    </div>
  );
}

interface Props {
  agreement: Agreement;
  /** The ETag the agreement was read at. */
  etag: string;
  /** Called after a revision or close so the page reloads. */
  onChanged: () => void;
}

/**
 * Revise or close an agreement and read its history (FR-27.10, BR-27.9). The
 * actions show only to a holder of sales.agreement.maintain and never on a
 * closed agreement; the service enforces both.
 */
export function AgreementLifecycle({ agreement, etag, onChanged }: Props) {
  const canMaintain = holdsDuty(usePrincipalDuties(), DUTY_AGREEMENT_MAINTAIN);
  const history = useLoad((signal) => listAgreementVersions(agreement.id, signal), `${agreement.id}:${agreement.version}`);
  const [values, setValues] = useState<RevisionValues>(() => revisionValues(agreement));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState("");
  const [busy, setBusy] = useState<"revise" | "close" | "reprice" | null>(null);
  const [repriced, setRepriced] = useState<AgreementRepriceResult | null>(null);
  const [closeReason, setCloseReason] = useState("");
  const [closeError, setCloseError] = useState("");
  const closed = agreement.status === "CLOSED";

  function set(patch: Partial<RevisionValues>) {
    setValues((v) => ({ ...v, ...patch }));
  }

  async function revise(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRevision(values, agreement);
    setErrors(found);
    setFailure(null);
    setSaved("");
    if (!input) return;
    setBusy("revise");
    try {
      const res = await reviseAgreement(agreement.id, etag, input);
      setValues(revisionValues(res.value));
      setSaved(`Agreement revised to version ${res.value.version}.`);
      onChanged();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(null);
    }
  }

  async function close(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setSaved("");
    if (!closeReason.trim()) {
      setCloseError("Give the reason for closing the agreement.");
      return;
    }
    setCloseError("");
    setBusy("close");
    try {
      await closeAgreement(agreement.id, etag, closeReason.trim());
      setSaved("Agreement closed.");
      onChanged();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function reprice() {
    setFailure(null);
    setSaved("");
    setRepriced(null);
    setBusy("reprice");
    try {
      const res = await repriceAgreementOrders(agreement.id, etag);
      setRepriced(res.value);
      onChanged();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const field = (name: keyof RevisionValues, label: string, extra: { type?: string; decimal?: boolean; hint?: string } = {}) => (
    <FormField id={`revise-${name}`} label={label} hint={extra.hint} error={errors[name]}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          inputMode={extra.decimal ? "decimal" : undefined}
          value={values[name]}
          onChange={(e) => set({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  const versions = history.data ?? [];
  const newest = versions[0];
  return (
    <>
      <Alert tone="success" live="status">
        {saved || null}
      </Alert>
      {closed ? (
        <Alert tone="warning" title="Closed">
          {agreement.closeReason ?? "This agreement was closed."} {agreement.closedAt ? `(${formatDateTime(agreement.closedAt)} by ${agreement.closedBy ?? "unknown"})` : null}
        </Alert>
      ) : canMaintain ? (
        <>
          <Card title="Revise the agreement">
            <form className={styles.form} onSubmit={revise} noValidate aria-label="Revise the agreement">
              {field("commitmentQty", agreement.commitmentType === "AMOUNT" ? "Committed amount" : "Committed quantity", { decimal: true, hint: `With the tolerance, not below the ${commitmentText(agreement, agreement.releasedQty)} already released.` })}
              {field("overdeliveryPct", "Over-delivery tolerance (%)", { decimal: true, hint: "Releases may go this many percent above the commitment." })}
              {field("unitPrice", "Agreed price", { decimal: true, hint: "Applies to later releases; existing orders keep their price." })}
              {field("cumTolerance", "Cumulative tolerance", { decimal: true })}
              {field("validFrom", "Valid from", { type: "date" })}
              {field("validTo", "Valid to", { type: "date" })}
              <FormField id="revise-reason" label="Reason" hint="Optional, kept in the history." error={errors.reason}>
                {(control) => <Textarea {...control} rows={2} maxLength={REASON_MAX} value={values.reason} onChange={(e) => set({ reason: e.target.value })} />}
              </FormField>
              {errors.form ? (
                <div className={styles.formActions}>
                  <Alert tone="warning" live="alert">
                    {errors.form}
                  </Alert>
                </div>
              ) : null}
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy === "revise"} disabled={busy === "close"}>
                  Save revision
                </Button>
              </div>
            </form>
          </Card>
          <Card title="Carry the price to released orders">
            <div className={styles.stack}>
              <p>
                A revised price applies to later releases. This puts the agreed price of {money(agreement.unitPrice, agreement.currency)} on the release line of every released order that is still a draft or open, keeping its quantity and discount. Confirmed orders are listed and left as they are.
              </p>
              <div className={styles.formActions}>
                <Button onClick={reprice} loading={busy === "reprice"} disabled={busy === "revise" || busy === "close"}>
                  Reprice released orders
                </Button>
              </div>
              {repriced ? <RepriceOutcome result={repriced} /> : null}
            </div>
          </Card>
          <Card title="Close the agreement">
            <form className={styles.form} onSubmit={close} noValidate aria-label="Close the agreement">
              <FormField id="close-reason" label="Reason" hint="A closed agreement takes no more releases. Released orders are not touched." error={closeError || undefined}>
                {(control) => <Textarea {...control} rows={2} maxLength={REASON_MAX} value={closeReason} onChange={(e) => setCloseReason(e.target.value)} invalid={Boolean(closeError)} />}
              </FormField>
              <div className={styles.formActions}>
                <Button type="submit" variant="danger" loading={busy === "close"} disabled={busy === "revise"}>
                  Close agreement
                </Button>
              </div>
            </form>
          </Card>
        </>
      ) : null}
      {failure ? (
        <Alert tone="danger" title={problemTitle[failure.code] ?? "Could not save the change"} live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <Card title="History">
        {history.error ? (
          <Alert tone="danger" title="Could not load the history" live="alert" action={<Button size="sm" onClick={history.reload}>Try again</Button>}>
            {history.error.message} ({history.error.code})
          </Alert>
        ) : !history.loading && versions.length === 0 ? (
          <EmptyState compact headingLevel={3} title="No changes yet" description="Revisions, the close, and quantity returned or drawn by order changes are recorded here." />
        ) : (
          <div className={styles.stack}>
            <DataTable label="Agreement history" columns={versionColumns} rows={versions} getRowKey={(v) => String(v.version)} loading={history.loading && versions.length === 0} />
            {newest ? <DataTable label={`Changes in version ${newest.version}`} columns={diffColumns} rows={newest.diff} getRowKey={(c, i) => `${c.field}-${i}`} /> : null}
          </div>
        )}
      </Card>
    </>
  );
}
