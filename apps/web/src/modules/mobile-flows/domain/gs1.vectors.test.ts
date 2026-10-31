import { describe, expect, it } from "vitest";
import { parseGS1 } from "./gs1";
import suite from "./testdata/gs1-ai-vectors.json";

// AC-32.1: the GS1 AI test suite vectors (GS1 Barcode Syntax Engine, within the
// BR-32.1 AI table) and the 60 Passage edge vectors, each parsed with and without
// a symbology identifier. Expected maps and verdicts come from the fixture, which
// gen_gs1_ai_vectors.py builds with its own reference parser and checks against
// GS1's verdicts; nothing here is derived from gs1.ts.

interface Vector {
  id: string;
  origin: string;
  data: string;
  ok: boolean;
  fields: Record<string, string>;
  order: string[];
  firstError: string | null;
}

const vectors = suite.vectors as unknown as Vector[];
const canonical = (m: Record<string, string>) => JSON.stringify(Object.entries(m).sort(([a], [b]) => (a < b ? -1 : 1)));
const now = new Date(`${suite.referenceDate}T00:00:00Z`);

describe("GS1 AI vectors (AC-32.1)", () => {
  it("holds the GS1 suite vectors and 60 Passage vectors, with valid and invalid cases of each", () => {
    expect(suite.counts.passage).toBe(60);
    expect(suite.counts.gs1 + suite.counts.passage).toBe(vectors.length);
    for (const origin of ["gs1", "passage"]) {
      const group = vectors.filter((v) => (origin === "passage" ? v.origin === "passage" : v.origin !== "passage"));
      expect(group.some((v) => v.ok)).toBe(true);
      expect(group.some((v) => v.firstError === "MOB-0102")).toBe(true);
      expect(group.some((v) => v.firstError === "MOB-0103")).toBe(true);
      expect(group.some((v) => v.firstError === "MOB-0104")).toBe(true);
    }
  });

  for (const prefix of suite.symbologyPrefixes) {
    it(`matches every vector ${prefix ? `with symbology identifier ${prefix}` : "without a symbology identifier"}`, () => {
      const mismatches: string[] = [];
      for (const v of vectors) {
        const got = parseGS1(prefix + v.data, { now });
        const first = got.errors[0]?.code ?? null;
        const same = v.ok
          ? got.ok && canonical(got.fields) === canonical(v.fields) && JSON.stringify(got.order) === JSON.stringify(v.order)
          : !got.ok && first === v.firstError;
        if (!same) {
          mismatches.push(`${v.id} (${v.origin}): want ok=${v.ok} ${JSON.stringify(v.ok ? v.fields : v.firstError)}, got ok=${got.ok} ${JSON.stringify(got.ok ? got.fields : first)}`);
        }
      }
      expect(mismatches).toEqual([]);
    });
  }
});
