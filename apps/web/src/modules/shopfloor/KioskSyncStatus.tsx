import { useCallback, useEffect, useState } from "react";
import { Alert, Button } from "../../shell/ui";
import type { FlushResult, OfflineQueue } from "./offlineQueue";

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

/**
 * The kiosk's offline queue (D5): how many actions wait on this device, a button to sync them,
 * and an automatic sync when the browser comes back online and when the kiosk opens.
 */
export function KioskSyncStatus({ queue }: { queue: OfflineQueue }) {
  const [size, setSize] = useState(queue.lastKnownSize);
  const [busy, setBusy] = useState(false);
  const [last, setLast] = useState<FlushResult | null>(null);

  const sync = useCallback(async () => {
    setBusy(true);
    try {
      const res = await queue.flush();
      setLast(res.sent + res.duplicates > 0 || res.refused ? res : null);
    } finally {
      setBusy(false);
    }
  }, [queue]);

  useEffect(() => queue.subscribe(setSize), [queue]);

  useEffect(() => {
    const online = () => void sync();
    window.addEventListener("online", online);
    void queue.size().then((n) => {
      if (n > 0) void sync();
    });
    return () => window.removeEventListener("online", online);
  }, [queue, sync]);

  if (size > 0) {
    return (
      <Alert
        tone={last?.refused ? "danger" : "warning"}
        title={`${plural(size, "action", "actions")} waiting to sync`}
        live="status"
        action={
          <Button size="sm" loading={busy} onClick={() => void sync()}>
            Sync now
          </Button>
        }
      >
        {last?.refused
          ? `The service refused the queued actions: ${last.refused.message} (${last.refused.code}). Ask your supervisor.`
          : "This kiosk is offline. Punches and job actions are saved here and sent in order when the network is back."}
      </Alert>
    );
  }
  if (last && last.remaining === 0) {
    return (
      <Alert tone="success" live="status">
        Synced {plural(last.sent + last.duplicates, "offline action", "offline actions")}.
      </Alert>
    );
  }
  return null;
}
