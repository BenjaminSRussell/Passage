// Cube configuration for the Passage semantic layer (FR-46.7).
//
// Row-level security mirrors the OPA data-scope decision. The analytics API
// passes the caller's legal-entity ids in the Cube security context (from the
// same Keycloak token as the OLTP APIs). queryRewrite adds an equals filter on
// the legal-entity column of EVERY cube the query touches (measures,
// dimensions, time dimensions, segments and filter members, including nested
// and/or filters), so joins, aggregations and totals never see other entities.
// It also adds a tenant filter on every cube, for unrestricted callers too.
// A query with no scope, no tenant or any unmapped cube is rejected, never widened.

// Models that carry a legal-entity column. Mirrors modelScopeColumns in
// services/analytics/internal/domain/scope.go; the Go contract test
// (cube_contract_test.go) fails when the two drift.
const SCOPE_COLUMNS = {
  fin_journal_line: "legal_entity_id",
  sales_line: "legal_entity_id",
  prod_resource_shift: "legal_entity_id",
};

// Every mart is keyed (tenant_id, id) and holds all tenants' rows; every
// mapped model carries this column.
const TENANT_COLUMN = "tenant_id";

function filterMembers(filters) {
  const out = [];
  for (const f of filters || []) {
    if (f.or) out.push(...filterMembers(f.or));
    else if (f.and) out.push(...filterMembers(f.and));
    else out.push(f.member || f.dimension);
  }
  return out;
}

/** Every cube a query reads, in first-seen order. */
function cubesOf(query) {
  const members = [
    ...(query.measures || []),
    ...(query.dimensions || []),
    ...(query.segments || []),
    ...(query.timeDimensions || []).map((t) => t.dimension),
    ...filterMembers(query.filters),
  ].filter((m) => typeof m === "string" && m !== "");
  return [...new Set(members.map((m) => m.split(".")[0]))];
}

function queryRewrite(query, { securityContext }) {
  const cubes = cubesOf(query);
  if (cubes.length === 0) {
    throw new Error("ANA-0103 query names no model and cannot be scoped (SCOPE_UNMAPPED)");
  }
  const unmapped = cubes.filter((c) => !Object.prototype.hasOwnProperty.call(SCOPE_COLUMNS, c));
  if (unmapped.length > 0) {
    throw new Error(`ANA-0103 model ${unmapped.join(", ")} has no security mapping (SCOPE_UNMAPPED)`);
  }
  const ctx = securityContext || {};
  // The marts hold every tenant's rows, so the tenant filter is added on every
  // cube for every caller, unrestricted ones included.
  if (typeof ctx.tenantId !== "string" || ctx.tenantId === "") {
    throw new Error("ANA-0102 security context names no tenant (NO_SCOPE)");
  }
  const tenantFilters = cubes.map((c) => ({ member: `${c}.${TENANT_COLUMN}`, operator: "equals", values: [ctx.tenantId] }));
  if (ctx.unrestricted === true) {
    return { ...query, filters: [...(query.filters || []), ...tenantFilters] };
  }
  const ids = [...new Set((ctx.legalEntityIds || []).filter((id) => typeof id === "string" && id !== ""))].sort();
  if (ids.length === 0) {
    // ANA-0102: NO_SCOPE. The planner returns an empty result before it gets here.
    throw new Error("ANA-0102 no data scope for this model (NO_SCOPE)");
  }
  return {
    ...query,
    filters: [
      ...(query.filters || []),
      ...tenantFilters,
      ...cubes.map((c) => ({ member: `${c}.${SCOPE_COLUMNS[c]}`, operator: "equals", values: ids })),
    ],
  };
}

module.exports = {
  // The gateway already verified the JWT; the security context carries only
  // the scope claims the planner needs. One app id per tenant keeps the Cube
  // caches and pre-aggregations of tenants apart.
  contextToAppId: ({ securityContext }) =>
    `CUBEJS_APP_${(securityContext && securityContext.tenantId) || "default"}`,
  queryRewrite,
};
