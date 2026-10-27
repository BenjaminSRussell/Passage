import { useEffect, useRef, useState, type MutableRefObject } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Select, type DataTableColumn } from "../../shell/ui";
import {
  firmPlannedOrders,
  FirmGrouping,
  MAX_FIRM_BATCH,
  type FirmBatch,
  type FirmedGroup,
  type PlannedOrder,
} from "./api/client";
import { describeError, type LoadError } from "./hooks";
import styles from "./pages.module.css";

const dash = "—";

const groupingLabel: Record<FirmGrouping, string> = {
  "vendor-date": "Vendor and delivery date",
  "vendor-week": "Vendor and delivery week",
  vendor: "Vendor",
  none: "One purchase order per planned order",
};

const targetLabel: Record<FirmedGroup["targetType"], string> = {
  "purchase-order": "Purchase order",
  "production-order": "Production order",
  "transfer-order": "Transfer order",
};

/** Only unprocessed and approved planned orders can be firmed. */
export const isFirmable = (o: Pick<PlannedOrder, "status">) => o.status === "unprocessed" || o.status === "approved";

/** Checks a selection before it is sent: a message, or null when it can be firmed. */
export function validateFirmSelection(ids: readonly string[]): string | null {
  if (ids.length === 0) return "Select at least one planned order to firm.";
  if (ids.length > MAX_FIRM_BATCH) return `Firm at most ${MAX_FIRM_BATCH} planned orders at a time.`;
  return null;
}

/**
 * Ids of the firmable planned orders from the row of fromId to the row of
 * toId, inclusive and in either direction (keyboard and shift-click range
 * selection, 4.22.6 interaction rules). Empty when either row is not listed.
 */
export function rangeIds(rows: readonly PlannedOrder[], fromId: string, toId: string): string[] {
  const from = rows.findIndex((o) => o.id === fromId);
  const to = rows.findIndex((o) => o.id === toId);
  if (from < 0 || to < 0) return [];
  return rows
    .slice(Math.min(from, to), Math.max(from, to) + 1)
    .filter(isFirmable)
    .map((o) => o.id);
}

const NON_TEXT_INPUTS = new Set(["checkbox", "radio", "button", "submit", "reset", "range", "color", "file", "image"]);

/** True when a key press goes to a field that takes text, where the `F` shortcut must not fire. */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable || target.closest('[contenteditable]:not([contenteditable="false"])')) return true;
  if (target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement) return true;
  return target instanceof HTMLInputElement && !NON_TEXT_INPUTS.has(target.type);
}

/**
 * Tracks whether Shift is held, so Shift+Space on a focused checkbox extends
 * the selection like Shift+click does (the click a key press makes need not
 * carry the modifier).
 */
export function useShiftHeld(): MutableRefObject<boolean> {
  const held = useRef(false);
  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.key === "Shift") held.current = true;
    };
    const up = (e: KeyboardEvent) => {
      if (e.key === "Shift") held.current = false;
    };
    const reset = () => {
      held.current = false;
    };
    window.addEventListener("keydown", down);
    window.addEventListener("keyup", up);
    window.addEventListener("blur", reset);
    return () => {
      window.removeEventListener("keydown", down);
      window.removeEventListener("keyup", up);
      window.removeEventListener("blur", reset);
    };
  }, []);
  return held;
}

function newKey(): string {
  const c = globalThis.crypto as Crypto | undefined;
  return c?.randomUUID ? c.randomUUID() : `firm-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

const groupColumns: DataTableColumn<FirmedGroup>[] = [
  { key: "targetType", header: "Becomes", render: (g) => <Badge tone="success">{targetLabel[g.targetType]}</Badge> },
  { key: "targetId", header: "Order id", mono: true },
  { key: "vendorId", header: "Vendor", mono: true, render: (g) => g.vendorId ?? dash },
  { key: "deliveryDate", header: "Delivery date" },
  { key: "qty", header: "Quantity", align: "end" },
  { key: "plannedOrderIds", header: "Planned orders", align: "end", render: (g) => g.plannedOrderIds.length },
];

interface Props {
  /** Ids of the selected planned orders. */
  selected: readonly string[];
  /** How many open orders the list shows (for "Select all open"). */
  openCount: number;
  onSelectAllOpen: () => void;
  onClear: () => void;
  /** Called after a successful firming, so the list reloads. */
  onFirmed: (batch: FirmBatch) => void;
}

/**
 * Firm planned orders (FR-22.10): the selected orders become purchase,
 * production and transfer orders, planned purchases merged per the grouping.
 * The Idempotency-Key is kept while selection and grouping are unchanged, so
 * a retry after a network error replays the same firming. Pressing `F`
 * outside a text field firms the selection (4.22.6 interaction rules).
 */
export function FirmCard({ selected, openCount, onSelectAllOpen, onClear, onFirmed }: Props) {
  const [grouping, setGrouping] = useState<FirmGrouping>("vendor-date");
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [result, setResult] = useState<FirmBatch | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ input: "", value: newKey() });
  const problem = validateFirmSelection(selected);

  async function firm() {
    if (busy || problem) return;
    const ids = [...selected].sort();
    const input = `${grouping}|${ids.join(",")}`;
    if (key.current.input !== input) key.current = { input, value: newKey() };
    setBusy(true);
    setFailure(null);
    setResult(null);
    try {
      const batch = await firmPlannedOrders(ids, grouping, key.current.value);
      key.current = { input: "", value: newKey() };
      setResult(batch);
      onFirmed(batch);
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  // The `F` shortcut calls the latest firm(); with nothing to firm (or a
  // firming under way) the key is left alone.
  const shortcut = useRef({ firm, ready: false });
  shortcut.current = { firm, ready: !busy && problem === null };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "f" && e.key !== "F") return;
      if (e.ctrlKey || e.metaKey || e.altKey || e.repeat || e.defaultPrevented || isTypingTarget(e.target)) return;
      if (!shortcut.current.ready) return;
      e.preventDefault();
      void shortcut.current.firm();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const count = selected.length;
  return (
    <Card title="Firm planned orders">
      <div className={styles.stack}>
        <p className={styles.summary}>
          {count === 0
            ? "Select open planned orders in the list below. Firmed orders become purchase, production and transfer orders."
            : `${count} planned ${count === 1 ? "order" : "orders"} selected.`}
        </p>
        <p className={styles.summary}>
          Hold <kbd>Shift</kbd> while ticking a box (click or <kbd>Space</kbd>) to select every open order from the last box
          ticked; press <kbd>F</kbd> to firm the selection.
        </p>
        <Alert tone="danger" title={failure ? "The orders were not firmed" : undefined} live="alert">
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
        <Alert
          tone="success"
          title={result ? `Firmed ${result.firmed} planned ${result.firmed === 1 ? "order" : "orders"} into ${result.groups.length} ${result.groups.length === 1 ? "order" : "orders"}` : undefined}
          live="status"
        >
          {result ? "Purchasing and production create these orders with the ids shown." : null}
        </Alert>
        <div className={styles.toolbar}>
          <div className={styles.toolbarField}>
            <FormField id="planning-firm-grouping" label="Purchase order grouping">
              {(a11y) => (
                <Select {...a11y} value={grouping} onChange={(e) => setGrouping(FirmGrouping.parse(e.target.value))}>
                  {FirmGrouping.options.map((g) => (
                    <option key={g} value={g}>
                      {groupingLabel[g]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
          <Button variant="primary" loading={busy} disabled={problem !== null} onClick={firm} aria-keyshortcuts="F">
            Firm selected
          </Button>
          <Button onClick={onSelectAllOpen} disabled={openCount === 0 || busy}>
            Select all open
          </Button>
          <Button variant="ghost" onClick={onClear} disabled={count === 0 || busy}>
            Clear selection
          </Button>
        </div>
        {count > MAX_FIRM_BATCH ? <p className={styles.formError}>{problem}</p> : null}
        {result ? (
          <DataTable label="Orders created by firming" columns={groupColumns} rows={result.groups} getRowKey={(g) => g.targetId} />
        ) : null}
      </div>
    </Card>
  );
}
