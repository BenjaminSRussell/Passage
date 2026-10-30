import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { loadModules } from "../registry";
import migrationFactory from "./module";
import {
  completeStep,
  describeProblem,
  explainVariance,
  getProjectOpening,
  listRuns,
  lookupXref,
  MigrationApiError,
  releaseQuarantined,
  rollbackPlan,
  signOffParallelRun,
  startPackRun,
  type PackRunRequest,
} from "./api/client";

describe("migration-factory module", () => {
  it("uses its folder name as id and keeps every route and nav entry under /migration-factory", () => {
    expect(migrationFactory.id).toBe("migration-factory");
    expect(migrationFactory.routes.every((r) => r.element != null && r.path?.startsWith("/migration-factory"))).toBe(true);
    expect(migrationFactory.nav.every((n) => n.to.startsWith("/migration-factory"))).toBe(true);
  });

  it("has list, create and detail pages for the pack catalog, pack runs, GL balances, project balances, parallel runs, legacy references and cutover plans", () => {
    expect(migrationFactory.routes.map((r) => r.path)).toEqual([
      "/migration-factory",
      "/migration-factory/packs",
      "/migration-factory/runs/new",
      "/migration-factory/runs/new-gl",
      "/migration-factory/runs/new-project",
      "/migration-factory/runs/:id",
      "/migration-factory/runs/:id/projects/:project",
      "/migration-factory/parallel-runs",
      "/migration-factory/parallel-runs/new",
      "/migration-factory/parallel-runs/:id",
      "/migration-factory/xref",
      "/migration-factory/cutover",
      "/migration-factory/cutover/new",
      "/migration-factory/cutover/:id",
    ]);
  });

  it("guards the navigation with the duties the service enforces", () => {
    expect(migrationFactory.nav).toEqual([
      { label: "Pack runs", to: "/migration-factory", duty: "MigrationView" },
      { label: "Pack catalog", to: "/migration-factory/packs", duty: "MigrationView" },
      { label: "Parallel runs", to: "/migration-factory/parallel-runs", duty: "MigrationView" },
      { label: "Legacy reference", to: "/migration-factory/xref" },
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
    const posting = { kind: "DOCUMENT" as const, entity: "USMF", documentRef: "SI-9", postedAt: "2027-01-02T08:00:00Z", account: "1200", currency: "USD", amount: "125.50" };
    await rollbackPlan("p/1", ["alice", "bob"], [posting], 8, "r-key");
    const calls = fetchMock.mock.calls as unknown as [string, RequestInit][];
    expect(calls.map(([url]) => url)).toEqual(["/api/v1/cutover/plans/p%2F1/steps/3/complete", "/api/v1/cutover/plans/p%2F1/rollback"]);
    expect(JSON.parse(calls[0][1].body as string)).toEqual({ actualDurationMin: 45, version: 7 });
    expect(JSON.parse(calls[1][1].body as string)).toEqual({ approvers: ["alice", "bob"], postings: [posting], version: 8 });
    expect(calls.map(([, init]) => new Headers(init.headers).get("Idempotency-Key"))).toEqual(["c-key", "r-key"]);
  });

  it("sends slice-2 commands to their paths with the version and key", async () => {
    await releaseQuarantined("r/1", 2, { party: "V001", version: 4 }, "rel-key");
    await explainVariance("p1", "v/1", "cut-off timing", 3, "ex-key");
    await signOffParallelRun("p1", 4, "so-key");
    const calls = fetchMock.mock.calls as unknown as [string, RequestInit][];
    expect(calls.map(([url]) => url)).toEqual([
      "/api/v1/migration/runs/r%2F1/quarantine/2/release",
      "/api/v1/migration/parallel-runs/p1/variances/v%2F1/explain",
      "/api/v1/migration/parallel-runs/p1/sign-off",
    ]);
    expect(calls.map(([, init]) => JSON.parse(init.body as string))).toEqual([{ party: "V001", version: 4 }, { reason: "cut-off timing", version: 3 }, { version: 4 }]);
    expect(calls.map(([, init]) => new Headers(init.headers).get("Idempotency-Key"))).toEqual(["rel-key", "ex-key", "so-key"]);
  });

  it("builds the xref and project opening queries", async () => {
    await lookupXref({ system: "SAPECC", table: "open_items", id: "1900000123", company: undefined });
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ lines: [] }), { status: 200 }));
    await getProjectOpening("r1", "PRJ 1", { limit: 50, after: "PT-049" });
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual([
      "/api/v1/migration/xref?system=SAPECC&table=open_items&id=1900000123",
      "/api/v1/migration/runs/r1/projects/PRJ%201/opening?limit=50&after=PT-049",
    ]);
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
