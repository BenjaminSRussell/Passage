import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { formatOption, resolveOption, type LookupEntityKey, type LookupOption } from "../../shell/lookup/entities";
import { getOrder } from "./api/client";

const numbers = new Map<string, Promise<string | null>>();

function lookup(id: string): Promise<string | null> {
  let pending = numbers.get(id);
  if (!pending) {
    pending = getOrder(id).then(
      (res) => res.value.orderNumber || null,
      () => null,
    );
    numbers.set(id, pending);
    void pending.then((found) => {
      if (found === null) numbers.delete(id);
    });
  }
  return pending;
}

/** A sales order reference shown as its order number, linked to the order. Rows that carry only the order id use this. */
export function OrderNumber({ id, fallback = "Sales order", link = true }: { id: string; fallback?: string; link?: boolean }) {
  const [number, setNumber] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    setNumber(null);
    if (id !== "") void lookup(id).then((found) => live && setNumber(found));
    return () => {
      live = false;
    };
  }, [id]);
  if (id === "") return <span>{fallback}</span>;
  const shown = number ?? fallback;
  return link ? (
    <Link to={`/sales/orders/${encodeURIComponent(id)}`} onClick={(e) => e.stopPropagation()}>
      {shown}
    </Link>
  ) : (
    <span>{shown}</span>
  );
}

/**
 * A name as plain text, for a table's first column: that cell is wrapped in a button when the row opens, and the
 * shared EntityName (which holds a menu) must not sit inside a button.
 */
export function PlainName({ entity, id, short = false }: { entity: LookupEntityKey; id: string; short?: boolean }) {
  const fallback = short ? id.slice(0, 8) : id;
  const [entry, setEntry] = useState<LookupOption | null>(null);
  useEffect(() => {
    let live = true;
    setEntry(null);
    if (id !== "") void resolveOption(entity, id).then((found) => live && setEntry(found));
    return () => {
      live = false;
    };
  }, [entity, id]);
  return <span>{entry ? formatOption(entry) : fallback}</span>;
}

const lineNumbers =new Map<string, Promise<Map<string, number> | null>>();

function linesOf(orderId: string): Promise<Map<string, number> | null> {
  let pending = lineNumbers.get(orderId);
  if (!pending) {
    pending = getOrder(orderId).then(
      (res) => new Map(res.value.lines.map((l): [string, number] => [l.id, l.lineNo])),
      () => null,
    );
    lineNumbers.set(orderId, pending);
  }
  return pending;
}

/** An order line shown as its line number on the order, for rows that carry only the line id. */
export function OrderLineNo({ orderId, lineId }: { orderId: string; lineId: string }) {
  const [no, setNo] = useState<number | null>(null);
  useEffect(() => {
    let live = true;
    void linesOf(orderId).then((found) => live && setNo(found?.get(lineId) ?? null));
    return () => {
      live = false;
    };
  }, [orderId, lineId]);
  return <span>{no === null ? "Order line" : `Line ${no}`}</span>;
}
