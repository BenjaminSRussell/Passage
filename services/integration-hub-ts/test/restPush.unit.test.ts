import assert from "node:assert/strict";
import { test } from "vitest";

import { restStep } from "../src/dispatch.ts";
import type { HubEvent } from "../src/dispatcher.ts";
import type { MigrationClient, RenderInput } from "../src/migrationClient.ts";
import { createServiceTokenSigner } from "../src/serviceToken.ts";
import {
  classifyStatus,
  excerptOf,
  expandUrl,
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
    return { ok: true, rendered: { body: { id: "o1" }, idempotencyKey: "k", refs: [{ entity: "salesOrder", id: "o1" }] } };
  },
  async recordXref() {
    return { ok: true };
  },
};

function target(auth: RestTarget["config"]["auth"], credentials: RestTarget["credentials"]): RestTarget {
  return {
    tenantId: TENANT, subscriptionId: "s1", urlTemplate: "https://erp.example/api/orders/{id}",
    config: { outboundMappingId: MAPPING, method: "PUT", responseKeyPath: "", onClientError: "dead_letter", auth },
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

import { httpMigrationClient, RENDER_ROUTE_MISSING } from "../src/migrationClient.ts";

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

  assert.deepEqual(await client.render(input), { ok: true, rendered: { body: { a: 1 }, idempotencyKey: "k1", refs: [1] } });
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
  assert.deepEqual(await client.recordXref({ tenantId: TENANT, legalEntityId: "33333333-4444-4555-8666-777777777777", mappingId: MAPPING, refs: [1], legacyKey: "SO-1", eventId: "e1" }), { ok: false, status: 409, detail: "conflict" });
  // A mapping the service does not know is its refusal; a 404 without a problem code means the S5 route is not there.
  assert.deepEqual(await client.render(input), { ok: false, status: 404, code: "MIG-0404", detail: "no such mapping", retryable: false });
  assert.deepEqual(await client.render({ ...input, actor: "11111111-1111-4111-8111-111111111111" }), { ok: false, status: 404, code: null, detail: RENDER_ROUTE_MISSING, retryable: false });
  const last = JSON.parse(Buffer.from((seen.at(-1)?.headers["x-passage-service-token"] as string).split(".")[0] as string, "base64url").toString());
  assert.equal(last.act, "11111111-1111-4111-8111-111111111111");
  assert.equal(last.le, "33333333-4444-4555-8666-777777777777");
});
