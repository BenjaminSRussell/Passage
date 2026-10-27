import { useState, type FormEvent } from "react";
import {
  CLUSTER_HARD_MAX,
  checkPosition,
  completePosition,
  createCluster,
  nextOpen,
  shortPosition,
  type Cluster,
  type ClusterWork,
} from "./domain/cluster";

/**
 * Cluster pick cart on the device (BR-32.5): up to 8 works, each bound to one cart position and
 * one order's LP. The worker scans the next open position; a wrong position is rejected with
 * MOB-0109, and the position can be completed or shorted only after the right one is scanned.
 * A short pick releases nothing else. Device UI: it uses the warehouse-mobile shell classes
 * (app.css) and is rendered by apps/warehouse-mobile. Works are typed in until the mobile
 * service serves them (handoff, see apps/warehouse-mobile/NOT-IMPLEMENTED.md).
 */

/** Parses "workId,orderId,lp" lines. Blank lines are dropped; missing fields become empty strings. */
export function parseWorks(text: string): ClusterWork[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "")
    .map((line) => {
      const [workId = "", orderId = "", lp = ""] = line.split(",").map((part) => part.trim());
      return { workId, orderId, lp };
    });
}

/** The 1-based line number of the first work missing a field, or null when every work is complete. */
export function incompleteLine(works: readonly ClusterWork[]): number | null {
  const i = works.findIndex((w) => !w.workId || !w.orderId || !w.lp);
  return i < 0 ? null : i + 1;
}

const CODE_WRONG_POSITION = "MOB-0109";

function errorText(e: unknown): string {
  return e instanceof Error ? e.message : "The cluster could not be updated.";
}

interface Notice {
  tone: "error" | "info";
  text: string;
}

export interface ClusterPanelProps {
  /** §6.32.13 cluster_max_positions; capped at 8 by BR-32.5. */
  maxPositions?: number;
}

export function ClusterPanel({ maxPositions = CLUSTER_HARD_MAX }: ClusterPanelProps) {
  const [worksText, setWorksText] = useState("");
  const [cluster, setCluster] = useState<Cluster | null>(null);
  const [confirmed, setConfirmed] = useState<number | null>(null);
  const [scan, setScan] = useState("");
  const [notice, setNotice] = useState<Notice | null>(null);

  const next = cluster ? nextOpen(cluster) : null;
  const ready = next !== null && confirmed === next.position;

  function onBuild(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const works = parseWorks(worksText);
    const bad = incompleteLine(works);
    if (bad !== null) {
      setNotice({ tone: "error", text: `Line ${bad} needs a work, an order and an LP.` });
      return;
    }
    try {
      setCluster(createCluster(works, maxPositions));
      setConfirmed(null);
      setNotice(null);
    } catch (err) {
      setNotice({ tone: "error", text: errorText(err) });
    }
  }

  function onNewCluster() {
    setCluster(null);
    setConfirmed(null);
    setWorksText("");
    setNotice(null);
  }

  function onScanPosition(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!cluster || !next) return;
    const raw = scan.trim();
    setScan("");
    if (!/^\d+$/.test(raw)) {
      setNotice({ tone: "error", text: "Scan a cart position number." });
      return;
    }
    const mismatch = checkPosition(cluster, next.position, Number(raw));
    if (mismatch) {
      setConfirmed(null);
      setNotice({ tone: "error", text: `${CODE_WRONG_POSITION} ${mismatch}` });
      return;
    }
    setConfirmed(next.position);
    setNotice({ tone: "info", text: `Position ${next.position} confirmed for LP ${next.lp}.` });
  }

  function applyChange(change: (c: Cluster, position: number) => Cluster, verb: string) {
    if (!cluster || !next || !ready) return;
    try {
      setCluster(change(cluster, next.position));
      setConfirmed(null);
      setNotice({ tone: "info", text: `Position ${next.position} ${verb}.` });
    } catch (err) {
      setNotice({ tone: "error", text: errorText(err) });
    }
  }

  return (
    <section className="app__panel" aria-labelledby="cluster-heading">
      <h2 id="cluster-heading">Cluster pick</h2>

      {cluster ? null : (
        <form onSubmit={onBuild} aria-label="Build cluster">
          <label className="app__field">
            Works (workId, orderId, LP per line)
            <textarea rows={4} value={worksText} onChange={(e) => setWorksText(e.target.value)} />
          </label>
          <button type="submit" className="app__button">
            Build cluster
          </button>
        </form>
      )}

      {cluster ? (
        <>
          <ol aria-label="Cart positions" className="app__log">
            {cluster.positions.map((p) => (
              <li key={p.position}>
                Position {p.position}: LP {p.lp} for order {p.orderId} · {p.status}
              </li>
            ))}
          </ol>

          {next ? (
            <>
              <form onSubmit={onScanPosition} aria-label="Scan cart position">
                <label className="app__field">
                  Scan cart position {next.position}
                  <input autoFocus inputMode="numeric" autoComplete="off" value={scan} onChange={(e) => setScan(e.target.value)} />
                </label>
              </form>
              <button type="button" className="app__button" disabled={!ready} onClick={() => applyChange(completePosition, "picked in full")}>
                Complete position {next.position}
              </button>
              <button
                type="button"
                className="app__button app__button--secondary"
                disabled={!ready}
                onClick={() => applyChange(shortPosition, "picked short")}
              >
                Short position {next.position}
              </button>
            </>
          ) : (
            <p className="app__message">Cluster finished: every position is picked or short.</p>
          )}

          <button type="button" className="app__button app__button--secondary" onClick={onNewCluster}>
            New cluster
          </button>
        </>
      ) : null}

      {notice ? (
        <p role={notice.tone === "error" ? "alert" : "status"} className={notice.tone === "error" ? "app__alert" : "app__message"}>
          {notice.text}
        </p>
      ) : null}
    </section>
  );
}
