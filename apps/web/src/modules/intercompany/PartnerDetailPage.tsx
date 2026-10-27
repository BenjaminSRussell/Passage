import { useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, EmptyState, PageHeader, Skeleton, StatusPill } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  evaluateChange,
  getPartner,
  priceLine,
  updatePartner,
  type LineTransferPrice,
  type OrderChangeRequest,
  type Partner,
  type PriceLineRequest,
} from "./api/partners";
import { Fields, type FieldSpec } from "./Fields";
import { dash, isCurrency, isDate, isDecimal, isPositive } from "./format";
import { actionLabels, PolicyFields, pricingLabels, validatePolicy, type PolicyValues } from "./PartnerFormPage";
import { pricingText, toleranceText } from "./PartnersPage";
import { RowGrid, type RowField } from "./RowGrid";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";

const versionConflict = "IC-0843";

function policyOf(p: Partner): PolicyValues {
  return {
    text: {
      customerAccount: p.customerAccount,
      vendorAccount: p.vendorAccount,
      pricing: p.pricing,
      markupPct: p.markupPct,
      pricePct: p.tolerance.pricePct,
      dateDays: String(p.tolerance.dateDays),
    },
    actions: p.actions,
    active: p.active,
  };
}

function ConflictNote({ failure, onReload }: { failure: Problem; onReload(): void }) {
  if (failure.code !== versionConflict) return <>{failure.message} ({failure.code})</>;
  return (
    <>
      Someone changed this pairing since you opened it. Reload it to see the latest version, then make your change again.{" "}
      <Button size="sm" onClick={onReload}>
        Reload pairing
      </Button>
    </>
  );
}

// ---- edit policy ----

function EditPolicy({ partner, onSaved, onReload }: { partner: Partner; onSaved(p: Partner): void; onReload(): void }) {
  const [values, setValues] = useState<PolicyValues>(() => policyOf(partner));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, policy } = validatePolicy({ ...values, active: partner.active });
    setErrors(found);
    setFailure(null);
    setSaved(false);
    if (!policy) return;
    delete policy.active;
    setBusy(true);
    try {
      onSaved(await updatePartner(partner.id, partner.version, policy));
      setSaved(true);
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Edit policy" headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <PolicyFields idPrefix="edit" values={values} errors={errors} onChange={setValues} />
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="The policy was not saved" live="alert">
              <ConflictNote failure={failure} onReload={onReload} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save policy
          </Button>
          <p className={styles.note} role="status">
            {saved ? `Policy saved as version ${partner.version}.` : ""}
          </p>
        </div>
      </form>
    </Card>
  );
}

// ---- price a line (FR-40.4) ----

type LineKey = "item" | "date" | "currency" | "quantity" | "listPrice" | "unitCost";
type RowKey = "item" | "method" | "markupPct" | "unitPrice" | "effectiveFrom";

export interface LineValues {
  line: Record<LineKey, string>;
  rows: Record<RowKey, string>[];
}

const lineFields: FieldSpec<LineKey>[] = [
  { name: "item", label: "Item", mono: true },
  { name: "date", label: "Line date", type: "date" },
  { name: "currency", label: "Currency", mono: true },
  { name: "quantity", label: "Quantity", decimal: true },
  { name: "listPrice", label: "List price (unit)", decimal: true, optional: true, hint: "Needed for an agreed price list." },
  { name: "unitCost", label: "Unit cost", decimal: true, optional: true, hint: "Needed for cost plus and transfer price tables." },
];

const rowFields: RowField<RowKey>[] = [
  { name: "item", label: "Item", mono: true },
  {
    name: "method",
    label: "Method",
    options: [
      { value: "Table", label: "Fixed price" },
      { value: "CostPlus", label: "Cost plus" },
      { value: "Cost", label: "Cost" },
    ],
  },
  { name: "markupPct", label: "Markup %", decimal: true, optional: true },
  { name: "unitPrice", label: "Unit price", decimal: true, optional: true },
  { name: "effectiveFrom", label: "Effective from", type: "date" },
];

const emptyRow = (): Record<RowKey, string> => ({ item: "", method: "Table", markupPct: "", unitPrice: "", effectiveFrom: "" });

export const sampleLine: LineValues = {
  line: { item: "PUMP-100", date: "2026-11-02", currency: "EUR", quantity: "8", listPrice: "", unitCost: "60.00" },
  rows: [{ item: "PUMP-100", method: "Table", markupPct: "", unitPrice: "66.00", effectiveFrom: "2026-01-01" }],
};

/** Checks that mirror the service for the pairing's pricing policy, and the request. */
export function validateLine(values: LineValues, pricing: Partner["pricing"]): Checked<PriceLineRequest> {
  const errors: Record<string, string> = {};
  const v = (k: LineKey) => values.line[k].trim();
  if (v("item") === "") errors.item = "Enter the item number.";
  if (!isDate(v("date"))) errors.date = "Enter the line date.";
  const currency = v("currency").toUpperCase();
  if (!isCurrency(currency)) errors.currency = "Enter a three-letter currency code such as EUR.";
  if (!isPositive(v("quantity"))) errors.quantity = "Enter a quantity above 0.";
  const needList = pricing === "PriceList";
  const needCost = pricing !== "PriceList";
  if ((needList || v("listPrice") !== "") && !isDecimal(v("listPrice"))) errors.listPrice = "Enter the unit list price, 0 or more.";
  if ((needCost || v("unitCost") !== "") && !isDecimal(v("unitCost"))) errors.unitCost = "Enter the unit cost, 0 or more.";
  const req: PriceLineRequest = { item: v("item"), date: v("date"), currency, quantity: v("quantity") };
  if (v("listPrice") !== "") req.listPrice = v("listPrice");
  if (v("unitCost") !== "") req.unitCost = v("unitCost");
  if (pricing === "TransferPriceTable") {
    if (values.rows.length === 0) errors.transferPrices = "Add the item's transfer prices.";
    req.transferPrices = values.rows.map((row, i): LineTransferPrice => {
      const r = (k: RowKey) => row[k].trim();
      const at = (k: RowKey) => `transferPrices[${i}].${k}`;
      if (r("item") === "") errors[at("item")] = "Enter the item.";
      if (!isDate(r("effectiveFrom"))) errors[at("effectiveFrom")] = "Enter the date the price applies from.";
      const tp: LineTransferPrice = { item: r("item"), method: r("method") as LineTransferPrice["method"], effectiveFrom: r("effectiveFrom") };
      if (tp.method === "CostPlus") {
        if (!isDecimal(r("markupPct"))) errors[at("markupPct")] = "Enter the markup, 0 or more.";
        tp.markupPct = r("markupPct");
      }
      if (tp.method === "Table") {
        if (!isDecimal(r("unitPrice"))) errors[at("unitPrice")] = "Enter the unit price, 0 or more.";
        tp.unitPrice = r("unitPrice");
      }
      return tp;
    });
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, request: req };
}

function PriceLine({ partner }: { partner: Partner }) {
  const [values, setValues] = useState<LineValues>(sampleLine);
  const { errors, failure, busy, result, run } = usePreview((req: PriceLineRequest) => priceLine(partner.id, req));
  const table = partner.pricing === "TransferPriceTable";

  function submit(event: FormEvent) {
    event.preventDefault();
    void run(validateLine(values, partner.pricing));
  }

  return (
    <Card title="Price a line" headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={styles.note}>
          Prices one intercompany line by this pairing's policy ({pricingText(partner)}) and shows the pricing basis the document link records.
          Nothing is stored.
        </p>
        <Fields idPrefix="line" fields={lineFields} values={values.line} errors={errors} onChange={(line) => setValues({ ...values, line })} />
        {table ? (
          <>
            <h4 className={styles.section}>Transfer prices</h4>
            {errors.transferPrices ? <p className={styles.danger}>{errors.transferPrices}</p> : null}
            <RowGrid
              idPrefix="line-tp"
              path="transferPrices"
              noun="Transfer price"
              fields={rowFields}
              rows={values.rows}
              errors={errors}
              newRow={emptyRow}
              onChange={(rows) => setValues({ ...values, rows })}
            />
          </>
        ) : null}
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="The line was not priced" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Price line
          </Button>
        </div>
      </form>
      {result ? (
        <dl className={styles.details} aria-label="Priced line">
          <div>
            <dt>Unit price</dt>
            <dd className={styles.mono}>
              {result.unitPrice} {result.currency}
            </dd>
          </div>
          <div>
            <dt>Line amount</dt>
            <dd className={styles.mono}>
              {result.extended} {result.currency}
            </dd>
          </div>
          <div>
            <dt>Method</dt>
            <dd>
              {result.basis.method}
              {result.basis.markupPct ? ` ${result.basis.markupPct} %` : ""}
            </dd>
          </div>
          <div>
            <dt>Priced from</dt>
            <dd>{result.basis.source === "transfer-price-table" ? `Transfer price from ${result.basis.effectiveFrom ?? dash}` : "Pairing policy"}</dd>
          </div>
        </dl>
      ) : null}
    </Card>
  );
}

// ---- check a linked order change (BR-40.3) ----

type ChangeKey = keyof OrderChangeRequest;

const changeFields: FieldSpec<ChangeKey>[] = [
  { name: "oldQuantity", label: "Quantity before", decimal: true },
  { name: "newQuantity", label: "Quantity after", decimal: true },
  { name: "oldPrice", label: "Unit price before", decimal: true },
  { name: "newPrice", label: "Unit price after", decimal: true },
  { name: "oldDate", label: "Date before", type: "date" },
  { name: "newDate", label: "Date after", type: "date" },
];

export const sampleChange: Record<ChangeKey, string> = {
  oldQuantity: "100",
  newQuantity: "100",
  oldPrice: "25.00",
  newPrice: "25.10",
  oldDate: "2026-12-10",
  newDate: "2026-12-12",
};

export function validateChange(values: Record<ChangeKey, string>): Checked<OrderChangeRequest> {
  const errors: Record<string, string> = {};
  const v = (k: ChangeKey) => values[k].trim();
  for (const k of ["oldQuantity", "newQuantity"] as const) if (!isPositive(v(k))) errors[k] = "Enter a quantity above 0.";
  for (const k of ["oldPrice", "newPrice"] as const) if (!isDecimal(v(k))) errors[k] = "Enter the unit price, 0 or more.";
  for (const k of ["oldDate", "newDate"] as const) if (!isDate(v(k))) errors[k] = "Enter the date.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    request: {
      oldQuantity: v("oldQuantity"),
      newQuantity: v("newQuantity"),
      oldPrice: v("oldPrice"),
      newPrice: v("newPrice"),
      oldDate: v("oldDate"),
      newDate: v("newDate"),
    },
  };
}

function CheckChange({ partner }: { partner: Partner }) {
  const [values, setValues] = useState(sampleChange);
  const { errors, failure, busy, result, run } = usePreview((req: OrderChangeRequest) => evaluateChange(partner.id, req));

  function submit(event: FormEvent) {
    event.preventDefault();
    void run(validateChange(values));
  }

  return (
    <Card title="Check an order change" headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={styles.note}>
          A change to a linked sales order updates the mirror purchase order silently within {toleranceText(partner)}; any quantity change
          or a larger move sends it for re-approval.
        </p>
        <Fields idPrefix="change" fields={changeFields} values={values} errors={errors} onChange={setValues} />
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="The change was not checked" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Check change
          </Button>
        </div>
      </form>
      {result ? (
        result.action === "UpdateSilently" ? (
          <Alert tone="success" title="The mirror purchase order updates silently">
            The change is within the pairing's tolerance.
          </Alert>
        ) : (
          <Alert tone="warning" title={`The mirror purchase order goes back for approval (${result.code ?? "IC-0804"})`}>
            <ul className={styles.fieldErrors}>
              {result.reasons.map((reason) => (
                <li key={reason}>{reason}</li>
              ))}
            </ul>
          </Alert>
        )
      ) : null}
    </Card>
  );
}

// ---- page ----

type Load = { status: "loading" } | { status: "ready"; partner: Partner } | { status: "error"; failure: Problem };

/** One trading-partner pairing: its policy, edits, activation, line pricing and change check. */
export function PartnerDetailPage() {
  const { id = "" } = useParams();
  const [load, setLoad] = useState<Load>({ status: "loading" });
  const [reload, setReload] = useState(0);
  const [toggling, setToggling] = useState(false);
  const [toggleFailure, setToggleFailure] = useState<Problem | null>(null);

  useEffect(() => {
    const ctl = new AbortController();
    setLoad({ status: "loading" });
    getPartner(id, ctl.signal)
      .then((partner) => setLoad({ status: "ready", partner }))
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setLoad({ status: "error", failure: describeProblem(err) });
      });
    return () => ctl.abort();
  }, [id, reload]);

  const back = (
    <Link className={buttonClassName("secondary")} to="/intercompany/partners">
      All trading partners
    </Link>
  );

  if (load.status === "loading") {
    return (
      <div className={styles.loading} aria-busy="true">
        <span className="visually-hidden" aria-live="polite">
          Loading the pairing…
        </span>
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }
  if (load.status === "error") {
    if (load.failure.status === 404) {
      return (
        <EmptyState
          icon="search"
          headingLevel={2}
          title="We couldn't find that pairing"
          description="It may belong to another tenant, or the link is wrong."
          action={back}
        />
      );
    }
    return (
      <Alert
        tone="danger"
        title="We couldn't load this pairing"
        live="alert"
        action={
          <Button size="sm" onClick={() => setReload((n) => n + 1)}>
            Try again
          </Button>
        }
      >
        {load.failure.message} ({load.failure.code})
      </Alert>
    );
  }

  const partner = load.partner;
  const setPartner = (p: Partner) => setLoad({ status: "ready", partner: p });
  const reloadPartner = () => setReload((n) => n + 1);

  async function toggleActive() {
    setToggling(true);
    setToggleFailure(null);
    try {
      setPartner(await updatePartner(partner.id, partner.version, { active: !partner.active }));
    } catch (err) {
      setToggleFailure(describeProblem(err));
    } finally {
      setToggling(false);
    }
  }

  const enabled = (Object.keys(actionLabels) as (keyof Partner["actions"])[]).filter((k) => partner.actions[k]);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${partner.sellerEntity} to ${partner.buyerEntity}`}
        meta={<StatusPill status={partner.active ? "active" : "inactive"} />}
        description={`Customer ${partner.customerAccount} in ${partner.sellerEntity} trades with vendor ${partner.vendorAccount} in ${partner.buyerEntity}. Version ${partner.version}.`}
        actions={
          <>
            {back}
            <Button variant={partner.active ? "danger" : "primary"} loading={toggling} onClick={() => void toggleActive()}>
              {partner.active ? "Deactivate pairing" : "Activate pairing"}
            </Button>
          </>
        }
      />
      {toggleFailure ? (
        <Alert tone="danger" title={partner.active ? "The pairing was not deactivated" : "The pairing was not activated"} live="alert">
          <ConflictNote failure={toggleFailure} onReload={reloadPartner} />
        </Alert>
      ) : null}
      <Card title="Policy" headingLevel={3}>
        <dl className={styles.details}>
          <div>
            <dt>Pricing</dt>
            <dd>{pricingLabels[partner.pricing]}</dd>
          </div>
          <div>
            <dt>Markup</dt>
            <dd className={styles.mono}>{partner.markupPct} %</dd>
          </div>
          <div>
            <dt>Change tolerance</dt>
            <dd>{toleranceText(partner)}</dd>
          </div>
          <div>
            <dt>Actions</dt>
            <dd>{enabled.length > 0 ? enabled.map((k) => actionLabels[k]).join("; ") : "None; mirror documents wait for a user"}</dd>
          </div>
          <div>
            <dt>Last changed</dt>
            <dd>
              <time dateTime={partner.updatedAt}>{partner.updatedAt.slice(0, 10)}</time>
            </dd>
          </div>
        </dl>
      </Card>
      {/* A reload remounts the form with the stored policy; a save keeps what was typed. */}
      <EditPolicy key={`${partner.id}-${reload}`} partner={partner} onSaved={setPartner} onReload={reloadPartner} />
      <PriceLine partner={partner} />
      <CheckChange partner={partner} />
    </div>
  );
}
