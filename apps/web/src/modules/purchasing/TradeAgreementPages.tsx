import { useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  buttonClassName,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createTradeAgreement,
  describeProblem,
  getTradeAgreement,
  listTradeAgreements,
  newIdempotencyKey,
  type AccountCode,
  type ItemCode,
  type TradeAgreement,
  type TradeAgreementInput,
} from "./api/client";
import {
  Details,
  LoadError,
  ShortId,
  StatusBadge,
  currencyPattern,
  datePattern,
  decimalPattern,
  money,
  todayIso,
  useResource,
  uuidPattern,
  type Failure,
} from "./parts";
import styles from "./pages.module.css";

const accountLabels: Record<AccountCode, string> = { VENDOR: "Vendor", GROUP: "Vendor group", ALL: "All vendors" };
const itemLabels: Record<ItemCode, string> = { PRODUCT: "Product", GROUP: "Product group", ALL: "All products" };

function level(label: string, id?: string) {
  return id ? (
    <>
      {label} <ShortId id={id} />
    </>
  ) : (
    label
  );
}

/** "10 – 100" style quantity break; to is exclusive. */
export function quantityBreak(t: Pick<TradeAgreement, "fromQty" | "toQty">): string {
  return t.toQty ? `${t.fromQty} to under ${t.toQty}` : `${t.fromQty} and up`;
}

const listColumns: DataTableColumn<TradeAgreement>[] = [
  { key: "account", header: "Vendor", render: (t) => level(accountLabels[t.accountCode], t.accountId) },
  { key: "item", header: "Item", render: (t) => level(itemLabels[t.itemCode], t.itemId) },
  { key: "price", header: "Price", align: "end", render: (t) => `${money(t.price, t.currency)} / ${t.priceUnit} ${t.unit}` },
  { key: "break", header: "Quantity", render: quantityBreak },
  { key: "validFrom", header: "Valid from", mono: true },
  { key: "validTo", header: "Valid to", mono: true, render: (t) => t.validTo ?? "Open" },
  { key: "status", header: "Status", render: (t) => <StatusBadge status={t.status} /> },
];

/** Trade agreement journal: every price row, newest first. */
export function TradeAgreementsPage() {
  const navigate = useNavigate();
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<TradeAgreement[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listTradeAgreements({}, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [cursor],
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Trade agreements"
        description="Vendor price rows. The most specific valid row wins: vendor over group over all, product over group over all."
        actions={
          <Link className={buttonClassName("primary")} to="/purchasing/trade-agreements/new">
            New price row
          </Link>
        }
      />
      {page.error ? <LoadError what="trade agreements" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Trade agreements"
        columns={listColumns}
        rows={page.error ? [] : rows}
        getRowKey={(t) => t.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "No trade agreements yet", description: "Price rows you add here price purchase order lines automatically." }}
        onRowOpen={(t) => navigate(`/purchasing/trade-agreements/${t.id}`)}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button loading={page.loading} onClick={() => setCursor(page.data?.nextCursor)}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

interface Draft {
  accountCode: AccountCode;
  accountId: string;
  itemCode: ItemCode;
  itemId: string;
  currency: string;
  unit: string;
  fromQty: string;
  toQty: string;
  price: string;
  priceUnit: string;
  validFrom: string;
  validTo: string;
  consignment: boolean;
}

type Errors = Partial<Record<keyof Draft, string>>;

export function validateAgreement(d: Draft): Errors {
  const errors: Errors = {};
  if (d.accountCode !== "ALL" && !uuidPattern.test(d.accountId)) errors.accountId = "Enter the vendor or vendor group id (a UUID).";
  if (d.itemCode !== "ALL" && !uuidPattern.test(d.itemId)) errors.itemId = "Enter the product or product group id (a UUID).";
  if (!currencyPattern.test(d.currency)) errors.currency = "Use a three-letter ISO code such as USD.";
  if (d.unit.trim() === "") errors.unit = "Enter a unit.";
  if (d.fromQty !== "" && !decimalPattern.test(d.fromQty)) errors.fromQty = "Enter a decimal quantity.";
  if (d.toQty !== "" && (!decimalPattern.test(d.toQty) || Number(d.toQty) <= Number(d.fromQty || "0"))) {
    errors.toQty = "Enter a quantity above the from quantity.";
  }
  if (!decimalPattern.test(d.price)) errors.price = "Enter a decimal price.";
  if (!decimalPattern.test(d.priceUnit) || Number(d.priceUnit) <= 0) errors.priceUnit = "Enter a positive price unit.";
  if (!datePattern.test(d.validFrom)) errors.validFrom = "Enter a date.";
  if (d.validTo !== "" && (!datePattern.test(d.validTo) || d.validTo < d.validFrom)) errors.validTo = "Enter a date on or after valid from.";
  return errors;
}

function toInput(d: Draft): TradeAgreementInput {
  const opt = (v: string) => (v.trim() === "" ? undefined : v.trim());
  return {
    relation: "PRICE",
    accountCode: d.accountCode,
    accountId: d.accountCode === "ALL" ? undefined : d.accountId.trim(),
    itemCode: d.itemCode,
    itemId: d.itemCode === "ALL" ? undefined : d.itemId.trim(),
    currency: d.currency,
    unit: d.unit.trim(),
    fromQty: opt(d.fromQty),
    toQty: opt(d.toQty),
    price: d.price.trim(),
    priceUnit: opt(d.priceUnit),
    validFrom: d.validFrom,
    validTo: opt(d.validTo),
    consignment: d.consignment || undefined,
  };
}

/** New price row; the service refuses an equal-specificity overlap with PRC-TA-002. */
export function TradeAgreementCreatePage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<Draft>({
    accountCode: "VENDOR",
    accountId: "",
    itemCode: "PRODUCT",
    itemId: "",
    currency: "USD",
    unit: "EA",
    fromQty: "",
    toQty: "",
    price: "",
    priceUnit: "1",
    validFrom: todayIso(),
    validTo: "",
    consignment: false,
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [draft]);
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateAgreement(draft);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await createTradeAgreement(toInput(draft), key);
      navigate(`/purchasing/trade-agreements/${body.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors as Errors);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New price row" description="A purchase price for a vendor (or group, or all vendors) and a product (or group, or all products)." />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <Card title="Applies to">
          <div className={styles.grid}>
            <FormField id="ta-account-code" label="Vendor level">
              {(c) => (
                <Select {...c} value={draft.accountCode} onChange={(e) => set({ accountCode: e.target.value as AccountCode })}>
                  {(Object.keys(accountLabels) as AccountCode[]).map((k) => (
                    <option key={k} value={k}>
                      {accountLabels[k]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {draft.accountCode !== "ALL" ? (
              <FormField id="ta-account-id" label="Vendor or group id" error={errors.accountId}>
                {(c) => <Input {...c} value={draft.accountId} onChange={(e) => set({ accountId: e.target.value })} />}
              </FormField>
            ) : null}
            <FormField id="ta-item-code" label="Item level">
              {(c) => (
                <Select {...c} value={draft.itemCode} onChange={(e) => set({ itemCode: e.target.value as ItemCode })}>
                  {(Object.keys(itemLabels) as ItemCode[]).map((k) => (
                    <option key={k} value={k}>
                      {itemLabels[k]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {draft.itemCode !== "ALL" ? (
              <FormField id="ta-item-id" label="Product or group id" error={errors.itemId}>
                {(c) => <Input {...c} value={draft.itemId} onChange={(e) => set({ itemId: e.target.value })} />}
              </FormField>
            ) : null}
          </div>
        </Card>
        <Card title="Price">
          <div className={styles.grid}>
            <FormField id="ta-price" label="Price" error={errors.price}>
              {(c) => <Input {...c} inputMode="decimal" value={draft.price} onChange={(e) => set({ price: e.target.value })} />}
            </FormField>
            <FormField id="ta-price-unit" label="Per quantity" hint="Price unit, e.g. 100 for a price per hundred" error={errors.priceUnit}>
              {(c) => <Input {...c} inputMode="decimal" value={draft.priceUnit} onChange={(e) => set({ priceUnit: e.target.value })} />}
            </FormField>
            <FormField id="ta-currency" label="Currency" error={errors.currency}>
              {(c) => <Input {...c} value={draft.currency} maxLength={3} onChange={(e) => set({ currency: e.target.value.toUpperCase() })} />}
            </FormField>
            <FormField id="ta-unit" label="Unit" error={errors.unit}>
              {(c) => <Input {...c} value={draft.unit} onChange={(e) => set({ unit: e.target.value })} />}
            </FormField>
            <FormField id="ta-from-qty" label="From quantity" optional error={errors.fromQty}>
              {(c) => <Input {...c} inputMode="decimal" value={draft.fromQty} onChange={(e) => set({ fromQty: e.target.value })} />}
            </FormField>
            <FormField id="ta-to-qty" label="To quantity" optional hint="Exclusive" error={errors.toQty}>
              {(c) => <Input {...c} inputMode="decimal" value={draft.toQty} onChange={(e) => set({ toQty: e.target.value })} />}
            </FormField>
            <FormField id="ta-valid-from" label="Valid from" error={errors.validFrom}>
              {(c) => <Input {...c} type="date" value={draft.validFrom} onChange={(e) => set({ validFrom: e.target.value })} />}
            </FormField>
            <FormField id="ta-valid-to" label="Valid to" optional error={errors.validTo}>
              {(c) => <Input {...c} type="date" value={draft.validTo} onChange={(e) => set({ validTo: e.target.value })} />}
            </FormField>
          </div>
          <label className={styles.checkboxRow}>
            <Input type="checkbox" checked={draft.consignment} onChange={(e) => set({ consignment: e.target.checked })} />
            Consignment price (not used to price purchase orders)
          </label>
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save price row
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/trade-agreements">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

/** One trade agreement row. */
export function TradeAgreementDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource((signal) => getTradeAgreement(id, signal), [id]);
  const t = resource.data?.body;
  if (resource.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Price row" />
        {resource.error.code === "PRC-TA-NOT-FOUND" ? (
          <Card>
            <EmptyState
              icon="percent"
              title="Price row not found"
              description="Check the link, or pick a row from the trade agreement journal."
              action={
                <Link className={buttonClassName("secondary")} to="/purchasing/trade-agreements">
                  Back to trade agreements
                </Link>
              }
            />
          </Card>
        ) : (
          <LoadError what="the price row" error={resource.error} onRetry={resource.reload} />
        )}
      </div>
    );
  }
  if (!t) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Price row" />
        <p className={styles.muted} role="status">
          Loading price row…
        </p>
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader title={`Price row ${t.id.slice(0, 8)}`} meta={<StatusBadge status={t.status} />} />
      <Card title="Details">
        <Details
          items={[
            { label: "Vendor level", value: accountLabels[t.accountCode] },
            { label: "Vendor or group", value: t.accountId ?? "", mono: true },
            { label: "Item level", value: itemLabels[t.itemCode] },
            { label: "Product or group", value: t.itemId ?? "", mono: true },
            { label: "Price", value: `${money(t.price, t.currency)} per ${t.priceUnit} ${t.unit}`, mono: true },
            { label: "Quantity", value: quantityBreak(t) },
            { label: "Valid from", value: t.validFrom, mono: true },
            { label: "Valid to", value: t.validTo ?? "Open-ended", mono: true },
            { label: "Consignment", value: t.consignment ? "Yes" : "No" },
            { label: "Id", value: t.id, mono: true },
          ]}
        />
      </Card>
    </div>
  );
}
