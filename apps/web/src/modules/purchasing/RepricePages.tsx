import { useMemo, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
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
  buttonClassName,
  type DataTableColumn,
} from "../../shell/ui";
import {
  applyReprice,
  describeProblem,
  getPurchaseOrder,
  newIdempotencyKey,
  previewReprice,
  runReprice,
  type RepriceChange,
  type RepriceResult,
  type RepriceRunInput,
  type RepriceRunResult,
  type RepriceSkip,
  type RepriceSkipReason,
} from "./api/client";
import { Details, LoadError, ShortId, StatusBadge, money, useResource, uuidPattern, type Failure } from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage, problemSummary } from "../../shell/errors";

// Subsequent price change (FR-17.22, BR-17.18): move confirmed order lines priced
// from a trade agreement to the price row that wins now, as the next order version.

/** Plain-language reasons for lines the run leaves alone. */
export const SKIP_REASONS: Record<RepriceSkipReason, string> = {
  MANUAL_PRICE: "Priced by hand; a price change never moves it",
  AGREEMENT_LINE: "Drawn against a purchase agreement; its released amount cannot change",
  NOT_TRADE_AGREEMENT: "Not priced from a trade agreement",
  NO_CURRENT_PRICE: "No trade agreement price applies any more",
  UNCHANGED: "Already at the current price",
  INVOICED: "Invoiced; keeps the old price and goes to payables",
  HANDED_OVER: "Invoiced; this price change was already sent to payables",
};

/** "-50.00" → "−50.00 USD", "12" → "+12 USD": the sign of a delta is the point of it. */
export function signedMoney(value: string, currency: string): string {
  const trimmed = value.trim();
  if (trimmed.startsWith("-")) return `−${trimmed.slice(1)} ${currency}`;
  if (/^0+(\.0+)?$/.test(trimmed)) return money(trimmed, currency);
  return `+${trimmed} ${currency}`;
}

/** "−50.00 USD over 2 lines" summary of a result. */
export function repriceSummary(r: RepriceResult): string {
  const n = r.changes.length;
  const lines = `${n} line${n === 1 ? "" : "s"}`;
  return n === 0 ? "No line moves" : `${lines} move, ${signedMoney(r.totalDelta, r.currency)} in total`;
}

const pricePer = (price: string, unit: string) => (unit === "1" ? price : `${price} per ${unit}`);

function changeColumns(currency: string, qtyHeader: string): DataTableColumn<RepriceChange>[] {
  return [
    { key: "lineNo", header: "Line", align: "end", mono: true },
    { key: "qty", header: qtyHeader, align: "end", mono: true },
    { key: "oldPrice", header: "Old price", align: "end", mono: true, render: (c) => pricePer(c.oldPrice, c.oldPriceUnit) },
    { key: "newPrice", header: "New price", align: "end", mono: true, render: (c) => pricePer(c.newPrice, c.newPriceUnit) },
    { key: "oldAmount", header: "Old amount", align: "end", mono: true, render: (c) => money(c.oldAmount, currency) },
    { key: "newAmount", header: "New amount", align: "end", mono: true, render: (c) => money(c.newAmount, currency) },
    { key: "delta", header: "Change", align: "end", mono: true, render: (c) => signedMoney(c.delta, currency) },
    { key: "newPriceSource", header: "Price row", render: (c) => <ShortId id={c.newPriceSource.replace(/^TRADE_AGREEMENT:/, "")} /> },
  ];
}

const skipColumns: DataTableColumn<RepriceSkip>[] = [
  { key: "lineNo", header: "Line", align: "end", mono: true },
  { key: "reason", header: "Left alone because", render: (s) => SKIP_REASONS[s.reason] ?? s.reason },
];

/** The three tables of one order's run: lines that move, invoiced hand-overs, lines left alone. */
function RepriceTables({ result }: { result: RepriceResult }) {
  return (
    <>
      <Card title="Lines that move">
        <DataTable
          label="Lines that move"
          columns={changeColumns(result.currency, "Quantity")}
          rows={result.changes}
          getRowKey={(c) => String(c.lineNo)}
          empty={{ title: "No line moves", description: "Every line priced from a trade agreement is already at the current price." }}
        />
      </Card>
      {result.invoiced.length > 0 ? (
        <Card title="Invoiced lines sent to payables">
          <p className={styles.muted}>
            These lines are invoiced, so they keep the old price. Payables raises a subsequent credit or debit for the invoiced quantity.
          </p>
          <DataTable
            label="Invoiced lines sent to payables"
            columns={changeColumns(result.currency, "Invoiced")}
            rows={result.invoiced}
            getRowKey={(c) => String(c.lineNo)}
          />
        </Card>
      ) : null}
      {result.skipped.length > 0 ? (
        <Card title="Lines left alone">
          <DataTable label="Lines left alone" columns={skipColumns} rows={result.skipped} getRowKey={(s) => String(s.lineNo)} />
        </Card>
      ) : null}
    </>
  );
}

function appliedMessage(r: RepriceResult): string {
  if (r.replayed) return `This price change was already applied. The order is at version ${r.versionNo}.`;
  const what = repriceSummary(r);
  return r.reapprovalRequired
    ? `${what}. Version ${r.versionNo} waits for re-approval before the vendor receives it.`
    : `${what}. Version ${r.versionNo} is confirmed and goes to the vendor.`;
}

/**
 * Subsequent price change on one confirmed order: a read-only preview loads at
 * once; Apply writes it as the next version under the previewed row version, so
 * a preview that went stale is refused (PRC-PO-009) rather than applied.
 */
export function RepricePage() {
  const { id = "" } = useParams();
  const order = useResource((signal) => getPurchaseOrder(id, signal), [id]);
  const confirmed = order.data?.body.approvalStatus === "CONFIRMED";
  const preview = useResource(
    async (signal) => (confirmed ? previewReprice(id, signal) : null),
    [id, confirmed],
  );
  const [applied, setApplied] = useState<RepriceResult | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const rowVersion = preview.data?.etag ? Number(preview.data.etag.replace(/"/g, "")) : null;
  // One key per previewed version, so a retried click is a replay, not a second run.
  const key = useMemo(() => newIdempotencyKey(), [rowVersion]);

  async function apply() {
    if (rowVersion === null || Number.isNaN(rowVersion)) return;
    setBusy(true);
    setFailure(null);
    try {
      const { body } = await applyReprice(id, rowVersion, key);
      setApplied(body);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const back = (
    <Link className={buttonClassName("secondary")} to={`/purchasing/purchase-orders/${id}`}>
      Back to the order
    </Link>
  );

  if (order.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Price change" />
        {order.error.code === "PRC-PO-NOT-FOUND" ? (
          <Card>
            <EmptyState
              icon="receipt"
              title="Purchase order not found"
              description="It may belong to another legal entity, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/purchasing/purchase-orders">
                  Back to purchase orders
                </Link>
              }
            />
          </Card>
        ) : (
          <LoadError what="the purchase order" error={order.error} onRetry={order.reload} />
        )}
      </div>
    );
  }
  const po = order.data?.body;
  if (!po) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Price change" />
        <p className={styles.muted} role="status">
          Loading purchase order…
        </p>
      </div>
    );
  }

  const result = applied ?? preview.data?.body ?? null;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Price change for ${po.number}`}
        description="Moves open lines priced from a trade agreement to the price that applies now, as the next version of the order."
        meta={<StatusBadge status={po.approvalStatus} />}
        actions={back}
      />
      {!confirmed ? (
        <Alert tone="warning" title="Only confirmed orders can be repriced">
          {`This order is ${po.approvalStatus.toLowerCase().replace(/_/g, " ")}. A draft is priced again on every edit, and an order in review already has a change waiting.`}
        </Alert>
      ) : null}
      {applied ? (
        <Alert tone="success" live="status" title="Price change applied">
          {appliedMessage(applied)}
        </Alert>
      ) : null}
      <Alert
        tone="danger"
        live="alert"
        action={
          failure?.code === "PRC-PO-009" ? (
            <Button
              size="sm"
              onClick={() => {
                setFailure(null);
                preview.reload();
              }}
            >
              Preview again
            </Button>
          ) : undefined
        }
      >
        {failure
          ? failure.code === "PRC-PO-009"
            ? <ProblemMessage problem={failure} message="The order changed after this preview. Preview again before applying." />
            : <ProblemMessage problem={failure} />
          : null}
      </Alert>
      {preview.error ? <LoadError what="the price change preview" error={preview.error} onRetry={preview.reload} /> : null}
      {confirmed && preview.loading && !result ? (
        <p className={styles.muted} role="status">
          Working out the current prices…
        </p>
      ) : null}
      {result ? (
        <>
          <Card
            title={applied ? "Applied" : "Preview"}
            actions={
              applied ? null : (
                <Button
                  variant="primary"
                  loading={busy}
                  disabled={result.changes.length === 0 && result.invoiced.length === 0}
                  onClick={() => void apply()}
                >
                  Apply price change
                </Button>
              )
            }
          >
            <Details
              items={[
                { label: "Lines that move", value: String(result.changes.length) },
                { label: "Total change", value: signedMoney(result.totalDelta, result.currency), mono: true },
                { label: "Invoiced lines to payables", value: String(result.invoiced.length) },
                { label: "Prices as of", value: result.pricingDate, mono: true },
                {
                  label: applied ? "Version written" : "Current version",
                  value: (
                    <>
                      {result.versionNo}{" "}
                      {applied && result.reapprovalRequired ? <Badge tone="info">Awaiting re-approval</Badge> : null}
                    </>
                  ),
                },
              ]}
            />
            {!applied ? (
              <p className={styles.muted}>Nothing is written until you apply. Applying needs the Confirm purchase orders duty.</p>
            ) : null}
          </Card>
          <RepriceTables result={result} />
        </>
      ) : null}
    </div>
  );
}

interface RunDraft {
  legalEntityId: string;
  vendorId: string;
  tradeAgreementId: string;
}

/** Checks the optional UUID filters and builds the run input. */
export function toRunInput(d: RunDraft): { input?: RepriceRunInput; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  for (const field of ["legalEntityId", "vendorId", "tradeAgreementId"] as const) {
    const v = d[field].trim();
    if (v !== "" && !uuidPattern.test(v)) errors[field] = "Enter a UUID, or leave it blank.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: d.legalEntityId.trim() || undefined,
      vendorId: d.vendorId.trim() || undefined,
      tradeAgreementId: d.tradeAgreementId.trim() || undefined,
    },
  };
}

function runColumns(): DataTableColumn<RepriceResult>[] {
  return [
    {
      key: "number",
      header: "Order",
      render: (r) => (
        <Link className={styles.link} to={`/purchasing/purchase-orders/${r.purchaseOrderId}/reprice`}>
          {r.number || r.purchaseOrderId.slice(0, 8)}
        </Link>
      ),
    },
    { key: "changes", header: "Lines that move", align: "end", mono: true, render: (r) => String(r.changes.length) },
    { key: "invoiced", header: "Invoiced to payables", align: "end", mono: true, render: (r) => String(r.invoiced.length) },
    { key: "totalDelta", header: "Total change", align: "end", mono: true, render: (r) => (r.currency ? signedMoney(r.totalDelta, r.currency) : "—") },
    {
      key: "result",
      header: "Result",
      render: (r) =>
        r.error ? (
          <Badge tone="danger">{`Failed: ${problemSummary(r.error)}`}</Badge>
        ) : r.applied ? (
          <Badge tone="success">{r.reapprovalRequired ? "Applied, awaiting re-approval" : "Applied"}</Badge>
        ) : (
          <Badge tone="neutral">Preview</Badge>
        ),
    },
  ];
}

/**
 * The subsequent price change run over many confirmed orders (FR-17.22):
 * filter by legal entity, vendor or the price row the lines came from, preview
 * one page, then apply that page. Each order is applied on its own, so a failed
 * order is listed with its code and the others go through.
 */
export function RepriceRunPage() {
  const [draft, setDraft] = useState<RunDraft>({ legalEntityId: "", vendorId: "", tradeAgreementId: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [input, setInput] = useState<RepriceRunInput | null>(null);
  const [result, setResult] = useState<RepriceRunResult | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState<"preview" | "apply" | null>(null);
  // One key per previewed page, so a retried apply does not write an order twice.
  const [applyKey, setApplyKey] = useState(() => newIdempotencyKey());

  async function preview(next: RepriceRunInput) {
    setBusy("preview");
    setFailure(null);
    try {
      setResult(await runReprice(next, false));
      setInput(next);
      setApplyKey(newIdempotencyKey());
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function apply() {
    if (!input) return;
    setBusy("apply");
    setFailure(null);
    try {
      setResult(await runReprice(input, true, applyKey));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    const checked = toRunInput(draft);
    setErrors(checked.errors);
    if (checked.input) void preview(checked.input);
  }

  const set = (field: keyof RunDraft) => (e: { target: { value: string } }) => setDraft((d) => ({ ...d, [field]: e.target.value }));
  const failed = result?.items.filter((i) => i.error).length ?? 0;
  const pending = result !== null && !result.applied && result.items.some((i) => !i.error);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Price changes"
        description="Find confirmed orders whose trade agreement prices changed, preview the new prices and apply them as new order versions."
      />
      <Card title="Which orders">
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <div className={styles.grid}>
            <FormField id="rp-trade-agreement" label="Trade agreement row" optional hint="Only orders with lines priced from this row." error={errors.tradeAgreementId}>
              {(control) => <Input {...control} value={draft.tradeAgreementId} onChange={set("tradeAgreementId")} invalid={!!errors.tradeAgreementId} />}
            </FormField>
            <FormField id="rp-vendor" label="Vendor" optional error={errors.vendorId}>
              {(control) => <Input {...control} value={draft.vendorId} onChange={set("vendorId")} invalid={!!errors.vendorId} />}
            </FormField>
            <FormField id="rp-legal-entity" label="Legal entity" optional error={errors.legalEntityId}>
              {(control) => <Input {...control} value={draft.legalEntityId} onChange={set("legalEntityId")} invalid={!!errors.legalEntityId} />}
            </FormField>
          </div>
          <div className={styles.actions}>
            <Button type="submit" variant="secondary" loading={busy === "preview"} disabled={busy === "apply"}>
              Preview price changes
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {failure ? <ProblemMessage problem={failure} /> : null}
      </Alert>
      {result?.applied ? (
        <Alert tone={failed > 0 ? "warning" : "success"} live="status" title="Price changes applied">
          {`${result.items.length - failed} order${result.items.length - failed === 1 ? "" : "s"} repriced${failed > 0 ? `, ${failed} failed; open an order to see why` : ""}.`}
        </Alert>
      ) : null}
      {result ? (
        <Card
          title={result.applied ? "Applied" : "Preview"}
          actions={
            pending ? (
              <Button variant="primary" loading={busy === "apply"} disabled={busy === "preview"} onClick={() => void apply()}>
                Apply to these orders
              </Button>
            ) : null
          }
        >
          <p className={styles.muted}>{`${result.visited} confirmed order${result.visited === 1 ? "" : "s"} checked on this page.`}</p>
          <DataTable
            label="Orders with price changes"
            columns={runColumns()}
            rows={result.items}
            getRowKey={(r) => r.purchaseOrderId}
            loading={busy === "preview"}
            empty={{ title: "No price changes", description: "Every confirmed order on this page is already at its current trade agreement prices." }}
          />
          {result.nextCursor && input ? (
            <div className={styles.actions}>
              <Button loading={busy === "preview"} onClick={() => void preview({ ...input, cursor: result.nextCursor })}>
                Next page
              </Button>
            </div>
          ) : null}
        </Card>
      ) : (
        <Card>
          <EmptyState
            icon="receipt"
            title="Nothing previewed yet"
            description="Choose the orders to check, or leave the filters blank to check every confirmed order, then preview."
          />
        </Card>
      )}
    </div>
  );
}
