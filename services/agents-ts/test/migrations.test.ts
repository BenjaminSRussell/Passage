import { readdirSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { MIGRATIONS_DIR } from "../src/store/pg.js";

describe("migrations", () => {
  // Two branches that each add the next number merge into two files with the same prefix; the apply order between
  // them is then only alphabetical. Every NNNN_ prefix is used once.
  it("uses every migration number once", () => {
    const names = readdirSync(MIGRATIONS_DIR).filter((n) => n.endsWith(".sql"));
    const prefixes = names.map((n) => n.split("_")[0]);
    const duplicates = prefixes.filter((p, i) => prefixes.indexOf(p) !== i);
    expect(duplicates, names.join(", ")).toEqual([]);
  });
});
