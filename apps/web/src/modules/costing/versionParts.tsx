import type { ReactNode } from "react";
import { Alert, Button } from "../../shell/ui";
import { CostingApiError, describeError, type OnHandLine } from "./api/client";
import styles from "./pages.module.css";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const SIGNED_DECIMAL = /^-?\d+(\.\d{1,6})?$/;

export const dash = "—";

/** The stable problem code of a failed call, or "" for a network failure. */
export function problemCode(err: unknown): string {
  return err instanceof CostingApiError ? err.problem.code : "";
}

export function Detail({ term, children, mono = false }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

export function LoadError({ title, error, onRetry }: { title: string; error: unknown; onRetry(): void }) {
  return (
    <Alert
      tone="danger"
      title={title}
      live="alert"
      action={
        <Button size="sm" onClick={onRetry}>
          Try again
        </Button>
      }
    >
      {describeError(error, "The costing service could not be reached.")}
    </Alert>
  );
}

/** Formats an ISO timestamp as a short local date and time, or a dash. */
export function formatDateTime(iso: string | undefined): string {
  if (!iso) return dash;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

const SCALE = 6;

function toMicros(value: string): bigint {
  const negative = value.startsWith("-");
  const [whole, frac = ""] = (negative ? value.slice(1) : value).split(".");
  const micros = BigInt(whole || "0") * 10n ** BigInt(SCALE) + BigInt((frac + "0".repeat(SCALE)).slice(0, SCALE));
  return negative ? -micros : micros;
}

function fromMicros(micros: bigint): string {
  const negative = micros < 0n;
  const abs = negative ? -micros : micros;
  const unit = 10n ** BigInt(SCALE);
  const frac = (abs % unit).toString().padStart(SCALE, "0").replace(/0+$/, "");
  return `${negative ? "-" : ""}${abs / unit}${frac ? `.${frac}` : ""}`;
}

/**
 * a − b for decimal strings of up to six places, exactly (costs never pass
 * through floating point). A positive result is shown with a leading "+".
 */
export function costDelta(a: string, b: string | undefined): string {
  if (b === undefined) return dash;
  const diff = toMicros(a) - toMicros(b);
  const text = fromMicros(diff);
  return diff > 0n ? `+${text}` : text;
}

/**
 * Parses the on-hand CSV the activation revalues: a header naming productId,
 * siteId and qty (bookValue optional, any order), then one row per product and
 * site. Empty text means no stock anywhere.
 */
export function parseOnHandCsv(text: string): { ok: true; value: OnHandLine[] } | { ok: false; error: string } {
  const rows = text
    .split(/\r?\n/)
    .map((line, i) => ({ line: i + 1, cells: line.split(",").map((c) => c.trim()) }))
    .filter((r) => r.cells.some((c) => c !== ""));
  if (rows.length === 0) return { ok: true, value: [] };
  const header = rows[0].cells.map((c) => c.toLowerCase());
  const at = { productId: header.indexOf("productid"), siteId: header.indexOf("siteid"), qty: header.indexOf("qty"), bookValue: header.indexOf("bookvalue") };
  if (at.productId < 0 || at.siteId < 0 || at.qty < 0) {
    return { ok: false, error: "The first line must name the columns productId, siteId and qty (bookValue is optional)." };
  }
  const seen = new Set<string>();
  const value: OnHandLine[] = [];
  for (const { line, cells } of rows.slice(1)) {
    const productId = cells[at.productId] ?? "";
    const siteId = cells[at.siteId] ?? "";
    const qty = cells[at.qty] ?? "";
    const bookValue = at.bookValue >= 0 ? cells[at.bookValue] ?? "" : "";
    if (!UUID.test(productId) || !UUID.test(siteId)) return { ok: false, error: `Line ${line}: productId and siteId must be UUIDs.` };
    if (!SIGNED_DECIMAL.test(qty)) return { ok: false, error: `Line ${line}: qty must be a number with at most 6 decimals.` };
    if (bookValue !== "" && !SIGNED_DECIMAL.test(bookValue)) return { ok: false, error: `Line ${line}: bookValue must be a number with at most 6 decimals.` };
    const key = `${productId.toLowerCase()}|${siteId.toLowerCase()}`;
    if (seen.has(key)) return { ok: false, error: `Line ${line}: this product and site is already listed.` };
    seen.add(key);
    value.push(bookValue === "" ? { productId, siteId, qty } : { productId, siteId, qty, bookValue });
  }
  return { ok: true, value };
}
