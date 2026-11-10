import { useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
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
  changePurchaseAgreement,
  createPurchaseAgreement,
  describeProblem,
  getPurchaseAgreement,
  listPurchaseAgreements,
  newIdempotencyKey,
  type AgreementAction,
  type AgreementRelease,
  type AgreementStatus,
  type CommitmentType,
  type MaxEnforced,
  type PurchaseAgreement,
  type PurchaseAgreementInput,
  type PurchaseAgreementLine,
  type PurchaseAgreementSummary,
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
  statusLabel,
  todayIso,
  useResource,
  uuidPattern,
  type Failure,
} from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const STATUSES: AgreementStatus[] = ["DRAFT", "EFFECTIVE", "ON_HOLD", "CLOSED"];

export const commitmentLabels: Record<CommitmentType, string> = {
  QUANTITY: "Quantity of a product",
  VALUE: "Value of a product",
  CATEGORY_VALUE: "Value of a category",
  TOTAL_VALUE: "Total value",
};

const enforcementLabels: Record<MaxEnforced, string> = {
  HARD: "Hard: refuse releases over the commitment",
  SOFT: "Soft: warn and allow",
  NONE: "None: unlimited",
};

/** True when the commitment is measured in quantity rather than amount. */
const byQuantity = (t: CommitmentType) => t === "QUANTITY";
const forProduct = (t: CommitmentType) => t === "QUANTITY" || t === "VALUE";

/** Validity as "2027-01-01 to 2027-12-31" or "from 2027-01-01". */
export function validity(a: { validFrom: string; validTo?: string }): string {
  return a.validTo ? `${a.validFrom} to ${a.validTo}` : `From ${a.validFrom}, open-ended`;
}

/** Released percentage with a badge tone: warning at 90 % (FR-17.14), danger above 100 %. */
export function ReleaseLevel({ pct }: { pct: string }) {
  const n = Number(pct);
  const tone = n > 100 ? "danger" : n >= 90 ? "warning" : "neutral";
  return <Badge tone={tone}>{pct} %</Badge>;
}

const listColumns: DataTableColumn<PurchaseAgreementSummary>[] = [
  { key: "number", header: "Number", mono: true },
  { key: "status", header: "Status", render: (a) => <StatusBadge status={a.status} /> },
  { key: "vendorId", header: "Vendor", render: (a) => <ShortId id={a.vendorId} /> },
  { key: "validity", header: "Validity", mono: true, render: validity },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "maxPctReleased", header: "Highest release", align: "end", render: (a) => <ReleaseLevel pct={a.maxPctReleased} /> },
];

/** Purchase agreement list with a status filter and load-more paging. */
export function PurchaseAgreementsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<AgreementStatus | "">("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<PurchaseAgreementSummary[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listPurchaseAgreements({ status }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [status, cursor],
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Purchase agreements"
        description="Blanket agreements with a vendor: what was committed, what purchase orders have released, and what remains."
        actions={
          <Link className={buttonClassName("primary")} to="/purchasing/agreements/new">
            New agreement
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="pa-status-filter" label="Status">
            {(control) => (
              <Select
                {...control}
                value={status}
                onChange={(e) => {
                  setCursor(undefined);
                  setStatus(e.target.value as AgreementStatus | "");
                }}
              >
                <option value="">All statuses</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </div>
      {page.error ? <LoadError what="purchase agreements" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Purchase agreements"
        columns={listColumns}
        rows={page.error ? [] : rows}
        getRowKey={(a) => a.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "No purchase agreements yet", description: "Agree quantities or spend with a vendor, then release purchase orders against them." }}
        onRowOpen={(a) => navigate(`/purchasing/agreements/${a.id}`)}
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

interface LineDraft {
  key: string;
  commitmentType: CommitmentType;
  productId: string;
  category: string;
  unit: string;
  committed: string;
  unitPrice: string;
  discountPct: string;
  maxEnforced: MaxEnforced;
  priceOverride: boolean;
}

interface HeaderDraft {
  legalEntityId: string;
  vendorId: string;
  currency: string;
  status: "EFFECTIVE" | "DRAFT";
  validFrom: string;
  validTo: string;
}

type Errors = Record<string, string>;

function emptyLine(): LineDraft {
  return {
    key: newIdempotencyKey(),
    commitmentType: "QUANTITY",
    productId: "",
    category: "",
    unit: "EA",
    committed: "",
    unitPrice: "",
    discountPct: "",
    maxEnforced: "HARD",
    priceOverride: false,
  };
}

/** Mirrors the service rules so most mistakes are caught before the round trip. */
export function validateAgreement(h: HeaderDraft, lines: LineDraft[]): Errors {
  const errors: Errors = {};
  if (!uuidPattern.test(h.legalEntityId)) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!uuidPattern.test(h.vendorId)) errors.vendorId = "Enter the vendor id (a UUID).";
  if (!currencyPattern.test(h.currency)) errors.currency = "Use a three-letter ISO code such as USD.";
  if (!datePattern.test(h.validFrom)) errors.validFrom = "Enter a date.";
  if (h.validTo !== "" && (!datePattern.test(h.validTo) || h.validTo < h.validFrom)) errors.validTo = "Enter a date on or after valid from.";
  lines.forEach((l, i) => {
    const f = (name: string) => `lines[${i}].${name}`;
    const measure = byQuantity(l.commitmentType) ? "committedQty" : "committedAmount";
    if (forProduct(l.commitmentType) && !uuidPattern.test(l.productId)) errors[f("productId")] = "Enter the product id (a UUID).";
    if (l.commitmentType === "CATEGORY_VALUE" && l.category.trim() === "") errors[f("category")] = "Enter a category.";
    if (byQuantity(l.commitmentType) && l.unit.trim() === "") errors[f("unit")] = "Enter a unit.";
    if (!decimalPattern.test(l.committed) || Number(l.committed) <= 0) {
      errors[f(measure)] = byQuantity(l.commitmentType) ? "Enter a positive quantity." : "Enter a positive amount.";
    }
    if (l.unitPrice !== "" && !decimalPattern.test(l.unitPrice)) errors[f("unitPrice")] = "Enter a decimal price.";
    if (l.priceOverride && (l.unitPrice === "" || Number(l.unitPrice) <= 0)) errors[f("unitPrice")] = "Enter the agreed price to override with.";
    if (l.discountPct !== "" && (!decimalPattern.test(l.discountPct) || Number(l.discountPct) > 100)) {
      errors[f("discountPct")] = "Enter a percent from 0 to 100.";
    }
  });
  return errors;
}

function toInput(h: HeaderDraft, lines: LineDraft[]): PurchaseAgreementInput {
  const opt = (v: string) => (v.trim() === "" ? undefined : v.trim());
  return {
    legalEntityId: h.legalEntityId.trim(),
    vendorId: h.vendorId.trim(),
    currency: h.currency,
    status: h.status,
    validFrom: h.validFrom,
    validTo: opt(h.validTo),
    lines: lines.map((l) => {
      const t = l.commitmentType;
      return {
        commitmentType: t,
        productId: forProduct(t) ? l.productId.trim() : undefined,
        category: t === "CATEGORY_VALUE" ? l.category.trim() : undefined,
        unit: byQuantity(t) ? l.unit.trim() : undefined,
        committedQty: byQuantity(t) ? l.committed.trim() : undefined,
        committedAmount: byQuantity(t) ? undefined : l.committed.trim(),
        unitPrice: opt(l.unitPrice),
        discountPct: opt(l.discountPct),
        maxEnforced: l.maxEnforced,
        priceOverride: forProduct(t) && l.priceOverride ? true : undefined,
      };
    }),
  };
}

/** New purchase agreement: header, validity and commitment lines (FR-17.5). */
export function PurchaseAgreementCreatePage() {
  const navigate = useNavigate();
  const [header, setHeader] = useState<HeaderDraft>({
    legalEntityId: "",
    vendorId: "",
    currency: "USD",
    status: "EFFECTIVE",
    validFrom: todayIso(),
    validTo: "",
  });
  const [lines, setLines] = useState<LineDraft[]>(() => [emptyLine()]);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [header, lines]);
  const setH = (patch: Partial<HeaderDraft>) => setHeader((h) => ({ ...h, ...patch }));
  const update = (index: number, patch: Partial<LineDraft>) => setLines((ls) => ls.map((l, i) => (i === index ? { ...l, ...patch } : l)));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateAgreement(header, lines);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await createPurchaseAgreement(toInput(header, lines), key);
      navigate(`/purchasing/agreements/${body.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New purchase agreement"
        description="Commit a quantity or a spend with one vendor. Purchase order lines that reference an agreement line release against it."
      />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <Card title="Agreement">
          <div className={styles.grid}>
            <FormField id="pa-legal-entity" label="Legal entity id" error={errors.legalEntityId}>
              {(c) => <Input {...c} value={header.legalEntityId} onChange={(e) => setH({ legalEntityId: e.target.value })} />}
            </FormField>
            <FormField id="pa-vendor" label="Vendor id" error={errors.vendorId}>
              {(c) => <Input {...c} value={header.vendorId} onChange={(e) => setH({ vendorId: e.target.value })} />}
            </FormField>
            <FormField id="pa-currency" label="Currency" error={errors.currency}>
              {(c) => <Input {...c} value={header.currency} maxLength={3} onChange={(e) => setH({ currency: e.target.value.toUpperCase() })} />}
            </FormField>
            <FormField id="pa-valid-from" label="Valid from" error={errors.validFrom}>
              {(c) => <Input {...c} type="date" value={header.validFrom} onChange={(e) => setH({ validFrom: e.target.value })} />}
            </FormField>
            <FormField id="pa-valid-to" label="Valid to" optional hint="Blank is open-ended" error={errors.validTo}>
              {(c) => <Input {...c} type="date" value={header.validTo} onChange={(e) => setH({ validTo: e.target.value })} />}
            </FormField>
            <FormField id="pa-status" label="Start as">
              {(c) => (
                <Select {...c} value={header.status} onChange={(e) => setH({ status: e.target.value as HeaderDraft["status"] })}>
                  <option value="EFFECTIVE">Effective: releases allowed now</option>
                  <option value="DRAFT">Draft: activate later</option>
                </Select>
              )}
            </FormField>
          </div>
        </Card>
        <Card
          title="Commitments"
          actions={
            <Button size="sm" onClick={() => setLines((ls) => [...ls, emptyLine()])}>
              Add line
            </Button>
          }
        >
          {lines.map((line, i) => {
            const id = (name: string) => `pa-line-${i}-${name}`;
            const err = (name: string) => errors[`lines[${i}].${name}`];
            const t = line.commitmentType;
            return (
              <fieldset key={line.key} className={styles.lineCard} aria-label={`Line ${i + 1}`}>
                <div className={styles.lineHead}>
                  <p className={styles.lineTitle}>Line {i + 1}</p>
                  {lines.length > 1 ? (
                    <Button size="sm" variant="ghost" onClick={() => setLines((ls) => ls.filter((_, j) => j !== i))}>
                      Remove line {i + 1}
                    </Button>
                  ) : null}
                </div>
                <div className={styles.grid}>
                  <FormField id={id("type")} label="Commitment" error={err("commitmentType")}>
                    {(c) => (
                      <Select {...c} value={t} onChange={(e) => update(i, { commitmentType: e.target.value as CommitmentType })}>
                        {(Object.keys(commitmentLabels) as CommitmentType[]).map((k) => (
                          <option key={k} value={k}>
                            {commitmentLabels[k]}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                  {forProduct(t) ? (
                    <FormField id={id("product")} label="Product id" error={err("productId")}>
                      {(c) => <Input {...c} value={line.productId} onChange={(e) => update(i, { productId: e.target.value })} />}
                    </FormField>
                  ) : null}
                  {t === "CATEGORY_VALUE" ? (
                    <FormField id={id("category")} label="Category" error={err("category")}>
                      {(c) => <Input {...c} value={line.category} onChange={(e) => update(i, { category: e.target.value })} />}
                    </FormField>
                  ) : null}
                  {byQuantity(t) ? (
                    <FormField id={id("unit")} label="Unit" error={err("unit")}>
                      {(c) => <Input {...c} value={line.unit} onChange={(e) => update(i, { unit: e.target.value })} />}
                    </FormField>
                  ) : null}
                  <FormField
                    id={id("committed")}
                    label={byQuantity(t) ? "Committed quantity" : "Committed amount"}
                    error={err(byQuantity(t) ? "committedQty" : "committedAmount")}
                  >
                    {(c) => <Input {...c} inputMode="decimal" value={line.committed} onChange={(e) => update(i, { committed: e.target.value })} />}
                  </FormField>
                  <FormField id={id("price")} label="Unit price" optional error={err("unitPrice")}>
                    {(c) => <Input {...c} inputMode="decimal" value={line.unitPrice} onChange={(e) => update(i, { unitPrice: e.target.value })} />}
                  </FormField>
                  <FormField id={id("discount")} label="Discount %" optional error={err("discountPct")}>
                    {(c) => <Input {...c} inputMode="decimal" value={line.discountPct} onChange={(e) => update(i, { discountPct: e.target.value })} />}
                  </FormField>
                  <FormField id={id("enforced")} label="Limit" error={err("maxEnforced")}>
                    {(c) => (
                      <Select {...c} value={line.maxEnforced} onChange={(e) => update(i, { maxEnforced: e.target.value as MaxEnforced })}>
                        {(Object.keys(enforcementLabels) as MaxEnforced[]).map((k) => (
                          <option key={k} value={k}>
                            {enforcementLabels[k]}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                </div>
                {forProduct(t) ? (
                  <label className={styles.checkboxRow}>
                    <Input type="checkbox" checked={line.priceOverride} onChange={(e) => update(i, { priceOverride: e.target.checked })} />
                    Use this price instead of trade agreements on releases
                  </label>
                ) : null}
              </fieldset>
            );
          })}
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save agreement
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/agreements">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

/** What a line commits: the product, category or "all spend". */
function lineItem(l: PurchaseAgreementLine) {
  if (l.productId) return <ShortId id={l.productId} />;
  if (l.category) return l.category;
  return "All spend";
}

const lineColumns = (currency: string): DataTableColumn<PurchaseAgreementLine>[] => {
  const measure = (l: PurchaseAgreementLine, qty: string, amount: string) =>
    byQuantity(l.commitmentType) ? `${qty} ${l.unit ?? ""}`.trim() : money(amount, currency);
  return [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "commitmentType", header: "Commitment", render: (l) => commitmentLabels[l.commitmentType] },
    { key: "item", header: "Item", render: lineItem },
    { key: "committed", header: "Committed", align: "end", render: (l) => measure(l, l.committedQty, l.committedAmount) },
    { key: "released", header: "Released", align: "end", render: (l) => measure(l, l.releasedQty, l.releasedAmount) },
    {
      key: "remaining",
      header: "Remaining",
      align: "end",
      render: (l) => (byQuantity(l.commitmentType) ? `${l.remaining} ${l.unit ?? ""}`.trim() : money(l.remaining, currency)),
    },
    { key: "pctReleased", header: "Released %", align: "end", render: (l) => <ReleaseLevel pct={l.pctReleased} /> },
    { key: "maxEnforced", header: "Limit", render: (l) => statusLabel(l.maxEnforced) },
    {
      key: "price",
      header: "Price",
      align: "end",
      render: (l) => (l.priceOverride ? `${money(l.unitPrice, currency)} (overrides)` : l.unitPrice === "0" ? "—" : money(l.unitPrice, currency)),
    },
  ];
};

const releaseColumns = (agreement: PurchaseAgreement): DataTableColumn<AgreementRelease>[] => [
  {
    key: "purchaseOrderNumber",
    header: "Purchase order",
    render: (r) => (
      <Link className={styles.link} to={`/purchasing/purchase-orders/${r.purchaseOrderId}`}>
        {r.purchaseOrderNumber}
      </Link>
    ),
  },
  { key: "lineNo", header: "PO line", align: "end" },
  {
    key: "agreementLine",
    header: "Agreement line",
    align: "end",
    render: (r) => agreement.lines.find((l) => l.id === r.agreementLineId)?.lineNo ?? "—",
  },
  { key: "qty", header: "Quantity", align: "end", render: (r) => `${r.qty} ${r.unit}` },
  { key: "lineAmount", header: "Amount", align: "end", render: (r) => money(r.lineAmount, agreement.currency) },
  { key: "deliveryDate", header: "Delivery", mono: true },
  { key: "approvalStatus", header: "PO status", render: (r) => <StatusBadge status={r.approvalStatus} /> },
];

/** The status actions each agreement status allows (activate, hold, close). */
export function agreementActions(status: AgreementStatus): { action: AgreementAction; label: string }[] {
  switch (status) {
    case "DRAFT":
      return [{ action: "activate", label: "Activate" }];
    case "EFFECTIVE":
      return [
        { action: "hold", label: "Put on hold" },
        { action: "close", label: "Close" },
      ];
    case "ON_HOLD":
      return [
        { action: "activate", label: "Reactivate" },
        { action: "close", label: "Close" },
      ];
    default:
      return [];
  }
}

/** One agreement: commitments with release position, the releases, and status actions. */
export function PurchaseAgreementDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource((signal) => getPurchaseAgreement(id, signal), [id]);
  const [changed, setChanged] = useState<PurchaseAgreement | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState<AgreementAction | null>(null);
  const agreement = changed ?? resource.data?.body ?? null;
  const actionKey = useMemo(() => newIdempotencyKey(), [agreement?.version]);

  async function run(action: AgreementAction) {
    if (!agreement) return;
    setBusy(action);
    setFailure(null);
    try {
      const { body } = await changePurchaseAgreement(agreement.id, action, agreement.version, `${actionKey}-${action}`);
      setChanged(body);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  if (resource.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Purchase agreement" />
        {resource.error.code === "PRC-AGR-NOT-FOUND" ? (
          <Card>
            <EmptyState
              icon="receipt"
              title="Purchase agreement not found"
              description="It may belong to another legal entity, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/purchasing/agreements">
                  Back to purchase agreements
                </Link>
              }
            />
          </Card>
        ) : (
          <LoadError what="the purchase agreement" error={resource.error} onRetry={resource.reload} />
        )}
      </div>
    );
  }
  if (!agreement) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Purchase agreement" />
        <p className={styles.muted} role="status">
          Loading purchase agreement…
        </p>
      </div>
    );
  }

  const actions = agreementActions(agreement.status);
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Purchase agreement ${agreement.number}`}
        meta={<StatusBadge status={agreement.status} />}
        actions={
          actions.length > 0 ? (
            <div className={styles.actions}>
              {actions.map((a) => (
                <Button
                  key={a.action}
                  variant={a.action === "activate" ? "primary" : "secondary"}
                  loading={busy === a.action}
                  disabled={busy !== null && busy !== a.action}
                  onClick={() => void run(a.action)}
                >
                  {a.label}
                </Button>
              ))}
            </div>
          ) : undefined
        }
      />
      <Alert tone="danger" live="alert">
        {failure ? <ProblemMessage problem={failure} /> : null}
      </Alert>
      <Alert tone="success" live="status">
        {changed ? `The agreement is now ${statusLabel(changed.status).toLowerCase()}.` : null}
      </Alert>
      <Card title="Summary">
        <Details
          items={[
            { label: "Vendor", value: agreement.vendorId, mono: true },
            { label: "Legal entity", value: agreement.legalEntityId, mono: true },
            { label: "Currency", value: agreement.currency, mono: true },
            { label: "Validity", value: validity(agreement), mono: true },
            { label: "Version", value: String(agreement.version), mono: true },
            { label: "Created", value: agreement.createdAt.slice(0, 10), mono: true },
          ]}
        />
      </Card>
      <DataTable
        label="Agreement lines"
        columns={lineColumns(agreement.currency)}
        rows={agreement.lines}
        getRowKey={(l) => l.id}
      />
      <DataTable
        label="Releases"
        columns={releaseColumns(agreement)}
        rows={agreement.releases}
        getRowKey={(r) => `${r.purchaseOrderId}-${r.lineNo}`}
        empty={{
          title: "No releases yet",
          description: "Purchase order lines that name one of these agreement lines appear here and draw it down.",
        }}
      />
    </div>
  );
}
