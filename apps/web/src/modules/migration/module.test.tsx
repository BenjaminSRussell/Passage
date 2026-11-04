import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import { loadModules } from "../registry";
import type { MappingSpec, MigrationRun } from "./api/client";
import { parseMappingText } from "./domain";
import migration from "./module";

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
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
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: "no fake" });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = ["migration.run", "migration.spec.write"]) {
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

const RUN = "0195f0a0-0000-7000-8000-0000000000a1";
const SPEC = "0195f0a0-0000-7000-8000-0000000000b1";
const run: MigrationRun = {
  id: RUN,
  sourceSystem: "ax2012",
  mode: "rehearsal",
  entity: "customer",
  status: "partial",
  dryRun: false,
  mappingVersion: 1,
  mappingSpecId: SPEC,
  counts: { extracted: 3, mapped: 2, created: 2, updated: 0, unchanged: 0, rejected: 1, warned: 0 },
  rejects: [{ sourceKey: "C3", rejects: [{ field: "name", code: "MIG-MAP-REQUIRED", detail: "is required" }] }],
  warnings: [],
  absent: [],
  reconciliation: { source: 3, target: 2, checks: [{ name: "row_count", ok: false, detail: "1 rejected" }] },
  createdAt: "2026-10-30T10:00:00Z",
};
const spec: MappingSpec = { id: SPEC, entity: "customer", version: 1, checksum: "ab".repeat(32), fieldCount: 2, testCount: 1, naturalKey: [], createdAt: "2026-10-30T09:00:00Z", mapping: { entity: "customer", version: 1 } };

describe("migration module", () => {
  it("is discovered and keeps every route under its prefix", () => {
    expect(loadModules().map((m) => m.id)).toContain("migration");
    expect(migration.nav.every((n) => n.to.startsWith("/migration"))).toBe(true);
  });

  it("parses pasted mapping JSON", () => {
    expect(parseMappingText("").error).toBeTruthy();
    expect(parseMappingText("[1]").error).toMatch(/object/);
    expect(parseMappingText("{").error).toMatch(/Not valid JSON/);
    expect(parseMappingText('{"a":1}').mapping).toEqual({ a: 1 });
  });
});

describe("runs list", () => {
  it("shows runs, filters by status and opens a run", async () => {
    handler = () => json(200, { items: [run] });
    const { container } = renderAt("/migration");
    const table = await findTable({ name: "Migration runs" });
    expect(table).toHaveTextContent("customer");
    expect(table).toHaveTextContent("Dynamics AX 2012");
    expect(violations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "failed" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("status") === "failed")).toBe(true));
  });

  it("shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration");
    expect(await screen.findByText("No migration runs yet")).toBeInTheDocument();
  });

  it("shows the error state and retries", async () => {
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty" });
    renderAt("/migration");
    expect(await screen.findByText(/missing duty \(AUTH-0003\)/)).toBeInTheDocument();
    handler = () => json(200, { items: [run] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Migration runs" })).toHaveTextContent("customer");
  });

  it("hides Start run without the duty", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration", []);
    await screen.findByText("No migration runs yet");
    expect(screen.queryByRole("link", { name: "Start run" })).not.toBeInTheDocument();
  });
});

describe("start run", () => {
  it("requires a spec, then posts with an idempotency key and opens the run", async () => {
    handler = (url, init) => (init.method === "POST" ? json(202, { run }) : url.pathname.endsWith("/mapping-specs") ? json(200, { items: [spec] }) : json(200, run));
    const { container } = renderAt("/migration/runs/new");
    await screen.findByRole("option", { name: "customer v1" });
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    expect(await screen.findByText("Choose the mapping spec to run.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Mapping spec"), { target: { value: SPEC } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    expect(await screen.findByRole("heading", { name: "customer migration" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ sourceSystem: "ax2012", mode: "rehearsal", dryRun: true, mappingSpecId: SPEC });
    expect(new Headers(post?.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("shows the service problem when the backend is not configured", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(501, { code: "MIG-BACKEND-NOT-CONFIGURED", detail: "backends are not configured" }) : json(200, { items: [spec] }));
    renderAt("/migration/runs/new");
    await screen.findByRole("option", { name: "customer v1" });
    fireEvent.change(screen.getByLabelText("Mapping spec"), { target: { value: SPEC } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    expect(await screen.findByText(/MIG-BACKEND-NOT-CONFIGURED/)).toBeInTheDocument();
  });

  it("lists the field reasons when the service rejects the spec reference", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? json(422, { code: "MIG-SPEC-UNKNOWN", detail: "mappingSpecId names no published mapping spec", errors: { mappingSpecId: "no published mapping spec has this id" } })
        : json(200, { items: [spec] });
    renderAt("/migration/runs/new");
    await screen.findByRole("option", { name: "customer v1" });
    fireEvent.change(screen.getByLabelText("Mapping spec"), { target: { value: SPEC } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    expect(await screen.findByText("mappingSpecId: no published mapping spec has this id")).toBeInTheDocument();
  });
});

describe("run detail", () => {
  it("shows counts, rejects and reconciliation and re-runs the rejects", async () => {
    const child = { ...run, id: "0195f0a0-0000-7000-8000-0000000000a2", parentRunId: RUN, status: "succeeded", rejects: [], counts: { ...run.counts, rejected: 0 } };
    handler = (url, init) => (init.method === "POST" ? json(202, { run: child }) : url.pathname.endsWith(child.id) ? json(200, child) : json(200, run));
    const { container } = renderAt(`/migration/runs/${RUN}`);
    expect(await screen.findByRole("heading", { name: "customer migration" })).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Rejected rows" })).getByText("name: MIG-MAP-REQUIRED")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Reconciliation checks" })).toHaveTextContent("Differs");
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Re-run rejected rows" }));
    expect(await screen.findByText("No rejected rows")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.url.pathname).toBe(`/api/v1/integration/migration-runs/${RUN}:rerun`);
    expect(screen.getByRole("link", { name: "the original run" })).toBeInTheDocument();
  });

  it("shows a not-found error", async () => {
    handler = () => json(404, { code: "AUTH-0404", detail: "migration run not found" });
    renderAt(`/migration/runs/${RUN}`);
    expect(await screen.findByText(/migration run not found/)).toBeInTheDocument();
  });
});

describe("mapping specs", () => {
  it("lists specs, shows the empty state and the error state", async () => {
    handler = () => json(200, { items: [spec] });
    const first = renderAt("/migration/specs");
    expect(await findTable({ name: "Mapping specs" })).toHaveTextContent("customer");
    first.unmount();
    handler = () => json(200, { items: [] });
    const second = renderAt("/migration/specs");
    expect(await screen.findByText("No mapping specs yet")).toBeInTheDocument();
    second.unmount();
    handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/migration/specs");
    expect(await screen.findByText(/internal error/)).toBeInTheDocument();
  });

  it("validates the JSON locally, then publishes and shows the stored spec", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, spec) : json(200, spec));
    renderAt("/migration/specs/new");
    fireEvent.click(screen.getByRole("button", { name: "Publish spec" }));
    expect(await screen.findByText("Paste the mapping JSON.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Mapping JSON"), { target: { value: '{"entity":"customer","version":1}' } });
    fireEvent.click(screen.getByRole("button", { name: "Publish spec" }));
    expect(await screen.findByRole("heading", { name: "customer v1" })).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ mapping: { entity: "customer", version: 1 } });
  });

  it("lists the failing mapping tests from the service", async () => {
    handler = () => json(422, { code: "MIG-MAP-TEST-FAIL", detail: "1 mapping tests failed for customer.", errors: { uppercase: "name was a, want A" } });
    renderAt("/migration/specs/new");
    fireEvent.change(screen.getByLabelText("Mapping JSON"), { target: { value: "{}" } });
    fireEvent.click(screen.getByRole("button", { name: "Publish spec" }));
    expect(await screen.findByText("uppercase: name was a, want A")).toBeInTheDocument();
  });

  it("shows a conflict when the version is used", async () => {
    handler = () => json(409, { code: "MIG-SPEC-VERSION-EXISTS", detail: "publish a higher version" });
    renderAt("/migration/specs/new");
    fireEvent.change(screen.getByLabelText("Mapping JSON"), { target: { value: "{}" } });
    fireEvent.click(screen.getByRole("button", { name: "Publish spec" }));
    expect(await screen.findByText(/MIG-SPEC-VERSION-EXISTS/)).toBeInTheDocument();
  });

  it("starts a run from the spec page with the spec preselected", async () => {
    handler = (url) => (url.pathname.endsWith("/mapping-specs") ? json(200, { items: [spec] }) : json(200, spec));
    renderAt(`/migration/specs/${SPEC}`);
    fireEvent.click(await screen.findByRole("link", { name: "Start run with this spec" }));
    await screen.findByRole("option", { name: "customer v1" });
    expect(screen.getByLabelText("Mapping spec")).toHaveValue(SPEC);
  });

  it("hides the start-run action without the run duty", async () => {
    handler = () => json(200, spec);
    renderAt(`/migration/specs/${SPEC}`, ["migration.spec.write"]);
    await screen.findByRole("heading", { name: "customer v1" });
    expect(screen.queryByRole("link", { name: "Start run with this spec" })).not.toBeInTheDocument();
  });

  it("lists the reject conditions of a spec with their reason codes", async () => {
    const rejectWhen = [
      { field: "group", op: "empty", code: "CUST-NO-GROUP" },
      { field: "group", op: "notIn", values: ["10", "20"], code: "CUST-BAD-GROUP" },
      { field: "id", op: "match", value: "^TMP", code: "CUST-TEMP" },
    ];
    handler = () => json(200, { ...spec, mapping: { entity: "customer", version: 1, rejectWhen } });
    renderAt(`/migration/specs/${SPEC}`);
    const table = await screen.findByRole("table", { name: "Reject conditions" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows.map((r) => r.textContent)).toEqual(["CUST-NO-GROUPgroupis empty", "CUST-BAD-GROUPgroupis not one of 10, 20", "CUST-TEMPidmatches ^TMP"]);
  });

  it("lists the field rules of a spec, with the expression of an expr rule and an expr reject condition", async () => {
    const mapping = {
      entity: "item",
      version: 1,
      fields: [
        { source: "SKU", target: "sku", type: "string" },
        { rule: "concat", sources: ["A", "B"], target: "ab", type: "string" },
        { rule: "const", value: "EA", target: "uom", type: "string" },
        { rule: "expr", expr: "round(QTY * PRICE, 2)", target: "value", type: "decimal" },
        { source: "ACTIVE", fn: "ax_noyes", target: "active", type: "bool" },
      ],
      rejectWhen: [{ field: "value", op: "expr", value: "value > 1000", code: "ITEM-VALUE-HIGH" }],
    };
    handler = () => json(200, { ...spec, mapping });
    renderAt(`/migration/specs/${SPEC}`);
    const table = await screen.findByRole("table", { name: "Field rules" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows.map((r) => r.textContent)).toEqual([
      "skustringcopySKU",
      "abstringconcatA + B",
      'uomstringconst"EA"',
      "valuedecimalexprround(QTY * PRICE, 2)",
      "activeboolcopy (ax_noyes)ACTIVE",
    ]);
    const reject = await screen.findByRole("table", { name: "Reject conditions" });
    expect(reject).toHaveTextContent("ITEM-VALUE-HIGHvalueis true: value > 1000");
    expect(violations(table.closest("main") ?? document.body)).toEqual([]);
  });

  it("shows no field rules card for a spec whose mapping has no fields", async () => {
    handler = () => json(200, { ...spec, mapping: { entity: "customer", version: 1 } });
    renderAt(`/migration/specs/${SPEC}`);
    expect(await screen.findByText("Checksum")).toBeInTheDocument();
    expect(screen.queryByText("Field rules")).not.toBeInTheDocument();
  });

  it("shows no reject conditions card for a spec without rejectWhen", async () => {
    handler = () => json(200, spec);
    renderAt(`/migration/specs/${SPEC}`);
    expect(await screen.findByText("Checksum")).toBeInTheDocument();
    expect(screen.queryByText("Reject conditions")).not.toBeInTheDocument();
  });

  it("shows a spec with its mapping and a load error", async () => {
    handler = () => json(200, spec);
    const first = renderAt(`/migration/specs/${SPEC}`);
    expect(await screen.findByText(/"entity": "customer"/)).toBeInTheDocument();
    first.unmount();
    handler = () => json(404, { code: "AUTH-0404", detail: "mapping spec not found" });
    renderAt(`/migration/specs/${SPEC}`);
    expect(await screen.findByText(/mapping spec not found/)).toBeInTheDocument();
  });
});
