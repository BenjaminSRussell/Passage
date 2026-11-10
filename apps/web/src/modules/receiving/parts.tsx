import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Badge, type BadgeTone } from "../../shell/ui";
import { fetchPurchaseOrderRef, newKey, type Receipt, type ReceiptState } from "./api/client";
import { ApiError, fieldErrors } from "../../shell/api/http";
import styles from "../../shell/pageLayout.module.css";

const stateTone: Record<ReceiptState, BadgeTone> = {
  ARRIVED: "info",
  REGISTERED: "warning",
  POSTED: "success",
  CANCELLED: "neutral",
};

const stateLabel: Record<ReceiptState, string> = {
  ARRIVED: "Arrived",
  REGISTERED: "Registered",
  POSTED: "Posted",
  CANCELLED: "Cancelled",
};

export function StateBadge({ state }: { state: ReceiptState }) {
  return <Badge tone={stateTone[state]}>{stateLabel[state]}</Badge>;
}

/** The purchase order a receipt is against, shown by its number and linked to the order. */
export function PurchaseOrderRef({ id }: { id: string }) {
  const found = useQuery({ queryKey: ["receiving", "purchase-order-ref", id], queryFn: () => fetchPurchaseOrderRef(id), enabled: id !== "", staleTime: 60_000 });
  if (!found.data) return <>{found.isError ? "Purchase order" : "…"}</>;
  return <Link to={`/purchasing/purchase-orders/${encodeURIComponent(id)}`}>{found.data.number}</Link>;
}

export function Detail({ term, children, mono = false }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/**
 * Runs one receipt command with a stable Idempotency-Key per intended action: a retry after a
 * timeout replays, and the key is renewed once the command succeeded.
 */
export function useAction(receiptId: string) {
  const queryClient = useQueryClient();
  const key = useRef(newKey());
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ error: unknown; fields: Record<string, string>; stale: boolean } | null>(null);

  async function run(command: (idempotencyKey: string) => Promise<Receipt>): Promise<boolean> {
    setBusy(true);
    setFailure(null);
    try {
      const updated = await command(key.current);
      key.current = newKey();
      queryClient.setQueryData(["receiving", "receipt", receiptId], updated);
      await queryClient.invalidateQueries({ queryKey: ["receiving", "receipts"] });
      return true;
    } catch (error) {
      setFailure({ error, fields: fieldErrors(error), stale: error instanceof ApiError && error.status === 412 });
      return false;
    } finally {
      setBusy(false);
    }
  }

  return { run, busy, failure, clear: () => setFailure(null) };
}

export { Named } from "../purchasing/Named";
