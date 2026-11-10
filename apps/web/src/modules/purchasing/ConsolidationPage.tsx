import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, buttonClassName, type DataTableColumn } from "../../shell/ui";
import {
  consolidateRequisitions,
  createRFQFromRequisitions,
  describeProblem,
  listOpenRequisitionLines,
  newIdempotencyKey,
  splitRequisitionLine,
  type ConsolidationInput,
  type PurchaseOrder,
  type SourcingFilter,
  type SourcingLine,
  type Warning,
} from "./api/client";
import { defaultDeadline } from "./RfqPages";
import { LoadError, money, useResource, uuidPattern, type Failure, Named } from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const DEFAULT_WINDOW = "7";

const sourcingLabels: Record<SourcingFilter | "", string> = {
  "": "All open lines",
  NEEDS_SOURCING: "Needs sourcing (no vendor)",
  HAS_VENDOR: "Has a preferred vendor",
};

export const lineLabel = (l: SourcingLine) => `${l.requisitionNumber} line ${l.lineNo}`;

type Errors = Record<string, string>;

/**
 * Checks the selection before the round trip: every selected line needs a
 * vendor (the override or its preferred vendor), overrides must be UUIDs, and
 * the window is 1–60 days (FR-17.3).
 */
export function validateConsolidation(rows: SourcingLine[], selected: Set<string>, vendors: Record<string, string>, window: string): Errors {
  const errors: Errors = {};
  if (selected.size === 0) errors.lines = "Select at least one line.";
  if (!/^\d+$/.test(window) || Number(window) < 1 || Number(window) > 60) errors.windowDays = "Enter a whole number of days from 1 to 60.";
  for (const l of rows) {
    if (!selected.has(l.id)) continue;
    const override = (vendors[l.id] ?? "").trim();
    if (override !== "" && !uuidPattern.test(override)) errors[l.id] = `Vendor for ${lineLabel(l)} must be a UUID.`;
    if (override === "" && !l.preferredVendorId) errors[l.id] = `Choose a vendor for ${lineLabel(l)}: it has no preferred vendor.`;
  }
  return errors;
}

/**
 * Checks the selection before an RFQ is started: at least one line, all of one legal entity (the service
 * refuses a mix with PRC-REQ-005), and a reply deadline in the future (FR-17.3, FR-17.4).
 */
export function validateSourcing(rows: SourcingLine[], selected: Set<string>, deadline: string, now = new Date()): Errors {
  const errors: Errors = {};
  const chosen = rows.filter((l) => selected.has(l.id));
  if (chosen.length === 0) errors.lines = "Select at least one line.";
  else if (new Set(chosen.map((l) => l.legalEntityId)).size > 1) errors.lines = "Select lines of one legal entity: an RFQ belongs to one.";
  const when = new Date(deadline);
  if (deadline === "" || Number.isNaN(when.getTime())) errors.replyDeadline = "Enter the reply deadline.";
  else if (when <= now) errors.replyDeadline = "Choose a deadline in the future.";
  return errors;
}

const QTY_SCALE = 6;
const MAX_SPLIT_PARTS = 20;

/** A non-negative decimal string as an integer count of millionths, or null when it is not one (at most 6 decimals). */
function toMicros(raw: string): bigint | null {
  const m = /^(\d+)(?:\.(\d{1,6}))?$/.exec(raw.trim());
  if (!m) return null;
  return BigInt(m[1]) * 10n ** BigInt(QTY_SCALE) + BigInt((m[2] ?? "").padEnd(QTY_SCALE, "0") || "0");
}

/** The quantities typed for a split, comma separated; empty entries are ignored. */
export const splitQuantities = (raw: string) =>
  raw
    .split(",")
    .map((q) => q.trim())
    .filter((q) => q !== "");

/**
 * Checks a split before the round trip (FR-17.3): at least one quantity and at most 20, each above zero with at
 * most 6 decimals, and together less than the line so that it keeps a quantity.
 */
export function validateSplit(line: SourcingLine | undefined, raw: string): string | null {
  if (!line) return "Select exactly one line to split.";
  const parts = splitQuantities(raw);
  if (parts.length === 0) return "Enter the quantity of each new line, separated by commas.";
  if (parts.length > MAX_SPLIT_PARTS) return `Split into at most ${MAX_SPLIT_PARTS} new lines at a time.`;
  let total = 0n;
  for (const part of parts) {
    const micros = toMicros(part);
    if (micros === null || micros === 0n) return `"${part}" is not a quantity above zero with at most ${QTY_SCALE} decimals.`;
    total += micros;
  }
  const have = toMicros(line.qty);
  if (have !== null && total >= have) return `The new lines must leave some of the ${line.qty} ${line.unit} on ${lineLabel(line)}.`;
  return null;
}

/**
 * Consolidation board (FR-17.3, 3.17.6): approved requisition lines waiting to
 * be ordered. The buyer selects lines, may override the vendor per line, and
 * the service groups them into one purchase order per vendor, currency, site
 * and delivery window.
 */
export function ConsolidationPage() {
  const navigate = useNavigate();
  const [deadline, setDeadline] = useState(() => defaultDeadline());
  const [sourcing, setSourcing] = useState<SourcingFilter | "">("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<SourcingLine[]>([]);
  const [tick, setTick] = useState(0);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [vendors, setVendors] = useState<Record<string, string>>({});
  const [windowDays, setWindowDays] = useState(DEFAULT_WINDOW);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [created, setCreated] = useState<PurchaseOrder[]>([]);
  const [warnings, setWarnings] = useState<Warning[]>([]);
  const [busy, setBusy] = useState(false);
  const [splitText, setSplitText] = useState("");
  const [splitError, setSplitError] = useState<string | null>(null);
  const [splitDone, setSplitDone] = useState<string | null>(null);
  const page = useResource(
    async (signal) => {
      const result = await listOpenRequisitionLines({ sourcing }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [sourcing, cursor, tick],
  );
  const key = useMemo(() => newIdempotencyKey(), [selected, vendors, windowDays]);
  // One key per selection and deadline, so a retry after a lost response returns the RFQ already started.
  const rfqKey = useMemo(() => newIdempotencyKey(), [selected, deadline]);
  // One key per line and quantities, so a retry after a lost response does not split the line twice.
  const splitKey = useMemo(() => newIdempotencyKey(), [selected, splitText]);
  const chosen = rows.filter((l) => selected.has(l.id));

  const toggle = (id: string, on: boolean) =>
    setSelected((s) => {
      const next = new Set(s);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });

  async function consolidate() {
    const found = validateConsolidation(rows, selected, vendors, windowDays);
    setErrors(found);
    setFailure(null);
    setCreated([]);
    setWarnings([]);
    if (Object.keys(found).length > 0) return;
    const input: ConsolidationInput = {
      windowDays: Number(windowDays),
      lines: rows
        .filter((l) => selected.has(l.id))
        .map((l) => ({ requisitionLineId: l.id, vendorId: (vendors[l.id] ?? "").trim() || undefined })),
    };
    setBusy(true);
    try {
      const { body } = await consolidateRequisitions(input, key);
      setCreated(body.purchaseOrders);
      setWarnings(body.warnings ?? []);
      setSelected(new Set());
      setVendors({});
      setCursor(undefined);
      setTick((n) => n + 1);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  async function startRFQ() {
    const found = validateSourcing(rows, selected, deadline);
    setErrors(found);
    setFailure(null);
    setCreated([]);
    setWarnings([]);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await createRFQFromRequisitions(
        {
          requisitionLineIds: rows.filter((l) => selected.has(l.id)).map((l) => l.id),
          replyDeadline: new Date(deadline).toISOString(),
        },
        rfqKey,
      );
      navigate(`/purchasing/rfqs/${body.id}`);
    } catch (error) {
      setFailure(describeProblem(error));
      setBusy(false);
    }
  }

  async function split() {
    const line = chosen.length === 1 ? chosen[0] : undefined;
    const found = validateSplit(line, splitText);
    setSplitError(found);
    setSplitDone(null);
    setFailure(null);
    if (found !== null || !line) return;
    setBusy(true);
    try {
      await splitRequisitionLine(line.id, line.requisitionVersion, splitQuantities(splitText), splitKey);
      setSplitDone(`${lineLabel(line)} was split into ${splitQuantities(splitText).length + 1} lines.`);
      setSplitText("");
      setSelected(new Set());
      setCursor(undefined);
      setTick((n) => n + 1);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<SourcingLine>[] = [
    {
      key: "select",
      header: "Order",
      render: (l) => (
        <Input
          type="checkbox"
          aria-label={`Select ${lineLabel(l)}`}
          checked={selected.has(l.id)}
          onChange={(e) => toggle(l.id, e.target.checked)}
        />
      ),
    },
    {
      key: "requisition",
      header: "Requisition",
      render: (l) => (
        <Link className={styles.link} to={`/purchasing/requisitions/${l.requisitionId}`}>
          {l.requisitionNumber}
        </Link>
      ),
    },
    { key: "lineNo", header: "Line", align: "end" },
    {
      key: "item",
      header: "Item",
      render: (l) => (
        <>
          {l.description || <Named entity="product" id={l.productId} />}
          {l.splitFromLineId ? <span className={styles.muted}> (split)</span> : null}
        </>
      ),
    },
    { key: "qty", header: "Quantity", align: "end", render: (l) => `${l.qty} ${l.unit}` },
    { key: "amount", header: "Estimate", align: "end", render: (l) => money(l.amount, l.currency) },
    { key: "requestedDate", header: "Requested", mono: true },
    { key: "siteId", header: "Site", render: (l) => <Named entity="site" id={l.siteId} /> },
    { key: "warehouseId", header: "Warehouse", render: (l) => <Named entity="warehouse" id={l.warehouseId} /> },
    {
      key: "vendor",
      header: "Vendor",
      render: (l) => (
        <EntityLookup
          entity="vendor"
          compact
          aria-label={`Vendor for ${lineLabel(l)}`}
          placeholder="Needs a vendor"
          invalid={Boolean(errors[l.id])}
          value={vendors[l.id] ?? l.preferredVendorId ?? ""}
          onChange={(e) => setVendors((v) => ({ ...v, [l.id]: e.target.value }))}
        />
      ),
    },
  ];

  const lineErrors = Object.entries(errors).filter(([k]) => k !== "windowDays" && k !== "replyDeadline");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Consolidation"
        description="Approved requisition lines waiting to be ordered. Select lines and create purchase orders: one per vendor, currency, site, warehouse and delivery window. Lines with no vendor can start an RFQ instead; they stay out of this list until the RFQ is awarded or cancelled."
        actions={
          <Link className={buttonClassName("secondary")} to="/purchasing/releases/new">
            Release all lines with a vendor
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="cons-sourcing" label="Show">
            {(c) => (
              <Select
                {...c}
                value={sourcing}
                onChange={(e) => {
                  setCursor(undefined);
                  setSourcing(e.target.value as SourcingFilter | "");
                }}
              >
                {(Object.keys(sourcingLabels) as (SourcingFilter | "")[]).map((k) => (
                  <option key={k} value={k}>
                    {sourcingLabels[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
        <div className={styles.filter}>
          <FormField id="cons-window" label="Delivery window (days)" hint="Lines requested within this many days share an order" error={errors.windowDays}>
            {(c) => <Input {...c} inputMode="numeric" value={windowDays} onChange={(e) => setWindowDays(e.target.value)} />}
          </FormField>
        </div>
        <div className={styles.filter}>
          <FormField id="cons-deadline" label="RFQ reply deadline" hint="Your local time; used by Start RFQ" error={errors.replyDeadline}>
            {(c) => <Input {...c} type="datetime-local" value={deadline} onChange={(e) => setDeadline(e.target.value)} />}
          </FormField>
        </div>
      </div>
      {page.error ? <LoadError what="requisition lines" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Lines to order"
        columns={columns}
        rows={page.error ? [] : rows}
        getRowKey={(l) => l.id}
        loading={page.loading && rows.length === 0}
        empty={{
          title: "Nothing to order",
          description: "Approved requisition lines appear here until they are on a purchase order.",
        }}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button loading={page.loading} onClick={() => setCursor(page.data?.nextCursor)}>
            Load more
          </Button>
        </div>
      ) : null}
      <Alert tone="danger" live="alert">
        {lineErrors.length > 0 ? (
          <ul className={styles.cardLinks}>
            {lineErrors.map(([k, msg]) => (
              <li key={k}>{msg}</li>
            ))}
          </ul>
        ) : failure ? (
          <ProblemMessage problem={failure} />
        ) : null}
      </Alert>
      <div className={styles.actions}>
        <Button variant="primary" loading={busy} onClick={() => void consolidate()}>
          Create purchase orders
        </Button>
        <Button loading={busy} onClick={() => void startRFQ()}>
          Start RFQ for selected lines
        </Button>
        <p className={styles.muted}>{selected.size === 1 ? "1 line selected" : `${selected.size} lines selected`}</p>
      </div>
      <Card title="Split a line">
        <p className={styles.muted}>
          Order part of a line from one vendor and the rest from another: select one line, enter the quantities of the new lines, and the
          line keeps what is left. A line on a running RFQ cannot be split.
        </p>
        <div className={styles.filter}>
          <FormField
            id="cons-split"
            label="Quantities of the new lines"
            hint={chosen.length === 1 ? `${lineLabel(chosen[0])} has ${chosen[0].qty} ${chosen[0].unit}; separate quantities with commas` : "Select one line first"}
            error={splitError ?? undefined}
          >
            {(c) => <Input {...c} inputMode="decimal" placeholder="40, 35" value={splitText} onChange={(e) => setSplitText(e.target.value)} />}
          </FormField>
        </div>
        <div className={styles.actions}>
          <Button loading={busy} onClick={() => void split()}>
            Split line
          </Button>
        </div>
        <Alert tone="success" live="status">
          {splitDone}
        </Alert>
      </Card>
      {warnings.length > 0 ? (
        <Alert tone="warning" title="Check the vendors against their categories">
          <ul className={styles.cardLinks}>
            {warnings.map((w, i) => (
              <li key={`${w.code}-${i}`}>
                {w.detail} ({w.code})
              </li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {created.length > 0 ? (
        <Card title={created.length === 1 ? "1 purchase order created" : `${created.length} purchase orders created`}>
          <ul className={styles.cardLinks} aria-label="Purchase orders created">
            {created.map((po) => (
              <li key={po.id}>
                <Link className={styles.link} to={`/purchasing/purchase-orders/${po.id}`}>
                  {po.number}
                </Link>{" "}
                · {po.lines.length} {po.lines.length === 1 ? "line" : "lines"} · {money(po.total, po.currency)}
              </li>
            ))}
          </ul>
        </Card>
      ) : null}
    </div>
  );
}
