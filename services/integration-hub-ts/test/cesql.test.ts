import { describe, expect, test } from "vitest";

import { CesqlSyntaxError, compileFilter, filterFor, MAX_EXPRESSION_LENGTH, validateFilter, type FilterEvent } from "../src/cesql.ts";

const event: FilterEvent = {
  attributes: {
    id: "ev-1", type: "dev.passage.sales.order.confirmed.v1", subject: "order-1", source: "passage/sales", tenantid: "t",
    legalentityid: "le-1", time: "2026-11-03T10:00:00.000Z", passagesequence: 7,
  },
  data: { currency: "USD", net_total: "1200.00", qty: 12, flag: true, customer: { id: "c-1", tier: "gold" }, lines: [1, 2], none: null, ratio: 1.5 },
};
const ok = (src: string, e: FilterEvent = event) => compileFilter(src).matches(e);

describe("CESQL filter", () => {
  test.each([
    ["type = 'dev.passage.sales.order.confirmed.v1'", true],
    ["type = 'other'", false],
    ["type != 'other' AND data.currency = 'USD'", true],
    ["data.currency <> 'EUR'", true],
    ["data.currency IN ('EUR', 'USD')", true],
    ["data.currency NOT IN ('EUR', 'GBP')", true],
    ["data.customer.tier = 'gold'", true],
    ["data.qty > 10 AND data.qty <= 12", true],
    ["data.qty = '12'", true],
    ["data.qty + 3 = 15 AND data.qty * 2 = 24 AND data.qty - 2 = 10 AND data.qty / 5 = 2 AND data.qty % 5 = 2", true],
    ["-data.qty = -12", true],
    ["data.flag", true],
    ["data.flag = TRUE AND NOT data.flag = FALSE", true],
    ["data.flag XOR TRUE", false],
    ["data.currency = 'EUR' OR data.qty = 12", true],
    ["subject LIKE 'order-%'", true],
    ["subject LIKE 'order-_'", true],
    ["subject NOT LIKE '%x%'", true],
    ["subject LIKE '%r-1'", true],
    ["subject LIKE 'order-'", false],
    ["data.currency LIKE 'U%X%'", false],
    ["EXISTS actor", false],
    ["NOT EXISTS actor AND EXISTS legalentityid AND EXISTS data.customer.id", true],
    ["EXISTS data.lines", false],
    ["EXISTS data.none", false],
    ["LOWER(data.currency) = 'usd' AND UPPER(subject) = 'ORDER-1' AND LENGTH(subject) = 7", true],
    ["CONCAT(data.currency, '-', data.customer.tier) = 'USD-gold'", true],
    ["CONCAT_WS('/', 'a', 'b') = 'a/b' AND TRIM('  x ') = 'x'", true],
    ["LEFT(subject, 5) = 'order' AND RIGHT(subject, 1) = '1' AND SUBSTRING(subject, 7) = '1' AND SUBSTRING(subject, 1, 5) = 'order'", true],
    ["ABS(-4) = 4 AND INT('12') = 12 AND STRING(12) = '12' AND BOOL('true')", true],
    ["passagesequence >= 7", true],
    ["(data.currency = 'EUR' OR data.currency = 'USD') AND NOT (data.qty < 5)", true],
    ["data.currency = \"USD\"", true],
    ["subject = 'it\\'s' OR subject = 'order-1'", true],
    ["type like 'dev.%' and data.qty = 12", true],
  ])("%s -> %s", (src, expected) => {
    expect(ok(src)).toBe(expected);
  });

  test("an evaluation error means no match, not an exception", () => {
    for (const src of [
      "data.missing = 'x'", "data.missing != 'x'", "actor = 'u'", "NOT data.missing = 1", "data.currency + 1 = 2", "data.qty / 0 = 1", "data.qty % 0 = 1",
      "2147483647 + 1 > 0", "data.net_total > 1000", "data.ratio > 1", "data.customer = 'x'", "data.lines = 1", "data.flag > 1", "data.qty = TRUE",
      "BOOL('maybe')", "data.qty < 1 OR data.missing = 1",
    ]) {
      expect(ok(src), src).toBe(false);
    }
  });

  test("a missing attribute does not make NOT true", () => {
    expect(ok("NOT (actor = 'u')")).toBe(false);
    expect(ok("NOT EXISTS actor")).toBe(true);
  });

  test.each([
    "", "   ", "type =", "type = = 'x'", "(type = 'x'", "type = 'x')", "type = 'x", "type = 'x' AND", "NOT", "type IN ()", "type IN ('a',)",
    "type LIKE data.currency", "type NOT 'x'", "1 + 2", "'text'", "LENGTH(type)", "nosuch(type) = 1", "LENGTH() = 1", "LENGTH(a, b) = 1",
    "Type = 'x'", "data = 'x'", "data. = 'x'", "data..a = 'x'", "data.a b = 'x'", "type = 99999999999", "12abc = 1", "type = 'x' ; DROP", "type @ 'x'",
    "EXISTS", "EXISTS 'x'", "EXISTS AND", "data.a$ = 1", "AND = 1",
  ])("rejects %j", (src) => {
    expect(() => compileFilter(src)).toThrow(CesqlSyntaxError);
    expect(validateFilter(src)).toEqual(expect.any(String));
  });

  test("limits: length, depth and size are bounded", () => {
    expect(validateFilter(`type = '${"x".repeat(MAX_EXPRESSION_LENGTH)}'`)).toMatch(/longer than/);
    expect(validateFilter(`${"(".repeat(20)}type = 'x'${")".repeat(20)}`)).toBeNull();
    expect(validateFilter(`${"(".repeat(40)}type = 'x'${")".repeat(40)}`)).toMatch(/deeper/);
    expect(validateFilter(`${"NOT ".repeat(40)}type = 'x'`)).toMatch(/deeper/);
    expect(validateFilter(Array.from({ length: 100 }, () => "a1=1").join(" OR "))).toMatch(/more than/);
  });

  test("nesting is refused on the way down: a filter of only open brackets or nested calls is a 400, not a stack overflow", () => {
    expect(validateFilter(`${"(".repeat(MAX_EXPRESSION_LENGTH - 1)}x`)).toMatch(/deeper/);
    expect(validateFilter(`${"LOWER(".repeat(170)}x`)).toMatch(/deeper/);
    expect(validateFilter(`${"-".repeat(MAX_EXPRESSION_LENGTH - 6)}1 = 1`)).toMatch(/deeper/);
    expect(validateFilter(`data.a IN (${"(".repeat(200)}`)).toMatch(/deeper/);
  });

  test("LIKE has no exponential case and a bounded cost: an adversarial pattern over a huge value does not match", () => {
    const text = "a".repeat(60_000);
    let started = Date.now();
    expect(ok(`data.s LIKE '${"%a".repeat(100)}%b'`, { attributes: {}, data: { s: text } })).toBe(false);
    expect(Date.now() - started).toBeLessThan(2000);
    // O(text x pattern) without the step budget: seconds per event on a 1 MB string, on the consumer's one thread.
    const huge = "a".repeat(1_000_000);
    const costly = compileFilter(`data.s LIKE '%${"a".repeat(480)}b' OR data.s LIKE '%${"a".repeat(479)}b'`);
    started = Date.now();
    expect(costly.matches({ attributes: {}, data: { s: huge } })).toBe(false);
    expect(Date.now() - started).toBeLessThan(500);
    // The budget is per evaluation: an ordinary pattern over a long value still matches afterwards.
    expect(ok("data.s LIKE '%needle'", { attributes: {}, data: { s: `${"x".repeat(200_000)}needle` } })).toBe(true);
    expect(ok("data.s LIKE '100\\%'", { attributes: {}, data: { s: "100%" } })).toBe(true);
    expect(ok("data.s LIKE '100\\%'", { attributes: {}, data: { s: "1000" } })).toBe(false);
    expect(ok("data.s LIKE '%'", { attributes: {}, data: { s: "" } })).toBe(true);
  });

  test("keywords are case-insensitive, attribute names are not, and prototype keys are not data", () => {
    expect(ok("data.qty = 12 and type like '%v1'")).toBe(true);
    expect(ok("EXISTS data.constructor")).toBe(false);
    expect(ok("EXISTS data.__proto__")).toBe(false);
  });

  test("filterFor caches compiled filters and throws on a bad one", () => {
    expect(filterFor("data.qty = 12")).toBe(filterFor("data.qty = 12"));
    expect(() => filterFor("data.qty =")).toThrow(CesqlSyntaxError);
  });
});
