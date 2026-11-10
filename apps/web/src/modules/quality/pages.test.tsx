import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Instrument, Order, OrderView, TestGroup } from "./api/client";
import { validateAssociation } from "./AssociationsPage";
import quality from "./module";
import { emptyPlan } from "./PlanFields";
import { outsideLimits } from "./ResultsEntry";
import { validateTestGroup } from "./TestGroupsPage";
import { sumQuantities, validateDecision } from "./UsageDecisionForm";

const ORDER = "0195f0a0-0000-7000-8000-000000000001";
const GROUP = "0195f0a0-0000-7000-8000-0000000000a1";
const LENGTH = "0195f0a0-0000-7000-8000-0000000000b1";
const LOOK = "0195f0a0-0000-7000-8000-0000000000b2";
const GOOD = "0195f0a0-0000-7000-8000-0000000000c1";
const ITEM = "0195f0a0-0000-7000-8000-0000000000d1";
const SITE = "0195f0a0-0000-7000-8000-0000000000e1";
const WH = "0195f0a0-0000-7000-8000-0000000000e2";
const INST = "0195f0a0-0000-7000-8000-0000000000f1";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

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
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {quality.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

const group: TestGroup = {
  id: GROUP,
  code: "INCOMING-STEEL",
  version: 2,
  tests: [
    { testId: LENGTH, name: "Length", kind: "VARIABLE", required: true, decimals: 1, min: "9.9", max: "10.1", destructive: false },
    { testId: LOOK, name: "Appearance", kind: "ATTRIBUTE", required: true, decimals: 0, passingOutcomeIds: [GOOD], destructive: false },
  ],
};

const order: Order = {
  id: ORDER,
  trigger: "PURCHASE_RECEIPT",
  sourceRef: { type: "receipt_line", id: ITEM },
  itemId: ITEM,
  siteId: SITE,
  warehouseId: WH,
  dims: {},
  lotQty: "100.000000",
  sampleQty: "2.000000",
  uom: "EA",
  testGroupId: GROUP,
  testGroupVersion: 2,
  samplingMethod: "FIXED",
  severity: "NORMAL",
  codeLetter: "",
  acceptNo: 0,
  rejectNo: 1,
  status: "RESULTS_IN_PROGRESS",
  version: 3,
  createdAt: "2026-10-26T08:00:00Z",
  certificateRequired: "NONE",
};

function view(patch: Partial<OrderView> = {}): OrderView {
  return {
    ...order,
    testGroup: group,
    results: [],
    evaluation: { defectiveSamples: 0, passed: true, missingRequired: [LENGTH, LOOK] },
    certificateStatus: "NOT_REQUIRED",
    ...patch,
  };
}

const instrument: Instrument = { id: INST, code: "CAL-01", name: "Caliper", calibrationIntervalDays: 365, lastCalibratedAt: "2026-01-01T00:00:00Z", dueAt: "2027-01-01T00:00:00Z", calibrationStatus: "IN_CALIBRATION" };

describe("quality orders list", () => {
  it("shows a loading state, then the orders with status and a link to each", async () => {
    handler = (url) => (url.pathname === "/api/v1/quality/quality-orders" ? json(200, { items: [order] }) : undefined);
    const { container } = renderAt("/quality/orders");
    expect(screen.getByRole("table", { name: "Quality orders" })).toHaveAttribute("aria-busy", "true");
    const table = await findTable({ name: "Quality orders" });
    await waitFor(() => expect(within(table).getByText("Results in progress")).toBeInTheDocument());
    expect(within(table).getByRole("link", { name: "Open order 0195f0a0" })).toHaveAttribute("href", `/quality/orders/${ORDER}`);
    expect(within(table).getByText("100 EA")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters by status through the query string", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/quality/orders");
    await screen.findByText("No quality orders yet");
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "PENDING_DECISION" } });
    await screen.findByText("No quality orders match these filters");
    expect(calls.at(-1)?.url.search).toBe("?status=PENDING_DECISION");
  });

  it("ignores typed text that is not a site id and applies a pasted site id", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/quality/orders");
    await screen.findByText("No quality orders yet");
    const before = calls.length;
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "plant 1" } });
    fireEvent.click(screen.getByRole("button", { name: "Show orders" }));
    expect(calls.length).toBe(before);
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
    fireEvent.click(screen.getByRole("button", { name: "Show orders" }));
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe(`?siteId=${SITE}`));
  });

  it("shows the error with a retry", async () => {
    let fail = true;
    handler = () => (fail ? json(500, { code: "COM-0500", detail: "database unavailable" }) : json(200, { items: [order] }));
    renderAt("/quality/orders");
    expect(await screen.findByText("database unavailable")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("link", { name: "Open order 0195f0a0" })).toBeInTheDocument();
  });
});

describe("quality order detail", () => {
  function serve(v: OrderView) {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/quality/quality-orders/${ORDER}` && (init.method ?? "GET") === "GET") return json(200, v);
      if (url.pathname === "/api/v1/quality/instruments") return json(200, { items: [instrument] });
      return undefined;
    };
  }

  it("shows the lot, the results still needed and the results form; no decision yet", async () => {
    serve(view());
    const { container } = renderAt(`/quality/orders/${ORDER}`);
    expect(await screen.findByRole("heading", { name: "Quality order 0195f0a0" })).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Inspection summary" })).toHaveTextContent("Lot 100 EA");
    expect(screen.getByText("Results still needed")).toBeInTheDocument();
    expect(screen.getByText("No results yet")).toBeInTheDocument();
    expect(screen.getByRole("form", { name: "Record results" })).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Usage decision" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("records a batch for one sample with the chosen instrument", async () => {
    serve(view());
    renderAt(`/quality/orders/${ORDER}`);
    await screen.findByRole("form", { name: "Record results" });
    handler = ((prev) => (url: URL, init: RequestInit) => {
      if (url.pathname.endsWith("/results")) return json(201, view({ results: [] }));
      return prev(url, init);
    })(handler);
    fireEvent.change(screen.getByLabelText("Sample"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Instrument"), { target: { value: INST } });
    fireEvent.change(screen.getByLabelText("Length"), { target: { value: "10.3" } });
    expect(screen.getByText("Outside the limits: limits 9.9 to 10.1.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Appearance"), { target: { value: GOOD } });
    fireEvent.click(screen.getByRole("button", { name: "Record results" }));
    expect(await screen.findByText("Recorded 2 results for sample 2.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({
      results: [
        { testId: LENGTH, sampleNo: 2, retestNo: 0, value: "10.3", instrumentId: INST },
        { testId: LOOK, sampleNo: 2, retestNo: 0, outcomeId: GOOD, instrumentId: INST },
      ],
    });
    expect(new Headers(post?.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("refuses a sample number beyond the sample and shows the service refusal for an overdue instrument", async () => {
    serve(view());
    renderAt(`/quality/orders/${ORDER}`);
    await screen.findByRole("form", { name: "Record results" });
    fireEvent.change(screen.getByLabelText("Sample"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("Length"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Record results" }));
    expect(await screen.findByText("Enter a sample number from 1 to 2.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    handler = ((prev) => (url: URL, init: RequestInit) =>
      url.pathname.endsWith("/results") ? json(422, { code: "QM-0102", detail: "Instrument CAL-01 is overdue for calibration since 2026-10-01." }) : prev(url, init))(handler);
    fireEvent.change(screen.getByLabelText("Sample"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Record results" }));
    expect(await screen.findByText("Instrument CAL-01 is overdue for calibration since 2026-10-01.")).toBeInTheDocument();
  });

  it("posts a split usage decision with If-Match once results are complete", async () => {
    const pending = view({ status: "PENDING_DECISION", evaluation: { defectiveSamples: 1, passed: false, missingRequired: [] } });
    serve(pending);
    renderAt(`/quality/orders/${ORDER}`);
    expect(await screen.findByText("The lot fails")).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Usage decision" });
    handler = ((prev) => (url: URL, init: RequestInit) =>
      url.pathname.endsWith(":usage-decision") ? json(201, { order: pending, decision: {} }) : prev(url, init))(handler);
    fireEvent.change(within(form).getByLabelText("Decision"), { target: { value: "ACCEPT" } });
    fireEvent.change(within(form).getByLabelText("Quantity"), { target: { value: "90" } });
    fireEvent.click(within(form).getByRole("button", { name: "Add decision line" }));
    fireEvent.click(within(form).getByRole("button", { name: "Post usage decision" }));
    expect(await within(form).findAllByText("Enter a quantity above 0.")).toHaveLength(1);
    fireEvent.change(within(form).getAllByLabelText("Quantity")[1], { target: { value: "5" } });
    fireEvent.click(within(form).getByRole("button", { name: "Post usage decision" }));
    expect(await within(form).findByText("The lines add up to 95; they must equal the lot quantity 100.")).toBeInTheDocument();
    fireEvent.change(within(form).getAllByLabelText("Quantity")[1], { target: { value: "10" } });
    fireEvent.click(within(form).getByRole("button", { name: "Post usage decision" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith(":usage-decision"))).toBe(true));
    const post = calls.find((c) => c.url.pathname.endsWith(":usage-decision"));
    expect(post?.body).toEqual({ lines: [{ decision: "ACCEPT", quantity: "90" }, { decision: "REJECT", quantity: "10" }] });
    expect(new Headers(post?.init.headers).get("If-Match")).toBe('"3"');
  });

  it("explains a stale version on the decision", async () => {
    const pending = view({ status: "PENDING_DECISION", evaluation: { defectiveSamples: 0, passed: true, missingRequired: [] } });
    serve(pending);
    renderAt(`/quality/orders/${ORDER}`);
    const form = await screen.findByRole("form", { name: "Usage decision" });
    handler = ((prev) => (url: URL, init: RequestInit) =>
      url.pathname.endsWith(":usage-decision") ? json(412, { code: "conflict_version", detail: "current version is 4, not 3" }) : prev(url, init))(handler);
    fireEvent.click(within(form).getByRole("button", { name: "Post usage decision" }));
    expect(await within(form).findByText(/The order changed since you opened it/)).toBeInTheDocument();
  });

  it("shows a closed order's decision and no entry forms", async () => {
    serve(
      view({
        status: "CLOSED",
        evaluation: { defectiveSamples: 0, passed: true, missingRequired: [] },
        decision: { id: "d", orderId: ORDER, lines: [{ decision: "ACCEPT", quantity: "100" }], split: { acceptQty: "100", rejectQty: "0", deviationQty: "0" }, decidedBy: "qa-1", decidedAt: "2026-10-26T10:00:00Z" },
      }),
    );
    renderAt(`/quality/orders/${ORDER}`);
    expect(await screen.findByRole("list", { name: "Decision lines" })).toHaveTextContent("Accept: 100 EA");
    expect(screen.queryByRole("form", { name: "Record results" })).toBeNull();
    expect(screen.queryByRole("form", { name: "Usage decision" })).toBeNull();
  });

  it("shows a not-found order as an error with retry", async () => {
    handler = (url) => (url.pathname.includes("/quality-orders/") ? json(404, { code: "QM-0141", detail: "quality order not found" }) : json(200, { items: [] }));
    renderAt(`/quality/orders/${ORDER}`);
    expect(await screen.findByText("quality order not found")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("new quality order", () => {
  it("creates a manual AQL order and opens it", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/quality/test-groups") return json(200, { items: [group] });
      if (url.pathname === "/api/v1/quality/quality-orders" && init.method === "POST") return json(201, order);
      return undefined;
    };
    const { container } = renderAt("/quality/orders/new");
    await screen.findByRole("option", { name: "INCOMING-STEEL v2 (2 tests)" });
    expect(screen.queryByLabelText("Source document")).toBeNull();
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
    fireEvent.change(screen.getByLabelText("Warehouse"), { target: { value: WH } });
    fireEvent.change(screen.getByLabelText("Lot quantity"), { target: { value: "51" } });
    fireEvent.change(screen.getByLabelText("Test group"), { target: { value: GROUP } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create quality order" }));
    expect(await screen.findByRole("heading", { name: "Quality order" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({
      trigger: "MANUAL",
      sourceRef: { type: "manual", id: expect.stringMatching(/^[0-9a-f-]{36}$/) },
      itemId: ITEM,
      siteId: SITE,
      warehouseId: WH,
      lotQty: "51",
      uom: "EA",
      testGroupId: GROUP,
      samplingPlan: { method: "AQL", aql: "2.5", severity: "NORMAL", inspectionLevel: "II" },
    });
  });

  it("validates before sending and explains a trigger without an association", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/quality/test-groups") return json(200, { items: [group] });
      if (init.method === "POST") return json(422, { code: "QM-0100", detail: "no quality association matches this trigger" });
      return undefined;
    };
    renderAt("/quality/orders/new");
    fireEvent.click(screen.getByRole("button", { name: "Create quality order" }));
    expect(await screen.findByText("Choose the test group to inspect against.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    fireEvent.change(screen.getByLabelText("Trigger"), { target: { value: "PURCHASE_RECEIPT" } });
    expect(screen.queryByLabelText("Test group")).toBeNull();
    for (const label of ["Source document", "Item", "Site", "Warehouse"]) fireEvent.change(screen.getByLabelText(label), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Lot quantity"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Create quality order" }));
    expect(await screen.findByText(/No quality association matches this trigger, item, party and site/)).toBeInTheDocument();
  });
});

describe("setup pages", () => {
  it("lists test groups, with an empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/quality/test-groups");
    expect(await screen.findByText("No test groups yet")).toBeInTheDocument();
  });

  it("creates a test group version from the form", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, group) : json(200, { items: [group] }));
    const { container } = renderAt("/quality/test-groups/new");
    fireEvent.change(screen.getByLabelText("Group code"), { target: { value: "INCOMING-STEEL" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Length" } });
    fireEvent.change(screen.getByLabelText("Minimum"), { target: { value: "9.9" } });
    fireEvent.change(screen.getByLabelText("Maximum"), { target: { value: "10.1" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save test group" }));
    const table = await findTable({ name: "Test groups" });
    await waitFor(() => expect(within(table).getByText("INCOMING-STEEL")).toBeInTheDocument());
    const post = calls.find((c) => c.method === "POST")?.body as { code: string; tests: { name: string; min: string; max: string; decimals: number }[] };
    expect(post.code).toBe("INCOMING-STEEL");
    expect(post.tests[0]).toMatchObject({ name: "Length", kind: "VARIABLE", min: "9.9", max: "10.1", decimals: 2, required: true });
  });

  it("lists instruments with their calibration state", async () => {
    handler = () => json(200, { items: [instrument, { ...instrument, id: "x", code: "PH-02", calibrationStatus: "OVERDUE" }] });
    const { container } = renderAt("/quality/instruments");
    const table = await findTable({ name: "Instruments" });
    await waitFor(() => expect(within(table).getByText("Overdue")).toBeInTheDocument());
    expect(within(table).getByText("In calibration")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("records a calibration done today from the list", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(200, { ...instrument, code: "PH-02", dueAt: "2027-10-27T00:00:00Z" }) : json(200, { items: [{ ...instrument, code: "PH-02", calibrationStatus: "OVERDUE" }] });
    renderAt("/quality/instruments");
    fireEvent.click(await screen.findByRole("button", { name: "Calibrated today: PH-02" }));
    expect(await screen.findByText("PH-02 calibrated; next due 2027-10-27.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/quality/instruments/${INST}:calibrate`);
    expect(typeof (post?.body as { calibratedAt: string }).calibratedAt).toBe("string");
  });

  it("registers an instrument and maps a duplicate code to the field", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(409, { code: "QM-0151", detail: "an instrument with this code already exists" }) : undefined);
    renderAt("/quality/instruments/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "cal" } });
    fireEvent.click(screen.getByRole("button", { name: "Register instrument" }));
    expect(await screen.findByText("Use 2 to 32 capital letters, digits, _ or -, such as PH-01.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "CAL-01" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Caliper" } });
    fireEvent.change(screen.getByLabelText("Last calibrated"), { target: { value: "2026-10-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Register instrument" }));
    expect(await screen.findByText("An instrument with this code already exists.")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ code: "CAL-01", name: "Caliper", calibrationIntervalDays: 365, lastCalibratedAt: "2026-10-01T00:00:00Z" });
  });

  it("lists associations with their test group and sampling", async () => {
    handler = (url) =>
      url.pathname.endsWith("/associations")
        ? json(200, { items: [{ id: "a", trigger: "PURCHASE_RECEIPT", itemScope: "ITEM", itemRef: ITEM, partyScope: "ALL", siteScope: "ALL", sequence: 10, testGroupId: GROUP, samplingPlan: { method: "AQL", aql: "2.5", severity: "NORMAL", inspectionLevel: "II" } }] })
        : json(200, { items: [group] });
    renderAt("/quality/associations");
    const table = await findTable({ name: "Quality associations" });
    await waitFor(() => expect(within(table).getByText("INCOMING-STEEL v2")).toBeInTheDocument());
    expect(within(table).getByText("AQL 2.5, normal, level II")).toBeInTheDocument();
    expect(within(table).getByText("Item 0195f0a0")).toBeInTheDocument();
  });

  it("creates an association scoped to one item", async () => {
    handler = (url, init) => (init.method === "POST" ? json(201, {}) : json(200, { items: url.pathname.endsWith("/test-groups") ? [group] : [] }));
    const { container } = renderAt("/quality/associations/new");
    await screen.findByRole("option", { name: "INCOMING-STEEL v2" });
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: "ITEM" } });
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Test group"), { target: { value: GROUP } });
    fireEvent.change(screen.getByLabelText("Sampling method"), { target: { value: "PERCENT" } });
    fireEvent.change(screen.getByLabelText("Percentage"), { target: { value: "10" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save association" }));
    await findTable({ name: "Quality associations" });
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      trigger: "PURCHASE_RECEIPT",
      itemScope: "ITEM",
      itemRef: ITEM,
      partyScope: "ALL",
      siteScope: "ALL",
      sequence: 10,
      testGroupId: GROUP,
      samplingPlan: { method: "PERCENT", percent: "10" },
      certificateRequired: "NONE",
    });
  });

  it("sizes a sample in the calculator and shows plan errors from the service", async () => {
    handler = (_url, init) => {
      const body = JSON.parse(String(init.body));
      return body.lotQty === "400" ? json(200, { codeLetter: "H", sampleQty: "50", acceptNo: 3, rejectNo: 4, severity: "NORMAL", inspectionLevel: "II" }) : json(400, { code: "COM-0001", detail: "x", errors: { lotQty: "AQL sizing needs a whole lot of at least 2" } });
    };
    const { container } = renderAt("/quality/sampling");
    fireEvent.change(screen.getByLabelText("Lot quantity"), { target: { value: "400" } });
    fireEvent.click(screen.getByRole("button", { name: "Size sample" }));
    const draw = await screen.findByLabelText("Sample draw");
    expect(draw).toHaveTextContent("Sample quantity50");
    expect(draw).toHaveTextContent("Accept / reject3 / 4");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Lot quantity"), { target: { value: "1.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Size sample" }));
    expect(await screen.findByText("AQL sizing needs a whole lot of at least 2")).toBeInTheDocument();
  });
});

describe("form rules", () => {
  it("sums decimal quantities exactly", () => {
    expect(sumQuantities(["0.1", "0.2"])).toBe("0.3");
    expect(sumQuantities(["90", "10.000000"])).toBe("100");
  });

  it("requires a reason for a deviation and the lot total", () => {
    expect(validateDecision([{ decision: "ACCEPT_WITH_DEVIATION", quantity: "100", reason: "" }], "100.000000").errors["0.reason"]).toBeTruthy();
    expect(validateDecision([{ decision: "ACCEPT", quantity: "100", reason: "" }], "100.000000").input).toEqual([{ decision: "ACCEPT", quantity: "100" }]);
  });

  it("flags values outside a test's limits", () => {
    expect(outsideLimits(group.tests[0], "10.2")).toBe(true);
    expect(outsideLimits(group.tests[0], "10.1")).toBe(false);
    expect(outsideLimits(group.tests[0], "abc")).toBe(false);
  });

  it("checks test group lines like the service", () => {
    const bad = validateTestGroup("x", [{ name: "", kind: "ATTRIBUTE", required: true, destructive: false, decimals: "0", min: "", max: "", passingOutcomeIds: "pass" }]);
    expect(Object.keys(bad.errors).sort()).toEqual(["0.name", "0.passingOutcomeIds", "code"]);
    const minMax = validateTestGroup("GRP", [{ name: "L", kind: "VARIABLE", required: true, destructive: false, decimals: "1", min: "5", max: "1", passingOutcomeIds: "" }]);
    expect(minMax.errors["0.min"]).toBeTruthy();
  });

  it("needs an id for a scoped association", () => {
    const out = validateAssociation(
      { trigger: "PURCHASE_RECEIPT", itemScope: "ITEM", itemRef: "", partyScope: "ALL", partyRef: "", siteScope: "ALL", siteRef: "", sequence: "1", testGroupId: GROUP, certificateRequired: "NONE" },
      emptyPlan,
    );
    expect(out.errors.itemRef).toBeTruthy();
    expect(out.input).toBeUndefined();
  });
});
