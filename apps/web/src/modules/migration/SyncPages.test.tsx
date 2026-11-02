import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import type { MigrationRun, SyncAbsent, SyncConflict, SyncStatus } from "./api/client";
import migration from "./module";
import { summarize, syncHealth } from "./SyncPages";

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const violations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: "no fake" });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = ["migration.run", "migration.project.write"]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {migration.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const BINDING = "0195f0a0-0000-7000-8000-0000000000b1";
const RUN = "0195f0a0-0000-7000-8000-0000000000c1";
const healthy: SyncStatus = {
  bindingId: BINDING,
  entity: "customer",
  connectionProfileId: "0195f0a0-0000-7000-8000-0000000000a1",
  connectionProfileName: "Acme ERP",
  bindingStatus: "active",
  scheduleCron: "*/15 * * * *",
  sourceOfTruth: "legacy",
  paused: false,
  watermark: "2026-11-01T09:00:00.000000000Z",
  nextDueAt: "2026-11-01T10:15:00Z",
  running: false,
  lastRunId: RUN,
  lastRunAt: "2026-11-01T10:00:00Z",
  lastSuccessAt: "2026-11-01T10:00:00Z",
  lastStatus: "succeeded",
  consecutiveFailures: 0,
  rowsLoaded: 12,
  rowsRejected: 1,
  openConflicts: 0,
  version: 2,
  keyCheckHours: 24,
  lastKeyCheckAt: "2026-11-01T09:00:00Z",
  absentCount: 0,
};
const gone: SyncAbsent = { bindingId: BINDING, sourceKey: "C9", targetId: "0195f0a0-0000-7000-8000-0000000000e1", runId: RUN, firstAbsentAt: "2026-11-01T09:00:00Z", lastCheckedAt: "2026-11-02T09:00:00Z" };
const failing: SyncStatus = { ...healthy, bindingId: "0195f0a0-0000-7000-8000-0000000000b2", entity: "vendor", consecutiveFailures: 3, lastStatus: "failed", lastError: "EGRESS-DENIED: the host is not allowed", nextDueAt: "2026-11-01T10:45:00Z" };
const conflict: SyncConflict = { id: "0195f0a0-0000-7000-8000-0000000000d1", bindingId: BINDING, entity: "customer", sourceKey: "C1", legacyFields: { name: "Legacy name" }, status: "open", detectedAt: "2026-11-01T10:00:00Z", version: 3 };
const run: MigrationRun = {
  id: RUN,
  sourceSystem: "custom_acme",
  mode: "delta",
  entity: "customer",
  status: "partial",
  dryRun: false,
  mappingVersion: 1,
  bindingId: BINDING,
  watermark: "2026-11-01T09:00:00.000000000Z",
  counts: { extracted: 3, mapped: 3, created: 1, updated: 1, unchanged: 0, conflicted: 1, rejected: 1, warned: 0 },
  rejects: [{ sourceKey: "C1", rejects: [{ field: "", code: "MIG-TARGET-DRIFT", detail: "Passage is the source of truth" }] }],
  warnings: [],
  absent: [],
  createdAt: "2026-11-01T10:00:00Z",
};

function detailHandler(status: SyncStatus = healthy, conflicts: SyncConflict[] = [], absent: SyncAbsent[] = []) {
  handler = (url, init) => {
    const path = url.pathname;
    if (path.endsWith(`/sync-status/${BINDING}`) && (init.method ?? "GET") === "GET") return json(200, status);
    if (path.endsWith(`/sync-status/${BINDING}`) && init.method === "PATCH") return json(200, { ...status, ...JSON.parse(String(init.body)), version: status.version + 1 });
    if (path.endsWith(`/sync-status/${BINDING}/absent`)) return json(200, { items: absent });
    if (path.endsWith("/migration-runs")) return json(200, { items: [run] });
    if (path.endsWith(`/migration-runs/${RUN}`)) return json(200, run);
    if (path.endsWith("/sync-conflicts")) return json(200, { items: conflicts });
    if (path.includes("/sync-conflicts/") && path.endsWith(":resolve")) return json(200, { ...conflict, status: "kept" });
    return undefined;
  };
}

describe("sync health", () => {
  it("names the state a binding is in", () => {
    expect(syncHealth(healthy).label).toBe("Healthy");
    expect(syncHealth({ ...healthy, running: true }).label).toBe("Running");
    expect(syncHealth({ ...healthy, paused: true }).label).toBe("Paused");
    expect(syncHealth({ ...healthy, scheduleCron: "" }).label).toBe("Not scheduled");
    expect(syncHealth({ ...healthy, consecutiveFailures: 1 }).label).toBe("Failing");
    expect(syncHealth(failing).label).toBe("Backing off");
    expect(syncHealth({ ...healthy, openConflicts: 2 }).label).toBe("Conflicts");
  });
  it("shortens long legacy values", () => {
    expect(summarize({ a: "x", b: 2 })).toBe("a=x, b=2");
    expect(summarize({ a: "y".repeat(200) })).toHaveLength(120);
  });
});

describe("delta sync list", () => {
  it("shows totals and every scheduled binding, and filters failing ones", async () => {
    handler = (url) => json(200, { items: url.searchParams.get("failing") === "true" ? [failing] : [healthy, failing] });
    const { container } = renderAt("/migration/sync");
    const table = await findTable({ name: "Delta sync status" });
    expect(table).toHaveTextContent("customer");
    expect(table).toHaveTextContent("Backing off");
    expect(table).toHaveTextContent("Acme ERP");
    expect(await screen.findByRole("list", { name: "Delta sync totals" })).toHaveTextContent("Failing");
    expect(violations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Show"), { target: { value: "failing" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("failing") === "true")).toBe(true));
    await waitFor(() => expect(screen.getByRole("table", { name: "Delta sync status" })).not.toHaveTextContent("customer"));
  });

  it("shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration/sync");
    expect(await screen.findByText("No scheduled bindings")).toBeInTheDocument();
  });

  it("shows the error with a way to retry", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderAt("/migration/sync");
    expect(await screen.findByText(/Could not load the sync status/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("loads the next page with the cursor", async () => {
    handler = (url) => (url.searchParams.get("cursor") ? json(200, { items: [failing] }) : json(200, { items: [healthy], nextCursor: "c1" }));
    renderAt("/migration/sync");
    await findTable({ name: "Delta sync status" });
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await waitFor(() => expect(screen.getByRole("table")).toHaveTextContent("vendor"));
  });
});

describe("deleted legacy rows", () => {
  it("lists the keys the last full read did not find, with the Passage record they were loaded into", async () => {
    detailHandler({ ...healthy, absentCount: 1 }, [], [gone]);
    const { container } = renderAt(`/migration/sync/${BINDING}`);
    const table = await findTable({ name: "Keys missing in the legacy system" });
    expect(table).toHaveTextContent("C9");
    expect(table).toHaveTextContent("0195f0a0-0000-7000-8000-0000000000e1");
    expect(screen.getByText(/Nothing is deactivated in Passage/)).toBeInTheDocument();
    expect(violations(container)).toEqual([]);
  });

  it("shows the empty state and sends a period change", async () => {
    detailHandler();
    const inner = handler;
    renderAt(`/migration/sync/${BINDING}`);
    expect(await screen.findByText("No missing keys")).toBeInTheDocument();
    handler = (url, init) => (url.pathname.endsWith("/absent") ? json(500, { code: "COM-0500", detail: "boom" }) : inner(url, init));
    fireEvent.change(screen.getByLabelText("Full read to find deleted rows"), { target: { value: "6" } });
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
  });

  it("says why the last full read did not update the list", async () => {
    detailHandler({ ...healthy, absentCount: 1, keyCheckError: "the full read did not complete: customer has more than 2000000 rows to extract" }, [], [gone]);
    renderAt(`/migration/sync/${BINDING}`);
    expect(await screen.findByText("The last full read did not update this list")).toBeInTheDocument();
    expect(screen.getByText(/more than 2000000 rows/)).toBeInTheDocument();
    expect(await findTable({ name: "Keys missing in the legacy system" })).toHaveTextContent("C9");
    // Only a refused large vanish can be accepted; a failed read cannot.
    expect(screen.queryByRole("button", { name: "Accept the next full read once" })).not.toBeInTheDocument();
  });

  it("shows a load failure of the list", async () => {
    detailHandler();
    const inner = handler;
    handler = (url, init) => (url.pathname.endsWith("/absent") ? json(500, { code: "COM-0500", detail: "boom" }) : inner(url, init));
    renderAt(`/migration/sync/${BINDING}`);
    expect(await screen.findByText(/Could not load the missing keys/)).toBeInTheDocument();
  });

  it("changes the period and asks for a check at the next run", async () => {
    detailHandler();
    renderAt(`/migration/sync/${BINDING}`);
    fireEvent.change(await screen.findByLabelText("Full read to find deleted rows"), { target: { value: "168" } });
    await waitFor(() => expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ version: 2, keyCheckHours: 168 }));
    fireEvent.click(await screen.findByRole("button", { name: "Check at the next run" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "PATCH")[1].body).toEqual({ version: 3, keyCheckNow: true });
  });

  it("offers to accept a refused large vanish once", async () => {
    detailHandler({ ...healthy, keyCheckError: "the full read lists 8 of the 10 loaded keys not listed already as newly absent (more than half); taken for a broken source rather than deletions, so the list was kept." });
    renderAt(`/migration/sync/${BINDING}`);
    fireEvent.click(await screen.findByRole("button", { name: "Accept the next full read once" }));
    await waitFor(() => expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ version: 2, keyCheckAcceptLarge: true }));
  });

  it("says an acceptance is waiting instead of offering it again", async () => {
    detailHandler({ ...healthy, keyCheckAcceptLarge: true, keyCheckError: "the full read lists 8 of the 10 loaded keys not listed already as newly absent (more than half); taken for a broken source rather than deletions, so the list was kept." });
    renderAt(`/migration/sync/${BINDING}`);
    expect(await screen.findByText(/The next full read will be accepted whatever it lists/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Accept the next full read once" })).not.toBeInTheDocument();
  });

  it("is read-only without migration.project.write, and offers no check when the period is off", async () => {
    detailHandler({ ...healthy, keyCheckHours: 0 });
    renderAt(`/migration/sync/${BINDING}`, ["migration.run"]);
    expect(await screen.findByLabelText("Full read to find deleted rows")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Check at the next run" })).not.toBeInTheDocument();
  });

  it("totals the missing keys on the monitor", async () => {
    handler = () => json(200, { items: [{ ...healthy, absentCount: 4 }] });
    renderAt("/migration/sync");
    const table = await findTable({ name: "Delta sync status" });
    expect(table).toHaveTextContent("Missing in legacy");
    expect(await screen.findByRole("list", { name: "Delta sync totals" })).toHaveTextContent("Missing in legacy");
  });
});

describe("delta sync detail", () => {
  it("shows status, recent runs and the rejects of the last run", async () => {
    detailHandler();
    const { container } = renderAt(`/migration/sync/${BINDING}`);
    expect(await screen.findByRole("heading", { name: "customer sync" })).toBeInTheDocument();
    const runs = await findTable({ name: "Recent sync runs" });
    expect(runs).toHaveTextContent("Partial");
    const rejects = await findTable({ name: "Rejected rows of the last run" });
    expect(rejects).toHaveTextContent("MIG-TARGET-DRIFT");
    expect(screen.getAllByText("2026-11-01T09:00:00.000000000Z").length).toBeGreaterThan(0);
    expect(calls.some((c) => c.url.searchParams.get("bindingId") === BINDING)).toBe(true);
    expect(await screen.findByText("No open conflicts")).toBeInTheDocument();
    expect(violations(container)).toEqual([]);
  });

  it("pauses the schedule under the status version", async () => {
    detailHandler();
    renderAt(`/migration/sync/${BINDING}`);
    fireEvent.click(await screen.findByRole("button", { name: "Pause schedule" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ version: 2, paused: true });
    expect(await screen.findByRole("button", { name: "Resume schedule" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run now" })).toBeDisabled();
  });

  it("makes the binding due now, and changes the source of truth", async () => {
    detailHandler();
    renderAt(`/migration/sync/${BINDING}`);
    fireEvent.click(await screen.findByRole("button", { name: "Run now" }));
    await waitFor(() => expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ version: 2, runNow: true }));
    fireEvent.change(await screen.findByLabelText("Source of truth"), { target: { value: "passage" } });
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "PATCH")[1].body).toMatchObject({ sourceOfTruth: "passage" });
  });

  it("explains failures and the backoff", async () => {
    detailHandler({ ...failing, bindingId: BINDING });
    renderAt(`/migration/sync/${BINDING}`);
    const alert = await screen.findByText(/3 failed runs in a row/);
    expect(alert.closest("[role]") ?? alert.parentElement).toHaveTextContent("EGRESS-DENIED");
    expect(screen.getByText(/backs off/)).toBeInTheDocument();
  });

  it("hides the schedule actions without migration.project.write", async () => {
    detailHandler();
    renderAt(`/migration/sync/${BINDING}`, ["migration.run"]);
    await screen.findByRole("heading", { name: "customer sync" });
    expect(screen.queryByRole("button", { name: "Pause schedule" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("Source of truth")).toBeDisabled();
  });

  it("resolves a conflict after confirmation", async () => {
    detailHandler({ ...healthy, openConflicts: 1, sourceOfTruth: "passage" }, [conflict]);
    const { container } = renderAt(`/migration/sync/${BINDING}`);
    const table = await findTable({ name: "Sync conflicts" });
    expect(table).toHaveTextContent("Legacy name");
    fireEvent.click(within(table).getByRole("button", { name: "Keep Passage" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(violations(container)).toEqual([]);
    fireEvent.click(within(dialog).getByRole("button", { name: "Keep Passage" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith(":resolve"))).toBe(true));
    expect(calls.find((c) => c.url.pathname.endsWith(":resolve"))?.body).toEqual({ resolution: "keep_passage", version: 3 });
  });

  it("shows the service's refusal when accepting legacy values fails", async () => {
    detailHandler({ ...healthy, openConflicts: 1 }, [conflict]);
    const inner = handler;
    handler = (url, init) => (url.pathname.endsWith(":resolve") ? json(409, { code: "MIG-SYNC-ACCEPT-FAILED", detail: "the target refused the legacy values" }) : inner(url, init));
    renderAt(`/migration/sync/${BINDING}`);
    const table = await findTable({ name: "Sync conflicts" });
    fireEvent.click(within(table).getByRole("button", { name: "Accept legacy" }));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Accept legacy" }));
    expect(await screen.findByText(/MIG-SYNC-ACCEPT-FAILED/)).toBeInTheDocument();
  });

  it("reloads the conflicts when a run replaced the legacy values after they were shown", async () => {
    detailHandler({ ...healthy, openConflicts: 1, sourceOfTruth: "passage" }, [conflict]);
    const inner = handler;
    let lists = 0;
    handler = (url, init) => {
      if (url.pathname.endsWith(":resolve")) return json(409, { code: "MIG-VERSION-CONFLICT", detail: "current version is 4: the legacy row changed again; review the new values" });
      if (url.pathname.endsWith("/sync-conflicts")) lists++;
      return inner(url, init);
    };
    renderAt(`/migration/sync/${BINDING}`);
    const table = await findTable({ name: "Sync conflicts" });
    const before = lists;
    fireEvent.click(within(table).getByRole("button", { name: "Keep Passage" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Keep Passage" }));
    expect(await screen.findByText(/MIG-VERSION-CONFLICT/)).toBeInTheDocument();
    await waitFor(() => expect(lists).toBeGreaterThan(before));
  });

  it("shows a load failure", async () => {
    handler = () => json(404, { code: "AUTH-0404", detail: "entity binding not found" });
    renderAt(`/migration/sync/${BINDING}`);
    expect(await screen.findByText(/Could not load this binding's sync/)).toBeInTheDocument();
  });
});

describe("module registration", () => {
  it("adds the delta sync nav item gated on migration.run", () => {
    expect(migration.nav?.find((n) => n.to === "/migration/sync")).toMatchObject({ label: "Delta sync", duty: "migration.run" });
    expect(migration.routes.some((r) => r.path === "/migration/sync/:bindingId")).toBe(true);
  });
});
