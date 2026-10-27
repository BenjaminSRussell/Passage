import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  CostingApiError,
  createAdjustment,
  describeError,
  getAdjustment,
  listAdjustments,
  listValuationLedgers,
  previewAdjustment,
  type Adjustment,
  type AdjustmentKind,
  type AdjustmentMode,
  type AdjustmentRequest,
} from "./api/client";
import styles from "./pages.module.css";
import { DECIMAL } from "./shared";
import { useLoad } from "./useLoad";
import { LegalEntityFilterField, useLegalEntityFilter } from "./ValuationSetupPage";
import { dash, Detail, formatDateTime, LoadError, problemCode, UUID } from "./versionParts";

const DAY = /^\d{4}-\d{2}-\d{2}$/;

export const KIND_LABEL: Record<AdjustmentKind, string> = { ON_HAND: "On-hand stock", RECEIPT: "One receipt" };
export const MODE_LABEL: Record<AdjustmentMode, string> = { UNIT_COST: "New unit cost", AMOUNT: "Amount" };

function DeltaBadge({ delta, currency }: { delta: string; currency: string }) {
  const negative = delta.startsWith("-");
  return <Badge tone={negative ? "warning" : "success"}>{`${negative ? "" : "+"}${delta} ${currency}`}</Badge>;
}

const adjustmentColumns: DataTableColumn<Adjustment>[] = [
  { key: "financialDate", header: "Financial date", mono: true },
  { key: "kind", header: "Adjusts", render: (row) => KIND_LABEL[row.kind] },
  { key: "productId", header: "Product", mono: true },
  { key: "siteId", header: "Site", mono: true },
  { key: "mode", header: "By", render: (row) => MODE_LABEL[row.mode] },
  { key: "oldValue", header: "Value before", align: "end", mono: true },
  { key: "newValue", header: "Value after", align: "end", mono: true },
  { key: "delta", header: "Change", align: "end", render: (row) => <DeltaBadge delta={row.delta} currency={row.currency} /> },
  { key: "reason", header: "Reason", render: (row) => row.reason || dash },
];

/** Posted manual cost adjustments (FR-20.6), newest financial date first. */
export function AdjustmentsPage() {
  const navigate = useNavigate();
  const filter = useLegalEntityFilter();
  const [kind, setKind] = useState<AdjustmentKind | "">("");
  const key = `${filter.applied}|${kind}`;
  const state = useLoad((signal) => listAdjustments({ legalEntityId: filter.applied, kind }, signal), key);
  const [more, setMore] = useState<{ key: string; rows: Adjustment[]; next?: string }>({ key: "", rows: [] });
  const [moreError, setMoreError] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const extra = more.key === key ? more : { key, rows: [], next: undefined };
  const rows = [...(state.data?.items ?? []), ...extra.rows];
  const next = extra.rows.length > 0 ? extra.next : state.data?.nextCursor;

  async function loadMore() {
    if (!next) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listAdjustments({ legalEntityId: filter.applied, kind, cursor: next });
      setMore({ key, rows: [...extra.rows, ...page.items], next: page.nextCursor });
    } catch (err) {
      setMoreError(describeError(err, "The next page could not be loaded."));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cost adjustments"
        description="Manual revaluations of on-hand stock and of single receipts. Each posts a MANUAL_ADJ cost transaction, so the inventory value report moves at once; the next close flows a receipt adjustment to its issues."
        actions={
          <Link className={buttonClassName("primary")} to="/costing/adjustments/new">
            New cost adjustment
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <LegalEntityFilterField id="adjustments-le" filter={filter} />
        <FormField id="adjustments-kind" label="Adjusts" optional>
          {(c) => (
            <Select {...c} value={kind} onChange={(e) => setKind(e.target.value as AdjustmentKind | "")}>
              <option value="">Stock and receipts</option>
              <option value="ON_HAND">{KIND_LABEL.ON_HAND}</option>
              <option value="RECEIPT">{KIND_LABEL.RECEIPT}</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load the cost adjustments" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Cost adjustments"
          columns={adjustmentColumns}
          rows={rows}
          getRowKey={(row) => row.id ?? `${row.productId}|${row.financialDate}`}
          loading={state.loading && !state.data}
          empty={{ title: "No cost adjustments yet", description: "Adjust the value of on-hand stock or of a receipt to see it here." }}
          onRowOpen={(row) => row.id && navigate(`/costing/adjustments/${row.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" live="alert">
          {moreError}
        </Alert>
      ) : null}
      {next && !state.error ? (
        <div className={styles.more}>
          <Button loading={loadingMore} onClick={loadMore}>
            Load more adjustments
          </Button>
        </div>
      ) : null}
    </div>
  );
}

interface AdjustmentForm {
  legalEntityId: string;
  kind: AdjustmentKind;
  mode: AdjustmentMode;
  productId: string;
  siteId: string;
  warehouseId: string;
  valuationType: string;
  valuationLedgerId: string;
  receiptTransactionId: string;
  financialDate: string;
  value: string;
  reason: string;
}

/**
 * Checks the form and builds the request; reason is only required to post.
 * Returns field messages keyed like the API's field errors.
 */
export function buildAdjustmentRequest(f: AdjustmentForm, post: boolean): { ok: true; value: AdjustmentRequest } | { ok: false; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  const le = f.legalEntityId.trim().toLowerCase();
  if (!UUID.test(le)) errors.legalEntityId = "Enter the legal entity's id (a UUID).";
  if (!DAY.test(f.financialDate)) errors.financialDate = "Enter the financial date.";
  const mode: AdjustmentMode = f.kind === "RECEIPT" ? "AMOUNT" : f.mode;
  const value = f.value.trim();
  if (mode === "UNIT_COST" && !(/^\d+(\.\d{1,6})?$/.test(value) && Number(value) > 0)) {
    errors.unitCost = "Enter the new unit cost, greater than 0 with at most 6 decimals.";
  }
  if (mode === "AMOUNT" && !(DECIMAL.test(value) && /^-?\d+(\.\d{1,4})?$/.test(value))) {
    errors.amount = "Enter the signed change in value, such as -125.50, with at most 4 decimals.";
  }
  const body: AdjustmentRequest = { legalEntityId: le, kind: f.kind, mode, financialDate: f.financialDate };
  if (f.kind === "ON_HAND") {
    if (!UUID.test(f.productId.trim())) errors.productId = "Enter the product's id (a UUID).";
    if (!UUID.test(f.siteId.trim())) errors.siteId = "Enter the site's id (a UUID).";
    if (f.warehouseId.trim() !== "" && !UUID.test(f.warehouseId.trim())) errors.warehouseId = "Enter a warehouse id (a UUID), or leave it empty.";
    body.productId = f.productId.trim().toLowerCase();
    body.siteId = f.siteId.trim().toLowerCase();
    if (f.warehouseId.trim()) body.warehouseId = f.warehouseId.trim().toLowerCase();
    if (f.valuationType.trim()) body.valuationType = f.valuationType.trim();
  } else {
    if (!UUID.test(f.receiptTransactionId.trim())) errors.receiptTransactionId = "Enter the receipt transaction's id (a UUID).";
    body.receiptTransactionId = f.receiptTransactionId.trim().toLowerCase();
  }
  if (mode === "UNIT_COST") body.unitCost = value;
  else body.amount = value;
  if (f.valuationLedgerId) body.valuationLedgerId = f.valuationLedgerId;
  if (post) {
    if (f.reason.trim() === "" || f.reason.trim().length > 500) errors.reason = "Give a reason of 1 to 500 characters.";
    body.reason = f.reason.trim();
  }
  return Object.keys(errors).length > 0 ? { ok: false, errors } : { ok: true, value: body };
}

const today = () => new Date().toISOString().slice(0, 10);

/** Adjust on-hand value or a receipt (3.20.6 "Cost adjustment"): preview the change, then post it. */
export function CreateAdjustmentPage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<AdjustmentForm>({
    legalEntityId: "",
    kind: "ON_HAND",
    mode: "UNIT_COST",
    productId: "",
    siteId: "",
    warehouseId: "",
    valuationType: "",
    valuationLedgerId: "",
    receiptTransactionId: "",
    financialDate: today(),
    value: "",
    reason: "",
  });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [preview, setPreview] = useState<Adjustment | null>(null);
  const [busy, setBusy] = useState<"preview" | "post" | null>(null);
  // One key per previewed form: a retried post is a replay, an edited form a new adjustment.
  const key = useRef(crypto.randomUUID());

  function set<K extends keyof AdjustmentForm>(field: K, value: AdjustmentForm[K]) {
    key.current = crypto.randomUUID();
    setPreview(null);
    setForm((prev) => ({ ...prev, [field]: value }));
  }

  const le = UUID.test(form.legalEntityId.trim()) ? form.legalEntityId.trim().toLowerCase() : "";
  const ledgers = useLoad((signal) => (le ? listValuationLedgers(le, signal) : Promise.resolve({ items: [] })), `ledgers|${le}`);
  const mode: AdjustmentMode = form.kind === "RECEIPT" ? "AMOUNT" : form.mode;
  const valueField = mode === "UNIT_COST" ? "unitCost" : "amount";

  async function onPreview(e: FormEvent) {
    e.preventDefault();
    setFailure(null);
    const built = buildAdjustmentRequest(form, false);
    setErrors(built.ok ? {} : built.errors);
    if (!built.ok) return;
    setBusy("preview");
    try {
      setPreview(await previewAdjustment(built.value));
    } catch (err) {
      setPreview(null);
      if (err instanceof CostingApiError && err.problem.errors) setErrors(err.problem.errors);
      setFailure(describeError(err, "The adjustment could not be previewed."));
    } finally {
      setBusy(null);
    }
  }

  async function onPost() {
    if (!preview) return;
    setFailure(null);
    const built = buildAdjustmentRequest(form, true);
    setErrors(built.ok ? {} : built.errors);
    if (!built.ok) return;
    setBusy("post");
    try {
      const adj = await createAdjustment({ ...built.value, expectedValue: preview.oldValue }, key.current);
      navigate(`/costing/adjustments/${adj.id}`);
    } catch (err) {
      if (problemCode(err) === "CST-ADJ-006") {
        setPreview(null);
        key.current = crypto.randomUUID();
      }
      if (err instanceof CostingApiError && err.problem.errors) setErrors(err.problem.errors);
      setFailure(describeError(err, "The adjustment could not be posted."));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New cost adjustment"
        description="Revalue on-hand stock to a new unit cost or by an amount, or change the value of one receipt. Preview the change against the current value, then post it with a reason."
        actions={
          <Link className={buttonClassName("ghost")} to="/costing/adjustments">
            Back to cost adjustments
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={onPreview} noValidate>
          <FormField id="adj-le" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={form.legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(e) => set("legalEntityId", e.target.value)} />}
          </FormField>
          <FormField id="adj-kind" label="Adjust" error={errors.kind}>
            {(c) => (
              <Select {...c} value={form.kind} onChange={(e) => set("kind", e.target.value as AdjustmentKind)}>
                <option value="ON_HAND">{KIND_LABEL.ON_HAND}</option>
                <option value="RECEIPT">{KIND_LABEL.RECEIPT}</option>
              </Select>
            )}
          </FormField>
          {form.kind === "ON_HAND" ? (
            <>
              <FormField id="adj-product" label="Product" error={errors.productId}>
                {(c) => <Input {...c} className={styles.mono} value={form.productId} invalid={Boolean(errors.productId)} onChange={(e) => set("productId", e.target.value)} />}
              </FormField>
              <FormField id="adj-site" label="Site" error={errors.siteId}>
                {(c) => <Input {...c} className={styles.mono} value={form.siteId} invalid={Boolean(errors.siteId)} onChange={(e) => set("siteId", e.target.value)} />}
              </FormField>
              <FormField id="adj-warehouse" label="Warehouse" optional hint="Empty is stock recorded without a warehouse." error={errors.warehouseId}>
                {(c) => <Input {...c} className={styles.mono} value={form.warehouseId} invalid={Boolean(errors.warehouseId)} onChange={(e) => set("warehouseId", e.target.value)} />}
              </FormField>
              <FormField id="adj-valuation-type" label="Valuation type" optional hint="For split valuation, such as IMPORT." error={errors.valuationType}>
                {(c) => <Input {...c} value={form.valuationType} maxLength={64} invalid={Boolean(errors.valuationType)} onChange={(e) => set("valuationType", e.target.value)} />}
              </FormField>
              <FormField id="adj-mode" label="Adjust by" error={errors.mode}>
                {(c) => (
                  <Select {...c} value={form.mode} onChange={(e) => set("mode", e.target.value as AdjustmentMode)}>
                    <option value="UNIT_COST">{MODE_LABEL.UNIT_COST}</option>
                    <option value="AMOUNT">{MODE_LABEL.AMOUNT}</option>
                  </Select>
                )}
              </FormField>
            </>
          ) : (
            <FormField id="adj-receipt" label="Receipt transaction" hint="The receipt's inventory transaction id; the change is an amount." error={errors.receiptTransactionId}>
              {(c) => <Input {...c} className={styles.mono} value={form.receiptTransactionId} invalid={Boolean(errors.receiptTransactionId)} onChange={(e) => set("receiptTransactionId", e.target.value)} />}
            </FormField>
          )}
          <FormField
            id="adj-value"
            label={mode === "UNIT_COST" ? "New unit cost" : "Change in value"}
            hint={mode === "UNIT_COST" ? "The value becomes unit cost × on-hand quantity, rounded to the currency's minor unit." : "Signed: negative writes the value down."}
            error={errors[valueField]}
          >
            {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={form.value} invalid={Boolean(errors[valueField])} onChange={(e) => set("value", e.target.value)} />}
          </FormField>
          <FormField id="adj-ledger" label="Valuation ledger" hint="Each ledger values the same stock in its own currency." error={errors.valuationLedgerId}>
            {(c) => (
              <Select {...c} value={form.valuationLedgerId} disabled={!le} onChange={(e) => set("valuationLedgerId", e.target.value)}>
                <option value="">Primary valuation</option>
                {(ledgers.data?.items ?? [])
                  .filter((l) => !l.primary)
                  .map((l) => (
                    <option key={l.id} value={l.id}>
                      {`${l.code} (${l.currency})`}
                    </option>
                  ))}
              </Select>
            )}
          </FormField>
          <FormField id="adj-date" label="Financial date" hint="The value is read up to and including this day; not after today." error={errors.financialDate}>
            {(c) => <Input {...c} type="date" value={form.financialDate} invalid={Boolean(errors.financialDate)} onChange={(e) => set("financialDate", e.target.value)} />}
          </FormField>
          {ledgers.error ? (
            <div className={styles.wide}>
              <Alert tone="warning">{describeError(ledgers.error, "The legal entity's valuation ledgers could not be loaded.")}</Alert>
            </div>
          ) : null}
          <div className={styles.wide}>
            <FormField id="adj-reason" label="Reason" hint="Required to post, such as a net realisable value write-down." error={errors.reason}>
              {(c) => <Textarea {...c} rows={2} maxLength={500} value={form.reason} invalid={Boolean(errors.reason)} onChange={(e) => setForm((prev) => ({ ...prev, reason: e.target.value }))} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant={preview ? "secondary" : "primary"} loading={busy === "preview"}>
              Preview adjustment
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {failure}
      </Alert>
      {preview ? (
        <Card title="Preview" headingLevel={2}>
          <div className={styles.stack}>
            <StatGroup label="Adjustment preview">
              <StatTile label="Quantity" value={preview.qty} hint="Never changed by an adjustment" />
              <StatTile label={`Current value (${preview.currency})`} value={preview.oldValue} hint={preview.oldUnitCost ? `${preview.oldUnitCost} per unit` : undefined} />
              <StatTile label={`New value (${preview.currency})`} value={preview.newValue} hint={preview.newUnitCost ? `${preview.newUnitCost} per unit` : undefined} />
              <StatTile label={`Change (${preview.currency})`} value={preview.delta} hint="Posted as a MANUAL_ADJ cost transaction" />
            </StatGroup>
            <p className={styles.note}>
              {`Posting moves the inventory value of product ${preview.productId} at site ${preview.siteId} by ${preview.delta} ${preview.currency}. If the value changes before you post, the post is refused and you preview again.`}
            </p>
            <div className={styles.formActions}>
              <Button variant="primary" loading={busy === "post"} onClick={onPost}>
                Post adjustment
              </Button>
            </div>
          </div>
        </Card>
      ) : null}
    </div>
  );
}

/** One posted cost adjustment. */
export function AdjustmentDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getAdjustment(id, signal), id);
  const adj = state.data;
  const back = (
    <Link className={buttonClassName("ghost")} to="/costing/adjustments">
      Back to cost adjustments
    </Link>
  );

  if (state.error) {
    if (problemCode(state.error) === "CST-ADJ-007") {
      return <EmptyState icon="xCircle" headingLevel={2} title="Cost adjustment not found" description="It may belong to another legal entity, or the link is wrong." action={back} />;
    }
    return <LoadError title="Could not load the cost adjustment" error={state.error} onRetry={state.reload} />;
  }
  if (!adj) {
    return (
      <div className={styles.stack} aria-busy="true">
        <span className="visually-hidden">Loading cost adjustment…</span>
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  const drill = new URLSearchParams({ legalEntityId: adj.legalEntityId, productId: adj.productId, siteId: adj.siteId, kind: "MANUAL_ADJ" });
  if (adj.valuationLedgerId) drill.set("valuationLedgerId", adj.valuationLedgerId);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Cost adjustment on ${adj.financialDate}`}
        meta={<DeltaBadge delta={adj.delta} currency={adj.currency} />}
        description={`${KIND_LABEL[adj.kind]} by ${MODE_LABEL[adj.mode].toLowerCase()}. ${adj.reason ?? ""}`.trim()}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={`/costing/cost-transactions?${drill.toString()}`}>
              Cost transactions
            </Link>
            {back}
          </>
        }
      />
      <StatGroup label="Adjustment">
        <StatTile label="Quantity" value={adj.qty} />
        <StatTile label={`Value before (${adj.currency})`} value={adj.oldValue} hint={adj.oldUnitCost ? `${adj.oldUnitCost} per unit` : undefined} />
        <StatTile label={`Value after (${adj.currency})`} value={adj.newValue} hint={adj.newUnitCost ? `${adj.newUnitCost} per unit` : undefined} />
        <StatTile label={`Change (${adj.currency})`} value={adj.delta} />
      </StatGroup>
      <Card title="Details" headingLevel={2}>
        <dl className={styles.details}>
          <Detail term="Legal entity" mono>
            {adj.legalEntityId}
          </Detail>
          <Detail term="Product" mono>
            {adj.productId}
          </Detail>
          <Detail term="Site" mono>
            {adj.siteId}
          </Detail>
          <Detail term="Warehouse" mono>
            {adj.warehouseId ?? "None"}
          </Detail>
          <Detail term="Valuation type">{adj.valuationType || "None"}</Detail>
          <Detail term="Valuation ledger" mono>
            {adj.valuationLedgerId ?? "Primary valuation"}
          </Detail>
          {adj.receiptTransactionId ? (
            <Detail term="Receipt transaction" mono>
              {adj.receiptTransactionId}
            </Detail>
          ) : null}
          <Detail term={adj.mode === "UNIT_COST" ? "Unit cost asked" : "Amount asked"} mono>
            {adj.mode === "UNIT_COST" ? (adj.unitCost ?? dash) : (adj.amount ?? dash)}
          </Detail>
          <Detail term="Cost transaction" mono>
            {adj.costTransactionId ?? dash}
          </Detail>
          <Detail term="Posted">{formatDateTime(adj.createdAt)}</Detail>
        </dl>
      </Card>
      <Alert tone="info">The ledger voucher to the inventory and cost adjustment accounts is posted once the general ledger posting client is available.</Alert>
    </div>
  );
}
