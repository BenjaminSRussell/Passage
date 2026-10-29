import { describe, expect, it } from "vitest";
import { fuseRankings, gateCitations, type RetrievedChunk } from "../src/domain/knowledge.js";

const chunk = (id: string, score = 1): RetrievedChunk => ({
  chunkId: id,
  sourceType: "ARTICLE",
  sourceRef: `kb-${id}`,
  section: "Steps",
  score,
});

describe("fuseRankings (FR-47.14 hybrid search)", () => {
  it("ranks a chunk found by both retrievers above one found by only one", () => {
    const fused = fuseRankings(
      [chunk("a", 9), chunk("b", 8)],
      [{ ...chunk("b"), similarity: 0.9 }, { ...chunk("c"), similarity: 0.95 }],
    );
    expect(fused[0]?.chunkId).toBe("b");
    expect(fused.map((c) => c.chunkId)).toEqual(["b", "a", "c"]);
  });

  it("normalises the best possible fusion to 1 and keeps similarity when present", () => {
    const fused = fuseRankings([chunk("a", 2)], [{ ...chunk("a"), similarity: 0.8 }]);
    expect(fused[0]?.fusedScore).toBeCloseTo(1, 12);
    expect(fused[0]?.similarity).toBe(0.8);
  });

  it("is deterministic on ties and respects the limit", () => {
    const fused = fuseRankings([chunk("z", 1), chunk("a", 1)], [], 1);
    expect(fused).toHaveLength(1);
    expect(fused[0]?.chunkId).toBe("a");
  });

  it("returns nothing for empty retrievers", () => {
    expect(fuseRankings([], [])).toEqual([]);
  });

  it("property: fused scores are in (0,1] and sorted descending", () => {
    for (let n = 0; n < 40; n++) {
      const kw = Array.from({ length: n % 7 }, (_, i) => chunk(`k${i}`, (i * 37) % 11));
      const sem = Array.from({ length: n % 5 }, (_, i) => ({ ...chunk(`s${i}`), similarity: ((i * 13) % 10) / 10 }));
      const fused = fuseRankings(kw, sem, 50);
      for (let i = 0; i < fused.length; i++) {
        const score = fused[i]?.fusedScore ?? -1;
        expect(score).toBeGreaterThan(0);
        expect(score).toBeLessThanOrEqual(1);
        if (i > 0) expect(score).toBeLessThanOrEqual(fused[i - 1]?.fusedScore ?? 0);
      }
    }
  });
});

describe("gateCitations (FR-47.14 citation threshold 0.75)", () => {
  const base = { sourceType: "ARTICLE" as const, sourceRef: "kb-1", section: "Steps", chunkId: "c1", fusedScore: 1 };

  it.each([
    { name: "similarity above threshold", citations: [{ ...base, similarity: 0.82 }], answerable: true },
    { name: "similarity exactly at threshold", citations: [{ ...base, similarity: 0.75 }], answerable: true },
    { name: "similarity below threshold", citations: [{ ...base, similarity: 0.74 }], answerable: false },
    { name: "keyword-only hit", citations: [{ ...base }], answerable: false },
    { name: "no hits", citations: [], answerable: false },
  ])("$name", ({ citations, answerable }) => {
    const gate = gateCitations(citations);
    expect(gate.answerable).toBe(answerable);
    if (!gate.answerable) {
      expect(gate.code).toBe("AGT-0212");
    }
  });

  it("keeps only grounded citations when answerable", () => {
    const gate = gateCitations([
      { ...base, chunkId: "hi", similarity: 0.9 },
      { ...base, chunkId: "lo", similarity: 0.3 },
    ]);
    expect(gate.answerable && gate.citations.map((c) => c.chunkId)).toEqual(["hi"]);
  });
});
