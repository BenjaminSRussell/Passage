import { useEffect, useState } from "react";
import { Badge, Card, StatGroup, StatTile } from "../../shell/ui";
import { oeeStreamUrl, parseOeeTile, type OeeBucketSize, type OeeTile } from "./api/client";
import { formatDateTime, percent } from "./format";
import styles from "./pages.module.css";

type EventSourceCtor = new (url: string, init?: EventSourceInit) => EventSource;

export interface OeeLiveTileProps {
  resourceId: string;
  machineLabel: string;
  bucket: OeeBucketSize;
  /** Replaced in tests; the browser's EventSource otherwise. */
  EventSource?: EventSourceCtor;
}

type Link = "connecting" | "live" | "reconnecting" | "unsupported";

const LINK_LABEL: Record<Link, string> = {
  connecting: "Connecting",
  live: "Live",
  reconnecting: "Reconnecting",
  unsupported: "Live updates unavailable",
};

/**
 * The live OEE tile of one machine (FR-25.14, AC-25.4): the current bucket pushed over
 * Server-Sent Events by GET /oee/stream, so the tile follows new machine counts within
 * seconds. The browser reconnects by itself when the stream ends or drops.
 */
export function OeeLiveTile({ resourceId, machineLabel, bucket, EventSource: Source = globalThis.EventSource }: OeeLiveTileProps) {
  const [tile, setTile] = useState<OeeTile | null>(null);
  const [link, setLink] = useState<Link>(Source ? "connecting" : "unsupported");

  useEffect(() => {
    setTile(null);
    if (!Source) {
      setLink("unsupported");
      return undefined;
    }
    setLink("connecting");
    const es = new Source(oeeStreamUrl(resourceId, bucket), { withCredentials: true });
    const onTile = (event: MessageEvent<string>) => {
      const next = parseOeeTile(event.data);
      if (next && next.resourceId === resourceId) {
        setTile(next);
        setLink("live");
      }
    };
    es.addEventListener("oee", onTile as EventListener);
    es.onopen = () => setLink("live");
    es.onerror = () => setLink("reconnecting");
    return () => {
      es.removeEventListener("oee", onTile as EventListener);
      es.close();
    };
  }, [Source, resourceId, bucket]);

  const b = tile?.bucket ?? null;
  return (
    <Card
      title={`Live OEE: ${machineLabel}`}
      actions={<Badge tone={link === "live" ? "success" : link === "unsupported" ? "neutral" : "warning"}>{LINK_LABEL[link]}</Badge>}
    >
      {!tile ? (
        <p className={styles.value}>Waiting for the current bucket…</p>
      ) : (
        <div className={styles.stack}>
          <p className={styles.value}>
            Bucket from {formatDateTime(tile.bucketStart)}, updated {formatDateTime(tile.computedAt)}.
          </p>
          {b ? (
            <StatGroup label="Current bucket">
              <StatTile label="OEE" value={percent(b.oee)} />
              <StatTile label="Availability" value={percent(b.availability)} />
              <StatTile label="Performance" value={percent(b.performance)} hint={b.overRate ? "Capped: counts exceed the ideal rate" : undefined} />
              <StatTile label="Quality" value={percent(b.quality)} />
              <StatTile label="Good / total" value={`${b.goodCount} / ${b.totalCount}`} />
            </StatGroup>
          ) : (
            <p className={styles.value}>No planned time in this bucket yet: the machine is off or has not reported a state.</p>
          )}
        </div>
      )}
    </Card>
  );
}
