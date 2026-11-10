// @vitest-environment node
/**
 * Every problem code the procure-to-pay and order-to-cash services can return has
 * a plain-language message in the catalog (#501), so a person never sees a bare
 * code. The codes are read from the services' Go source: any quoted string shaped
 * like `PRC-PO-009` or `AR-CASE-NOT-FOUND`.
 *
 * Add the code to `catalog.ts` when this fails. List a string here only when it
 * looks like a code but is never returned as a problem.
 */
import { problemCatalog } from "./catalog";

/** Quoted strings shaped like a code that are not problem codes: aging buckets, credit rule ids, test fixtures. */
const NOT_PROBLEM_CODES = new Set(["OVER-180", "SELFTEST-1", "CR-1", "CR-2", "CR-3", "CR-4", "CR-5", "CR-6"]);

const CODE = /"([A-Z]{2,8}-[A-Z0-9][A-Z0-9-]*)"/g;

/** The purchase-to-pay, order-to-cash and master-data services. Glob patterns must be literals. */
const sources = import.meta.glob<string>(
  [
    "../../../../../services/purchasing/**/*.go",
    "../../../../../services/receiving/**/*.go",
    "../../../../../services/payables/**/*.go",
    "../../../../../services/sales/**/*.go",
    "../../../../../services/fulfillment/**/*.go",
    "../../../../../services/billing/**/*.go",
    "../../../../../services/receivables/**/*.go",
    "../../../../../services/inventory/**/*.go",
    "../../../../../services/pricing/**/*.go",
    "../../../../../services/party/**/*.go",
    "../../../../../services/tax/**/*.go",
    "../../../../../services/product/**/*.go",
    "../../../../../services/ledger/**/*.go",
    "!**/*_test.go",
    "!**/testdata/**",
  ],
  { query: "?raw", import: "default", eager: true },
);

describe("problem code catalog", () => {
  it("has a message for every code the purchase-to-pay and order-to-cash services define", () => {
    const entries = Object.entries(sources);
    expect(entries.length).toBeGreaterThan(100);
    const missing = new Map<string, string>();
    for (const [file, source] of entries) {
      for (const match of source.matchAll(CODE)) {
        const code = match[1];
        if (!NOT_PROBLEM_CODES.has(code) && !(code in problemCatalog) && !missing.has(code)) missing.set(code, file.replace("../../../../../", ""));
      }
    }
    const report = [...missing].sort().map(([code, file]) => `${code}  (${file})`).join("\n");
    expect(report, "Add these codes to apps/web/src/shell/errors/catalog.ts").toBe("");
  });

  it("gives every entry a message and an action that end like sentences and do not repeat a code", () => {
    for (const [code, entry] of Object.entries(problemCatalog)) {
      expect(entry.message, code).toMatch(/[.!?]$/);
      expect(entry.action, code).toMatch(/[.!?]$/);
      expect(`${entry.message} ${entry.action}`, code).not.toMatch(/\b[A-Z]{2,8}-\d{3,4}\b/);
    }
  });
});
