import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  consolidateRequisitions,
  describeProblem,
  listOpenRequisitionLines,
  newIdempotencyKey,
  type ConsolidationInput,
  type PurchaseOrder,
  type SourcingFilter,
  type SourcingLine,
  type Warning,
} from "./api/client";
import { LoadError, ShortId, money, useResource, uuidPattern, type Failure } from "./parts";
import styles from "./pages.module.css";

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
 * Consolidation board (FR-17.3, 3.17.6): approved requisition lines waiting to
 * be ordered. The buyer selects lines, may override the vendor per line, and
 * the service groups them into one purchase order per vendor, currency, site
 * and delivery window.
 */
export function ConsolidationPage() {
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
  const page = useResource(
    async (signal) => {
      const result = await listOpenRequisitionLines({ sourcing }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [sourcing, cursor, tick],
  );
  const key = useMemo(() => newIdempotencyKey(), [selected, vendors, windowDays]);

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
    { key: "item", header: "Item", render: (l) => l.description || <ShortId id={l.productId} /> },
    { key: "qty", header: "Quantity", align: "end", render: (l) => `${l.qty} ${l.unit}` },
    { key: "amount", header: "Estimate", align: "end", render: (l) => money(l.amount, l.currency) },
    { key: "requestedDate", header: "Requested", mono: true },
    { key: "siteId", header: "Site", render: (l) => <ShortId id={l.siteId} /> },
    {
      key: "vendor",
      header: "Vendor",
      render: (l) => (
        <Input
          aria-label={`Vendor for ${lineLabel(l)}`}
          placeholder={l.preferredVendorId ? l.preferredVendorId.slice(0, 8) : "Needs a vendor"}
          title={l.preferredVendorId ? `Preferred vendor ${l.preferredVendorId}` : "No preferred vendor"}
          invalid={Boolean(errors[l.id])}
          value={vendors[l.id] ?? ""}
          onChange={(e) => setVendors((v) => ({ ...v, [l.id]: e.target.value }))}
        />
      ),
    },
  ];

  const lineErrors = Object.entries(errors).filter(([k]) => k !== "windowDays");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Consolidation"
        description="Approved requisition lines waiting to be ordered. Select lines and create purchase orders: one per vendor, currency, site and delivery window."
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
          `${failure.message} (${failure.code})`
        ) : null}
      </Alert>
      <div className={styles.actions}>
        <Button variant="primary" loading={busy} onClick={() => void consolidate()}>
          Create purchase orders
        </Button>
        <p className={styles.muted}>{selected.size === 1 ? "1 line selected" : `${selected.size} lines selected`}</p>
      </div>
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
