import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { ComplianceEvaluation, ComplianceList, Composition, SpcResult, Study, TestGroup } from "./api/client";
import { validateComplianceList } from "./ComplianceListsPage";
import { validCAS } from "./format";
import { percent, validateComposition } from "./ItemCompliancePage";
import quality from "./module";
import { rulesByPoint } from "./SpcPage";
import { validateStudy } from "./StabilityStudiesPage";

const ITEM = "0195f0a0-0000-7000-8000-0000000000d1";
const PART = "0195f0a0-0000-7000-8000-0000000000d2";
const CASING = "0195f0a0-0000-7000-8000-0000000000d3";
const GROUP = "0195f0a0-0000-7000-8000-0000000000a1";
const TEST = "0195f0a0-0000-7000-8000-0000000000b1";
const STUDY = "0195f0a0-0000-7000-8000-000000000051";
const ORDER_A = "0195f0a0-0000-7000-8000-000000000001";
const ORDER_B = "0195f0a0-0000-7000-8000-000000000002";
const DEHP = "117-81-7";
const LEAD = "7439-92-1";

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

function header(call: { init: RequestInit }, name: string): string | null {
  return new Headers(call.init.headers).get(name);
}

const rohs: ComplianceList = {
  id: "0195f0a0-0000-7000-8000-0000000000c1",
  code: "ROHS",
  version: 2,
  name: "RoHS Annex II",
  kind: "RESTRICTED",
  blockMode: "BLOCK",
  regions: ["EU", "CN"],
  limits: [{ cas: LEAD, name: "Lead", thresholdPct: "0.1" }],
};

const group: TestGroup = {
  id: GROUP,
  code: "STAB-LT",
  version: 1,
  tests: [{ testId: TEST, name: "Assay", kind: "VARIABLE", required: true, decimals: 2, min: "95", max: "105", destructive: false }],
};

const study: Study = {
  id: STUDY,
  status: "PLANNED",
  version: 1,
  createdAt: "2026-01-20T09:00:00Z",
  itemId: ITEM,
  batchId: "B-1",
  protocol: "ICH Q1A(R2)",
  condition: "25C/60RH",
  startDate: "2026-01-31T00:00:00Z",
  testGroupId: GROUP,
  pullMonths: [0, 3],
  leadDays: 7,
  windowDays: 3,
  pulls: [
    { months: 0, pullDate: "2026-01-31T00:00:00Z", orderDate: "2026-01-24T00:00:00Z", testGroupId: GROUP, status: "PLANNED" },
    { months: 3, pullDate: "2026-04-30T00:00:00Z", orderDate: "2026-04-23T00:00:00Z", testGroupId: GROUP, status: "PLANNED" },
  ],
};

describe("compliance lists", () => {
  it("shows a loading state, then each version with its kind and substances", async () => {
    handler = (url) => (url.pathname === "/api/v1/quality/compliance-lists" ? json(200, { items: [rohs] }) : undefined);
    const { container } = renderAt("/quality/compliance/lists");
    expect(screen.getByRole("table", { name: "Compliance lists" })).toHaveAttribute("aria-busy", "true");
    const table = await findTable({ name: "Compliance lists" });
    await waitFor(() => expect(within(table).getByText("ROHS")).toBeInTheDocument());
    expect(within(table).getByText("v2")).toBeInTheDocument();
    expect(within(table).getByText("Block (QM-0122)")).toBeInTheDocument();
    expect(within(table).getByText("Lead > 0.1 %")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an empty state, and an error with a retry", async () => {
    let fail = true;
    handler = () => (fail ? json(500, { code: "COM-0500", detail: "database unavailable" }) : json(200, { items: [] }));
    renderAt("/quality/compliance/lists");
    expect(await screen.findByText("database unavailable")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No compliance lists yet")).toBeInTheDocument();
  });

  it("creates a list version with an Idempotency-Key and returns to the lists", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, rohs) : json(200, { items: [rohs] }));
    const { container } = renderAt("/quality/compliance/lists/new");
    fireEvent.change(screen.getByLabelText("List code"), { target: { value: "ROHS" } });
    fireEvent.change(screen.getByLabelText("Regions"), { target: { value: "EU, CN" } });
    fireEvent.change(screen.getByLabelText("CAS number"), { target: { value: LEAD } });
    fireEvent.change(screen.getByLabelText("Substance name"), { target: { value: "Lead" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save list version" }));
    await findTable({ name: "Compliance lists" });
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/quality/compliance-lists");
    expect(header(post!, "Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ code: "ROHS", name: "", kind: "RESTRICTED", blockMode: "BLOCK", regions: ["EU", "CN"], limits: [{ cas: LEAD, name: "Lead", thresholdPct: "0.1" }] });
  });

  it("refuses a CAS number with a wrong check digit without calling the service, and maps service field errors", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(400, { code: "COM-0001", detail: "request failed validation", errors: { "limits[0].thresholdPct": "must be above 0 and at most 100" } }) : undefined);
    renderAt("/quality/compliance/lists/new");
    fireEvent.change(screen.getByLabelText("List code"), { target: { value: "ROHS" } });
    fireEvent.change(screen.getByLabelText("Regions"), { target: { value: "EU" } });
    fireEvent.change(screen.getByLabelText("CAS number"), { target: { value: "7439-92-2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save list version" }));
    expect(await screen.findByText("Enter a CAS number with a valid check digit, such as 7439-92-1.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("CAS number"), { target: { value: LEAD } });
    fireEvent.click(screen.getByRole("button", { name: "Save list version" }));
    expect(await screen.findByText("must be above 0 and at most 100")).toBeInTheDocument();
  });
});

describe("item compliance", () => {
  const composition: Composition = { itemId: ITEM, version: 3, direct: {}, parts: [{ itemId: PART, massFraction: "0.050000" }, { itemId: CASING, massFraction: "0.9" }] };
  const evaluation: ComplianceEvaluation = {
    itemId: ITEM,
    region: "EU",
    compositionVersion: 3,
    blocked: false,
    undeclaredItems: [CASING],
    results: [
      { listCode: "REACH-SVHC", listVersion: 1, listKind: "REPORTING", region: "EU", applies: true, restricted: false, code: "QM-0121", severity: "warning", version: 1, hits: [{ cas: DEHP, name: "DEHP", percentWw: "0.12", thresholdPct: "0.1" }] },
      { listCode: "ROHS", listVersion: 2, listKind: "RESTRICTED", region: "EU", applies: true, restricted: false, version: 1, hits: [] },
    ],
  };

  it("refuses a malformed item id, then opens the item", async () => {
    handler = () => json(404, { code: "QM-0164", detail: "no composition is recorded for this item" });
    renderAt("/quality/compliance/items");
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: "widget" } });
    fireEvent.click(screen.getByRole("button", { name: "Open item" }));
    expect(await screen.findByText("Enter the item id (a UUID).")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: ITEM } });
    fireEvent.click(screen.getByRole("button", { name: "Open item" }));
    expect(await screen.findByText("No composition declared yet")).toBeInTheDocument();
  });

  it("declares the first composition of an item without one", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(201, { itemId: ITEM, version: 1, direct: { [DEHP]: "0.024" }, parts: [] }) : json(404, { code: "QM-0164", detail: "no composition is recorded for this item" });
    const { container } = renderAt(`/quality/compliance/items/${ITEM}`);
    expect(await screen.findByText("No composition declared yet")).toBeInTheDocument();
    fireEvent.change(await screen.findByLabelText("CAS number"), { target: { value: DEHP } });
    fireEvent.change(screen.getByLabelText("Mass fraction"), { target: { value: "0.024" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save composition" }));
    expect(await screen.findByText("Composition version 1 saved. Evaluate again to record new results.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/quality/items/${ITEM}/composition`);
    expect(header(post!, "Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ direct: { [DEHP]: "0.024" }, parts: [] });
    expect(screen.getByText("0.024 (2.4 % w/w)")).toBeInTheDocument();
  });

  it("shows the composition and evaluates it for a region, with the hits and the undeclared components", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":evaluate-compliance")) return json(200, evaluation);
      if (url.pathname.endsWith("/composition")) return json(200, composition);
      return undefined;
    };
    const { container } = renderAt(`/quality/compliance/items/${ITEM}`);
    const parts = await findTable({ name: "Components" });
    expect(within(parts).getByRole("link", { name: CASING })).toHaveAttribute("href", `/quality/compliance/items/${CASING}`);
    fireEvent.click(screen.getByRole("button", { name: "Evaluate" }));
    const results = await findTable({ name: "Compliance results" });
    expect(within(results).getByText("QM-0121 warning")).toBeInTheDocument();
    expect(within(results).getByText("DEHP: 0.12 % > 0.1 %")).toBeInTheDocument();
    expect(within(results).getByText("Below every threshold")).toBeInTheDocument();
    expect(screen.getByText("Not blocked in EU (composition v3).")).toBeInTheDocument();
    expect(screen.getByText("Components without a composition")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ region: "EU" });
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a blocked item and refuses a malformed region", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(200, { ...evaluation, blocked: true, undeclaredItems: [] });
      if (url.pathname.endsWith("/composition")) return json(200, composition);
      return undefined;
    };
    renderAt(`/quality/compliance/items/${ITEM}`);
    await findTable({ name: "Components" });
    fireEvent.change(screen.getByLabelText("Destination region"), { target: { value: "europe" } });
    fireEvent.click(screen.getByRole("button", { name: "Evaluate" }));
    expect(await screen.findByText("Enter a region code in capitals, such as EU, DE or US-CA.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Destination region"), { target: { value: "EU" } });
    fireEvent.click(screen.getByRole("button", { name: "Evaluate" }));
    expect(await screen.findByText("Restricted in EU")).toBeInTheDocument();
  });

  it("shows a load failure with a retry", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "database unavailable" });
    renderAt(`/quality/compliance/items/${ITEM}`);
    expect(await screen.findByText("database unavailable")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("stability studies", () => {
  it("lists studies with the next pull and filters by status", async () => {
    handler = (url) => (url.pathname === "/api/v1/quality/stability-studies" ? json(200, { items: url.search ? [] : [study] }) : undefined);
    const { container } = renderAt("/quality/stability");
    const table = await findTable({ name: "Stability studies" });
    await waitFor(() => expect(within(table).getByRole("link", { name: "Open study 0195f0a0" })).toHaveAttribute("href", `/quality/stability/${STUDY}`));
    expect(within(table).getByText("0 months, 2026-01-31")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "ACTIVE" } });
    expect(await screen.findByText("No studies with this status")).toBeInTheDocument();
    expect(calls.at(-1)?.url.search).toBe("?status=ACTIVE");
  });

  it("plans a study with the test group and opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, study);
      if (url.pathname === "/api/v1/quality/test-groups") return json(200, { items: [group] });
      if (url.pathname === `/api/v1/quality/stability-studies/${STUDY}`) return json(200, study);
      return undefined;
    };
    const { container } = renderAt("/quality/stability/new");
    await screen.findByRole("option", { name: "STAB-LT v1" });
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Batch"), { target: { value: "B-1" } });
    fireEvent.change(screen.getByLabelText("Start date"), { target: { value: "2026-01-31" } });
    fireEvent.change(screen.getByLabelText("Test group"), { target: { value: GROUP } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Plan study" }));
    const schedule = await findTable({ name: "Pull schedule" });
    expect(within(schedule).getByText("2026-04-30")).toBeInTheDocument();
    expect(within(schedule).getByText("2026-04-23")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(header(post!, "Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({
      itemId: ITEM,
      batchId: "B-1",
      protocol: "ICH Q1A(R2)",
      condition: "25C/60RH",
      startDate: "2026-01-31",
      testGroupId: GROUP,
      pullMonths: [0, 3, 6, 9, 12, 18, 24, 36],
      leadDays: 7,
      windowDays: 3,
    });
  });

  it("maps a missing test group from the service to its field", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(404, { code: "QM-0140", detail: "test group not found" });
      if (url.pathname === "/api/v1/quality/test-groups") return json(200, { items: [group] });
      return undefined;
    };
    renderAt("/quality/stability/new");
    await screen.findByRole("option", { name: "STAB-LT v1" });
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Batch"), { target: { value: "B-1" } });
    fireEvent.change(screen.getByLabelText("Start date"), { target: { value: "2026-01-31" } });
    fireEvent.change(screen.getByLabelText("Test group"), { target: { value: GROUP } });
    fireEvent.click(screen.getByRole("button", { name: "Plan study" }));
    expect(await screen.findByText("This test group no longer exists.")).toBeInTheDocument();
  });

  it("shows a study that cannot be loaded as an error with a retry", async () => {
    handler = () => json(404, { code: "QM-0161", detail: "stability study not found" });
    renderAt(`/quality/stability/${STUDY}`);
    expect(await screen.findByText("stability study not found")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("SPC charts", () => {
  const xbar: SpcResult = {
    chart: "XBAR_R",
    observations: 8,
    orderIds: [ORDER_A, ORDER_B],
    skippedSubgroups: 1,
    xbarR: {
      subgroupSize: 4,
      xbar: [10.025, 12.5],
      range: [0.4, 0.3],
      xbarLimits: { cl: 10.1, ucl: 10.4, lcl: 9.8 },
      rLimits: { cl: 0.35, ucl: 0.8, lcl: 0 },
      sigma: 0.15,
      violations: [{ chart: "XBAR", index: 1, rule: 1 }],
    },
  };

  it("asks for an item and test before charting", async () => {
    handler = () => json(200, { items: [group] });
    const { container } = renderAt("/quality/spc");
    expect(await screen.findByText("Choose an item and a test")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show chart" }));
    expect(await screen.findByText("Enter the item id (a UUID).")).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname === "/api/v1/quality/spc")).toBe(false);
    expect(pageViolations(container)).toEqual([]);
  });

  it("charts X-bar and R from the chosen test and flags the rule violations", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/quality/test-groups") return json(200, { items: [group] });
      if (url.pathname === "/api/v1/quality/spc") return json(200, xbar);
      return undefined;
    };
    const { container } = renderAt("/quality/spc");
    await screen.findByRole("option", { name: "STAB-LT v1" });
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Test group"), { target: { value: GROUP } });
    fireEvent.change(screen.getByLabelText("Test"), { target: { value: TEST } });
    fireEvent.click(screen.getByRole("button", { name: "Show chart" }));
    const table = await findTable({ name: "Subgroups" });
    expect(within(table).getByText("Rule 1: a point beyond 3σ")).toBeInTheDocument();
    expect(within(table).getByRole("link", { name: "Open the order of point 2" })).toHaveAttribute("href", `/quality/orders/${ORDER_B}`);
    expect(screen.getByRole("img", { name: "X-bar chart: 2 points, centre 10.1, 1 flagged." })).toBeInTheDocument();
    const spc = calls.find((c) => c.url.pathname === "/api/v1/quality/spc");
    expect(spc?.url.searchParams.get("itemId")).toBe(ITEM);
    expect(spc?.url.searchParams.get("testId")).toBe(TEST);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an empty state when no results exist, and the service refusal otherwise", async () => {
    let reply: Reply = json(200, { chart: "NONE", observations: 0, orderIds: [], skippedSubgroups: 0 });
    handler = (url) => (url.pathname === "/api/v1/quality/spc" ? reply : json(200, { items: [] }));
    const { unmount } = renderAt(`/quality/spc?itemId=${ITEM}&testId=${TEST}`);
    expect(await screen.findByText("No results yet for this item and test")).toBeInTheDocument();
    unmount();
    reply = json(422, { code: "QM-0166", detail: "an X-bar/R chart needs subgroups of 2 to 25 samples; these orders have 1" });
    renderAt(`/quality/spc?itemId=${ITEM}&testId=${TEST}`);
    expect(await screen.findByText("an X-bar/R chart needs subgroups of 2 to 25 samples; these orders have 1")).toBeInTheDocument();
  });
});

describe("slice 2 form rules", () => {
  it("checks CAS check digits like the service", () => {
    expect(validCAS(LEAD)).toBe(true);
    expect(validCAS(DEHP)).toBe(true);
    expect(validCAS("50-00-0")).toBe(true);
    expect(validCAS("7439-92-2")).toBe(false);
    expect(validCAS("lead")).toBe(false);
  });

  it("turns fractions into exact percentages", () => {
    expect(percent("0.0024")).toBe("0.24");
    expect(percent("0.5")).toBe("50");
    expect(percent("1")).toBe("100");
    expect(percent("0.000001")).toBe("0.0001");
  });

  it("refuses duplicate substances and an item listed as its own part", () => {
    const { errors } = validateComposition(ITEM, [{ id: LEAD, fraction: "0.1" }, { id: LEAD, fraction: "0.1" }], [{ id: ITEM, fraction: "0.5" }]);
    expect(errors["s1.id"]).toBe("This substance is already declared.");
    expect(errors["p0.id"]).toBe("An item cannot be a part of itself.");
    expect(validateComposition(ITEM, [], []).errors.form).toBe("Declare at least one substance or component.");
  });

  it("needs ascending pull months and unique regions", () => {
    const base = { itemId: ITEM, batchId: "B", protocol: "P", condition: "25C/60RH", startDate: "2026-01-31", testGroupId: GROUP, pullMonths: "0, 6, 3", leadDays: "7", windowDays: "3" };
    expect(validateStudy(base).errors.pullMonths).toBeDefined();
    expect(validateStudy({ ...base, pullMonths: "0, 3, 6" }).input?.pullMonths).toEqual([0, 3, 6]);
    const list = { code: "ROHS", name: "", kind: "RESTRICTED" as const, blockMode: "BLOCK" as const, regions: "EU, EU", limits: [{ cas: LEAD, name: "", thresholdPct: "0.1" }] };
    expect(validateComplianceList(list).errors.regions).toBeDefined();
  });

  it("groups rule violations by point for one chart", () => {
    const got = rulesByPoint([{ chart: "XBAR", index: 3, rule: 2 }, { chart: "XBAR", index: 3, rule: 1 }, { chart: "R", index: 3, rule: 1 }], "XBAR");
    expect(got.get(3)).toEqual([1, 2]);
    expect(got.size).toBe(1);
  });
});
