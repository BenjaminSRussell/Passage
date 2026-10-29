import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Asset, MaintenanceSequence, SequenceOccurrence, SequenceStep } from "./api/client";
import eam from "./module";
import { intervalText, previewSteps, validateSequence } from "./SequencePages";

// Slice 4 screens: sequence register, sequence editor with the 12-occurrence preview, and sequence
// detail with the simulation, occurrence generation and generated occurrences (FR-44.15, BR-44.10).

const PUMP = "0195f0a0-0000-7000-8000-000000000001";
const SITE = "0195f0a0-0000-7000-8000-0000000000aa";
const HOURS = "0195f0a0-0000-7000-8000-0000000000c1";
const SEQ = "0195f0a0-0000-7000-8000-0000000000a7";
const WO = "0195f0a0-0000-7000-8000-0000000000d1";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(500, { code: "TEST-500", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function Where() {
  const location = useLocation();
  return <p data-testid="where">{location.pathname + location.search}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {eam.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const isGet = (path: string) => (url: URL, init: RequestInit) => (init.method ?? "GET") === "GET" && url.pathname === `/api/v1/eam${path}`;
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));

/** The BR-44.10 example: A×1, B×4 (includes A), C×12 (includes A, B). */
const abc: SequenceStep[] = [
  { code: "A", jobType: "LUBE", multiple: 1, hierarchy: 1, includes: [] },
  { code: "B", jobType: "INSPECT", multiple: 4, hierarchy: 2, includes: ["A"] },
  { code: "C", jobType: "OVERHAUL", multiple: 12, hierarchy: 3, includes: ["A", "B"] },
];

const sequence: MaintenanceSequence = {
  id: SEQ,
  code: "PUMP-SEQ",
  name: "Pump sequence",
  assetId: PUMP,
  baseIntervalValue: 250,
  counterId: HOURS,
  steps: abc,
  lastOccurrenceNumber: 3,
  version: 4,
  createdAt: "2026-10-01T08:00:00Z",
};

const pump: Asset = {
  id: PUMP,
  assetNumber: "AST-1",
  name: "Feed pump",
  assetType: "PUMP",
  siteId: SITE,
  criticality: 3,
  lifecycleState: "ACTIVE",
  version: 2,
  createdAt: "2026-10-01T08:00:00Z",
};

describe("sequence module wiring", () => {
  it("registers the sequence register, editor and detail, with a nav entry", () => {
    const paths = eam.routes.map((r) => r.path);
    for (const path of ["/eam/sequences", "/eam/sequences/new", "/eam/sequences/:id"]) expect(paths).toContain(path);
    expect(eam.nav.find((n) => n.to === "/eam/sequences")).toMatchObject({ label: "Sequences", duty: "EamInquire" });
  });
});

describe("sequence step selection (BR-44.10)", () => {
  it("previews AC-44.10: A, A, A, B, A, A, A, B, A, A, A, C", () => {
    expect(previewSteps(abc, 1, 12).map((o) => o.stepCodes.join("+"))).toEqual(["A", "A", "A", "B", "A", "A", "A", "B", "A", "A", "A", "C"]);
    expect(previewSteps(abc, 12, 1)[0].jobTypes).toEqual(["OVERHAUL"]);
  });

  it("adds a due step the selected one does not include, and reports occurrences with nothing due", () => {
    const steps: SequenceStep[] = [
      { code: "S", jobType: "SAFETY", multiple: 2, hierarchy: 1 },
      { code: "T", jobType: "TEST", multiple: 2, hierarchy: 2 },
    ];
    const [first, second] = previewSteps(steps, 1, 2);
    expect(first.stepCodes).toEqual([]);
    expect(second).toEqual({ occurrenceNumber: 2, stepCodes: ["T", "S"], jobTypes: ["TEST", "SAFETY"] });
  });

  it("words the base interval by unit or by counter", () => {
    expect(intervalText({ baseIntervalValue: 3, baseIntervalUnit: "MONTHS" })).toBe("Every 3 months");
    expect(intervalText({ baseIntervalValue: 250, counterId: HOURS })).toBe("Every 250 on the counter");
  });
});

describe("sequence form checks", () => {
  const step = (code: string, jobType: string, multiple: string, hierarchy: string, includes = "") => ({ code, jobType, multiple, hierarchy, includes });
  const base = {
    code: "pump-seq",
    name: "Pump sequence",
    scope: "ASSET" as const,
    assetId: PUMP,
    assetType: "",
    basis: "COUNTER" as const,
    baseIntervalValue: "250",
    baseIntervalUnit: "MONTHS" as const,
    counterId: HOURS,
    steps: [step("a", "lube", "1", "1"), step("B", "INSPECT", "4", "2", "a"), step("C", "OVERHAUL", "12", "3", "A, B")],
  };

  it("sends a counter sequence on one asset with upper-case codes and includes", () => {
    expect(validateSequence(base).input).toEqual({ code: "PUMP-SEQ", name: "Pump sequence", assetId: PUMP, baseIntervalValue: 250, counterId: HOURS, steps: abc });
  });

  it("sends an asset type template on a time unit", () => {
    const input = validateSequence({ ...base, scope: "ASSET_TYPE", assetType: "pump", basis: "TIME", counterId: "" }).input;
    expect(input).toMatchObject({ assetType: "PUMP", baseIntervalUnit: "MONTHS" });
    expect(input).not.toHaveProperty("assetId");
    expect(input).not.toHaveProperty("counterId");
  });

  it("refuses the step structures the service refuses with EAM-SEQ-001", () => {
    const { errors, input } = validateSequence({
      ...base,
      steps: [step("A", "LUBE", "0", "1"), step("A", "INSPECT", "4", "01"), step("C", "OVERHAUL", "12", "2", "C, Z")],
    });
    expect(input).toBeUndefined();
    expect(errors.steps[0].multiple).toMatch(/1 to 1,000/);
    expect(errors.steps[1].code).toMatch(/listed twice/);
    expect(errors.steps[1].hierarchy).toMatch(/its own hierarchy/);
    expect(errors.steps[2].includes).toMatch(/not another step/);
    const higher = validateSequence({ ...base, steps: [step("A", "LUBE", "1", "1", "B"), step("B", "INSPECT", "4", "2")] });
    expect(higher.errors.steps[0].includes).toMatch(/lower hierarchy/);
  });

  it("refuses a counter on an asset type sequence and a missing asset or counter", () => {
    expect(validateSequence({ ...base, scope: "ASSET_TYPE", assetType: "PUMP" }).errors.basis).toMatch(/time unit/);
    expect(Object.keys(validateSequence({ ...base, assetId: "", counterId: "", baseIntervalValue: "0" }).errors).filter((k) => k !== "steps").sort()).toEqual([
      "assetId",
      "baseIntervalValue",
      "counterId",
    ]);
  });
});

describe("sequence register", () => {
  it("lists sequences with scope, interval and steps, and opens one", async () => {
    handler = (url, init) => (isGet("/sequences")(url, init) ? json(200, { items: [sequence, { ...sequence, id: "t", code: "PUMP-TPL", assetId: undefined, assetType: "PUMP", counterId: undefined, baseIntervalUnit: "MONTHS", baseIntervalValue: 3 }] }) : undefined);
    const { container } = renderAt("/eam/sequences");
    const table = await findTable({ name: "Maintenance sequences" });
    expect(within(table).getAllByText("A×1, B×4, C×12")).toHaveLength(2);
    expect(within(table).getByText("Every 250 on the counter")).toBeInTheDocument();
    expect(within(table).getByText("Asset type PUMP")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "PUMP-SEQ" }));
    expect(screen.getByTestId("where")).toHaveTextContent(`/eam/sequences/${SEQ}`);
  });

  it("shows an empty register and a failed load", async () => {
    handler = (url, init) => (isGet("/sequences")(url, init) ? json(200, { items: [] }) : undefined);
    const { unmount } = renderAt("/eam/sequences");
    expect(await screen.findByText("No maintenance sequences yet")).toBeInTheDocument();
    unmount();
    handler = (url, init) => (isGet("/sequences")(url, init) ? json(503, { code: "COM-0503", detail: "down" }) : undefined);
    renderAt("/eam/sequences");
    expect(await findAlert()).toHaveTextContent("(COM-0503)");
  });
});

describe("sequence editor", () => {
  it("previews the first 12 occurrences and creates the sequence with an idempotency key", async () => {
    handler = (url, init) => {
      if (isGet("/assets")(url, init)) return json(200, { items: [pump] });
      if (isGet(`/assets/${PUMP}`)(url, init)) return json(200, { ...pump, counters: [{ id: HOURS, assetId: PUMP, name: "Run hours", unit: "h", cumulative: true, source: "MANUAL", lastReading: 0, avgDailyUsage: 0 }], rules: [], openWorkOrders: 0, partsCost: "0", laborCost: "0" });
      if (init.method === "POST" && url.pathname === "/api/v1/eam/sequences") return json(201, sequence);
      return undefined;
    };
    const { container } = renderAt("/eam/sequences/new");
    fireEvent.change(await screen.findByLabelText("Sequence code"), { target: { value: "PUMP-SEQ" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Pump sequence" } });
    fireEvent.change(screen.getByLabelText("Asset"), { target: { value: PUMP } });
    fireEvent.change(screen.getByLabelText("Base interval on"), { target: { value: "COUNTER" } });
    fireEvent.change(screen.getByLabelText("Base interval"), { target: { value: "250" } });
    fireEvent.change(await screen.findByLabelText("Counter"), { target: { value: HOURS } });
    const fill = (i: number, code: string, job: string, multiple: string, includes = "") => {
      fireEvent.change(screen.getByLabelText("Step code", { selector: `#step-${i}-code` }), { target: { value: code } });
      fireEvent.change(screen.getByLabelText("Job type", { selector: `#step-${i}-job` }), { target: { value: job } });
      fireEvent.change(screen.getByLabelText(/Every nth occurrence/, { selector: `#step-${i}-multiple` }), { target: { value: multiple } });
      if (includes) fireEvent.change(screen.getByLabelText(/Includes steps/, { selector: `#step-${i}-includes` }), { target: { value: includes } });
    };
    fill(0, "A", "LUBE", "1");
    fireEvent.click(screen.getByRole("button", { name: "Add step" }));
    fill(1, "B", "INSPECT", "4", "A");
    fireEvent.click(screen.getByRole("button", { name: "Add step" }));
    fill(2, "C", "OVERHAUL", "12", "A, B");
    expect(screen.getByText("First 12 occurrences: A, A, A, B, A, A, A, B, A, A, A, C")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create sequence" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/eam/sequences/${SEQ}`));
    const [call] = posts("/sequences");
    expect(call.body).toEqual({ code: "PUMP-SEQ", name: "Pump sequence", assetId: PUMP, baseIntervalValue: 250, counterId: HOURS, steps: abc });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
  });

  it("puts a duplicate code on the code field (EAM-DUP-001)", async () => {
    handler = (url, init) => {
      if (isGet("/assets")(url, init)) return json(200, { items: [pump] });
      if (init.method === "POST" && url.pathname === "/api/v1/eam/sequences") return json(409, { code: "EAM-DUP-001", detail: "maintenance sequence PUMP-SEQ already exists" });
      return undefined;
    };
    renderAt("/eam/sequences/new");
    fireEvent.change(await screen.findByLabelText("Sequence code"), { target: { value: "PUMP-SEQ" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Pump sequence" } });
    fireEvent.change(screen.getByLabelText("Asset"), { target: { value: PUMP } });
    fireEvent.change(screen.getByLabelText("Base interval"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("Step code", { selector: "#step-0-code" }), { target: { value: "A" } });
    fireEvent.change(screen.getByLabelText("Job type", { selector: "#step-0-job" }), { target: { value: "LUBE" } });
    fireEvent.click(screen.getByRole("button", { name: "Create sequence" }));
    expect(await screen.findByText("A sequence with this code already exists.")).toBeInTheDocument();
    expect(posts("/sequences")[0].body).toMatchObject({ baseIntervalUnit: "MONTHS", steps: [{ code: "A", jobType: "LUBE", multiple: 1, hierarchy: 1, includes: [] }] });
  });
});

describe("sequence detail", () => {
  const occurrence: SequenceOccurrence = {
    id: "occ-4",
    sequenceId: SEQ,
    occurrenceNumber: 4,
    stepCodes: ["B"],
    jobTypes: ["INSPECT"],
    workOrderId: WO,
    actor: "planner-1",
    createdAt: "2026-10-21T08:00:00Z",
  };

  function fakeSequence(extra: Handler = () => undefined) {
    let generated: SequenceOccurrence[] = [];
    let current = sequence;
    handler = (url, init) => {
      const hit = extra(url, init);
      if (hit) return hit;
      if (init.method === "POST" && url.pathname === `/api/v1/eam/sequences/${SEQ}/occurrences`) {
        generated = [occurrence];
        current = { ...current, lastOccurrenceNumber: 4, version: 5 };
        return json(201, occurrence);
      }
      if (isGet(`/sequences/${SEQ}`)(url, init)) return json(200, current);
      if (isGet(`/sequences/${SEQ}/simulation`)(url, init)) return json(200, { items: previewSteps(abc, current.lastOccurrenceNumber + 1, 12) });
      if (isGet(`/sequences/${SEQ}/occurrences`)(url, init)) return json(200, { items: generated });
      return undefined;
    };
  }

  it("shows the steps and the next 12 occurrences, and generates the next one", async () => {
    fakeSequence();
    const { container } = renderAt(`/eam/sequences/${SEQ}`);
    expect(await screen.findByRole("heading", { name: "Pump sequence" })).toBeInTheDocument();
    const sim = await findTable({ name: "Next 12 occurrences" });
    expect(within(sim).getAllByRole("row")[1]).toHaveTextContent("4B");
    expect(calls.find((c) => c.url.pathname.endsWith("/simulation"))?.url.searchParams.get("occurrences")).toBe("12");
    expect(await screen.findByText("No occurrence generated yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/Due \(UTC\)/), { target: { value: "2026-10-30T08:00" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate occurrence 4" }));
    expect(await findStatus()).toHaveTextContent("Occurrence 4: B. A preventive work order was opened for INSPECT.");
    const [call] = posts("/occurrences");
    expect(call.body).toEqual({ currency: "USD", dueAt: "2026-10-30T08:00:00.000Z" });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
    const generated = await findTable({ name: "Generated occurrences" });
    expect(within(generated).getByRole("link", { name: "Open work order" })).toHaveAttribute("href", `/eam/work-orders/${WO}`);
    expect(await screen.findByRole("button", { name: "Generate occurrence 5" })).toBeInTheDocument();
  });

  it("explains an open previous occurrence (EAM-SEQ-002) and a bad currency", async () => {
    fakeSequence((url, init) =>
      init.method === "POST" && url.pathname.endsWith("/occurrences") ? json(422, { code: "EAM-SEQ-002", detail: "the previous occurrence's work order is still open" }) : undefined,
    );
    renderAt(`/eam/sequences/${SEQ}`);
    fireEvent.change(await screen.findByLabelText("Work order currency"), { target: { value: "US" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate occurrence 4" }));
    expect(await screen.findByText("Enter a three-letter currency code.")).toBeInTheDocument();
    expect(posts("/occurrences")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Work order currency"), { target: { value: "eur" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate occurrence 4" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Finish or cancel the previous occurrence's work order first");
    expect(alert).toHaveTextContent("(EAM-SEQ-002)");
    expect(posts("/occurrences")[0].body).toEqual({ currency: "EUR" });
  });

  it("offers no generation on an asset type template", async () => {
    fakeSequence((url, init) =>
      isGet(`/sequences/${SEQ}`)(url, init) ? json(200, { ...sequence, assetId: undefined, counterId: undefined, assetType: "PUMP", baseIntervalUnit: "MONTHS" }) : undefined,
    );
    renderAt(`/eam/sequences/${SEQ}`);
    expect(await screen.findByText(/template for asset type PUMP/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Generate occurrence/ })).not.toBeInTheDocument();
  });

  it("says when the sequence is not found", async () => {
    fakeSequence((url, init) => (isGet(`/sequences/${SEQ}`)(url, init) ? json(404, { code: "EAM-NF-001", detail: "maintenance sequence not found" }) : undefined));
    renderAt(`/eam/sequences/${SEQ}`);
    expect(await screen.findByText("We couldn't find this sequence")).toBeInTheDocument();
  });
});
