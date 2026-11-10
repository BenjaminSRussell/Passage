import { useEffect, useState } from "react";
import { Select } from "../../shell/ui";
import { formatOption, resolveOption } from "../../shell/lookup/entities";

// Scheduling, quality and shop floor records point at production orders by id. This reads the order number (and the
// product it makes) so screens can show "MO-24618" instead of an id.

export interface OrderRef {
  id: string;
  orderNumber: string;
  productId: string;
  /** "Name (Number)" of the product, once resolved. */
  product?: string;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const cache = new Map<string, Promise<OrderRef | null>>();
let listCache: Promise<{ id: string; orderNumber: string; status?: string }[]> | null = null;

/** Forgets the loaded orders (tests). */
export function resetOrderCache(): void {
  cache.clear();
  listCache = null;
}

function loadOrder(id: string): Promise<OrderRef | null> {
  let pending = cache.get(id);
  if (!pending) {
    pending = fetch(`/api/v1/production/orders/${encodeURIComponent(id)}`, { headers: { Accept: "application/json" } })
      .then(async (res) => {
        if (!res.ok) return null;
        const body = (await res.json()) as { orderNumber?: string; productId?: string };
        if (!body.orderNumber) return null;
        const product = body.productId ? await resolveOption("product", body.productId) : null;
        return { id, orderNumber: body.orderNumber, productId: body.productId ?? "", product: product ? formatOption(product) : undefined };
      })
      .catch(() => null);
    cache.set(id, pending);
    void pending.then((found) => {
      if (found === null) cache.delete(id);
    });
  }
  return pending;
}

/** What is known so far about each order id; ids that do not resolve are simply absent. */
export function useOrderRefs(ids: string[]): Record<string, OrderRef> {
  const [refs, setRefs] = useState<Record<string, OrderRef>>({});
  const key = [...new Set(ids.filter(Boolean))].sort().join("|");
  useEffect(() => {
    let live = true;
    for (const id of key.split("|").filter(Boolean)) {
      void loadOrder(id).then((found) => {
        if (live && found) setRefs((prev) => (prev[id] ? prev : { ...prev, [id]: found }));
      });
    }
    return () => {
      live = false;
    };
  }, [key]);
  return refs;
}

/** The text to show for an order id before or without a resolved number. */
export function orderLabel(id: string, refs: Record<string, OrderRef>): string {
  return refs[id]?.orderNumber ?? (UUID.test(id) ? "Order" : id);
}

/** An order's number, resolved from its id. */
export function OrderNumber({ id }: { id: string }) {
  const refs = useOrderRefs([id]);
  return <span>{orderLabel(id, refs)}</span>;
}

function loadOrderList() {
  listCache ??= fetch("/api/v1/production/orders?limit=200", { headers: { Accept: "application/json" } })
    .then((res) => (res.ok ? (res.json() as Promise<{ items?: { id: string; orderNumber: string; status?: string }[] }>) : { items: [] }))
    .then((body) => (body.items ?? []).filter((o) => o.id && o.orderNumber))
    .catch(() => {
      listCache = null;
      return [];
    });
  return listCache;
}

/** The reference of a source document: a production order shows its number, other references show as given (short when an id). */
export function SourceDocument({ type, id }: { type: string; id: string }) {
  const refs = useOrderRefs(type === "production-order" ? [id] : []);
  if (type === "production-order") return <span>{orderLabel(id, refs)}</span>;
  return <span>{UUID.test(id) ? id.slice(0, 8) : id}</span>;
}

/** A production order picked by its number. The value is the order id. */
export function OrderSelect({
  value,
  onChange,
  emptyLabel = "Choose a production order",
  ...aria
}: {
  value: string;
  onChange(value: string): void;
  emptyLabel?: string;
  id?: string;
  "aria-label"?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean | "true" | "false";
  "aria-required"?: boolean;
}) {
  const [rows, setRows] = useState<{ id: string; orderNumber: string; status?: string }[]>([]);
  useEffect(() => {
    let live = true;
    void loadOrderList().then((found) => {
      if (live) setRows(found);
    });
    return () => {
      live = false;
    };
  }, []);
  return (
    <Select {...aria} value={value} onChange={(event) => onChange(event.target.value)}>
      <option value="">{emptyLabel}</option>
      {rows.map((o) => (
        <option key={o.id} value={o.id}>
          {o.orderNumber}
        </option>
      ))}
    </Select>
  );
}
