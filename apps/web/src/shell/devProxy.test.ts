import { describe, expect, it } from "vitest";
import { devIdentityHeaders, devProxy, stripApi } from "./devProxy";

describe("devProxy", () => {
  it("strips only the leading /api prefix", () => {
    expect(stripApi("/api/v1/ledger/ledgers")).toBe("/v1/ledger/ledgers");
    expect(stripApi("/v1/api/x")).toBe("/v1/api/x");
  });

  it("defaults every service to localhost:8080 with identity headers", () => {
    const proxy = devProxy({});
    expect(Object.keys(proxy)).toEqual(["/api"]);
    expect(proxy["/api"].target).toBe("http://localhost:8080");
    expect(proxy["/api"].headers).toMatchObject({ "X-Passage-Tenant": expect.any(String), "X-Passage-User": "dev" });
  });

  it("routes per-service overrides before the default, mapping NAME_PART to name-part", () => {
    const proxy = devProxy({ PASSAGE_SVC_LEDGER_URL: "http://localhost:8081", PASSAGE_SVC_AP_INVOICES_URL: "http://localhost:8082" });
    expect(Object.keys(proxy)).toEqual(["^/api/v1/ledger(/|$)", "^/api/v1/ap-invoices(/|$)", "/api"]);
    expect(proxy["^/api/v1/ledger(/|$)"].target).toBe("http://localhost:8081");
  });

  it("lets env override the identity headers", () => {
    expect(devIdentityHeaders({ PASSAGE_DEV_TENANT: "t", PASSAGE_DEV_DUTIES: "a.b,c.d" })).toMatchObject({
      "X-Passage-Tenant": "t",
      "X-Passage-Duties": "a.b,c.d",
    });
  });
});
