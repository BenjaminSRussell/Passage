import { describe, expect, it } from "vitest";
import {
  createLogger,
  EMAIL_REDACTED,
  isPersonalKey,
  MacKeys,
  pseudonym,
  pseudonymizationEnabled,
  pseudonymizeRecord,
  PREFIX,
  REDACTED,
} from "../src/logging/pseudonymize.js";

const TENANT_A = "11111111-1111-4111-8111-111111111111";
const TENANT_B = "22222222-2222-4222-8222-222222222222";
// Master key bytes 0..31, the key platform/go/logpseudo's TestKnownAnswers uses.
const KEY_B64 = Buffer.from(Array.from({ length: 32 }, (_, i) => i)).toString("base64");
const ENV_KEYS = { FIELDCRYPT_KEYS: `k1=${KEY_B64}`, FIELDCRYPT_CURRENT_KEY_ID: "k1" };
const keys = MacKeys.fromEnv(ENV_KEYS);

describe("pseudonyms", () => {
  it("match the Go implementation byte for byte", () => {
    expect(pseudonym(keys, TENANT_A, "Jane.Doe@Example.com")).toBe("pn_81c1af9dc7db4ac7");
    expect(pseudonym(keys, TENANT_B, "Jane.Doe@Example.com")).toBe("pn_cc6b2b4cc9040118");
  });

  it("are stable per tenant, differ across tenants, and ignore case and padding", () => {
    expect(pseudonym(keys, TENANT_A, "  JANE@example.com ")).toBe(pseudonym(keys, TENANT_A, "jane@example.com"));
    expect(pseudonym(keys, TENANT_A, "jane@example.com")).not.toBe(pseudonym(keys, TENANT_B, "jane@example.com"));
  });

  it("are null without a key or a tenant", () => {
    expect(pseudonym(null, TENANT_A, "x")).toBeNull();
    expect(pseudonym(keys, "", "x")).toBeNull();
  });
});

describe("pseudonymizeRecord", () => {
  it("never lets a raw email or name through", () => {
    const out = JSON.stringify(
      pseudonymizeRecord(
        {
          level: "info",
          message: "sign-in of jane.doe@example.com failed",
          tenantId: TENANT_A,
          user: "Jane Doe",
          email: "jane.doe@example.com",
          displayName: "Jane Doe",
          remoteIp: "203.0.113.9",
          detail: "contact jane.doe@example.com or JANE.DOE@EXAMPLE.COM",
          error: new Error("no mailbox jane.doe@example.com"),
          person: { first: "Jane", last: "Doe" },
          req: { subject: "auth0|jane", path: "/users/jane.doe@example.com", tags: ["jane.doe@example.com"] },
          customer_name: "Acme Jane",
        },
        keys,
      ),
    );
    for (const raw of ["jane.doe", "Jane", "Doe", "JANE", "example.com", "203.0.113.9", "auth0", "Acme"]) {
      expect(out, raw).not.toContain(raw);
    }
    expect(out).toContain(PREFIX);
  });

  it("maps the same input to the same pseudonym per tenant and to different ones across tenants", () => {
    const a1 = pseudonymizeRecord({ tenantId: TENANT_A, email: "jane@example.com" }, keys);
    const a2 = pseudonymizeRecord({ tenantId: TENANT_A, user: "Jane@Example.com" }, keys);
    const b = pseudonymizeRecord({ tenantId: TENANT_B, email: "jane@example.com" }, keys);
    expect(a1.email).toBe(a2.user);
    expect(a1.email).not.toBe(b.email);
    expect(a1.tenantId).toBe(TENANT_A);
  });

  it("passes unrelated fields through untouched", () => {
    const rec = { level: "info", message: "agents listening", service: "agents-ts", tenantId: TENANT_A, provider: "bedrock", agents: 12, ok: true, runId: "r-1", hostname: "pod-1", model: "m", nothing: null };
    expect(pseudonymizeRecord(rec, keys)).toEqual(rec);
  });

  it("replaces personal values with a marker when there is no key or no tenant, never the raw value", () => {
    const noKey = JSON.stringify(pseudonymizeRecord({ tenantId: TENANT_A, email: "a@example.com", user: "Jane", note: "cc b@example.com" }, null));
    const noTenant = JSON.stringify(pseudonymizeRecord({ email: "a@example.com", name: "Jane", note: "cc b@example.com" }, keys));
    for (const out of [noKey, noTenant]) {
      expect(out).not.toContain("example.com");
      expect(out).not.toContain("Jane");
      expect(out).toContain(REDACTED);
      expect(out).toContain(EMAIL_REDACTED);
      expect(out).not.toContain(PREFIX);
    }
  });

  it("leaves an empty personal value empty", () => {
    expect(pseudonymizeRecord({ tenantId: TENANT_A, email: "" }, keys).email).toBe("");
  });
});

describe("which keys name a person", () => {
  it("recognises them in every case style", () => {
    for (const k of ["user", "userId", "user_id", "user-name", "email", "contactEmail", "name", "displayName", "customer_name", "remoteIp", "client.ip", "subject", "actor", "firstName", "approvedBy_user"]) {
      expect(isPersonalKey(k), k).toBe(true);
    }
    for (const k of ["method", "path", "status", "requestId", "realmName", "hostname", "service", "tenantId", "runId", "durationMs", "description", "ipsum"]) {
      expect(isPersonalKey(k), k).toBe(false);
    }
  });
});

describe("createLogger", () => {
  const capture = (env: Record<string, string | undefined>) => {
    const lines: string[] = [];
    return { log: createLogger(env, "agents-ts", (l) => lines.push(l)), lines };
  };

  it("pseudonymizes under the eu-data-boundary overlay and writes one JSON line per call", () => {
    const { log, lines } = capture({ PASSAGE_CLOUD_PROFILE: "commercial+eu-data-boundary", ...ENV_KEYS });
    log("info", "run for jane@example.com", { tenantId: TENANT_A, email: "jane@example.com", user: "Jane Doe", runId: "r-1" });
    expect(lines).toHaveLength(1);
    expect(lines[0]).not.toMatch(/jane|Jane|example\.com/);
    const parsed = JSON.parse(lines[0]!) as Record<string, string>;
    expect(parsed).toMatchObject({ level: "info", service: "agents-ts", tenantId: TENANT_A, runId: "r-1" });
    expect(parsed.email).toBe(pseudonym(keys, TENANT_A, "jane@example.com"));
  });

  it("is on with LOG_PSEUDONYMIZE=true, and off otherwise", () => {
    expect(pseudonymizationEnabled({ LOG_PSEUDONYMIZE: "true" })).toBe(true);
    expect(pseudonymizationEnabled({ LOG_PSEUDONYMIZE: "false" })).toBe(false);
    expect(pseudonymizationEnabled({})).toBe(false);
    expect(pseudonymizationEnabled({ PASSAGE_CLOUD_PROFILE: "gcc" })).toBe(false);
    expect(pseudonymizationEnabled({ PASSAGE_CLOUD_PROFILE: "commercial+eu-data-boundary", LOG_PSEUDONYMIZE: "false" })).toBe(true);
    const { log, lines } = capture({});
    log("info", "x", { email: "jane@example.com" });
    expect(lines[0]).toContain("jane@example.com");
  });

  it("with the flag on and no key redacts instead of passing values through", () => {
    const { log, lines } = capture({ LOG_PSEUDONYMIZE: "true" });
    log("info", "x", { tenantId: TENANT_A, email: "jane@example.com" });
    expect(lines[0]).not.toContain("example.com");
    expect(lines[0]).toContain(REDACTED);
  });

  it("refuses a malformed key and an unknown profile instead of logging raw values", () => {
    expect(() => createLogger({ LOG_PSEUDONYMIZE: "true", FIELDCRYPT_KEYS: "k1=bad", FIELDCRYPT_CURRENT_KEY_ID: "k1" }, "s")).toThrow();
    expect(() => createLogger({ PASSAGE_CLOUD_PROFILE: "nope" }, "s")).toThrow();
  });
});
