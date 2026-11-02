import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "vitest";

import { classifyAddress, EgressPolicy, UnsafeWebhookUrlError, validateWebhookUrl } from "../src/ssrf.ts";

interface Vectors {
  denied: string[];
  private: string[];
  public: string[];
  policy: { allow: string; host: string; resolved: string; port: string; reachable: boolean }[];
  badPolicies: string[];
}
const v = JSON.parse(readFileSync(new URL("../testdata/egress-vectors.json", import.meta.url), "utf8")) as Vectors;

test("shared vectors: every address classifies as the Go egress guard does", () => {
  for (const a of v.denied) assert.equal(classifyAddress(a), "denied", a);
  for (const a of v.private) assert.equal(classifyAddress(a), "private", a);
  for (const a of v.public) assert.equal(classifyAddress(a), "public", a);
});

test("shared vectors: the operator allowlist opens private addresses only", async () => {
  for (const c of v.policy) {
    const url = `https://${c.host}:${c.port}/x`;
    const policy = new EgressPolicy(c.allow);
    const run = validateWebhookUrl(url, { resolve: async () => [c.resolved], egressAllow: policy });
    if (c.reachable) {
      assert.equal((await run).address, c.resolved, JSON.stringify(c));
    } else {
      await assert.rejects(run, UnsafeWebhookUrlError, JSON.stringify(c));
    }
  }
});

test("shared vectors: malformed allowlist entries are refused at start", () => {
  for (const raw of v.badPolicies) assert.throws(() => new EgressPolicy(raw), /INTEGRATION_HUB_EGRESS_ALLOW/, raw);
  assert.doesNotThrow(() => new EgressPolicy(" "));
});

test("an IP literal on the allowlist is reachable, a loopback literal never is", async () => {
  const policy = new EgressPolicy("10.9.0.0/16,127.0.0.0/8");
  const ok = await validateWebhookUrl("http://10.9.0.7:8080/api", { allowHttp: true, egressAllow: policy });
  assert.equal(ok.address, "10.9.0.7");
  await assert.rejects(validateWebhookUrl("http://127.0.0.1:8080/", { allowHttp: true, egressAllow: policy }), UnsafeWebhookUrlError);
  await assert.rejects(validateWebhookUrl("http://10.8.0.7/", { allowHttp: true, egressAllow: policy }), UnsafeWebhookUrlError);
});

test("one private answer among public ones still needs the allowlist", async () => {
  await assert.rejects(
    validateWebhookUrl("https://erp.example/", { resolve: async () => ["93.184.216.34", "10.0.0.5"], egressAllow: new EgressPolicy("") }),
    UnsafeWebhookUrlError,
  );
});
