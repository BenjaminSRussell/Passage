import { describe, expect, it } from "vitest";
import {
  type ClusterWork,
  checkPosition,
  completePosition,
  createCluster,
  isFinished,
  nextOpen,
  shortPosition,
} from "./cluster";

function works(n: number): ClusterWork[] {
  return Array.from({ length: n }, (_, i) => ({ workId: `w${i + 1}`, orderId: `o${i + 1}`, lp: `LP${i + 1}` }));
}

describe("createCluster (BR-32.5)", () => {
  it.each([
    [1, 8, true],
    [8, 8, true],
    [9, 8, false],
    [8, 4, false],
    [4, 4, true],
    [0, 8, false],
  ])("%i works with a limit of %i positions accepted=%s", (n, max, ok) => {
    if (ok) {
      expect(createCluster(works(n), max).positions.map((p) => p.position)).toEqual(
        Array.from({ length: n }, (_, i) => i + 1),
      );
    } else {
      expect(() => createCluster(works(n), max)).toThrow();
    }
  });

  it("never exceeds the hard maximum of 8 even if the menu allows more", () => {
    expect(() => createCluster(works(9), 24)).toThrow(/at most 8/);
  });

  it("rejects an order or LP that appears twice", () => {
    const dup = [
      { workId: "a", orderId: "o1", lp: "LP1" },
      { workId: "b", orderId: "o1", lp: "LP2" },
    ];
    expect(() => createCluster(dup)).toThrow(/once/);
  });
});

describe("cart position checks (BR-32.5, MOB-0109)", () => {
  const cluster = createCluster(works(3));

  it("accepts the expected position and rejects any other with the catalog text", () => {
    expect(checkPosition(cluster, 2, 2)).toBeNull();
    expect(checkPosition(cluster, 2, 3)).toBe("Wrong cart position: scanned 3, expected 2.");
  });

  it("refuses a position that is not part of the cluster", () => {
    expect(() => checkPosition(cluster, 5, 5)).toThrow(/not part of this cluster/);
  });
});

describe("short pick isolation (BR-32.5)", () => {
  it("a short pick on one position leaves the others OPEN and pickable", () => {
    let c = createCluster(works(4));
    c = shortPosition(c, 2);
    expect(c.positions.map((p) => p.status)).toEqual(["OPEN", "SHORT", "OPEN", "OPEN"]);
    expect(nextOpen(c)?.position).toBe(1);
    c = completePosition(c, 1);
    expect(nextOpen(c)?.position).toBe(3);
  });

  it("a finished position cannot be picked again", () => {
    const c = completePosition(createCluster(works(2)), 1);
    expect(() => completePosition(c, 1)).toThrow(/already DONE/);
    expect(() => shortPosition(c, 1)).toThrow(/already DONE/);
  });

  it("does not mutate the cart it was given", () => {
    const before = createCluster(works(2));
    const snapshot = JSON.stringify(before);
    completePosition(before, 1);
    expect(JSON.stringify(before)).toBe(snapshot);
  });
});

describe("cluster completion (property, seeded)", () => {
  function rng(seed: number) {
    let s = seed >>> 0;
    return () => {
      s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
      return s / 0x100000000;
    };
  }

  for (const seed of [1, 7, 42, 2026]) {
    it(`seed ${seed}: every operation changes exactly one position and the cluster finishes`, () => {
      const rand = rng(seed);
      let c = createCluster(works(1 + Math.floor(rand() * 8)));
      let guard = 0;
      while (!isFinished(c)) {
        if (++guard > 100) throw new Error("cluster did not finish");
        const target = nextOpen(c);
        if (!target) break;
        const before = c.positions;
        c = rand() < 0.3 ? shortPosition(c, target.position) : completePosition(c, target.position);
        const changed = c.positions.filter((p, i) => p.status !== before[i].status);
        expect(changed.map((p) => p.position)).toEqual([target.position]);
      }
      expect(isFinished(c)).toBe(true);
      expect(nextOpen(c)).toBeNull();
    });
  }
});
