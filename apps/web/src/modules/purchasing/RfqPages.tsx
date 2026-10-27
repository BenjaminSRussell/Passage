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
  Textarea,
  buttonClassName,
  type DataTableColumn,
} from "../../shell/ui";
import {
  awardRFQ,
  changeRFQ,
  compareRFQ,
  createRFQ,
  describeProblem,
  getRFQ,
  listRFQs,
  newIdempotencyKey,
  recordRFQReplies,
  sendRFQ,
  type PurchaseOrder,
  type RankingRule,
  type RFQ,
  type RFQAward,
  type RFQComparisonLine,
  type RFQInput,
  type RFQInvitation,
  type RFQLine,
  type RFQReply,
  type RFQReplyInput,
  type RFQStatus,
  type RFQSummary,
  type Warning,
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
  useResource,
  uuidPattern,
  type Failure,
} from "./parts";
import styles from "./pages.module.css";

const STATUSES: RFQStatus[] = ["DRAFT", "SENT", "REPLIES_OPEN", "CLOSED", "AWARDED", "CANCELLED"];

export const rankingLabels: Record<RankingRule, string> = {
  LOWEST_PRICE: "Lowest price, then lead time",
  LEAD_TIME: "Shortest lead time, then price",
};

/** An instant as "2026-11-01 17:00 UTC", whatever offset the service used. */
export function formatInstant(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${d.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** What a line asks for: its description, else the product id. */
function lineItem(l: { productId?: string; description?: string }) {
  return l.description ? l.description : <ShortId id={l.productId} />;
}

const listColumns: DataTableColumn<RFQSummary>[] = [
  { key: "number", header: "Number", mono: true },
  { key: "status", header: "Status", render: (r) => <StatusBadge status={r.status} /> },
  { key: "replyDeadline", header: "Reply deadline", mono: true, render: (r) => formatInstant(r.replyDeadline) },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "vendorCount", header: "Vendors", align: "end" },
  { key: "replyCount", header: "Replies", align: "end" },
];

/** RFQ list with a status filter and load-more paging. */
export function RFQsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<RFQStatus | "">("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<RFQSummary[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listRFQs({ statuses: status ? [status] : [] }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [status, cursor],
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Requests for quotation"
        description="Ask several vendors to quote, compare their replies line by line, and award the winners as purchase orders."
        actions={
          <Link className={buttonClassName("primary")} to="/purchasing/rfqs/new">
            New RFQ
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="rfq-status-filter" label="Status">
            {(control) => (
              <Select
                {...control}
                value={status}
                onChange={(e) => {
                  setCursor(undefined);
                  setStatus(e.target.value as RFQStatus | "");
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
      {page.error ? <LoadError what="requests for quotation" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Requests for quotation"
        columns={listColumns}
        rows={page.error ? [] : rows}
        getRowKey={(r) => r.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "No requests for quotation yet", description: "Create one for the lines that need a vendor, then send it to the vendors you want quotes from." }}
        onRowOpen={(r) => navigate(`/purchasing/rfqs/${r.id}`)}
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
  qty: string;
  unit: string;
  requiredDate: string;
}

interface HeaderDraft {
  legalEntityId: string;
  /** datetime-local value, the user's local time. */
  deadline: string;
  rankingRule: RankingRule;
}

type Errors = Record<string, string>;

function emptyLine(): LineDraft {
  return { key: newIdempotencyKey(), productId: "", description: "", qty: "", unit: "EA", requiredDate: "" };
}

/** A week from now at 17:00 local time, as a datetime-local value. */
export function defaultDeadline(now = new Date()): string {
  const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 7, 17, 0);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T17:00`;
}

/** Mirrors the service rules so most mistakes are caught before the round trip. */
export function validateRFQ(h: HeaderDraft, lines: LineDraft[], now = new Date()): Errors {
  const errors: Errors = {};
  if (!uuidPattern.test(h.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  const deadline = new Date(h.deadline);
  if (h.deadline === "" || Number.isNaN(deadline.getTime())) errors.replyDeadline = "Enter the reply deadline.";
  else if (deadline <= now) errors.replyDeadline = "Choose a deadline in the future.";
  lines.forEach((l, i) => {
    const f = (name: string) => `lines[${i}].${name}`;
    const product = l.productId.trim();
    if (product !== "" && !uuidPattern.test(product)) errors[f("productId")] = "Enter the product id (a UUID).";
    if (product === "" && l.description.trim() === "") errors[f("productId")] = "Enter a product id or a description.";
    if (!decimalPattern.test(l.qty.trim()) || Number(l.qty) <= 0) errors[f("qty")] = "Enter a positive quantity.";
    if (l.unit.trim() === "") errors[f("unit")] = "Enter a unit.";
    if (l.requiredDate !== "" && !datePattern.test(l.requiredDate)) errors[f("requiredDate")] = "Enter a date.";
  });
  return errors;
}

function toInput(h: HeaderDraft, lines: LineDraft[]): RFQInput {
  const opt = (v: string) => (v.trim() === "" ? undefined : v.trim());
  return {
    legalEntityId: h.legalEntityId.trim(),
    replyDeadline: new Date(h.deadline).toISOString(),
    rankingRule: h.rankingRule,
    lines: lines.map((l) => ({
      productId: opt(l.productId),
      description: opt(l.description),
      qty: l.qty.trim(),
      unit: l.unit.trim(),
      requiredDate: opt(l.requiredDate),
    })),
  };
}

/** New RFQ: legal entity, reply deadline, ranking rule and the lines to quote (FR-17.4). */
export function RFQCreatePage() {
  const navigate = useNavigate();
  const [header, setHeader] = useState<HeaderDraft>({ legalEntityId: "", deadline: defaultDeadline(), rankingRule: "LOWEST_PRICE" });
  const [lines, setLines] = useState<LineDraft[]>(() => [emptyLine()]);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [header, lines]);
  const setH = (patch: Partial<HeaderDraft>) => setHeader((h) => ({ ...h, ...patch }));
  const update = (index: number, patch: Partial<LineDraft>) => setLines((ls) => ls.map((l, i) => (i === index ? { ...l, ...patch } : l)));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateRFQ(header, lines);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await createRFQ(toInput(header, lines), key);
      navigate(`/purchasing/rfqs/${body.id}`);
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
        title="New request for quotation"
        description="The RFQ is saved as a draft. Send it to vendors from its page; replies after the deadline are kept but left out of the ranking."
      />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <Card title="Request">
          <div className={styles.grid}>
            <FormField id="rfq-legal-entity" label="Legal entity id" error={errors.legalEntityId}>
              {(c) => <Input {...c} value={header.legalEntityId} onChange={(e) => setH({ legalEntityId: e.target.value })} />}
            </FormField>
            <FormField id="rfq-deadline" label="Reply deadline" hint="Your local time" error={errors.replyDeadline}>
              {(c) => <Input {...c} type="datetime-local" value={header.deadline} onChange={(e) => setH({ deadline: e.target.value })} />}
            </FormField>
            <FormField id="rfq-ranking" label="Rank replies by" error={errors.rankingRule}>
              {(c) => (
                <Select {...c} value={header.rankingRule} onChange={(e) => setH({ rankingRule: e.target.value as RankingRule })}>
                  {(Object.keys(rankingLabels) as RankingRule[]).map((k) => (
                    <option key={k} value={k}>
                      {rankingLabels[k]}
                    </option>
                  ))}
                </Select>
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
          {lines.map((line, i) => {
            const id = (name: string) => `rfq-line-${i}-${name}`;
            const err = (name: string) => errors[`lines[${i}].${name}`];
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
                  <FormField id={id("product")} label="Product id" optional error={err("productId")}>
                    {(c) => <Input {...c} value={line.productId} onChange={(e) => update(i, { productId: e.target.value })} />}
                  </FormField>
                  <FormField id={id("description")} label="Description" optional hint="Needed when there is no product" error={err("description")}>
                    {(c) => <Input {...c} value={line.description} onChange={(e) => update(i, { description: e.target.value })} />}
                  </FormField>
                  <FormField id={id("qty")} label="Quantity" error={err("qty")}>
                    {(c) => <Input {...c} inputMode="decimal" value={line.qty} onChange={(e) => update(i, { qty: e.target.value })} />}
                  </FormField>
                  <FormField id={id("unit")} label="Unit" error={err("unit")}>
                    {(c) => <Input {...c} value={line.unit} onChange={(e) => update(i, { unit: e.target.value })} />}
                  </FormField>
                  <FormField id={id("required")} label="Required by" optional error={err("requiredDate")}>
                    {(c) => <Input {...c} type="date" value={line.requiredDate} onChange={(e) => update(i, { requiredDate: e.target.value })} />}
                  </FormField>
                </div>
              </fieldset>
            );
          })}
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save RFQ
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/rfqs">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

const lineColumns: DataTableColumn<RFQLine>[] = [
  { key: "lineNo", header: "Line", align: "end" },
  { key: "item", header: "Item", render: lineItem },
  { key: "qty", header: "Quantity", align: "end", render: (l) => `${l.qty} ${l.unit}` },
  { key: "requiredDate", header: "Required by", mono: true, render: (l) => l.requiredDate ?? "—" },
];

const invitationColumns: DataTableColumn<RFQInvitation>[] = [
  { key: "vendorId", header: "Vendor", mono: true },
  { key: "status", header: "Status", render: (i) => <StatusBadge status={i.status} /> },
  { key: "sentAt", header: "Sent", mono: true, render: (i) => formatInstant(i.sentAt) },
];

const replyColumns = (rfq: RFQ): DataTableColumn<RFQReply>[] => [
  { key: "line", header: "Line", align: "end", render: (r) => rfq.lines.find((l) => l.id === r.lineId)?.lineNo ?? "—" },
  { key: "vendorId", header: "Vendor", render: (r) => <ShortId id={r.vendorId} /> },
  { key: "unitPrice", header: "Price", align: "end", render: (r) => `${money(r.unitPrice, r.currency)} / ${r.priceUnit}` },
  { key: "leadTimeDays", header: "Lead time", align: "end", render: (r) => `${r.leadTimeDays} d` },
  { key: "validUntil", header: "Valid until", mono: true, render: (r) => r.validUntil ?? "—" },
  { key: "receivedAt", header: "Received", mono: true, render: (r) => formatInstant(r.receivedAt) },
  {
    key: "flags",
    header: "Flags",
    render: (r) => (
      <span className={styles.actions}>
        {r.late ? <Badge tone="warning">Late</Badge> : null}
        {r.awarded ? <Badge tone="success">Awarded</Badge> : null}
        {!r.late && !r.awarded ? "—" : null}
      </span>
    ),
  },
];

/** The close and cancel actions each status allows; send has its own form. */
export function rfqActions(status: RFQStatus): { action: "close" | "cancel"; label: string }[] {
  switch (status) {
    case "DRAFT":
      return [{ action: "cancel", label: "Cancel RFQ" }];
    case "SENT":
    case "REPLIES_OPEN":
      return [
        { action: "close", label: "Close replies" },
        { action: "cancel", label: "Cancel RFQ" },
      ];
    case "CLOSED":
      return [{ action: "cancel", label: "Cancel RFQ" }];
    default:
      return [];
  }
}

/** Splits pasted vendor ids on commas, spaces and new lines. */
export function parseVendorIds(raw: string): string[] {
  return raw
    .split(/[\s,]+/)
    .map((v) => v.trim())
    .filter((v) => v !== "");
}

/** One RFQ: summary, lines, the send form, invitations, replies, reply entry, and compare and award. */
export function RFQDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource((signal) => getRFQ(id, signal), [id]);
  const [changed, setChanged] = useState<RFQ | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [awardedOrders, setAwardedOrders] = useState<PurchaseOrder[]>([]);
  const rfq = changed ?? resource.data?.body ?? null;
  const actionKey = useMemo(() => newIdempotencyKey(), [rfq?.version]);

  async function run(label: string, call: () => Promise<RFQ>, done: (r: RFQ) => string) {
    setBusy(label);
    setFailure(null);
    setNotice(null);
    try {
      const next = await call();
      setChanged(next);
      setNotice(done(next));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  if (resource.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Request for quotation" />
        {resource.error.code === "PRC-RFQ-NOT-FOUND" ? (
          <Card>
            <EmptyState
              icon="receipt"
              title="Request for quotation not found"
              description="It may belong to another legal entity, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/purchasing/rfqs">
                  Back to requests for quotation
                </Link>
              }
            />
          </Card>
        ) : (
          <LoadError what="the request for quotation" error={resource.error} onRetry={resource.reload} />
        )}
      </div>
    );
  }
  if (!rfq) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Request for quotation" />
        <p className={styles.muted} role="status">
          Loading request for quotation…
        </p>
      </div>
    );
  }

  const actions = rfqActions(rfq.status);
  const openForReplies = rfq.status === "SENT" || rfq.status === "REPLIES_OPEN";
  const awardable = rfq.status === "REPLIES_OPEN" || rfq.status === "CLOSED";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Request for quotation ${rfq.number}`}
        meta={<StatusBadge status={rfq.status} />}
        actions={
          actions.length > 0 ? (
            <div className={styles.actions}>
              {actions.map((a) => (
                <Button
                  key={a.action}
                  variant="secondary"
                  loading={busy === a.action}
                  disabled={busy !== null && busy !== a.action}
                  onClick={() =>
                    void run(
                      a.action,
                      async () => (await changeRFQ(rfq.id, a.action, rfq.version, `${actionKey}-${a.action}`)).body,
                      (r) => `The RFQ is now ${statusLabel(r.status).toLowerCase()}.`,
                    )
                  }
                >
                  {a.label}
                </Button>
              ))}
            </div>
          ) : undefined
        }
      />
      <Alert tone="danger" live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {awardedOrders.length > 0 ? (
        <Card title="Purchase orders created">
          <ul className={styles.cardLinks}>
            {awardedOrders.map((po) => (
              <li key={po.id}>
                <Link className={styles.link} to={`/purchasing/purchase-orders/${po.id}`}>
                  {po.number}
                </Link>{" "}
                <span className={styles.muted}>
                  vendor <ShortId id={po.vendorId} />, {money(po.total, po.currency)}
                </span>
              </li>
            ))}
          </ul>
        </Card>
      ) : null}
      <Card title="Summary">
        <Details
          items={[
            { label: "Legal entity", value: rfq.legalEntityId, mono: true },
            { label: "Reply deadline", value: formatInstant(rfq.replyDeadline), mono: true },
            { label: "Ranking", value: rankingLabels[rfq.rankingRule] ?? rfq.rankingRule },
            { label: "Version", value: String(rfq.version), mono: true },
            { label: "Created", value: rfq.createdAt.slice(0, 10), mono: true },
          ]}
        />
      </Card>
      <DataTable label="RFQ lines" columns={lineColumns} rows={rfq.lines} getRowKey={(l) => l.id} />
      {rfq.status === "DRAFT" ? (
        <SendForm
          busy={busy === "send"}
          onSend={(vendorIds) =>
            void run(
              "send",
              async () => (await sendRFQ(rfq.id, rfq.version, vendorIds, `${actionKey}-send`)).body,
              (r) => `Sent to ${r.invitations.length} vendor${r.invitations.length === 1 ? "" : "s"}.`,
            )
          }
        />
      ) : null}
      {rfq.status !== "DRAFT" ? (
        <DataTable
          label="Invited vendors"
          columns={invitationColumns}
          rows={rfq.invitations}
          getRowKey={(i) => i.vendorId}
          empty={{ title: "No vendors invited" }}
        />
      ) : null}
      {openForReplies ? (
        <ReplyForm
          rfq={rfq}
          onRecorded={(r) => {
            setChanged(r);
            setFailure(null);
          }}
        />
      ) : null}
      {rfq.status !== "DRAFT" ? (
        <DataTable
          label="Replies"
          columns={replyColumns(rfq)}
          rows={rfq.replies}
          getRowKey={(r) => r.id}
          empty={{ title: "No replies yet", description: "Record each vendor's prices as they arrive. Replies after the deadline are flagged late." }}
        />
      ) : null}
      {awardable && rfq.replies.length > 0 ? (
        <CompareAndAward
          rfq={rfq}
          onAwarded={(r, orders) => {
            setChanged(r);
            setAwardedOrders(orders);
            setFailure(null);
            setNotice(`Awarded. ${orders.length} draft purchase order${orders.length === 1 ? "" : "s"} created.`);
          }}
        />
      ) : null}
    </div>
  );
}

/** Vendor ids to invite, pasted one per line or comma-separated. */
function SendForm({ busy, onSend }: { busy: boolean; onSend(vendorIds: string[]): void }) {
  const [raw, setRaw] = useState("");
  const [error, setError] = useState<string | undefined>();
  function onSubmit(event: FormEvent) {
    event.preventDefault();
    const ids = parseVendorIds(raw);
    if (ids.length === 0) return setError("Enter at least one vendor id.");
    const bad = ids.find((v) => !uuidPattern.test(v));
    if (bad) return setError(`${bad} is not a vendor id (a UUID).`);
    setError(undefined);
    onSend(ids);
  }
  return (
    <Card title="Send to vendors">
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <FormField id="rfq-vendors" label="Vendor ids" hint="One per line or separated by commas" error={error}>
          {(c) => <Textarea {...c} rows={3} value={raw} onChange={(e) => setRaw(e.target.value)} />}
        </FormField>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Send RFQ
          </Button>
        </div>
      </form>
    </Card>
  );
}

interface ReplyLineDraft {
  unitPrice: string;
  priceUnit: string;
  leadTimeDays: string;
  validUntil: string;
  notes: string;
}

const blankReplyLine = (): ReplyLineDraft => ({ unitPrice: "", priceUnit: "1", leadTimeDays: "", validUntil: "", notes: "" });

/** Lines with a price become reply lines; the rest are not quoted. */
export function validateReply(vendorId: string, currency: string, drafts: ReplyLineDraft[]): Errors {
  const errors: Errors = {};
  if (!uuidPattern.test(vendorId)) errors.vendorId = "Choose the vendor that replied.";
  if (!currencyPattern.test(currency)) errors.currency = "Use a three-letter ISO code such as USD.";
  let quoted = 0;
  drafts.forEach((d, i) => {
    if (d.unitPrice.trim() === "") return;
    quoted++;
    const f = (name: string) => `replies[${i}].${name}`;
    if (!decimalPattern.test(d.unitPrice.trim())) errors[f("unitPrice")] = "Enter a decimal price.";
    if (!decimalPattern.test(d.priceUnit.trim()) || Number(d.priceUnit) <= 0) errors[f("priceUnit")] = "Enter a positive price unit.";
    if (d.leadTimeDays.trim() !== "" && !/^\d+$/.test(d.leadTimeDays.trim())) errors[f("leadTimeDays")] = "Enter whole days.";
    if (d.validUntil !== "" && !datePattern.test(d.validUntil)) errors[f("validUntil")] = "Enter a date.";
  });
  if (quoted === 0 && Object.keys(errors).length === 0) errors.replies = "Enter a price for at least one line.";
  return errors;
}

/** Records one invited vendor's prices; lines left without a price are not quoted. */
function ReplyForm({ rfq, onRecorded }: { rfq: RFQ; onRecorded(rfq: RFQ): void }) {
  const [vendorId, setVendorId] = useState(rfq.invitations[0]?.vendorId ?? "");
  const [currency, setCurrency] = useState("USD");
  const [drafts, setDrafts] = useState<ReplyLineDraft[]>(() => rfq.lines.map(blankReplyLine));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [warnings, setWarnings] = useState<Warning[]>([]);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [vendorId, currency, drafts]);
  const update = (index: number, patch: Partial<ReplyLineDraft>) => setDrafts((ds) => ds.map((d, i) => (i === index ? { ...d, ...patch } : d)));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateReply(vendorId, currency, drafts);
    setErrors(found);
    setFailure(null);
    setWarnings([]);
    if (Object.keys(found).length > 0) return;
    const input: RFQReplyInput = { vendorId, currency, replies: [] };
    drafts.forEach((d, i) => {
      if (d.unitPrice.trim() === "") return;
      input.replies.push({
        lineId: rfq.lines[i].id,
        unitPrice: d.unitPrice.trim(),
        priceUnit: d.priceUnit.trim(),
        leadTimeDays: d.leadTimeDays.trim() === "" ? 0 : Number(d.leadTimeDays),
        validUntil: d.validUntil === "" ? undefined : d.validUntil,
        notes: d.notes.trim() === "" ? undefined : d.notes.trim(),
      });
    });
    setBusy(true);
    try {
      const { body } = await recordRFQReplies(rfq.id, input, key);
      setWarnings(body.warnings ?? []);
      setDrafts(rfq.lines.map(blankReplyLine));
      onRecorded(body);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Record a reply">
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <div className={styles.grid}>
          <FormField id="reply-vendor" label="Vendor" error={errors.vendorId}>
            {(c) => (
              <Select {...c} value={vendorId} onChange={(e) => setVendorId(e.target.value)}>
                {rfq.invitations.map((inv) => (
                  <option key={inv.vendorId} value={inv.vendorId}>
                    {inv.vendorId}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="reply-currency" label="Currency" error={errors.currency}>
            {(c) => <Input {...c} value={currency} maxLength={3} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />}
          </FormField>
        </div>
        {rfq.lines.map((line, i) => {
          const id = (name: string) => `reply-${i}-${name}`;
          const err = (name: string) => errors[`replies[${i}].${name}`];
          return (
            <fieldset key={line.id} className={styles.lineCard} aria-label={`Reply to line ${line.lineNo}`}>
              <p className={styles.lineTitle}>
                Line {line.lineNo}: {line.description || line.productId} ({line.qty} {line.unit})
              </p>
              <div className={styles.grid}>
                <FormField id={id("price")} label="Unit price" optional hint="Blank: not quoted" error={err("unitPrice")}>
                  {(c) => <Input {...c} inputMode="decimal" value={drafts[i].unitPrice} onChange={(e) => update(i, { unitPrice: e.target.value })} />}
                </FormField>
                <FormField id={id("per")} label="Price unit" error={err("priceUnit")}>
                  {(c) => <Input {...c} inputMode="decimal" value={drafts[i].priceUnit} onChange={(e) => update(i, { priceUnit: e.target.value })} />}
                </FormField>
                <FormField id={id("lead")} label="Lead time (days)" optional error={err("leadTimeDays")}>
                  {(c) => <Input {...c} inputMode="numeric" value={drafts[i].leadTimeDays} onChange={(e) => update(i, { leadTimeDays: e.target.value })} />}
                </FormField>
                <FormField id={id("valid")} label="Valid until" optional error={err("validUntil")}>
                  {(c) => <Input {...c} type="date" value={drafts[i].validUntil} onChange={(e) => update(i, { validUntil: e.target.value })} />}
                </FormField>
                <FormField id={id("notes")} label="Notes" optional error={err("notes")}>
                  {(c) => <Input {...c} value={drafts[i].notes} onChange={(e) => update(i, { notes: e.target.value })} />}
                </FormField>
              </div>
            </fieldset>
          );
        })}
        <Alert tone="danger" live="alert">
          {failure ? `${failure.message} (${failure.code})` : errors.replies ?? null}
        </Alert>
        <Alert tone="warning" live="status">
          {warnings.length > 0 ? warnings.map((w) => `Line ${w.lineNo}: ${w.detail} (${w.code})`).join(" ") : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save reply
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** A radio with its own label element (the in-repo a11y check pairs labels by for/id). */
function Radio({ id, name, checked, onSelect, label }: { id: string; name: string; checked: boolean; onSelect(): void; label: string }) {
  return (
    <div className={styles.checkboxRow}>
      <input id={id} type="radio" className={styles.radio} name={name} checked={checked} onChange={onSelect} />
      <label htmlFor={id}>{label}</label>
    </div>
  );
}

interface Choice {
  replyId: string;
  reason: string;
}

/**
 * The ranking per line with a choice of winner (rank 1 preselected), late
 * replies listed apart and selectable only with an override reason (BR-17.11),
 * and the award that creates the purchase orders.
 */
function CompareAndAward({ rfq, onAwarded }: { rfq: RFQ; onAwarded(rfq: RFQ, orders: PurchaseOrder[]): void }) {
  const [rule, setRule] = useState<RankingRule>(rfq.rankingRule);
  const comparison = useResource((signal) => compareRFQ(rfq.id, rule, signal), [rfq.id, rfq.version, rule]);
  const [choices, setChoices] = useState<Record<string, Choice>>({});
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const lines = comparison.data?.body.lines ?? [];
  const key = useMemo(() => newIdempotencyKey(), [rfq.version, choices]);
  const late = new Set(rfq.replies.filter((r) => r.late).map((r) => r.id));

  const choiceOf = (line: RFQComparisonLine): Choice =>
    choices[line.lineId] ?? { replyId: line.ranked[0]?.replyId ?? "", reason: "" };
  const choose = (lineId: string, patch: Partial<Choice>, line: RFQComparisonLine) =>
    setChoices((cs) => ({ ...cs, [lineId]: { ...choiceOf(line), ...patch } }));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const awards: RFQAward[] = [];
    const found: Errors = {};
    lines.forEach((line) => {
      const c = choiceOf(line);
      if (c.replyId === "") return;
      if (late.has(c.replyId) && c.reason.trim() === "") found[`reason-${line.lineId}`] = "Give a reason to award a late reply.";
      awards.push({ lineId: line.lineId, replyId: c.replyId, overrideReason: late.has(c.replyId) ? c.reason.trim() : undefined });
    });
    if (awards.length === 0) found.awards = "Choose a reply for at least one line.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await awardRFQ(rfq.id, rfq.version, awards, key);
      onAwarded(body.rfq, body.purchaseOrders);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const vendorLabel = (vendorId: string) => vendorId.slice(0, 8);
  return (
    <Card title="Compare and award">
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <div className={styles.toolbar}>
          <div className={styles.filter}>
            <FormField id="compare-rule" label="Rank by">
              {(c) => (
                <Select {...c} value={rule} onChange={(e) => setRule(e.target.value as RankingRule)}>
                  {(Object.keys(rankingLabels) as RankingRule[]).map((k) => (
                    <option key={k} value={k}>
                      {rankingLabels[k]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
        </div>
        {comparison.error ? <LoadError what="the comparison" error={comparison.error} onRetry={comparison.reload} /> : null}
        {comparison.loading && lines.length === 0 ? (
          <p className={styles.muted} role="status">
            Ranking replies…
          </p>
        ) : null}
        {lines.map((line) => {
          const c = choiceOf(line);
          const name = `award-${line.lineId}`;
          const excluded = line.excluded.map((e) => rfq.replies.find((r) => r.id === e.replyId)).filter((r): r is RFQReply => r !== undefined);
          return (
            <fieldset key={line.lineId} className={styles.lineCard}>
              <legend className={styles.lineTitle}>
                Line {line.lineNo}: {line.description || line.productId} ({line.qty} {line.unit})
              </legend>
              {line.ranked.length === 0 && excluded.length === 0 ? <p className={styles.muted}>No replies to this line.</p> : null}
              {line.ranked.map((r) => (
                <Radio
                  key={r.replyId}
                  id={`${name}-${r.replyId}`}
                  name={name}
                  checked={c.replyId === r.replyId}
                  onSelect={() => choose(line.lineId, { replyId: r.replyId }, line)}
                  label={`#${r.rank} vendor ${vendorLabel(r.vendorId)}: ${money(r.unitPrice, r.currency)} each, ${r.leadTimeDays} days, total ${money(r.total, r.currency)}`}
                />
              ))}
              {excluded.map((r) => (
                <Radio
                  key={r.id}
                  id={`${name}-${r.id}`}
                  name={name}
                  checked={c.replyId === r.id}
                  onSelect={() => choose(line.lineId, { replyId: r.id }, line)}
                  label={`Late, excluded from ranking: vendor ${vendorLabel(r.vendorId)}: ${money(r.unitPrice, r.currency)} per ${r.priceUnit}, ${r.leadTimeDays} days`}
                />
              ))}
              <Radio
                id={`${name}-none`}
                name={name}
                checked={c.replyId === ""}
                onSelect={() => choose(line.lineId, { replyId: "" }, line)}
                label="Do not award this line"
              />
              {late.has(c.replyId) ? (
                <FormField id={`reason-${line.lineId}`} label="Reason to award a late reply" error={errors[`reason-${line.lineId}`]}>
                  {(ctl) => <Input {...ctl} value={c.reason} onChange={(e) => choose(line.lineId, { reason: e.target.value }, line)} />}
                </FormField>
              ) : null}
            </fieldset>
          );
        })}
        <Alert tone="danger" live="alert">
          {failure ? `${failure.message} (${failure.code})` : errors.awards ?? null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy} disabled={lines.length === 0}>
            Award and create purchase orders
          </Button>
        </div>
      </form>
    </Card>
  );
}
