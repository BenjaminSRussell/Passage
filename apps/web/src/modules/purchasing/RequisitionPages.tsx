import { useMemo, useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useLocation, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Textarea,
  buttonClassName,
  type DataTableColumn,
} from "../../shell/ui";
import {
  actOnRequisition,
  cancelRequisitionLine,
  createRequisition,
  describeProblem,
  getRequisition,
  listRequisitions,
  newIdempotencyKey,
  submitRequisition,
  updateRequisition,
  type Requisition,
  type RequisitionAction,
  type RequisitionHistory,
  type CatalogHit,
  type RequisitionInput,
  type RequisitionLine,
  type RequisitionStatus,
  type RequisitionSummary,
  type Warning,
} from "./api/client";
import { approverRoleLabels } from "./ApprovalPages";
import { CatalogPicker } from "./CatalogPages";
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

const STATUSES: RequisitionStatus[] = ["DRAFT", "IN_REVIEW", "APPROVED", "REJECTED", "CLOSED", "CANCELLED"];

const roleLabels = approverRoleLabels;

const listColumns: DataTableColumn<RequisitionSummary>[] = [
  { key: "number", header: "Number", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusBadge status={row.status} /> },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "total", header: "Estimated total", align: "end", render: (row) => money(row.total, row.currency) },
  { key: "requesterId", header: "Requester", render: (row) => <ShortId id={row.requesterId} /> },
  { key: "createdAt", header: "Created", mono: true, render: (row) => row.createdAt.slice(0, 10) },
];

/** Requisition list: status filter, "only mine", newest first, load-more paging. */
export function RequisitionsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<RequisitionStatus | "">("");
  const [mine, setMine] = useState(false);
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<RequisitionSummary[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listRequisitions({ status, mine }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [status, mine, cursor],
  );

  const resetTo = (apply: () => void) => {
    setCursor(undefined);
    apply();
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Requisitions"
        description="Requests to buy goods and services, routed for approval by amount and category."
        actions={
          <Link className={buttonClassName("primary")} to="/purchasing/requisitions/new">
            New requisition
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="requisition-status-filter" label="Status">
            {(control) => (
              <Select {...control} value={status} onChange={(e) => resetTo(() => setStatus(e.target.value as RequisitionStatus | ""))}>
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
        <label className={styles.checkboxRow}>
          <Input type="checkbox" checked={mine} onChange={(e) => resetTo(() => setMine(e.target.checked))} />
          Only my requisitions
        </label>
      </div>
      {page.error ? <LoadError what="requisitions" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Requisitions"
        columns={listColumns}
        rows={page.error ? [] : rows}
        getRowKey={(row) => row.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "No requisitions yet", description: "Requisitions you or your team raise appear here." }}
        onRowOpen={(row) => navigate(`/purchasing/requisitions/${row.id}`)}
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
  productId: string;
  description: string;
  category: string;
  qty: string;
  unit: string;
  estimatedUnitPrice: string;
  requestedDate: string;
  preferredVendorId: string;
  /** The warehouse the line is wanted in; orders are split by it. */
  warehouseId: string;
  siteId: string;
  /** Set when the line was picked from a catalog; the service fills the item from the entry. */
  catalogEntryId: string;
}

function emptyLine(): LineDraft {
  return {
    key: newIdempotencyKey(),
    productId: "",
    description: "",
    category: "",
    qty: "1",
    unit: "EA",
    estimatedUnitPrice: "",
    requestedDate: todayIso(),
    preferredVendorId: "",
    warehouseId: "",
    siteId: "",
    catalogEntryId: "",
  };
}

/** A saved line as an editable draft; catalog lines keep their entry and the item and price the service took from it. */
function draftOf(l: RequisitionLine): LineDraft {
  return {
    key: l.id || newIdempotencyKey(),
    productId: l.productId ?? "",
    description: l.description ?? "",
    category: l.category ?? "",
    qty: l.qty,
    unit: l.unit,
    estimatedUnitPrice: l.estimatedUnitPrice,
    requestedDate: l.requestedDate,
    preferredVendorId: l.preferredVendorId ?? "",
    warehouseId: l.warehouseId ?? "",
    siteId: l.siteId ?? "",
    catalogEntryId: l.catalogEntryId ?? "",
  };
}

function catalogLine(hit: CatalogHit): LineDraft {
  return {
    ...emptyLine(),
    catalogEntryId: hit.id,
    productId: hit.productId ?? "",
    description: hit.name,
    category: hit.category,
    unit: hit.unit,
    estimatedUnitPrice: hit.unitPrice,
    preferredVendorId: hit.vendorId,
  };
}

type Errors = Record<string, string>;

/** Client-side checks mirroring the service's validation, keyed like its `errors` map. */
export function validateRequisition(legalEntityId: string, currency: string, lines: LineDraft[], operatingUnitId = ""): Errors {
  const errors: Errors = {};
  if (operatingUnitId !== "" && !uuidPattern.test(operatingUnitId)) errors.operatingUnitId = "Operating unit id must be a UUID.";
  if (!uuidPattern.test(legalEntityId)) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!currencyPattern.test(currency)) errors.currency = "Use a three-letter ISO code such as USD.";
  if (lines.length === 0) errors.lines = "Add at least one line.";
  lines.forEach((l, i) => {
    const f = (name: string) => `lines[${i}].${name}`;
    if (!decimalPattern.test(l.qty) || Number(l.qty) <= 0) errors[f("qty")] = "Enter a positive quantity.";
    if (!datePattern.test(l.requestedDate)) errors[f("requestedDate")] = "Enter a date.";
    if (l.siteId !== "" && !uuidPattern.test(l.siteId.trim())) errors[f("siteId")] = "Site id must be a UUID.";
    if (l.catalogEntryId !== "") return;
    if (l.productId !== "" && !uuidPattern.test(l.productId)) errors[f("productId")] = "Product id must be a UUID.";
    if (l.productId === "" && (l.description.trim() === "" || l.category.trim() === "")) {
      errors[f("description")] = "Enter a product id, or a description and a category.";
    }
    if (l.unit.trim() === "") errors[f("unit")] = "Enter a unit.";
    if (l.estimatedUnitPrice !== "" && !decimalPattern.test(l.estimatedUnitPrice)) errors[f("estimatedUnitPrice")] = "Enter a decimal price.";
    if (l.preferredVendorId !== "" && !uuidPattern.test(l.preferredVendorId)) errors[f("preferredVendorId")] = "Vendor id must be a UUID.";
    if (l.warehouseId !== "" && !uuidPattern.test(l.warehouseId.trim())) errors[f("warehouseId")] = "Warehouse id must be a UUID.";
  });
  return errors;
}

function toInput(legalEntityId: string, currency: string, lines: LineDraft[], operatingUnitId = ""): RequisitionInput {
  const opt = (v: string) => (v.trim() === "" ? undefined : v.trim());
  return {
    legalEntityId,
    operatingUnitId: opt(operatingUnitId),
    currency,
    lines: lines.map((l) =>
      l.catalogEntryId !== ""
        ? { catalogEntryId: l.catalogEntryId, qty: l.qty.trim(), unit: l.unit.trim(), requestedDate: l.requestedDate, warehouseId: opt(l.warehouseId), siteId: opt(l.siteId) }
        : {
      productId: opt(l.productId),
      description: opt(l.description),
      category: opt(l.category),
      qty: l.qty.trim(),
      unit: l.unit.trim(),
      estimatedUnitPrice: opt(l.estimatedUnitPrice),
      requestedDate: l.requestedDate,
      preferredVendorId: opt(l.preferredVendorId),
      warehouseId: opt(l.warehouseId),
      siteId: opt(l.siteId),
    },
    ),
  };
}

/** A line the requester has not touched, replaced by the first catalog pick. */
function isBlankLine(l: LineDraft): boolean {
  return l.catalogEntryId === "" && l.productId === "" && l.description === "" && l.category === "" && l.estimatedUnitPrice === "" && l.preferredVendorId === "";
}

/** New requisition: header plus any number of lines; saves a DRAFT and opens it. */
export function RequisitionCreatePage() {
  return <RequisitionForm />;
}

/** Edit a DRAFT requisition (a new one, or one reopened or withdrawn): the lines are saved as a set (BR-17.1). */
export function RequisitionEditPage() {
  const { id = "" } = useParams();
  const resource = useResource((signal) => getRequisition(id, signal), [id]);
  if (resource.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Edit requisition" />
        <LoadError what="the requisition" error={resource.error} onRetry={resource.reload} />
      </div>
    );
  }
  const req = resource.data?.body;
  if (!req) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Edit requisition" />
        <p className={styles.muted} role="status">
          Loading requisition…
        </p>
      </div>
    );
  }
  if (req.status !== "DRAFT") {
    return (
      <div className={styles.stack}>
        <PageHeader title={`Edit requisition ${req.number}`} meta={<StatusBadge status={req.status} />} />
        <Alert tone="warning" live="status">
          Only a draft can be edited. Withdraw it from review, or reopen it after a rejection, first.
        </Alert>
        <div className={styles.actions}>
          <Link className={buttonClassName("secondary")} to={`/purchasing/requisitions/${req.id}`}>
            Back to the requisition
          </Link>
        </div>
      </div>
    );
  }
  return <RequisitionForm existing={req} />;
}

function RequisitionForm({ existing }: { existing?: Requisition }) {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState(existing?.legalEntityId ?? "");
  const [operatingUnitId, setOperatingUnitId] = useState(existing?.operatingUnitId ?? "");
  const [currency, setCurrency] = useState(existing?.currency ?? "USD");
  const [lines, setLines] = useState<LineDraft[]>(() => (existing ? existing.lines.map(draftOf) : [emptyLine()]));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per logical create so a retry after a network failure cannot duplicate it.
  const key = useMemo(() => newIdempotencyKey(), [legalEntityId, operatingUnitId, currency, lines]);

  const update = (index: number, patch: Partial<LineDraft>) => setLines((ls) => ls.map((l, i) => (i === index ? { ...l, ...patch } : l)));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateRequisition(legalEntityId.trim(), currency, lines, operatingUnitId.trim());
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      if (existing) {
        const input = toInput(legalEntityId.trim(), currency, lines, operatingUnitId.trim());
        const { body } = await updateRequisition(
          existing.id,
          existing.version,
          { operatingUnitId: operatingUnitId.trim(), currency: input.currency, lines: input.lines },
          key,
        );
        navigate(`/purchasing/requisitions/${body.id}`, { state: { warnings: body.warnings ?? [] } });
        return;
      }
      const { body } = await createRequisition(toInput(legalEntityId.trim(), currency, lines, operatingUnitId.trim()), key);
      navigate(`/purchasing/requisitions/${body.id}`, { state: { warnings: body.warnings ?? [] } });
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  const field = (name: string) => errors[name];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={existing ? `Edit requisition ${existing.number}` : "New requisition"}
        description={
          existing
            ? "Saving replaces the lines and prices them again; leave an estimate blank to take the preferred vendor's trade agreement price."
            : "Leave the estimate blank to take the preferred vendor's trade agreement price, or the price of the product's last approved order."
        }
      />
      <CatalogPicker
        legalEntityId={legalEntityId.trim()}
        operatingUnitId={operatingUnitId.trim()}
        onPick={(hit) => {
          if (hit.currency !== currency) setCurrency(hit.currency);
          setLines((ls) => [...ls.filter((l) => !isBlankLine(l)), catalogLine(hit)]);
        }}
      />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <Card title="Requisition">
          <div className={styles.grid}>
            <FormField id="req-legal-entity" label="Legal entity" error={field("legalEntityId")}>
              {(control) => <EntityLookup entity="legalEntity" {...control} value={legalEntityId} disabled={existing !== undefined} onChange={(e) => setLegalEntityId(e.target.value)} />}
            </FormField>
            <FormField id="req-operating-unit" label="Operating unit id" optional hint="Decides which catalogs you can pick from" error={field("operatingUnitId")}>
              {(control) => <Input {...control} value={operatingUnitId} onChange={(e) => setOperatingUnitId(e.target.value)} />}
            </FormField>
            <FormField id="req-currency" label="Currency" error={field("currency")}>
              {(control) => (
                <Input {...control} value={currency} maxLength={3} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />
              )}
            </FormField>
          </div>
        </Card>
        <Card
          title="Lines"
          actions={
            <Button size="sm" onClick={() => setLines((ls) => [...ls, emptyLine()])}>
              Add line
            </Button>
          }
        >
          {field("lines") ? <p className={styles.muted}>{field("lines")}</p> : null}
          {lines.map((line, i) => {
            const id = (name: string) => `req-line-${i}-${name}`;
            const err = (name: string) => field(`lines[${i}].${name}`);
            const fromCatalog = line.catalogEntryId !== "";
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
                {fromCatalog ? (
                  <>
                    <p className={styles.muted}>
                      Catalog item: {line.description} at {money(line.estimatedUnitPrice, currency)} per {line.unit}
                    </p>
                    <div className={styles.grid}>
                      <FormField id={id("qty")} label="Quantity" error={err("qty")}>
                        {(c) => <Input {...c} inputMode="decimal" value={line.qty} onChange={(e) => update(i, { qty: e.target.value })} />}
                      </FormField>
                      <FormField id={id("date")} label="Requested date" error={err("requestedDate")}>
                        {(c) => <Input {...c} type="date" value={line.requestedDate} onChange={(e) => update(i, { requestedDate: e.target.value })} />}
                      </FormField>
                      <FormField id={id("warehouse")} label="Deliver to warehouse" optional error={err("warehouseId")}>
                        {(c) => <EntityLookup entity="warehouse" {...c} value={line.warehouseId} onChange={(e) => update(i, { warehouseId: e.target.value })} />}
                      </FormField>
                      <FormField id={id("site")} label="Site" optional error={err("siteId")}>
                        {(c) => <EntityLookup entity="site" prefill={false} {...c} value={line.siteId} onChange={(e) => update(i, { siteId: e.target.value })} />}
                      </FormField>
                    </div>
                  </>
                ) : null}
                {fromCatalog ? null : (
                <div className={styles.grid}>
                  <FormField id={id("product")} label="Product" optional error={err("productId")}>
                    {(c) => <EntityLookup entity="product" {...c} value={line.productId} onChange={(e) => update(i, { productId: e.target.value })} />}
                  </FormField>
                  <FormField id={id("description")} label="Description" error={err("description")}>
                    {(c) => <Input {...c} value={line.description} onChange={(e) => update(i, { description: e.target.value })} />}
                  </FormField>
                  <FormField id={id("category")} label="Category">
                    {(c) => <Input {...c} value={line.category} onChange={(e) => update(i, { category: e.target.value })} />}
                  </FormField>
                  <FormField id={id("qty")} label="Quantity" error={err("qty")}>
                    {(c) => <Input {...c} inputMode="decimal" value={line.qty} onChange={(e) => update(i, { qty: e.target.value })} />}
                  </FormField>
                  <FormField id={id("unit")} label="Unit" error={err("unit")}>
                    {(c) => <Input {...c} value={line.unit} onChange={(e) => update(i, { unit: e.target.value })} />}
                  </FormField>
                  <FormField id={id("price")} label="Estimated unit price" optional error={err("estimatedUnitPrice")}>
                    {(c) => (
                      <Input {...c} inputMode="decimal" value={line.estimatedUnitPrice} onChange={(e) => update(i, { estimatedUnitPrice: e.target.value })} />
                    )}
                  </FormField>
                  <FormField id={id("date")} label="Requested date" error={err("requestedDate")}>
                    {(c) => <Input {...c} type="date" value={line.requestedDate} onChange={(e) => update(i, { requestedDate: e.target.value })} />}
                  </FormField>
                  <FormField id={id("vendor")} label="Preferred vendor" optional error={err("preferredVendorId")}>
                    {(c) => <EntityLookup entity="vendor" scope={{ legalEntityId: legalEntityId || undefined }} {...c} value={line.preferredVendorId} onChange={(e) => update(i, { preferredVendorId: e.target.value })} />}
                  </FormField>
                  <FormField id={id("warehouse")} label="Deliver to warehouse" optional error={err("warehouseId")}>
                    {(c) => <EntityLookup entity="warehouse" {...c} value={line.warehouseId} onChange={(e) => update(i, { warehouseId: e.target.value })} />}
                  </FormField>
                  <FormField id={id("site")} label="Site" optional error={err("siteId")}>
                    {(c) => <EntityLookup entity="site" prefill={false} {...c} value={line.siteId} onChange={(e) => update(i, { siteId: e.target.value })} />}
                  </FormField>
                </div>
                )}
              </fieldset>
            );
          })}
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            {existing ? "Save changes" : "Save requisition"}
          </Button>
          <Link className={buttonClassName("ghost")} to={existing ? `/purchasing/requisitions/${existing.id}` : "/purchasing/requisitions"}>
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

const lineColumns = (
  currency: string,
  all: RequisitionLine[],
  onCancelLine?: (line: RequisitionLine) => void,
): DataTableColumn<RequisitionLine>[] => [
  { key: "lineNo", header: "Line", align: "end" },
  {
    key: "item",
    header: "Item",
    render: (l) => {
      const origin = l.splitFromLineId ? all.find((o) => o.id === l.splitFromLineId) : undefined;
      return (
        <>
          {l.description || <ShortId id={l.productId} />}
          {origin ? <span className={styles.muted}> (split from line {origin.lineNo})</span> : null}
        </>
      );
    },
  },
  { key: "category", header: "Category", render: (l) => l.category || "—" },
  { key: "source", header: "Source", render: (l) => (l.catalogEntryId ? "Catalog" : "Non-catalog") },
  { key: "qty", header: "Quantity", align: "end", render: (l) => `${l.qty} ${l.unit}` },
  { key: "estimatedUnitPrice", header: "Estimate", align: "end", render: (l) => money(l.estimatedUnitPrice, currency) },
  { key: "amount", header: "Amount", align: "end", render: (l) => money(l.amount, currency) },
  { key: "requestedDate", header: "Requested", mono: true },
  { key: "warehouseId", header: "Warehouse", render: (l) => <ShortId id={l.warehouseId} /> },
  { key: "status", header: "Status", render: (l) => <StatusBadge status={l.status} /> },
  {
    key: "purchaseOrder",
    header: "Purchase order",
    render: (l) =>
      l.purchaseOrderId ? (
        <Link className={styles.link} to={`/purchasing/purchase-orders/${l.purchaseOrderId}`}>
          Line {l.purchaseOrderLineNo} of the order
        </Link>
      ) : (
        "—"
      ),
  },
  ...(onCancelLine
    ? [
        {
          key: "actions",
          header: "Actions",
          render: (l: RequisitionLine) =>
            l.status === "OPEN" ? (
              <Button size="sm" variant="ghost" onClick={() => onCancelLine(l)}>
                Cancel line {l.lineNo}
              </Button>
            ) : null,
        } satisfies DataTableColumn<RequisitionLine>,
      ]
    : []),
];

const actionLabels: Record<RequisitionHistory["action"], string> = {
  SUBMITTED: "Submitted",
  APPROVED: "Approved",
  REJECTED: "Rejected",
  REOPENED: "Reopened as a draft",
  CANCELLED: "Cancelled",
  EDITED: "Edited",
  WITHDRAWN: "Withdrawn from review",
  LINE_CANCELLED: "Line cancelled",
};

const historyColumns = (lines: RequisitionLine[]): DataTableColumn<RequisitionHistory>[] => [
  { key: "createdAt", header: "When", mono: true, render: (h) => h.createdAt.slice(0, 16).replace("T", " ") },
  { key: "action", header: "Action", render: (h) => actionLabels[h.action] ?? h.action },
  { key: "stepNo", header: "Level", render: (h) => (h.stepNo ? `${h.stepNo}. ${roleLabels[h.role ?? ""] ?? h.role ?? ""}` : "—") },
  { key: "actorId", header: "By", render: (h) => <ShortId id={h.actorId} /> },
  {
    key: "comment",
    header: "Comment",
    render: (h) => {
      const line = h.lineId ? lines.find((l) => l.id === h.lineId) : undefined;
      const text = [line ? `Line ${line.lineNo}` : "", h.comment ?? ""].filter(Boolean).join(": ");
      return text || "—";
    },
  },
];

const doneMessages: Record<RequisitionAction, string> = {
  approve: "Approved.",
  reject: "Sent back to the requester.",
  reopen: "Reopened as a draft; submit it again when it is ready.",
  cancel: "Requisition cancelled.",
  withdraw: "Withdrawn from review; edit it and submit it again.",
};

/** Requisition detail: header, approval progress, decisions, lines, history, and the actions each status allows. */
export function RequisitionDetailPage() {
  const { id = "" } = useParams();
  // Warnings (PRC-REQ-003) come back only on create; the create page hands them over in router state.
  const initialWarnings = (useLocation().state as { warnings?: Warning[] } | null)?.warnings ?? [];
  const resource = useResource((signal) => getRequisition(id, signal), [id]);
  const [updated, setUpdated] = useState<Requisition | null>(null);
  const [submitted, setSubmitted] = useState<Requisition | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [comment, setComment] = useState("");
  const [commentError, setCommentError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState<RequisitionAction | "submit" | null>(null);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [cancelLine, setCancelLine] = useState<RequisitionLine | null>(null);
  const keys = useRef<Record<string, string>>({});
  const req = updated ?? submitted ?? resource.data?.body ?? null;

  /** One Idempotency-Key per action and version, so a retry of the same click replays instead of repeating. */
  function keyFor(action: string, version: number) {
    const k = `${action}:${version}`;
    keys.current[k] ??= newIdempotencyKey();
    return keys.current[k];
  }

  async function submit() {
    if (!req) return;
    setBusy("submit");
    setFailure(null);
    setNotice(null);
    try {
      const { body } = await submitRequisition(req.id, req.version, keyFor("submit", req.version));
      setSubmitted(body);
      setUpdated(null);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function act(action: RequisitionAction) {
    if (!req) return;
    const text = comment.trim();
    if (action === "reject" && !text) {
      setCommentError("Say why you are sending it back.");
      return;
    }
    setCommentError(undefined);
    setBusy(action);
    setFailure(null);
    setNotice(null);
    try {
      const { body } = await actOnRequisition(req.id, action, req.version, text, keyFor(action, req.version));
      setUpdated(body);
      setSubmitted(null);
      setComment("");
      setNotice(action === "approve" && body.status === "APPROVED" ? "Approved: the last level has approved." : doneMessages[action]);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function cancelOneLine(line: RequisitionLine) {
    if (!req) return;
    setBusy("cancel");
    setFailure(null);
    setNotice(null);
    try {
      const { body } = await cancelRequisitionLine(req.id, req.version, line.id, "", keyFor(`cancel-line-${line.id}`, req.version));
      setUpdated(body);
      setSubmitted(null);
      setNotice(
        body.status === "CLOSED"
          ? `Line ${line.lineNo} cancelled; every other line is already ordered, so the requisition is closed.`
          : `Line ${line.lineNo} cancelled; the rest of the requisition continues.`,
      );
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
      setCancelLine(null);
    }
  }

  if (resource.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Requisition" />
        {resource.error.code === "PRC-REQ-NOT-FOUND" ? (
          <Card>
            <EmptyState
              icon="receipt"
              title="Requisition not found"
              description="It may belong to another legal entity, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/purchasing/requisitions">
                  Back to requisitions
                </Link>
              }
            />
          </Card>
        ) : (
          <LoadError what="the requisition" error={resource.error} onRetry={resource.reload} />
        )}
      </div>
    );
  }
  if (!req) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Requisition" />
        <p className={styles.muted} role="status">
          Loading requisition…
        </p>
      </div>
    );
  }

  const chain = req.approverChain ?? [];
  const steps = req.approvedSteps ?? 0;
  const history = req.history ?? [];
  const stale = failure?.code === "PRC-VERSION-CONFLICT" || failure?.code === "PRC-REQ-STATE";
  const liveLines = req.lines.filter((l) => l.status !== "CANCELLED").length;
  const canCancelLine = req.status === "APPROVED" && liveLines > 1;
  const canWithdraw = req.status === "IN_REVIEW" && steps === 0;
  const canCancel = req.status === "DRAFT" || req.status === "REJECTED" || req.status === "APPROVED";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Requisition ${req.number}`}
        meta={<StatusBadge status={req.status} />}
        actions={
          <>
            {req.status === "DRAFT" ? (
              <Link className={buttonClassName("secondary")} to={`/purchasing/requisitions/${req.id}/edit`}>
                Edit
              </Link>
            ) : null}
            {canWithdraw ? (
              <Button variant="secondary" loading={busy === "withdraw"} onClick={() => void act("withdraw")}>
                Withdraw
              </Button>
            ) : null}
            {req.status === "DRAFT" ? (
              <Button variant="primary" loading={busy === "submit"} onClick={() => void submit()}>
                Submit for approval
              </Button>
            ) : null}
            {req.status === "REJECTED" ? (
              <Button variant="primary" loading={busy === "reopen"} onClick={() => void act("reopen")}>
                Reopen as a draft
              </Button>
            ) : null}
            {canCancel ? (
              <Button variant="secondary" loading={busy === "cancel"} onClick={() => setConfirmCancel(true)}>
                Cancel requisition
              </Button>
            ) : null}
          </>
        }
      />
      <Alert
        tone="danger"
        live="alert"
        action={
          stale ? (
            <Button
              type="button"
              variant="secondary"
              onClick={() => {
                setUpdated(null);
                setSubmitted(null);
                setFailure(null);
                resource.reload();
              }}
            >
              Reload requisition
            </Button>
          ) : undefined
        }
      >
        {failure ? <ProblemMessage problem={failure} /> : null}
      </Alert>
      <Alert tone="success" live="status">
        {submitted
          ? submitted.status === "APPROVED"
            ? "Submitted and approved: the amount is in the auto-approval band."
            : `Submitted for approval to ${chain.map((r) => roleLabels[r] ?? r).join(", then ")}.`
          : notice}
      </Alert>
      {submitted?.warnings && submitted.warnings.length > 0 ? (
        <Alert tone="warning" title="Check the vendors against their categories">
          <ul className={styles.cardLinks}>
            {submitted.warnings.map((w) => (
              <li key={`${w.code}-${w.lineNo}`}>
                {w.detail} ({w.code})
              </li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {initialWarnings.length > 0 ? (
        <Alert tone="warning" title="Check these estimates">
          <ul className={styles.cardLinks}>
            {initialWarnings.map((w) => (
              <li key={`${w.code}-${w.lineNo}`}>
                Line {w.lineNo}: {w.detail} ({w.code})
              </li>
            ))}
          </ul>
        </Alert>
      ) : null}
      <Card title="Summary">
        <Details
          items={[
            { label: "Estimated total", value: money(req.total, req.currency), mono: true },
            { label: "Legal entity", value: req.legalEntityId, mono: true },
            { label: "Requester", value: req.requesterId, mono: true },
            { label: "Policy version", value: req.policyVersion ?? "" },
            { label: "Version", value: String(req.version), mono: true },
            { label: "Created", value: req.createdAt.slice(0, 10), mono: true },
          ]}
        />
      </Card>
      <Card title="Approval">
        {req.status === "DRAFT" ? (
          <p className={styles.muted}>The approver chain is set when you submit, from the amount band and line categories.</p>
        ) : chain.length === 0 ? (
          <p className={styles.muted}>
            {req.status === "APPROVED" ? "Approved automatically: the total is within the auto-approval band." : "No approval levels."}
          </p>
        ) : (
          <>
            <ol className={styles.chain} aria-label="Approver chain">
              {chain.map((role, i) => (
                <li key={`${role}-${i}`}>
                  <Badge tone={i < steps ? "success" : req.status === "IN_REVIEW" && i === steps ? "warning" : "info"}>
                    {i + 1}. {roleLabels[role] ?? role}
                    {i < steps ? " (approved)" : ""}
                  </Badge>
                </li>
              ))}
            </ol>
            {req.status === "IN_REVIEW" && req.nextApprover ? (
              <p className={styles.muted}>Waiting for {(roleLabels[req.nextApprover] ?? req.nextApprover).toLowerCase()}.</p>
            ) : null}
          </>
        )}
      </Card>
      {req.status === "IN_REVIEW" ? (
        <Card title="Decision">
          <FormField
            id="requisition-decision-comment"
            label="Comment"
            optional
            hint="Required to send it back; kept in the history."
            error={commentError}
          >
            {(c) => <Textarea {...c} rows={3} value={comment} onChange={(e) => setComment(e.target.value)} />}
          </FormField>
          <div className={styles.actions}>
            <Button variant="primary" loading={busy === "approve"} onClick={() => void act("approve")}>
              Approve
            </Button>
            <Button variant="secondary" loading={busy === "reject"} onClick={() => void act("reject")}>
              Send back
            </Button>
          </div>
        </Card>
      ) : null}
      <DataTable
        label="Requisition lines"
        columns={lineColumns(req.currency, req.lines, canCancelLine ? setCancelLine : undefined)}
        rows={req.lines} getRowKey={(l) => l.id || String(l.lineNo)}
      />
      {history.length > 0 ? (
        <DataTable label="Requisition history" columns={historyColumns(req.lines)} rows={history} getRowKey={(h) => h.id} />
      ) : null}
      {cancelLine ? (
        <ConfirmDialog
          title={`Cancel line ${cancelLine.lineNo} of ${req.number}?`}
          description="The rest of the requisition continues. Budget control releases the amount of this line. This cannot be undone."
          confirmLabel={`Cancel line ${cancelLine.lineNo}`}
          cancelLabel="Keep it"
          onCancel={() => setCancelLine(null)}
          onConfirm={() => void cancelOneLine(cancelLine)}
        />
      ) : null}
      {confirmCancel ? (
        <ConfirmDialog
          title={`Cancel requisition ${req.number}?`}
          description={
            req.status === "APPROVED"
              ? "Its lines leave the consolidation queue and nothing can be ordered from it. This cannot be undone."
              : "It cannot be submitted again. This cannot be undone."
          }
          confirmLabel="Cancel requisition"
          cancelLabel="Keep it"
          onCancel={() => setConfirmCancel(false)}
          onConfirm={() => {
            setConfirmCancel(false);
            void act("cancel");
          }}
        />
      ) : null}
    </div>
  );
}
