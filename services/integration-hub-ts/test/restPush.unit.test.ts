import assert from "node:assert/strict";
import { test } from "vitest";

import { restStep } from "../src/dispatch.ts";
import type { HubEvent } from "../src/dispatcher.ts";
import { elementRefs, withLineKeys, type MigrationClient, type RenderInput, type XrefInput } from "../src/migrationClient.ts";
import { actorFor, createServiceTokenSigner } from "../src/serviceToken.ts";
import {
  classifyStatus,
  excerptOf,
  expandUrl,
  bodyArrayOf,
  matchLineKeys,
  parseElementPath,
  parseRestPatch,
  readElements,
  parseRestInput,
  parseRetryAfter,
  readPath,
  RestConfigError,
  RestPusher,
  TemplateError,
  type Exchange,
  type ExchangeRequest,
  type RestTarget,
} from "../src/restPush.ts";

const MAPPING = "6f1c6d62-3c0a-4bb7-8f5e-0d6f2a9a7a11";
const TENANT = "11111111-2222-4333-8444-555555555555";

test("JSONPath-lite reads fields and indexes, and refuses anything else", () => {
  const doc = { data: { id: "A1", orders: [{ no: 7 }, { no: 8 }] }, "x-y": 1 };
  assert.equal(readPath(doc, "$.data.id"), "A1");
  assert.equal(readPath(doc, "data.orders[1].no"), 8);
  assert.equal(readPath(doc, "$.x-y"), 1);
  assert.equal(readPath(doc, "$.data.missing"), undefined);
  assert.equal(readPath(doc, "$.data.orders[5].no"), undefined);
  assert.equal(readPath(doc, "$.data.constructor"), undefined);
  for (const bad of ["$", "", "$..id", "$.a[*]", "$.a[?(@.b)]", "a b", "$.a[-1]"]) {
    assert.throws(() => readPath(doc, bad), RestConfigError, bad);
  }
});

test("URL placeholders are filled from the rendered body and escaped as one segment", () => {
  const body = { CustAccount: "C/1 ?x=1#", n: 42, nested: { k: "v" } };
  assert.equal(expandUrl("https://erp.example/api/customers/{CustAccount}/orders", body), "https://erp.example/api/customers/C%2F1%20%3Fx%3D1%23/orders");
  assert.equal(expandUrl("https://erp.example/o?no={n}&k={$.nested.k}", body), "https://erp.example/o?no=42&k=v");
  assert.throws(() => expandUrl("https://erp.example/{missing}", body), TemplateError);
  assert.throws(() => expandUrl("https://erp.example/{nested}", body), TemplateError);
  assert.throws(() => expandUrl("https://erp.example/{d}", { d: ".." }), TemplateError);
  assert.throws(() => expandUrl("https://erp.example/{d}", { d: "" }), TemplateError);
});

test("rest settings are validated: placeholders never in the host, auth shape, header names, paths", () => {
  const good = { outboundMappingId: MAPPING, auth: { type: "bearer", token: "tok-123456" } };
  assert.equal(parseRestInput(good, "https://erp.example/api/{a}").method, "POST");
  for (const url of ["https://{h}.example/x", "https://erp.example:{p}/x", "{scheme}://erp.example/", "https://erp.example/{a", "https://erp.example/{{a}}", "https://erp.example/x#{a}", "https://erp.example/{a b}"]) {
    assert.throws(() => parseRestInput(good, url), RestConfigError, url);
  }
  assert.throws(() => parseRestInput({ ...good, outboundMappingId: "nope" }, "https://e.example/"), RestConfigError);
  assert.throws(() => parseRestInput({ ...good, method: "DELETE" }, "https://e.example/"), RestConfigError);
  assert.throws(() => parseRestInput({ ...good, responseKeyPath: "$..x" }, "https://e.example/"), RestConfigError);
  assert.throws(() => parseRestInput({ ...good, onClientError: "ignore" }, "https://e.example/"), RestConfigError);
  assert.throws(() => parseRestInput({ ...good, auth: { type: "api-key", headerName: "Idempotency-Key", value: "v" } }, "https://e.example/"), RestConfigError);
  assert.throws(() => parseRestInput({ ...good, auth: { type: "api-key", headerName: "X Key", value: "v" } }, "https://e.example/"), RestConfigError);
  assert.throws(() => parseRestInput({ ...good, auth: { type: "basic", username: "u" } }, "https://e.example/"), RestConfigError);
  assert.throws(() => parseRestInput({ ...good, auth: { type: "bearer", token: "t", extra: 1 } }, "https://e.example/"), RestConfigError);
  assert.throws(() => parseRestInput({ ...good, auth: { type: "none" } }, "https://e.example/"), RestConfigError);
  const oauth = parseRestInput({ ...good, auth: { type: "oauth2-client-credentials", tokenUrl: "https://id.example/token", clientId: "c", clientSecret: "s3cret!!", scope: "orders" } }, "https://e.example/");
  assert.deepEqual(oauth.auth, { type: "oauth2-client-credentials", tokenUrl: "https://id.example/token", clientId: "c", scope: "orders" });
  assert.deepEqual(oauth.credentials, { clientSecret: "s3cret!!" });
});

test("status classification: 2xx delivers, 410 pauses, 429 and 5xx retry, other 4xx follow the policy", () => {
  assert.equal(classifyStatus(201, "dead_letter").kind, "delivered");
  assert.equal(classifyStatus(410, "dead_letter").kind, "pause");
  assert.deepEqual(classifyStatus(429, "dead_letter", 90), { kind: "retry", status: 429, retryAfterSeconds: 90 });
  assert.equal(classifyStatus(503, "dead_letter").kind, "retry");
  assert.equal(classifyStatus(302, "dead_letter").kind, "retry");
  for (const s of [400, 401, 403, 404, 409, 422]) {
    assert.equal(classifyStatus(s, "dead_letter").kind, "dead", String(s));
    assert.equal(classifyStatus(s, "retry").kind, "retry", String(s));
  }
});

test("restStep: dead letters at once, retries on the 8-step schedule, honours Retry-After, dead-letters when spent", () => {
  assert.deepEqual(restStep(0, { kind: "dead" }, 100), { kind: "dead_letter", attempts: 1, code: "HUB-0117" });
  assert.deepEqual(restStep(0, { kind: "retry" }, 100), { kind: "retry", attempts: 1, nextRetryAt: 110 });
  assert.deepEqual(restStep(1, { kind: "retry" }, 100), { kind: "retry", attempts: 2, nextRetryAt: 160 });
  assert.deepEqual(restStep(2, { kind: "retry", retryAfterSeconds: 120 }, 100), { kind: "retry", attempts: 3, nextRetryAt: 220 });
  assert.deepEqual(restStep(2, { kind: "retry", retryAfterSeconds: 10 ** 9 }, 100), { kind: "retry", attempts: 3, nextRetryAt: 100 + 86_400 });
  assert.deepEqual(restStep(2, { kind: "retry", retryAfterSeconds: 0 }, 100), { kind: "retry", attempts: 3, nextRetryAt: 100 + 300 });
  assert.deepEqual(restStep(8, { kind: "retry" }, 100), { kind: "dead_letter", attempts: 9, code: "HUB-0115" });
  assert.deepEqual(restStep(3, { kind: "pause" }, 100), { kind: "paused", attempts: 4 });
  assert.deepEqual(restStep(3, { kind: "delivered" }, 100), { kind: "delivered", attempts: 4 });
});

test("Retry-After in seconds or as an HTTP date", () => {
  assert.equal(parseRetryAfter("120", 0), 120);
  assert.equal(parseRetryAfter(new Date(90_000).toUTCString(), 30_000), 60);
  assert.equal(parseRetryAfter("soon", 0), undefined);
  assert.equal(parseRetryAfter(undefined, 0), undefined);
});

test("excerpts are truncated, cleaned and never carry a secret", () => {
  const out = excerptOf(`token=hunter2-secret \u0000 ${"x".repeat(5000)}`, ["hunter2-secret"]);
  assert.ok(!out.includes("hunter2-secret"));
  assert.ok(out.includes("token=***"));
  assert.ok(out.length <= 2001);
  assert.ok(!out.includes("\u0000"));
});

test("the migration service token is signed by integration-hub and carries only the render and xref duties", () => {
  const seed = Buffer.alloc(32, 7).toString("base64");
  const token = createServiceTokenSigner({ seedBase64: seed, now: () => 1_800_000_000_000, newJti: () => "j" }).mintForMigration({ tenantId: TENANT, legalEntityId: "33333333-4444-4555-8666-777777777777" });
  const claims = JSON.parse(Buffer.from(token.split(".")[0] as string, "base64url").toString("utf8"));
  assert.deepEqual(claims, {
    iss: "integration-hub", aud: "migration", tid: TENANT, le: "33333333-4444-4555-8666-777777777777", act: "service:integration-hub",
    duties: ["migration.outbound.render", "migration.xref.record"], iat: 1_800_000_000, exp: 1_800_000_060, jti: "j",
  });
  assert.throws(() => createServiceTokenSigner({ seedBase64: "AAAA" }), TypeError);
  // The platform verifier refuses the nil legal entity, so the signer never mints a token without one.
  const signer = createServiceTokenSigner({ seedBase64: seed });
  assert.throws(() => signer.mintForMigration({ tenantId: TENANT }), /legal entity/);
  assert.throws(() => signer.mintForMigration({ tenantId: TENANT, legalEntityId: null }), /legal entity/);
});

// ---- auth schemes through RestPusher with a fake exchange --------------------------------------------------------

const event: HubEvent = { eventId: "evt-1", type: "dev.passage.sales.order.confirmed.v1", aggregateId: "o1", sequence: 1, occurredAt: 1_800_000_000, data: { order_id: "o1" }, legalEntityId: "33333333-4444-4555-8666-777777777777" };
const migration: MigrationClient = {
  async render(_i: RenderInput) {
    return { ok: true, rendered: { body: { id: "o1" }, idempotencyKey: "k", refs: [{ entity: "salesOrder", id: "o1" }], lookups: [] } };
  },
  async recordXref() {
    return { ok: true };
  },
};

function target(auth: RestTarget["config"]["auth"], credentials: RestTarget["credentials"]): RestTarget {
  return {
    tenantId: TENANT, subscriptionId: "s1", urlTemplate: "https://erp.example/api/orders/{id}",
    config: { outboundMappingId: MAPPING, method: "PUT", responseKeyPath: "", lineKeyPath: "", onClientError: "dead_letter", auth },
    credentials,
  };
}
const pending = { event, retriesDone: 0, nextRetryAt: 0, replay: false };

test("basic, bearer and api-key auth are applied; the request carries the method, expanded URL and Idempotency-Key", async () => {
  const seen: ExchangeRequest[] = [];
  const exchange: Exchange = async (r) => (seen.push(r), { status: 200, headers: {}, body: "{}" });
  const pusher = new RestPusher({ migration, exchange });
  await pusher.attempt(target({ type: "basic", username: "svc" }, { password: "pw/1" }), pending);
  await pusher.attempt(target({ type: "bearer" }, { token: "tok" }), pending);
  await pusher.attempt(target({ type: "api-key", headerName: "X-Api-Key" }, { value: "key-1" }), pending);
  assert.equal(seen[0]?.headers["authorization"], `Basic ${Buffer.from("svc:pw/1").toString("base64")}`);
  assert.equal(seen[1]?.headers["authorization"], "Bearer tok");
  assert.equal(seen[2]?.headers["x-api-key"], "key-1");
  for (const r of seen) {
    assert.equal(r.method, "PUT");
    assert.equal(r.url, "https://erp.example/api/orders/o1");
    assert.equal(r.headers["idempotency-key"], "evt-1");
    assert.equal(r.body, '{"id":"o1"}');
  }
});

test("OAuth2 client credentials: token fetched once and cached, refreshed after a 401, failure retries", async () => {
  let tokens = 0;
  const calls: string[] = [];
  const exchange: Exchange = async (r) => {
    if (r.url === "https://id.example/token") {
      tokens += 1;
      assert.ok(r.body?.includes("grant_type=client_credentials") && r.body.includes("client_id=cid") && r.body.includes("scope=orders"));
      return { status: 200, headers: {}, body: JSON.stringify({ access_token: `at-${tokens}`, expires_in: 3600 }) };
    }
    calls.push(r.headers["authorization"] as string);
    return r.headers["authorization"] === "Bearer at-1" && calls.length === 3 ? { status: 401, headers: {}, body: "expired" } : { status: 200, headers: {}, body: "{}" };
  };
  const pusher = new RestPusher({ migration, exchange, nowMs: () => 1_000 });
  const t = target({ type: "oauth2-client-credentials", tokenUrl: "https://id.example/token", clientId: "cid", scope: "orders" }, { clientSecret: "shh-secret" });
  assert.equal((await pusher.attempt(t, pending)).kind, "delivered");
  assert.equal((await pusher.attempt(t, pending)).kind, "delivered");
  assert.equal(tokens, 1);
  assert.equal((await pusher.attempt(t, pending)).kind, "delivered");
  assert.equal(tokens, 2);
  assert.deepEqual(calls, ["Bearer at-1", "Bearer at-1", "Bearer at-1", "Bearer at-2"]);

  const broken = new RestPusher({ migration, exchange: async () => ({ status: 401, headers: {}, body: "no" }) });
  const out = await broken.attempt(t, pending);
  assert.equal(out.kind, "retry");
});

test("a render outage retries, a render refusal dead-letters with the problem code, and the credentials stay out of the excerpt", async () => {
  const exchange: Exchange = async () => {
    throw new Error("must not push");
  };
  const t = target({ type: "bearer" }, { token: "tok-secret" });
  const down = new RestPusher({ migration: { ...migration, render: async () => ({ ok: false, status: 503, code: null, detail: "down", retryable: true }) }, exchange });
  assert.equal((await down.attempt(t, pending)).kind, "retry");
  const unresolved = new RestPusher({
    migration: { ...migration, render: async () => ({ ok: false, status: 422, code: "MIG-XREF-UNRESOLVED", detail: "customer C1 has no legacy key tok-secret", retryable: false }) },
    exchange,
  });
  const out = await unresolved.attempt(t, pending);
  assert.equal(out.kind, "dead");
  assert.ok(out.excerpt?.includes("MIG-XREF-UNRESOLVED"));
  assert.ok(!out.excerpt?.includes("tok-secret"));
});

// ---- the HTTP migration client ---------------------------------------------------------------------------------

import { httpMigrationClient, migrationRetryable, RENDER_ROUTE_MISSING } from "../src/migrationClient.ts";

test("the HTTP migration client calls the S5 contract with a service token and maps refusals", async () => {
  const seen: { url: string; headers: Record<string, string>; body: unknown }[] = [];
  const answers: { status: number; json?: unknown; throws?: boolean }[] = [
    { status: 200, json: { body: { a: 1 }, idempotencyKey: "k1", refs: [1] } },
    { status: 422, json: { code: "MIG-XREF-UNRESOLVED", detail: "no key" } },
    { status: 500, json: { detail: "boom" } },
    { status: 0, throws: true },
    { status: 204 },
    { status: 409, json: { detail: "conflict" } },
    { status: 404, json: { code: "MIG-0404", detail: "no such mapping" } },
    { status: 404 },
  ];
  const fakeFetch = (async (url: string, init: RequestInit) => {
    seen.push({ url, headers: init.headers as Record<string, string>, body: JSON.parse(init.body as string) });
    const a = answers.shift() as (typeof answers)[number];
    if (a.throws) throw new Error("connect ECONNREFUSED");
    return new Response(a.json === undefined ? null : JSON.stringify(a.json), { status: a.status });
  }) as unknown as typeof fetch;
  const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 1).toString("base64") });
  const client = httpMigrationClient({ baseUrl: "http://migration.svc:8080/", signer, fetch: fakeFetch });
  const input = { tenantId: TENANT, legalEntityId: "33333333-4444-4555-8666-777777777777", mappingId: MAPPING, event: { id: "e1" } };

  assert.deepEqual(await client.render(input), { ok: true, rendered: { body: { a: 1 }, idempotencyKey: "k1", refs: [1], lookups: [] } });
  assert.equal(seen[0]?.url, `http://migration.svc:8080/v1/integration/outbound-mappings/${MAPPING}:render`);
  assert.deepEqual(seen[0]?.body, { event: { id: "e1" } });
  const claims = JSON.parse(Buffer.from((seen[0]?.headers["x-passage-service-token"] as string).split(".")[0] as string, "base64url").toString());
  assert.equal(claims.iss, "integration-hub");
  assert.equal(claims.tid, TENANT);
  assert.deepEqual(await client.render(input), { ok: false, status: 422, code: "MIG-XREF-UNRESOLVED", detail: "no key", retryable: false });
  assert.deepEqual(await client.render(input), { ok: false, status: 500, code: null, detail: "boom", retryable: true });
  assert.deepEqual(await client.render(input), { ok: false, status: null, code: null, detail: "the migration service did not answer", retryable: true });
  assert.deepEqual(await client.recordXref({ tenantId: TENANT, legalEntityId: "33333333-4444-4555-8666-777777777777", mappingId: MAPPING, refs: [1], legacyKey: "SO-1", eventId: "e1" }), { ok: true });
  assert.equal(seen[4]?.url, "http://migration.svc:8080/v1/integration/xref:record");
  assert.deepEqual(seen[4]?.body, { outboundMappingId: MAPPING, refs: [1], legacyKey: "SO-1", eventId: "e1" });
  assert.deepEqual(await client.recordXref({ tenantId: TENANT, legalEntityId: "33333333-4444-4555-8666-777777777777", mappingId: MAPPING, refs: [1], legacyKey: "SO-1", eventId: "e1" }), { ok: false, status: 409, code: null, detail: "conflict", retryable: false });
  // A mapping the service does not know is its refusal; a 404 without a problem code means the S5 route is not there.
  assert.deepEqual(await client.render(input), { ok: false, status: 404, code: "MIG-0404", detail: "no such mapping", retryable: false });
  assert.deepEqual(await client.render({ ...input, actor: "11111111-1111-4111-8111-111111111111" }), { ok: false, status: 404, code: null, detail: RENDER_ROUTE_MISSING, retryable: false });
  const last = JSON.parse(Buffer.from((seen.at(-1)?.headers["x-passage-service-token"] as string).split(".")[0] as string, "base64url").toString());
  assert.equal(last.act, "11111111-1111-4111-8111-111111111111");
  assert.equal(last.le, "33333333-4444-4555-8666-777777777777");
});

// ---- the actor contract with platform/go/tenant VerifyContext --------------------------------------------------

const claimsOf = (token: string) => JSON.parse(Buffer.from(token.split(".")[0] as string, "base64url").toString("utf8"));

test("background pushes sign as service:integration-hub whatever actor produced the event; a human operator keeps their id", () => {
  const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 3).toString("base64") });
  const le = "33333333-4444-4555-8666-777777777777";
  // The verifier refuses a service: actor that is not registered, so a producing service's actor is never forwarded.
  for (const produced of [undefined, "", "service:sales", "SERVICE:billing", "service:migration-loader", "service:integration-hub"]) {
    assert.equal(claimsOf(signer.mintForMigration({ tenantId: TENANT, legalEntityId: le, ...(produced === undefined ? {} : { actor: produced }) })).act, "service:integration-hub", String(produced));
  }
  assert.equal(claimsOf(signer.mintForMigration({ tenantId: TENANT, legalEntityId: le, actor: "11111111-1111-4111-8111-111111111111" })).act, "11111111-1111-4111-8111-111111111111");
  assert.equal(actorFor(" user-1 "), "user-1");
});

test("RestPusher.attempt passes no actor to the migration client for an event another service produced; a test send passes the operator", async () => {
  const actors: (string | undefined)[] = [];
  const spy: MigrationClient = {
    async render(i) {
      actors.push(i.actor);
      return migration.render(i);
    },
    async recordXref(i) {
      actors.push(i.actor);
      return { ok: true };
    },
  };
  const exchange: Exchange = async () => ({ status: 201, headers: {}, body: '{"id":"SO-1"}' });
  const pusher = new RestPusher({ migration: spy, exchange });
  const t = { ...target({ type: "bearer" }, { token: "tok" }), config: { ...target({ type: "bearer" }, { token: "tok" }).config, responseKeyPath: "$.id" } };
  assert.equal((await pusher.attempt(t, { ...pending, event: { ...event, actor: "service:sales" } })).kind, "delivered");
  assert.deepEqual(actors, [undefined, undefined]);
  actors.length = 0;
  await pusher.preview(t, { ...event, actor: "11111111-1111-4111-8111-111111111111" }, true);
  assert.deepEqual(actors, ["11111111-1111-4111-8111-111111111111"]);
});

// ---- client contract: literal JSON from services/migration/internal/api/outbound_test.go ------------------------

function clientFor(status: number, json: unknown) {
  const fakeFetch = (async () => new Response(status === 204 || json === undefined ? null : JSON.stringify(json), { status, headers: { "content-type": status >= 400 ? "application/problem+json" : "application/json" } })) as unknown as typeof fetch;
  return httpMigrationClient({ baseUrl: "http://migration:8080", signer: createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 1).toString("base64") }), fetch: fakeFetch });
}
const LE = "33333333-4444-4555-8666-777777777777";
const renderIn = { tenantId: TENANT, legalEntityId: LE, mappingId: MAPPING, event: { id: "evt-1" } };
const xrefIn = { tenantId: TENANT, legalEntityId: LE, mappingId: MAPPING, refs: [{ entity: "salesOrder", id: "o1" }], legacyKey: "SO-77", eventId: "evt-1" };

test("render decodes the Go answer: body, idempotencyKey and refs [] or [{entity,id}] pass through", async () => {
  // renderView in outbound.go; TestOutbound_RenderHappyPathAndContract decodes {body, idempotencyKey, refs[{entity,id}]}.
  const ok = await clientFor(200, { body: { CustAccount: "C-1001", Header: { CustomerRef: "SO-9" } }, idempotencyKey: "abc123", refs: [{ entity: "salesOrder", id: "o1" }] }).render(renderIn);
  assert.deepEqual(ok, { ok: true, rendered: { body: { CustAccount: "C-1001", Header: { CustomerRef: "SO-9" } }, idempotencyKey: "abc123", refs: [{ entity: "salesOrder", id: "o1" }], lookups: [] } });
  // TestOutbound_RecordWithoutSubject: `"refs":[]`.
  const none = await clientFor(200, { body: { Journal: 1 }, idempotencyKey: "k", refs: [] }).render(renderIn);
  assert.deepEqual(none.ok && none.rendered.refs, []);
});

test("render 422 MIG-XREF-UNRESOLVED keeps the code and is not retried", async () => {
  // failRender in outbound.go: problem body with code, detail, errors and unresolved[].
  const out = await clientFor(422, { type: "https://passage.dev/problems/MIG-XREF-UNRESOLVED", title: "Unprocessable Entity", status: 422, detail: "customer c1 has no legacy key", code: "MIG-XREF-UNRESOLVED", errors: { customer: "no legacy key" }, unresolved: [{ name: "customer", entity: "customer", id: "c1", reason: "no legacy key" }] }).render(renderIn);
  assert.deepEqual(out, { ok: false, status: 422, code: "MIG-XREF-UNRESOLVED", detail: "customer c1 has no legacy key", retryable: false });
});

// Every row is a status the migration service answers (TestOutbound_RenderRefusals, TestOutbound_RecordXrefValidationConflictAndIsolation).
const CLASSES: { name: string; status: number | null; code: string | null; retryable: boolean }[] = [
  { name: "no answer", status: null, code: null, retryable: true },
  { name: "server error", status: 500, code: null, retryable: true },
  { name: "unavailable", status: 503, code: null, retryable: true },
  { name: "rate limited", status: 429, code: null, retryable: true },
  { name: "request timeout", status: 408, code: null, retryable: true },
  { name: "token refused (key or clock)", status: 401, code: null, retryable: true },
  { name: "other legal entity", status: 403, code: "MIG-OUTBOUND-LEGAL-ENTITY", retryable: false },
  { name: "unknown mapping", status: 404, code: "MIG-OUTBOUND-NOT-FOUND", retryable: false },
  { name: "unknown version", status: 404, code: "MIG-OUTBOUND-VERSION-NOT-FOUND", retryable: false },
  { name: "archived mapping", status: 409, code: "MIG-OUTBOUND-ARCHIVED", retryable: false },
  { name: "event already recorded with another key", status: 409, code: "MIG-XREF-EVENT-CONFLICT", retryable: false },
  { name: "key belongs to another record", status: 409, code: "MIG-XREF-CONFLICT", retryable: false },
  { name: "unresolved reference", status: 422, code: "MIG-XREF-UNRESOLVED", retryable: false },
  { name: "mapping rejected the event", status: 422, code: "MIG-OUTBOUND-REJECTED", retryable: false },
  { name: "ref is not the subject entity", status: 422, code: "MIG-XREF-REF-MISMATCH", retryable: false },
  { name: "bad request", status: 400, code: "COM-0001", retryable: false },
];

for (const c of CLASSES) {
  test(`status classification: ${c.name} (${c.status ?? "none"} ${c.code ?? ""}) is ${c.retryable ? "retried" : "dead-lettered"}`, async () => {
    const answer = c.status === null ? undefined : { detail: "d", ...(c.code === null ? {} : { code: c.code }) };
    const failing = c.status === null
      ? httpMigrationClient({ baseUrl: "http://m", signer: createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 1).toString("base64") }), fetch: (async () => { throw new Error("ECONNREFUSED"); }) as unknown as typeof fetch })
      : clientFor(c.status, answer);
    const r = await failing.render(renderIn);
    assert.ok(!r.ok);
    assert.equal(r.retryable, c.retryable);
    const x = await failing.recordXref(xrefIn);
    assert.ok(!x.ok);
    assert.equal(x.retryable, c.retryable);
    assert.equal(x.code, c.code);
  });
}

test("xref:record sends the Go request shape and accepts 204 (also with refs [])", async () => {
  let sent: unknown;
  const f = (async (_u: string, init: RequestInit) => ((sent = JSON.parse(init.body as string)), new Response(null, { status: 204 }))) as unknown as typeof fetch;
  const client = httpMigrationClient({ baseUrl: "http://m", signer: createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 1).toString("base64") }), fetch: f });
  assert.deepEqual(await client.recordXref({ ...xrefIn, refs: [] }), { ok: true });
  assert.deepEqual(sent, { outboundMappingId: MAPPING, refs: [], legacyKey: "SO-77", eventId: "evt-1" });
});

test("a 409 MIG-XREF-EVENT-CONFLICT after a successful push dead-letters without a second request to the legacy ERP; other xref refusals follow the table", async () => {
  let pushes = 0;
  const exchange: Exchange = async () => (pushes++, { status: 201, headers: {}, body: '{"id":"SO-2"}' });
  const t = { ...target({ type: "bearer" }, { token: "tok" }), config: { ...target({ type: "bearer" }, { token: "tok" }).config, responseKeyPath: "$.id" } };
  const withXref = (status: number, code: string | null): RestPusher =>
    new RestPusher({ migration: { ...migration, recordXref: async () => ({ ok: false, status, code, detail: "conflict", retryable: migrationRetryable(status) }) }, exchange });
  const conflict = await withXref(409, "MIG-XREF-EVENT-CONFLICT").attempt(t, pending);
  assert.equal(conflict.kind, "dead");
  assert.equal(conflict.kind === "dead" && conflict.code, "HUB-0118");
  assert.deepEqual(restStep(0, conflict, 100), { kind: "dead_letter", attempts: 1, code: "HUB-0118" });
  assert.equal(conflict.legacyKey, "SO-2");
  assert.ok(conflict.excerpt?.includes("not resent") && conflict.excerpt.includes("MIG-XREF-EVENT-CONFLICT"));
  assert.equal(pushes, 1);
  const other = await withXref(409, "MIG-XREF-CONFLICT").attempt(t, pending);
  assert.equal(other.kind, "dead");
  assert.equal(other.kind === "dead" && other.code, undefined);
  assert.deepEqual(restStep(0, other, 100), { kind: "dead_letter", attempts: 1, code: "HUB-0117" });
  assert.equal((await withXref(422, "MIG-XREF-REF-MISMATCH").attempt(t, pending)).kind, "dead");
  assert.equal((await withXref(503, null).attempt(t, pending)).kind, "retry");
});

test("an event with the nil legal entity is never rendered: the verifier would refuse the token with a 401, which retries", async () => {
  let renders = 0;
  const pusher = new RestPusher({ migration: { ...migration, render: async (i) => (renders++, migration.render(i)) }, exchange: async () => ({ status: 201, headers: {}, body: "{}" }) });
  const out = await pusher.attempt(target({ type: "bearer" }, { token: "tok" }), { ...pending, event: { ...event, legalEntityId: "00000000-0000-0000-0000-000000000000" } });
  assert.equal(out.kind, "dead");
  assert.ok(out.excerpt?.includes("no legalentityid"));
  assert.equal(renders, 0);
});

// ---- per-element response keys (lineKeyPath) ---------------------------------------------------------------------

const CFG = { outboundMappingId: MAPPING, method: "POST" as const, responseKeyPath: "$.id", lineKeyPath: "lines[*].id", onClientError: "dead_letter" as const, auth: { type: "bearer" as const } };

test("element paths have exactly one [*] and read every element, or nothing when the array is missing", () => {
  assert.deepEqual(parseElementPath("lines[*].id"), { array: "lines", element: ".id" });
  assert.deepEqual(parseElementPath("$[*]"), { array: "$", element: "" });
  for (const bad of ["lines.id", "a[*].b[*].c", "lines[*]x", "$..a[*]"]) assert.throws(() => parseElementPath(bad), RestConfigError, bad);
  const doc = { result: { lines: [{ no: 1 }, { no: 2 }, {}] }, root: [10, 20] };
  assert.deepEqual(readElements(doc, "$.result.lines[*].no"), [1, 2, undefined]);
  assert.deepEqual(readElements(doc.root, "$[*]"), [10, 20]);
  assert.equal(readElements(doc, "$.missing[*].no"), undefined);
});

test("line keys match by position, numbers become text, a count that differs from the refs fails", () => {
  const res = { id: "H", lines: [{ id: 501 }, { id: "L-2" }] };
  assert.deepEqual(matchLineKeys(CFG, {}, res, null), { ok: true, keys: [{ index: 0, legacyKey: "501" }, { index: 1, legacyKey: "L-2" }] });
  assert.deepEqual(matchLineKeys(CFG, {}, res, 2), { ok: true, keys: [{ index: 0, legacyKey: "501" }, { index: 1, legacyKey: "L-2" }] });
  assert.equal(matchLineKeys(CFG, {}, res, 3).ok, false);
  assert.equal(matchLineKeys(CFG, {}, { id: "H" }, null).ok, false);
  assert.equal(matchLineKeys(CFG, {}, { lines: [{ id: "a" }, {}] }, null).ok, false);
  assert.equal(matchLineKeys(CFG, {}, { lines: [{ id: "a" }, { id: "" }] }, null).ok, false);
});

test("line keys match by a correlation field when the ERP answers in another order; duplicates and strangers fail", () => {
  const cfg = { ...CFG, lineCorrelation: { bodyPath: "Lines[*].LineNum", responsePath: "lines[*].lineNo" } };
  const pushed = { Lines: [{ LineNum: 10 }, { LineNum: 20 }, { LineNum: 30 }] };
  const res = { lines: [{ id: "C", lineNo: 30 }, { id: "A", lineNo: "10" }, { id: "B", lineNo: 20 }] };
  assert.deepEqual(matchLineKeys(cfg, pushed, res, 3), { ok: true, keys: [{ index: 0, legacyKey: "A" }, { index: 1, legacyKey: "B" }, { index: 2, legacyKey: "C" }] });
  assert.equal(matchLineKeys(cfg, pushed, { lines: [{ id: "A", lineNo: 10 }, { id: "B", lineNo: 10 }] }, null).ok, false);
  assert.equal(matchLineKeys(cfg, pushed, { lines: [{ id: "A", lineNo: 99 }] }, null).ok, false);
  assert.equal(matchLineKeys(cfg, { Lines: [{ LineNum: 1 }, { LineNum: 1 }] }, { lines: [{ id: "A", lineNo: 1 }] }, null).ok, false);
  assert.equal(matchLineKeys(cfg, pushed, res, 2).ok, false);
});

test("lineKeyPath and lineCorrelation are validated and patched with the other rest settings", () => {
  const good = { outboundMappingId: MAPPING, responseKeyPath: "$.id", lineKeyPath: "lines[*].id", auth: { type: "bearer", token: "tok-123456" } };
  const t = "https://erp.example/api/orders";
  assert.equal(parseRestInput(good, t).lineKeyPath, "lines[*].id");
  assert.equal(parseRestInput({ ...good, lineKeyPath: undefined }, t).lineKeyPath, "");
  assert.throws(() => parseRestInput({ ...good, responseKeyPath: "" }, t), /needs responseKeyPath/);
  assert.throws(() => parseRestInput({ ...good, lineKeyPath: "lines.id" }, t), RestConfigError);
  assert.throws(() => parseRestInput({ ...good, lineKeyPath: undefined, lineCorrelation: { bodyPath: "L[*].n", responsePath: "lines[*].n" } }, t), /needs lineKeyPath/);
  assert.throws(() => parseRestInput({ ...good, lineCorrelation: { bodyPath: "L[*].n", responsePath: "other[*].n" } }, t), /same array/);
  assert.throws(() => parseRestInput({ ...good, lineCorrelation: { bodyPath: "L[*].n", responsePath: "lines[*].n", x: 1 } }, t), RestConfigError);
  const withC = parseRestInput({ ...good, lineCorrelation: { bodyPath: "L[*].n", responsePath: "lines[*].n" } }, t);
  assert.deepEqual(withC.lineCorrelation, { bodyPath: "L[*].n", responsePath: "lines[*].n" });
  const { credentials: _c, ...old } = withC;
  const patched = parseRestPatch(old, t, { lineCorrelation: null });
  assert.equal(patched?.config.lineCorrelation, undefined);
  assert.equal(patched?.config.lineKeyPath, "lines[*].id");
  assert.equal(parseRestPatch({ ...old, lineKeyPath: undefined, lineCorrelation: undefined } as unknown as typeof old, t, { method: "PUT" })?.config.lineKeyPath, "");
});

test("a push with line keys records them with the header in one xref call when the render names element refs, and keeps the render's lookups", async () => {
  const calls: { refs: unknown; legacyKey: string; lines?: unknown }[] = [];
  const lookups = [{ name: "uom", table: "uom_conversion", version: 3 }];
  const refs = [{ entity: "salesOrder", id: "o1" }, { entity: "line", id: "l0", collection: "Lines", index: 0 }, { entity: "line", id: "l1", collection: "Lines", index: 1 }];
  const m: MigrationClient = {
    async render() {
      return { ok: true, rendered: { body: { id: "o1", Lines: [{ n: 1 }, { n: 2 }] }, idempotencyKey: "k", refs, lookups } };
    },
    async recordXref(i) {
      calls.push(i);
      return { ok: true };
    },
  };
  const exchange: Exchange = async () => ({ status: 201, headers: {}, body: JSON.stringify({ id: "SO-1", lines: [{ id: "L-1" }, { id: "L-2" }] }) });
  const pusher = new RestPusher({ migration: m, exchange });
  const t = { ...target({ type: "bearer" }, { token: "tok" }), config: { ...target({ type: "bearer" }, { token: "tok" }).config, responseKeyPath: "$.id", lineKeyPath: "lines[*].id" } };
  const out = await pusher.attempt(t, pending);
  assert.equal(out.kind, "delivered", out.excerpt);
  assert.deepEqual(out.lineKeys, [{ index: 0, legacyKey: "L-1" }, { index: 1, legacyKey: "L-2" }]);
  assert.deepEqual(out.lookups, lookups);
  assert.equal(calls.length, 1);
  assert.equal(calls[0]?.legacyKey, "SO-1");
  assert.deepEqual(calls[0]?.lines, out.lineKeys);

  // A mismatch dead-letters without recording anything.
  calls.length = 0;
  const bad = await new RestPusher({ migration: m, exchange: async () => ({ status: 201, headers: {}, body: JSON.stringify({ id: "SO-1", lines: [{ id: "L-1" }] }) }) }).attempt(t, pending);
  assert.equal(bad.kind, "dead");
  assert.match(bad.excerpt ?? "", /line keys at lines\[\*\]\.id cannot be matched: 1 line keys for 2 pushed elements/);
  assert.equal(calls.length, 0);
});

test("without element refs in the render (a migration service before slice 23) the keys are kept on the delivery and the xref call is the single-subject one", async () => {
  const calls: { lines?: unknown }[] = [];
  const m: MigrationClient = {
    ...migration,
    async recordXref(i) {
      calls.push(i);
      return { ok: true };
    },
  };
  const exchange: Exchange = async () => ({ status: 201, headers: {}, body: JSON.stringify({ id: "SO-1", lines: [{ id: "L-1" }, { id: "L-2" }, { id: "L-3" }] }) });
  const t = { ...target({ type: "bearer" }, { token: "tok" }), config: { ...target({ type: "bearer" }, { token: "tok" }).config, responseKeyPath: "$.id", lineKeyPath: "lines[*].id" } };
  const out = await new RestPusher({ migration: m, exchange }).attempt(t, pending);
  assert.equal(out.kind, "delivered");
  assert.equal(out.lineKeys?.length, 3);
  assert.equal(calls[0]?.lines, undefined);
});

test("the xref call carries element keys inside refs; the lookups of the render decode from the Go answer", async () => {
  let sent: any;
  const fakeFetch = (async (_u: string, init: RequestInit) => {
    const body = JSON.parse(String(init.body));
    if (String(_u).endsWith(":render")) {
      return new Response(JSON.stringify({ body: { a: 1 }, idempotencyKey: "k", refs: [], lookups: [{ name: "uom", table: "u", version: 2 }, { name: 5 }, "x"] }), { status: 200 });
    }
    sent = body;
    return new Response(null, { status: 204 });
  }) as unknown as typeof fetch;
  const c = httpMigrationClient({ baseUrl: "http://migration:8080", signer: createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 1).toString("base64") }), fetch: fakeFetch });
  const r = await c.render({ tenantId: TENANT, legalEntityId: "33333333-4444-4555-8666-777777777777", mappingId: MAPPING, event: { id: "e" } });
  assert.ok(r.ok);
  assert.deepEqual(r.ok && r.rendered.lookups, [{ name: "uom", table: "u", version: 2 }]);
  const refs = [{ entity: "so", id: "a" }, { entity: "line", id: "b", collection: "Lines", index: 1 }, { entity: "line", id: "c", collection: "Lines", index: 0 }];
  await c.recordXref({ tenantId: TENANT, legalEntityId: "33333333-4444-4555-8666-777777777777", mappingId: MAPPING, refs, legacyKey: "H", eventId: "e", lines: [{ index: 0, legacyKey: "L0" }, { index: 1, legacyKey: "L1" }], linesCollection: "Lines" });
  assert.deepEqual(sent, { outboundMappingId: MAPPING, legacyKey: "H", eventId: "e", refs: [refs[0], { ...refs[1], legacyKey: "L1" }, { ...refs[2], legacyKey: "L0" }] });
});

test("line keys go only to the element refs of their own collection", () => {
  const refs = [{ entity: "so", id: "a" }, { entity: "line", id: "l0", collection: "Lines", index: 0 }, { entity: "charge", id: "c0", collection: "Charges", index: 0 }];
  const out = withLineKeys(refs, [{ index: 0, legacyKey: "L0" }], "Lines");
  assert.equal(out.recorded, 1);
  assert.deepEqual(out.refs, [refs[0], { ...refs[1], legacyKey: "L0" }, refs[2]]);
  assert.equal(elementRefs([{ entity: "so", id: "a" }]), null);
  assert.deepEqual(elementRefs(refs, "Charges"), { ok: true, collection: "Charges", count: 1 });
  // Several collections and nothing naming one: the keys cannot be placed.
  assert.match((elementRefs(refs) as { reason: string }).reason, /several collections \(Lines, Charges\)/);
  assert.equal(elementRefs(refs, "Other")?.ok, false);
  // One collection that is not the array the correlation read.
  assert.equal(elementRefs(refs.slice(0, 2), "Charges")?.ok, false);
  assert.deepEqual(elementRefs(refs.slice(0, 2)), { ok: true, collection: "Lines", count: 1 });
});

test("bodyArrayOf names the collection a correlation reads, as the migration service names it", () => {
  const cfg = (bodyPath: string) => ({ ...CFG, lineCorrelation: { bodyPath, responsePath: "lines[*].n" } });
  assert.equal(bodyArrayOf(CFG), undefined);
  assert.equal(bodyArrayOf(cfg("Lines[*].LineNum")), "Lines");
  assert.equal(bodyArrayOf(cfg("$.Header.Lines[*].LineNum")), "Header.Lines");
});

test("a correlated answer that leaves a pushed line without a key fails, as a short answer by position does", () => {
  const cfg = { ...CFG, lineCorrelation: { bodyPath: "Lines[*].LineNum", responsePath: "lines[*].lineNo" } };
  const pushed = { Lines: [{ LineNum: 10 }, { LineNum: 20 }, { LineNum: 30 }] };
  const partial = { lines: [{ id: "B", lineNo: 20 }, { id: "A", lineNo: 10 }] };
  assert.deepEqual(matchLineKeys(cfg, pushed, partial, null), { ok: false, reason: "2 line keys for 3 pushed elements at Lines[*].LineNum" });
  assert.equal(matchLineKeys(cfg, pushed, partial, 3).ok, false);
});

test("with elements of two collections the keys go to the collection the correlation names; without one the push dead-letters", async () => {
  const calls: XrefInput[] = [];
  const refs = [
    { entity: "salesOrder", id: "o1" },
    { entity: "line", id: "l0", collection: "Lines", index: 0 },
    { entity: "line", id: "l1", collection: "Lines", index: 1 },
    { entity: "charge", id: "c0", collection: "Charges", index: 0 },
  ];
  const m: MigrationClient = {
    async render() {
      return { ok: true, rendered: { body: { id: "o1", Lines: [{ n: 1 }, { n: 2 }], Charges: [{ code: "FRT" }] }, idempotencyKey: "k", refs, lookups: [] } };
    },
    async recordXref(i) {
      calls.push(i);
      return { ok: true };
    },
  };
  const exchange: Exchange = async () => ({ status: 201, headers: {}, body: JSON.stringify({ id: "SO-1", lines: [{ id: "L-2", n: 2 }, { id: "L-1", n: 1 }] }) });
  const base = target({ type: "bearer" }, { token: "tok" });
  const correlated = { ...base, config: { ...base.config, responseKeyPath: "$.id", lineKeyPath: "lines[*].id", lineCorrelation: { bodyPath: "Lines[*].n", responsePath: "lines[*].n" } } };
  const out = await new RestPusher({ migration: m, exchange }).attempt(correlated, pending);
  assert.equal(out.kind, "delivered", out.excerpt);
  assert.deepEqual(calls[0]?.lines, [{ index: 0, legacyKey: "L-1" }, { index: 1, legacyKey: "L-2" }]);
  assert.equal(calls[0]?.linesCollection, "Lines");

  calls.length = 0;
  const byPosition = { ...base, config: { ...base.config, responseKeyPath: "$.id", lineKeyPath: "lines[*].id" } };
  const dead = await new RestPusher({ migration: m, exchange }).attempt(byPosition, pending);
  assert.equal(dead.kind, "dead");
  assert.match(dead.excerpt ?? "", /several collections/);
  // Keys an earlier attempt read are cleared, and nothing is recorded.
  assert.deepEqual(dead.lineKeys, []);
  assert.equal(calls.length, 0);
});
