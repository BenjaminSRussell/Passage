import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { loadModules } from "../registry";
import migrationFactory from "./module";
import { completeStep, describeProblem, listRuns, MigrationApiError, rollbackPlan, startPackRun, type PackRunRequest } from "./api/client";

describe("migration-factory module", () => {
  it("uses its folder name as id and keeps every route and nav entry under /migration-factory", () => {
    expect(migrationFactory.id).toBe("migration-factory");
    expect(migrationFactory.routes.every((r) => r.element != null && r.path?.startsWith("/migration-factory"))).toBe(true);
    expect(migrationFactory.nav.every((n) => n.to.startsWith("/migration-factory"))).toBe(true);
  });

  it("has list, create and detail pages for pack runs and cutover plans", () => {
    expect(migrationFactory.routes.map((r) => r.path)).toEqual([
      "/migration-factory",
      "/migration-factory/runs/new",
      "/migration-factory/runs/:id",
      "/migration-factory/cutover",
      "/migration-factory/cutover/new",
      "/migration-factory/cutover/:id",
    ]);
  });

  it("guards the navigation with the duties the service enforces", () => {
    expect(migrationFactory.nav).toEqual([
      { label: "Pack runs", to: "/migration-factory", duty: "MigrationView" },
      { label: "Cutover runbooks", to: "/migration-factory/cutover", duty: "CutoverManager" },
    ]);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("migration-factory");
  });
});

describe("migration-factory client", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    fetchMock = vi.fn(async () => new Response(JSON.stringify({ items: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  const body: PackRunRequest = {
    sourceSystem: "AX2012",
    sourceCompany: "USMF",
    cutoffTs: "2026-12-31T00:00:00Z",
    freezeTs: "2026-12-31T18:00:00Z",
    parties: ["V001"],
    records: [{ legacyId: "OI-1", party: "V001", account: "2100", currency: "USD", amount: "10.25" }],
  };

  it("posts pack runs to the versioned path with the caller's Idempotency-Key", async () => {
    await startPackRun("open_items", body, "run-key");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/migration/packs/open_items/runs");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("run-key");
    expect(JSON.parse(init.body as string)).toEqual(body);
  });

  it("sends the plan version the user saw on step completion and rollback", async () => {
    await completeStep("p/1", 3, 45, 7, "c-key");
    await rollbackPlan("p/1", ["alice", "bob"], 8, "r-key");
    const calls = fetchMock.mock.calls as unknown as [string, RequestInit][];
    expect(calls.map(([url]) => url)).toEqual(["/api/v1/cutover/plans/p%2F1/steps/3/complete", "/api/v1/cutover/plans/p%2F1/rollback"]);
    expect(JSON.parse(calls[0][1].body as string)).toEqual({ actualDurationMin: 45, version: 7 });
    expect(JSON.parse(calls[1][1].body as string)).toEqual({ approvers: ["alice", "bob"], version: 8 });
    expect(calls.map(([, init]) => new Headers(init.headers).get("Idempotency-Key"))).toEqual(["c-key", "r-key"]);
  });

  it("drops empty filters from the run list query", async () => {
    await listRuns({ pack: "open_items", status: "" });
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/migration/runs?pack=open_items");
  });

  it("maps a problem response to its code and detail", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ title: "Unprocessable Entity", status: 422, code: "MIG-0001", detail: "MIG-1501 extract cutoff is after the legacy freeze" }), { status: 422 }),
    );
    const err = await startPackRun("open_items", body).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(MigrationApiError);
    expect(describeProblem(err)).toEqual({ code: "MIG-0001", message: "MIG-1501 extract cutoff is after the legacy freeze" });
  });

  it("falls back to the HTTP status when the body is not a problem", async () => {
    fetchMock.mockResolvedValueOnce(new Response("bad gateway", { status: 502 }));
    const err = await listRuns().catch((e: unknown) => e);
    expect(describeProblem(err)).toEqual({ code: "HTTP-502", message: "Request failed (HTTP 502)" });
  });
});
