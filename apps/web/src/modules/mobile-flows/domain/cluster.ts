// Cluster picking (BR-32.5). One cart holds up to 8 works, each bound to one cart position
// and one order's LP. Pure: every operation returns a new cart and never touches other
// positions, so a short pick on one position does not release the others.

export const CLUSTER_HARD_MAX = 8;

export type PositionStatus = "OPEN" | "DONE" | "SHORT";

export interface ClusterWork {
  workId: string;
  orderId: string;
  /** LP that the order's picks are put into. */
  lp: string;
}

export interface ClusterPosition extends ClusterWork {
  position: number; // 1-based cart position
  status: PositionStatus;
}

export interface Cluster {
  positions: ClusterPosition[];
}

export class ClusterError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "ClusterError";
  }
}

/**
 * Builds a cluster from works. At most min(maxPositions, 8) works (BR-32.5); each order and
 * each LP may appear once so a position is bound to exactly one order's LP.
 */
export function createCluster(works: ClusterWork[], maxPositions = CLUSTER_HARD_MAX): Cluster {
  const limit = Math.min(maxPositions, CLUSTER_HARD_MAX);
  if (works.length === 0) throw new ClusterError("CLUSTER_EMPTY", "A cluster needs at least one work.");
  if (works.length > limit) {
    throw new ClusterError("CLUSTER_TOO_LARGE", `A cluster takes at most ${limit} works; got ${works.length}.`);
  }
  const orders = new Set(works.map((w) => w.orderId));
  const lps = new Set(works.map((w) => w.lp));
  if (orders.size !== works.length || lps.size !== works.length) {
    throw new ClusterError("CLUSTER_DUPLICATE", "Each order and LP can appear once in a cluster.");
  }
  return {
    positions: works.map((w, i) => ({ ...w, position: i + 1, status: "OPEN" as const })),
  };
}

/** MOB-0109 text when the scanned cart position is not the expected one; null when it matches. */
export function checkPosition(cluster: Cluster, expected: number, scanned: number): string | null {
  if (!cluster.positions.some((p) => p.position === expected)) {
    throw new ClusterError("CLUSTER_NO_POSITION", `Cart position ${expected} is not part of this cluster.`);
  }
  if (scanned === expected) return null;
  return `Wrong cart position: scanned ${scanned}, expected ${expected}.`;
}

function update(cluster: Cluster, position: number, status: PositionStatus): Cluster {
  const current = cluster.positions.find((p) => p.position === position);
  if (!current) throw new ClusterError("CLUSTER_NO_POSITION", `Cart position ${position} is not part of this cluster.`);
  if (current.status !== "OPEN") {
    throw new ClusterError("CLUSTER_NOT_OPEN", `Position ${position} is already ${current.status}.`);
  }
  return {
    positions: cluster.positions.map((p) => (p.position === position ? { ...p, status } : { ...p })),
  };
}

/** Marks a position picked in full. Other positions are unchanged. */
export function completePosition(cluster: Cluster, position: number): Cluster {
  return update(cluster, position, "DONE");
}

/** Marks a position short. Other positions stay OPEN (BR-32.5). */
export function shortPosition(cluster: Cluster, position: number): Cluster {
  return update(cluster, position, "SHORT");
}

/** Lowest-numbered OPEN position, or null when nothing is left to pick. */
export function nextOpen(cluster: Cluster): ClusterPosition | null {
  return [...cluster.positions].sort((a, b) => a.position - b.position).find((p) => p.status === "OPEN") ?? null;
}

/** True when no position is OPEN. SHORT positions still count as finished for the cluster. */
export function isFinished(cluster: Cluster): boolean {
  return cluster.positions.every((p) => p.status !== "OPEN");
}
