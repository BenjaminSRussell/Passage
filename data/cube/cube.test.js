// Row-level security tests for queryRewrite (FR-46.7, AC3).
// Run: node --test data/cube/
const { test } = require("node:test");
const assert = require("node:assert/strict");
const { queryRewrite } = require("./cube.js");

const USMF = "11111111-0000-4000-8000-000000000001";
const DEMF = "22222222-0000-4000-8000-000000000002";

const scopeFilters = (q) => q.filters.filter((f) => f.member && f.member.endsWith(".legal_entity_id"));

test("a scoped user's query carries an equals filter on their legal entities", () => {
  const out = queryRewrite(
    { measures: ["fin_journal_line.revenue"], dimensions: ["fin_journal_line.account_code"] },
    { securityContext: { legalEntityIds: [DEMF, USMF, USMF] } },
  );
  assert.deepEqual(scopeFilters(out), [
    { member: "fin_journal_line.legal_entity_id", operator: "equals", values: [USMF, DEMF].sort() },
  ]);
});

test("two users scoped to different entities get different filters (AC-46.7)", () => {
  const q = { measures: ["fin_journal_line.revenue"] };
  const a = queryRewrite(q, { securityContext: { legalEntityIds: [USMF] } });
  const b = queryRewrite(q, { securityContext: { legalEntityIds: [DEMF] } });
  assert.deepEqual(scopeFilters(a)[0].values, [USMF]);
  assert.deepEqual(scopeFilters(b)[0].values, [DEMF]);
});

test("every cube in the query is filtered, not only the first one", () => {
  const out = queryRewrite(
    {
      measures: ["fin_journal_line.revenue"],
      dimensions: ["sales_line.customer_group"],
      filters: [{ or: [{ member: "prod_resource_shift.resource_id", operator: "equals", values: ["R1"] }] }],
    },
    { securityContext: { legalEntityIds: [USMF] } },
  );
  assert.deepEqual(scopeFilters(out).map((f) => f.member).sort(), [
    "fin_journal_line.legal_entity_id",
    "prod_resource_shift.legal_entity_id",
    "sales_line.legal_entity_id",
  ]);
});

test("existing filters are kept and the caller cannot widen the scope with their own filter", () => {
  const own = { member: "fin_journal_line.legal_entity_id", operator: "equals", values: [DEMF] };
  const out = queryRewrite({ measures: ["fin_journal_line.revenue"], filters: [own] }, { securityContext: { legalEntityIds: [USMF] } });
  assert.equal(out.filters.length, 2);
  assert.deepEqual(out.filters[0], own);
  assert.deepEqual(out.filters[1].values, [USMF]);
});

test("a cube with no security mapping is rejected with ANA-0103, even as a second cube", () => {
  assert.throws(
    () => queryRewrite({ measures: ["fin_journal_line.revenue"], dimensions: ["party.name"] }, { securityContext: { legalEntityIds: [USMF] } }),
    /ANA-0103 model party/,
  );
  assert.throws(() => queryRewrite({}, { securityContext: { legalEntityIds: [USMF] } }), /ANA-0103/);
});

test("no scope is NO_SCOPE, never an unfiltered query", () => {
  for (const securityContext of [undefined, {}, { legalEntityIds: [] }, { legalEntityIds: [""] }, { unrestricted: "yes" }]) {
    assert.throws(() => queryRewrite({ measures: ["fin_journal_line.revenue"] }, { securityContext }), /ANA-0102/);
  }
});

test("only an explicit unrestricted grant skips the filter", () => {
  const q = { measures: ["fin_journal_line.revenue"] };
  assert.equal(queryRewrite(q, { securityContext: { unrestricted: true } }), q);
});
