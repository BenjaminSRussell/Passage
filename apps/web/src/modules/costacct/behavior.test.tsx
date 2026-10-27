import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { BehaviorRule, CostElement } from "./api/client";
import { BehaviorRulesPage, CreateBehaviorRulePage } from "./BehaviorRulesPage";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "11111111-1111-4111-8111-111111111111";
const PRI = "66666666-6666-4666-8666-666666666666";
const RULE = "99999999-9999-4999-8999-999999999999";

const elements: CostElement[] = [
  { id: PRI, legalEntityId: LE, kind: "Primary", code: "PR-700", name: "Rent", mainAccountFrom: "700000", mainAccountTo: "709999", defaultBehavior: "Variable" },
];
const rule: BehaviorRule = {
  id: RULE,
  legalEntityId: LE,
  costElementId: PRI,
  objectKind: "CostCenter",
  behavior: "Fixed",
  effectiveFrom: "2026-09-01",
  createdAt: "2026-10-01T08:00:00Z",
};

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });

function stubRoutes(routes: Record<string, Handler>) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    const key = Object.keys(routes)
      .filter((k) => {
        const [m, prefix] = k.split(" ");
        return m === method && url.startsWith(prefix);
      })
      .sort((a, b) => b.length - a.length)[0];
    if (!key) return json(500, { title: "unrouted", code: "TEST-0000", detail: `${method} ${url}` });
    return routes[key](url, init);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function posts(fetchMock: ReturnType<typeof vi.fn>) {
  return (fetchMock.mock.calls as unknown as [string, RequestInit][]).filter(([url, init]) => init?.method === "POST" && url.includes("cost-behavior-rules"));
}

function at(path: string) {
  return render(
    createElement(
      MemoryRouter,
      { initialEntries: [path] },
      createElement(
        Routes,
        null,
        createElement(Route, { path: "/costacct/behavior-rules", element: createElement(BehaviorRulesPage) }),
        createElement(Route, { path: "/costacct/behavior-rules/new", element: createElement(CreateBehaviorRulePage) }),
      ),
    ),
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("BehaviorRulesPage (FR-38.16)", () => {
  it("lists rules with the element, kind and class", async () => {
    stubRoutes({
      "GET /api/v1/controlling/cost-behavior-rules": () => json(200, { items: [rule, { ...rule, id: "r2", objectKind: undefined, behavior: "Variable", effectiveFrom: "2026-01-01" }] }),
      "GET /api/v1/controlling/cost-elements": () => json(200, { items: elements }),
    });
    const { container } = at("/costacct/behavior-rules");
    const table = await screen.findByRole("table", { name: "Cost behavior rules" });
    expect(await within(table).findAllByText("PR-700 Rent")).toHaveLength(2);
    expect(within(table).getByText("Cost center")).toBeInTheDocument();
    expect(within(table).getByText("Every kind")).toBeInTheDocument();
    expect(within(table).getByText("Fixed")).toBeInTheDocument();
    expect(within(table).getByText("2026-09-01")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters by legal entity", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/controlling/cost-behavior-rules": () => json(200, { items: [] }),
      "GET /api/v1/controlling/cost-elements": () => json(200, { items: [] }),
    });
    at("/costacct/behavior-rules");
    expect(await screen.findByText("No behavior rules yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Legal entity/), { target: { value: LE } });
    await waitFor(() =>
      expect((fetchMock.mock.calls as unknown as [string][]).some(([url]) => url === `/api/v1/controlling/cost-behavior-rules?legalEntityId=${LE}`)).toBe(true),
    );
  });

  it("shows a load error with a retry", async () => {
    let fail = true;
    stubRoutes({
      "GET /api/v1/controlling/cost-behavior-rules": () => (fail ? json(503, { title: "Unavailable", code: "COM-0503", detail: "down" }) : json(200, { items: [rule] })),
      "GET /api/v1/controlling/cost-elements": () => json(200, { items: elements }),
    });
    at("/costacct/behavior-rules");
    expect(await screen.findByText("Could not load behavior rules")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("table", { name: "Cost behavior rules" })).toBeInTheDocument();
  });
});

describe("CreateBehaviorRulePage (FR-38.16)", () => {
  it("validates, then saves a rule for one kind with an Idempotency-Key", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/controlling/cost-elements": () => json(200, { items: elements }),
      "GET /api/v1/controlling/cost-behavior-rules": () => json(200, { items: [rule] }),
      "POST /api/v1/controlling/cost-behavior-rules": () => json(201, rule),
    });
    const { container } = at("/costacct/behavior-rules/new");
    fireEvent.click(screen.getByRole("button", { name: "Save behavior rule" }));
    expect(await screen.findByText("Choose the cost element the rule classifies.")).toBeInTheDocument();
    expect(screen.getByText("Enter the first day the rule applies, as YYYY-MM-DD.")).toBeInTheDocument();
    expect(posts(fetchMock)).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    await screen.findByRole("option", { name: "PR-700 Rent" });
    fireEvent.change(screen.getByLabelText("Cost element"), { target: { value: PRI } });
    fireEvent.change(screen.getByLabelText("Cost object kind"), { target: { value: "CostCenter" } });
    fireEvent.change(screen.getByLabelText("Behavior"), { target: { value: "Fixed" } });
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2026-02-30" } });
    fireEvent.click(screen.getByRole("button", { name: "Save behavior rule" }));
    expect(await screen.findByText("Enter the first day the rule applies, as YYYY-MM-DD.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2026-09-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Save behavior rule" }));
    expect(await screen.findByRole("table", { name: "Cost behavior rules" })).toBeInTheDocument();
    const [, init] = posts(fetchMock)[0];
    expect(JSON.parse(init.body as string)).toEqual({ legalEntityId: LE, costElementId: PRI, objectKind: "CostCenter", behavior: "Fixed", effectiveFrom: "2026-09-01" });
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
  });

  it("omits the kind for a rule on every kind and shows a duplicate refusal", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/controlling/cost-elements": () => json(200, { items: elements }),
      "POST /api/v1/controlling/cost-behavior-rules": () =>
        json(409, { title: "Conflict", status: 409, code: "CST-0020", detail: "a behavior rule already exists for this element, object kind and date" }),
    });
    at("/costacct/behavior-rules/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    await screen.findByRole("option", { name: "PR-700 Rent" });
    fireEvent.change(screen.getByLabelText("Cost element"), { target: { value: PRI } });
    fireEvent.change(screen.getByLabelText("Behavior"), { target: { value: "Variable" } });
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2026-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Save behavior rule" }));
    expect(await screen.findByText("CST-0020: a behavior rule already exists for this element, object kind and date")).toBeInTheDocument();
    expect(JSON.parse(posts(fetchMock)[0][1].body as string)).toEqual({ legalEntityId: LE, costElementId: PRI, behavior: "Variable", effectiveFrom: "2026-01-01" });
  });
});
