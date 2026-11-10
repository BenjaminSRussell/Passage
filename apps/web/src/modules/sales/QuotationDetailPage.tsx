import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import { convertQuotation, describeProblem, getQuotation, loseQuotation, sendQuotation, type Order, type Problem, type QuotationLine } from "./api/client";
import { formatDateTime, money, supplementaryNote } from "./format";
import styles from "./pages.module.css";
import { QuotationMaintenance } from "./QuotationMaintenance";
import { isExpired, QuotationStatusBadge } from "./QuotationsPage";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

const LOST_REASON_MAX = 200;
const OVERRIDE_MAX = 500;

/** Titles for the service's quotation problems (README catalogue). */
const problemTitle: Record<string, string> = {
  "SLS-0006": "The quotation was changed by someone else",
  "SLS-0011": "A product is not listed for this customer",
  "SLS-0030": "The quotation cannot take this action in its status",
  "SLS-0031": "The quotation's validity has passed",
  "SLS-0032": "A selected line is no longer open",
};

type Outcome = { tone: "success"; text: ReactNode } | { tone: "danger"; problem: Problem };

/** Quotation detail (PRD §5.27.6): lines, send, convert whole or by line, mark lost (FR-27.1). */
export function QuotationDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getQuotation(id, signal), id);
  const [selected, setSelected] = useState<Set<number> | null>(null);
  const [override, setOverride] = useState("");
  const [lostReason, setLostReason] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const [busy, setBusy] = useState<"send" | "convert" | "lose" | null>(null);

  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Sales quotation" />
        <Alert tone="danger" title="Could not load the quotation" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Sales quotation" />
        <span className="visually-hidden">Loading the quotation…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: quotation, etag } = state.data;
  const openLines = quotation.lines.filter((l) => l.lineStatus === "OPEN");
  const expired = isExpired(quotation);
  const convertible = (quotation.status === "SENT" || quotation.status === "CONVERTED") && openLines.length > 0 && !expired;
  const losable = quotation.status === "DRAFT" || quotation.status === "SENT";
  const chosen = selected ?? new Set(openLines.map((l) => l.lineNo));

  async function run(action: "send" | "convert" | "lose", call: () => Promise<ReactNode>) {
    setBusy(action);
    setOutcome(null);
    try {
      const text = await call();
      setOutcome({ tone: "success", text });
      setSelected(null);
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setOutcome({ tone: "danger", problem });
    } finally {
      setBusy(null);
    }
  }

  function send() {
    void run("send", async () => {
      const res = await sendQuotation(quotation.id, etag);
      return `Quotation ${res.value.quotationNumber} sent.`;
    });
  }

  function convert(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (chosen.size === 0) found.lineNos = "Select at least one open line.";
    if (override.trim().length > OVERRIDE_MAX) found.listingOverrideReason = `Use at most ${OVERRIDE_MAX} characters.`;
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    const lineNos = chosen.size === openLines.length ? [] : [...chosen].sort((a, b) => a - b);
    void run("convert", async () => {
      const res = await convertQuotation(quotation.id, etag, {
        lineNos,
        ...(override.trim() ? { listingOverrideReason: override.trim() } : {}),
      });
      const order: Order = res.value.order;
      const quoted = order.lines.filter((l) => !l.supplementaryOf).length;
      setOverride("");
      return (
        <>
          Draft order <Link to={`/sales/orders/${order.id}`}>{order.orderNumber}</Link> was created with {quoted} {quoted === 1 ? "line" : "lines"} at
          the quoted prices.{supplementaryNote(order.lines)}
        </>
      );
    });
  }

  function lose(event: FormEvent) {
    event.preventDefault();
    const reason = lostReason.trim();
    if (reason.length === 0 || reason.length > LOST_REASON_MAX) {
      setErrors({ reason: `Give a reason of 1 to ${LOST_REASON_MAX} characters.` });
      return;
    }
    setErrors({});
    void run("lose", async () => {
      await loseQuotation(quotation.id, etag, reason);
      setLostReason("");
      return "Quotation marked lost.";
    });
  }

  function toggle(lineNo: number, on: boolean) {
    const next = new Set(chosen);
    if (on) next.add(lineNo);
    else next.delete(lineNo);
    setSelected(next);
  }

  const lineColumns: DataTableColumn<QuotationLine>[] = [
    ...(convertible
      ? [
          {
            key: "select",
            header: "Convert",
            render: (l: QuotationLine) =>
              l.lineStatus === "OPEN" ? (
                <Input type="checkbox" aria-label={`Convert line ${l.lineNo}`} checked={chosen.has(l.lineNo)} onChange={(e) => toggle(l.lineNo, e.target.checked)} />
              ) : null,
          },
        ]
      : []),
    { key: "lineNo", header: "Line", align: "end" },
    { key: "productId", header: "Product", render: (row) => <EntityName entity="product" id={row.productId} /> },
    { key: "quantity", header: "Quantity", align: "end", render: (l) => `${l.quantity} ${l.unit}` },
    { key: "unitPrice", header: "Unit price", align: "end", render: (l) => money(l.unitPrice, quotation.currency) },
    { key: "discPct", header: "Discount", align: "end", render: (l) => `${l.discPct}%` },
    { key: "lineNet", header: "Net", align: "end", render: (l) => money(l.lineNet, quotation.currency) },
    { key: "requestedShipDate", header: "Requested ship" },
    {
      key: "lineStatus",
      header: "Status",
      render: (l) => (l.convertedOrderId ? <Link to={`/sales/orders/${l.convertedOrderId}`}>Converted</Link> : "Open"),
    },
  ];

  const facts: [string, ReactNode][] = [
    ["Customer", <EntityName entity="customer" id={quotation.customerId} />],
    ["Invoice account", <EntityName entity="customer" id={quotation.invoiceAccountId} />],
    ["Legal entity", <EntityName entity="legalEntity" id={quotation.legalEntityId} />],
    ["Net total", money(quotation.netTotal, quotation.currency)],
    ["Valid until", quotation.validUntil],
    ["Win probability", `${quotation.probabilityPct}%`],
    ...(quotation.customerGroup ? ([["Customer group", quotation.customerGroup]] as [string, ReactNode][]) : []),
    ["Open lines", `${quotation.openLines} of ${quotation.lineCount}`],
    ["Created", `${formatDateTime(quotation.createdAt)} by ${quotation.createdBy}`],
    ...(quotation.lostReason ? ([["Lost because", quotation.lostReason]] as [string, ReactNode][]) : []),
    ...(quotation.cancelReason ? ([["Cancelled because", quotation.cancelReason]] as [string, ReactNode][]) : []),
    ...(quotation.listingOverrides && quotation.listingOverrides.length > 0
      ? ([["Listing overrides", quotation.listingOverrides.map((o) => `Line ${o.lineNo}: ${o.reason} (${o.actor})`).join("; ")]] as [string, ReactNode][])
      : []),
  ];
  const mono = new Set<string>();

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Sales quotation ${quotation.quotationNumber}`}
        meta={<QuotationStatusBadge quotation={quotation} />}
        actions={
          <>
            {quotation.status === "DRAFT" && !expired ? (
              <Button variant="primary" loading={busy === "send"} disabled={busy !== null && busy !== "send"} onClick={send}>
                Send quotation
              </Button>
            ) : null}
            <Link className={buttonClassName("ghost")} to="/sales/quotations">
              Back to quotations
            </Link>
          </>
        }
      />
      {expired ? <p className={styles.note}>The validity passed on {quotation.validUntil}. The quotation can no longer be sent or converted; create a new one.</p> : null}
      {outcome?.tone === "danger" ? (
        <Alert
          tone="danger"
          title={problemTitle[outcome.problem.code] ?? "The action failed"}
          live="alert"
          action={
            outcome.problem.code === "SLS-0006" ? (
              <Button size="sm" onClick={() => { setOutcome(null); state.reload(); }}>
                Reload quotation
              </Button>
            ) : undefined
          }
        >
          <ProblemMessage problem={outcome.problem} />
        </Alert>
      ) : null}
      <Alert tone="success" live="status">
        {outcome?.tone === "success" ? outcome.text : null}
      </Alert>
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={mono.has(term) ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <DataTable label="Quotation lines" columns={lineColumns} rows={quotation.lines} getRowKey={(l) => l.id} empty={{ title: "No lines" }} />
      {convertible ? (
        <Card title="Convert to an order">
          <form className={styles.form} onSubmit={convert} noValidate aria-label="Convert to an order">
            <p className={`${styles.note} ${styles.formActions}`}>
              {chosen.size} of {openLines.length} open {openLines.length === 1 ? "line" : "lines"} selected. The selected lines become one draft order at the quoted prices; the rest stay open.
            </p>
            {errors.lineNos ? (
              <p role="alert" className={`${styles.fieldError} ${styles.formActions}`}>
                {errors.lineNos}
              </p>
            ) : null}
            <FormField id="convert-override" label="Listing override reason" hint="Needs the sales.order.approve duty. Accepts lines the product listing denies and logs the reason." error={errors.listingOverrideReason} optional>
              {(control) => <Input {...control} value={override} onChange={(e) => setOverride(e.target.value)} invalid={Boolean(errors.listingOverrideReason)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "convert"} disabled={busy !== null && busy !== "convert"}>
                Convert selected lines
              </Button>
            </div>
          </form>
        </Card>
      ) : quotation.status === "DRAFT" && !expired ? (
        <Card title="Convert to an order">
          <EmptyState compact headingLevel={3} title="Send the quotation first" description="A quotation converts into an order once it has been sent to the customer." />
        </Card>
      ) : null}
      {losable ? (
        <Card title="Mark as lost">
          <form className={styles.form} onSubmit={lose} noValidate aria-label="Mark as lost">
            <FormField id="lost-reason" label="Reason" hint="For example the competitor's price or the customer's decision." error={errors.reason}>
              {(control) => <Textarea {...control} rows={2} maxLength={LOST_REASON_MAX} value={lostReason} onChange={(e) => setLostReason(e.target.value)} invalid={Boolean(errors.reason)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="danger" loading={busy === "lose"} disabled={busy !== null && busy !== "lose"}>
                Mark lost
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {losable && !expired ? <QuotationMaintenance key={quotation.id} quotation={quotation} etag={etag} onDone={(text) => { setOutcome({ tone: "success", text }); state.reload(); }} onConflict={state.reload} /> : null}
    </div>
  );
}
