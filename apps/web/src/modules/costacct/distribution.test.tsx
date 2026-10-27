import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CostElement, CostObject, TopDownRun } from "./api/client";
import { CostObjectDetailPage } from "./CostObjectsPage";
import { previewTopDown, settlementError, validateTopDown } from "./logic";
import { NewTopDownRunPage, TopDownRunDetailPage, TopDownRunsPage } from "./TopDownRunsPage";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "11111111-1111-4111-8111-111111111111";
const ENT = "22222222-2222-4222-8222-222222222222";
const N = "33333333-3333-4333-8333-333333333333";
const S = "44444444-4444-4444-8444-444444444444";
const W = "55555555-5555-4555-8555-555555555555";
const IO = "66666666-6666-4666-8666-666666666666";
const FRT = "77777777-7777-4777-8777-777777777777";
const SET = "88888888-8888-4888-8888-888888888888";
const RUN = "99999999-9999-4999-8999-999999999999";

const objects: CostObject[] = [
  { id: ENT, legalEntityId: LE, kind: "CostCenter", code: "ENTITY", name: "Entity freight" },
  { id: N, legalEntityId: LE, kind: "Product", code: "NORTH", name: "North" },
  { id: S, legalEntityId: LE, kind: "Product", code: "SOUTH", name: "South" },
  { id: W, legalEntityId: LE, kind: "Product", code: "WEST", name: "West" },
  { id: IO, legalEntityId: LE, kind: "InternalOrder", code: "IO-SHOW", name: "Trade show" },
];
const elements: CostElement[] = [
  { id: FRT, legalEntityId: LE, kind: "Primary", code: "FREIGHT", name: "Freight", mainAccountFrom: "610000", mainAccountTo: "619999" },
  { id: SET, legalEntityId: LE, kind: "Secondary", code: "SETTLE", name: "Settlement" },
];
const run: TopDownRun = {
  id: RUN,
  legalEntityId: LE,
  period: "2026-09",
  referencePeriod: "2026-08",
  currency: "USD",
  sourceCostObjectId: ENT,
  costElementId: FRT,
  amount: "12000",
  shares: [
    { segmentId: N, basis: "300000", amount: "7200" },
    { segmentId: S, basis: "150000", amount: "3600" },
    { segmentId: W, basis: "50000", amount: "1200" },
  ],
  excluded: [],
  status: "Completed",
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

function calls(fetchMock: ReturnType<typeof vi.fn>, method: string, fragment: string) {
  return (fetchMock.mock.calls as unknown as [string, RequestInit][]).filter(([url, init]) => (init?.method ?? "GET") === method && url.includes(fragment));
}

function at(path: string) {
  return render(
    createElement(
      MemoryRouter,
      { initialEntries: [path] },
      createElement(
        Routes,
        null,
        createElement(Route, { path: "/costacct/cost-objects/:id", element: createElement(CostObjectDetailPage) }),
        createElement(Route, { path: "/costacct/top-down-runs", element: createElement(TopDownRunsPage) }),
        createElement(Route, { path: "/costacct/top-down-runs/new", element: createElement(NewTopDownRunPage) }),
        createElement(Route, { path: "/costacct/top-down-runs/:id", element: createElement(TopDownRunDetailPage) }),
      ),
    ),
  );
}

const lists = (extra: Record<string, Handler> = {}) =>
  stubRoutes({
    "GET /api/v1/controlling/cost-objects": () => json(200, { items: objects }),
    "GET /api/v1/controlling/cost-elements": () => json(200, { items: elements }),
    "GET /api/v1/controlling/top-down-runs": () => json(200, { items: [run] }),
    [`GET /api/v1/controlling/top-down-runs/${RUN}`]: () => json(200, run),
    ...extra,
  });

afterEach(() => vi.unstubAllGlobals());

describe("top-down logic", () => {
  it("matches the worked example AC-38.11", () => {
    expect(previewTopDown("12000.00", [{ id: "n", basis: "300000" }, { id: "s", basis: "150000" }, { id: "w", basis: "50000" }])).toEqual([
      { id: "n", amount: "7200.00" },
      { id: "s", amount: "3600.00" },
      { id: "w", amount: "1200.00" },
    ]);
  });

  it("gives the rounding residual to the largest basis and nothing to a zero basis (BR-38.13)", () => {
    expect(previewTopDown("100", [{ id: "a", basis: "1" }, { id: "b", basis: "1" }, { id: "c", basis: "1" }, { id: "z", basis: "0" }])).toEqual([
      { id: "a", amount: "33.34" },
      { id: "b", amount: "33.33" },
      { id: "c", amount: "33.33" },
      { id: "z", amount: "0.00" },
    ]);
    expect(previewTopDown("10", [{ id: "a", basis: "1" }, { id: "b", basis: "1" }, { id: "c", basis: "4" }])?.map((s) => s.amount)).toEqual(["1.67", "1.67", "6.66"]);
    expect(previewTopDown("10", [{ id: "a", basis: "0" }])).toBeNull();
  });

  it("validates the run form and the settlement cap", () => {
    const form = { legalEntityId: LE, period: "2026-09", referencePeriod: "", currency: "USD", sourceCostObjectId: ENT, costElementId: FRT, amount: "10.005" };
    expect(validateTopDown(form, [{ id: N, basis: "0" }])).toEqual(["Enter a positive amount with at most 2 decimal places.", "At least one segment needs a positive basis."]);
    expect(validateTopDown({ ...form, amount: "10" }, [{ id: N, basis: "1" }])).toEqual([]);
    expect(settlementError("500.01", 5000000n)).toBe("The settlement exceeds the collected cost of 500.00.");
    expect(settlementError("500", 5000000n)).toBeNull();
    expect(settlementError("-1", 5000000n)).not.toBeNull();
  });
});

describe("Top-down runs", () => {
  it("lists runs and shows a run's shares summing to the amount", async () => {
    const { container } = (lists(), at("/costacct/top-down-runs"));
    const table = await findTable({ name: "Top-down runs" });
    expect(await within(table).findByText("12000.00")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "2026-09" }));
    expect(await screen.findByRole("heading", { name: "Top-down run 2026-09" })).toBeInTheDocument();
    expect(screen.getByText("Distributed to the cent")).toBeInTheDocument();
    const shares = screen.getByRole("table", { name: "Shares" });
    expect(await within(shares).findByRole("link", { name: "NORTH" })).toHaveAttribute("href", `/costacct/cost-objects/${N}`);
    expect(within(shares).getByText("7200.00")).toBeInTheDocument();
  });

  it("shows the empty state and a load error with a retry", async () => {
    let fail = true;
    stubRoutes({ "GET /api/v1/controlling/top-down-runs": () => (fail ? json(503, { title: "down", code: "COM-0500" }) : json(200, { items: [] })) });
    at("/costacct/top-down-runs");
    expect(await findAlert()).toHaveTextContent("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No top-down runs yet")).toBeInTheDocument();
  });

  it("previews the shares, posts the run and shows a refusal for more than booked", async () => {
    let refuse = true;
    const fetchMock = lists({
      "POST /api/v1/controlling/top-down-runs": () =>
        refuse ? json(422, { title: "Unprocessable", code: "CST-0012", detail: "amount 12000.00 exceeds the 11000.00 booked on the source" }) : json(201, run),
    });
    const { container } = at("/costacct/top-down-runs/new");
    fireEvent.click(screen.getByRole("button", { name: "Run distribution" }));
    expect(await screen.findByText("Choose at least one segment.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", "top-down-runs")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-09" } });
    fireEvent.change(screen.getByLabelText("Reference period"), { target: { value: "2026-08" } });
    await screen.findByRole("option", { name: "FREIGHT Freight" });
    fireEvent.change(screen.getByLabelText("Source cost object"), { target: { value: ENT } });
    fireEvent.change(screen.getByLabelText("Cost element"), { target: { value: FRT } });
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "12000" } });
    expect(screen.queryByLabelText("ENTITY Entity freight")).not.toBeInTheDocument();
    for (const [label, basis] of [
      ["NORTH North", "300000"],
      ["SOUTH South", "150000"],
      ["WEST West", "50000"],
    ] as const) {
      fireEvent.click(screen.getByLabelText(label));
      fireEvent.change(screen.getByLabelText(`Basis of ${label.split(" ")[0]}`), { target: { value: basis } });
    }
    const preview = screen.getByRole("table", { name: "Segment basis" });
    expect(within(preview).getByText("7200.00")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Run distribution" }));
    expect(await screen.findByText(/CST-0012/)).toBeInTheDocument();
    refuse = false;
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "11000" } });
    fireEvent.click(screen.getByRole("button", { name: "Run distribution" }));
    expect(await screen.findByRole("heading", { name: "Top-down run 2026-09" })).toBeInTheDocument();
    const posts = calls(fetchMock, "POST", "top-down-runs");
    expect(JSON.parse(posts[1][1].body as string)).toEqual({
      legalEntityId: LE,
      period: "2026-09",
      referencePeriod: "2026-08",
      currency: "USD",
      sourceCostObjectId: ENT,
      costElementId: FRT,
      amount: "11000",
      segments: [
        { costObjectId: N, basis: "300000" },
        { costObjectId: S, basis: "150000" },
        { costObjectId: W, basis: "50000" },
      ],
    });
    expect((posts[0][1].headers as Record<string, string>)["Idempotency-Key"]).not.toBe((posts[1][1].headers as Record<string, string>)["Idempotency-Key"]);
  });
});

describe("Internal order settlement", () => {
  it("settles part of the collected cost, refuses more than collected and lists settlements", async () => {
    let settled = false;
    const fetchMock = lists({
      [`GET /api/v1/controlling/cost-objects/${IO}`]: () => json(200, objects[4]),
      [`GET /api/v1/controlling/cost-objects/${IO}/entries`]: () =>
        json(200, {
          items: [
            { id: "e1", costElementId: FRT, sourceId: "j1", period: "2026-09", currency: "USD", amount: "500", behavior: "Unclassified", origin: "Journal" },
            ...(settled
              ? [{ id: "e2", costElementId: SET, sourceId: "s1", settlementId: "s1", period: "2026-09", currency: "USD", amount: "-200", behavior: "Unclassified", origin: "Allocation" }]
              : []),
          ],
        }),
      [`GET /api/v1/controlling/internal-orders/${IO}/settlements`]: () =>
        json(200, {
          items: settled
            ? [{ id: "s1", legalEntityId: LE, orderId: IO, period: "2026-09", currency: "USD", receiverCostObjectId: N, secondaryElementId: SET, amount: "200", collectedBefore: "500", remaining: "300", createdAt: "2026-10-01T09:00:00Z" }]
            : [],
        }),
      [`POST /api/v1/controlling/internal-orders/${IO}:settle`]: () => {
        settled = true;
        return json(201, { id: "s1", legalEntityId: LE, orderId: IO, period: "2026-09", currency: "USD", receiverCostObjectId: N, secondaryElementId: SET, amount: "200", collectedBefore: "500", remaining: "300", createdAt: "2026-10-01T09:00:00Z" });
      },
    });
    const { container } = at(`/costacct/cost-objects/${IO}`);
    expect(await screen.findByRole("heading", { name: "IO-SHOW Trade show" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-09" } });
    expect(await screen.findByText(/Collected in 2026-09: 500.00 USD/)).toBeInTheDocument();
    expect(await screen.findByText("No settlements in this period")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    await screen.findByRole("option", { name: "SETTLE Settlement" });
    fireEvent.change(screen.getByLabelText("Receiver"), { target: { value: N } });
    fireEvent.change(screen.getByLabelText("Settlement element"), { target: { value: SET } });
    fireEvent.change(screen.getByLabelText("Settlement amount"), { target: { value: "500.01" } });
    fireEvent.click(screen.getByRole("button", { name: "Settle" }));
    expect(await screen.findByText("The settlement exceeds the collected cost of 500.00.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", ":settle")).toHaveLength(0);
    expect(within(screen.getByLabelText("Receiver")).queryByRole("option", { name: "IO-SHOW Trade show" })).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Settlement amount"), { target: { value: "200" } });
    fireEvent.click(screen.getByRole("button", { name: "Settle" }));
    expect(await screen.findByText("Settled 200.00 USD to NORTH; 300.00 is left on the order.")).toBeInTheDocument();
    expect(JSON.parse(calls(fetchMock, "POST", ":settle")[0][1].body as string)).toEqual({
      period: "2026-09",
      currency: "USD",
      receiverCostObjectId: N,
      secondaryElementId: SET,
      amount: "200",
    });
    const history = await findTable({ name: "Settlements" });
    expect(await within(history).findByText("300.00")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText(/Collected in 2026-09: 300.00 USD/)).toBeInTheDocument());
  });

  it("offers no settlement on a cost center", async () => {
    lists({
      [`GET /api/v1/controlling/cost-objects/${N}`]: () => json(200, objects[1]),
      [`GET /api/v1/controlling/cost-objects/${N}/entries`]: () => json(200, { items: [] }),
    });
    at(`/costacct/cost-objects/${N}`);
    expect(await screen.findByRole("heading", { name: "NORTH North" })).toBeInTheDocument();
    expect(await screen.findByText("No cost entries in this period")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Settle" })).not.toBeInTheDocument();
  });
});
