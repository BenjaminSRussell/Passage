import { findTable } from "../../test-utils";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Scenario } from "./api/client";
import planning from "./module";
import { draftOf, validateScenario } from "./ScenarioPages";

const ITEM = "00000000-0000-0000-0000-0000000000a1";
const SITE = "00000000-0000-0000-0000-0000000000b1";

const up20: Scenario = {
  id: "00000000-0000-0000-0000-0000000000c1",
  code: "UP20",
  version: 1,
  planCode: "MAIN",
  name: "Demand +20%",
  demandFactor: "1.2",
  itemFactors: [],
  addedDemand: [],
  leadTimeOverrides: [{ itemId: ITEM, leadTimeDays: 4 }],
  active: true,
};
const off: Scenario = { ...up20, id: "00000000-0000-0000-0000-0000000000c2", code: "OFF", name: "Parked", active: false };

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

type Handler = [match: string, respond: (url: string, init?: RequestInit) => Response];

function stubApi(...routes: Handler[]) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    for (const [match, respond] of routes) {
      if (url.includes(match)) return respond(url, init);
    }
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function Location() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname}</div>;
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
      <Location />
    </MemoryRouter>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("validateScenario", () => {
  it("accepts a neutral scenario and returns the settings", () => {
    const { errors, settings } = validateScenario({ ...draftOf("MAIN"), code: "UP20", name: "Up" }, true);
    expect(errors).toEqual({});
    expect(settings).toEqual({ planCode: "MAIN", name: "Up", demandFactor: "1", itemFactors: [], addedDemand: [], leadTimeOverrides: [], active: true });
  });

  it("names every rule the service enforces", () => {
    const draft = {
      ...draftOf("main"),
      code: "x",
      name: "",
      demandFactor: "101",
      itemFactors: '[{"itemId":"nope","factor":"1"}]',
      addedDemand: '[{"itemId":"' + ITEM + '","date":"2026-12-01","qty":"0"}]',
      leadTimeOverrides: "[",
    };
    const { errors, settings } = validateScenario(draft, true);
    expect(settings).toBeNull();
    expect(Object.keys(errors).sort()).toEqual(["addedDemand", "code", "demandFactor", "itemFactors", "leadTimeOverrides", "name", "planCode"]);
    expect(errors.itemFactors).toBe("Line 1: itemId must be a UUID.");
  });

  it("does not check the code of an existing scenario", () => {
    expect(validateScenario({ ...draftOf("MAIN", up20), code: "" }, false).errors).toEqual({});
  });
});

describe("Scenarios page", () => {
  it("lists the scenarios of the plan and opens one", async () => {
    const fetchMock = stubApi(["/scenarios/UP20", () => json(up20)], ["/scenarios", () => json({ items: [up20, off] })]);
    renderAt("/planning/scenarios");
    const table = await findTable({ name: "Scenarios" });
    await waitFor(() => expect(within(table).getByText("Demand +20%")).toBeInTheDocument());
    expect(within(table).getAllByText("×1.2")).toHaveLength(2);
    expect(within(table).getByText("Inactive")).toBeInTheDocument();
    expect(String(fetchMock.mock.calls[0][0])).toContain("planCode=MAIN");
    fireEvent.click(within(table).getByRole("button", { name: "UP20" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/planning/scenarios/UP20");
    expect(await screen.findByLabelText("Demand factor")).toHaveValue("1.2");
  });

  it("shows the empty state", async () => {
    stubApi(["/scenarios", () => json({ items: [] })]);
    renderAt("/planning/scenarios");
    expect(await screen.findByText("No scenarios yet")).toBeInTheDocument();
  });

  it("shows the service error instead of the table", async () => {
    stubApi(["/scenarios", () => problem(403, "AUTH-0003", "missing duty planning.read")]);
    renderAt("/planning/scenarios");
    expect(await screen.findByText("Could not load scenarios")).toBeInTheDocument();
    expect(screen.getByText(/AUTH-0003/)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});

describe("New scenario page", () => {
  it("validates before sending, then creates with an Idempotency-Key and opens the scenario", async () => {
    const fetchMock = stubApi(["/scenarios/UP20", () => json(up20)], ["/scenarios", () => json(up20, 201)]);
    renderAt("/planning/scenarios/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "up20" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Demand +20%" } });
    fireEvent.change(screen.getByLabelText("Demand factor"), { target: { value: "abc" } });
    fireEvent.click(screen.getByRole("button", { name: "Create scenario" }));
    expect(await screen.findByText(/Enter a factor from 0 to 100/)).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Demand factor"), { target: { value: "1.2" } });
    fireEvent.click(screen.getByRole("button", { name: "Create scenario" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("/planning/scenarios/UP20"));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/planning/scenarios");
    expect(init?.method).toBe("POST");
    expect((init?.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(JSON.parse(String(init?.body))).toEqual({
      code: "UP20", planCode: "MAIN", name: "Demand +20%", demandFactor: "1.2", itemFactors: [], addedDemand: [], leadTimeOverrides: [], active: true,
    });
  });

  it("shows the limit of active scenarios", async () => {
    stubApi(["/scenarios", () => problem(409, "PLAN-SCENARIO-LIMIT", "plan MAIN already has 10 active scenarios; deactivate one first")]);
    renderAt("/planning/scenarios/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "ELEVEN" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Eleventh" } });
    fireEvent.click(screen.getByRole("button", { name: "Create scenario" }));
    expect(await screen.findByText("The scenario was not saved")).toBeInTheDocument();
    expect(screen.getByText(/PLAN-SCENARIO-LIMIT/)).toBeInTheDocument();
  });
});

describe("Scenario page", () => {
  it("edits the scenario under If-Match and shows the new version", async () => {
    const fetchMock = stubApi([
      "/scenarios/UP20",
      (_url, init) => (init?.method === "PUT" ? json({ ...up20, version: 2, demandFactor: "1.5" }) : json(up20)),
    ]);
    renderAt("/planning/scenarios/UP20");
    const factor = await screen.findByLabelText("Demand factor");
    await act(async () => {});
    expect(screen.queryByLabelText("Code")).not.toBeInTheDocument();
    fireEvent.change(factor, { target: { value: "1.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Saved. Version 2.")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT");
    expect((put?.[1]?.headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(JSON.parse(String(put?.[1]?.body))).toMatchObject({ planCode: "MAIN", demandFactor: "1.5", leadTimeOverrides: up20.leadTimeOverrides });
  });

  it("offers a reload when someone else changed the scenario", async () => {
    let gets = 0;
    stubApi([
      "/scenarios/UP20",
      (_url, init) => {
        if (init?.method === "PUT") return problem(412, "conflict_version", "the scenario was changed by another user; current version is 2");
        gets += 1;
        return json(gets === 1 ? up20 : { ...up20, version: 2 });
      },
    ]);
    renderAt("/planning/scenarios/UP20");
    fireEvent.click(await screen.findByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Someone else changed this scenario")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload scenario" }));
    await waitFor(() => expect(gets).toBe(2));
  });

  it("shows a missing scenario", async () => {
    stubApi(["/scenarios/NOPE", () => problem(404, "PLAN-SCENARIO-NOT-FOUND", "planning scenario not found")]);
    renderAt("/planning/scenarios/NOPE");
    expect(await screen.findByText("Could not load scenario NOPE")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
  });

  it("runs the scenario on a snapshot and links to the simulated orders and the comparison", async () => {
    const version = {
      id: "00000000-0000-0000-0000-0000000000d1", planCode: "MAIN", number: 3, status: "complete", kind: "simulation", scenarioCode: "UP20",
      asOf: "2026-11-02", itemsPlanned: 1, plannedOrders: 1, messages: 0, durationMs: 2, planHash: "abc",
    };
    const fetchMock = stubApi(
      ["/scenarios/UP20:run", () => json(version, 201)],
      ["/scenarios/UP20", () => json(up20)],
    );
    renderAt("/planning/scenarios/UP20");
    await screen.findByRole("form", { name: "Run scenario UP20" });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "not-a-uuid" } });
    fireEvent.click(screen.getByRole("button", { name: "Run simulation" }));
    expect(await screen.findByText("Enter the site id as a UUID.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);

    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
    fireEvent.click(screen.getByRole("button", { name: "Run simulation" }));
    expect(await screen.findByText("Simulation version 3 of plan MAIN stored")).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(post?.[0]).toBe("/api/v1/planning/scenarios/UP20:run");
    expect((post?.[1]?.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(JSON.parse(String(post?.[1]?.body))).toMatchObject({ siteId: SITE });
    expect(JSON.parse(String(post?.[1]?.body))).not.toHaveProperty("carryForward");
    expect(screen.getByRole("link", { name: "View simulated orders" })).toHaveAttribute("href", "/planning/planned-orders?plan=MAIN&version=3");
    expect(screen.getByRole("link", { name: /Compare with the operational plan/ })).toHaveAttribute("href", "/planning/runs/compare?plan=MAIN&to=3");
  });

  it("sends carryForward on a simulation only when firmed orders are counted as supply, under a new key", async () => {
    const version = {
      id: "00000000-0000-0000-0000-0000000000d1", planCode: "MAIN", number: 3, status: "complete", kind: "simulation", scenarioCode: "UP20",
      asOf: "2026-11-02", itemsPlanned: 1, plannedOrders: 0, messages: 0, durationMs: 2, planHash: "abc",
    };
    const fetchMock = stubApi(
      ["/scenarios/UP20:run", () => json(version, 201)],
      ["/scenarios/UP20", () => json(up20)],
    );
    renderAt("/planning/scenarios/UP20");
    await screen.findByRole("form", { name: "Run scenario UP20" });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
    fireEvent.click(screen.getByLabelText("Count firmed orders as supply"));
    fireEvent.click(screen.getByRole("button", { name: "Run simulation" }));
    expect(await screen.findByText("Simulation version 3 of plan MAIN stored")).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(JSON.parse(String(post?.[1]?.body))).toMatchObject({ siteId: SITE, carryForward: true });
  });

  it("cannot run an inactive scenario", async () => {
    stubApi(["/scenarios/OFF", () => json(off)]);
    renderAt("/planning/scenarios/OFF");
    expect(await screen.findByText("This scenario is inactive and cannot be run")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run simulation" })).toBeDisabled();
  });

  it("shows why a simulation did not run", async () => {
    stubApi(
      ["/scenarios/UP20:run", () => problem(404, "PLAN-NOT-FOUND", "plan MAIN has no operational run to simulate against")],
      ["/scenarios/UP20", () => json(up20)],
    );
    renderAt("/planning/scenarios/UP20");
    await screen.findByRole("form", { name: "Run scenario UP20" });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
    fireEvent.click(screen.getByRole("button", { name: "Run simulation" }));
    expect(await screen.findByText("The simulation did not run")).toBeInTheDocument();
    expect(screen.getByText(/PLAN-NOT-FOUND/)).toBeInTheDocument();
  });
});

describe("Simulation versions in the operational screens", () => {
  it("reads the planned orders of a simulation version read only, without the firming card", async () => {
    const order = {
      id: "00000000-0000-0000-0000-0000000000e1", versionId: "00000000-0000-0000-0000-0000000000d1", number: "PLN-000001", itemId: ITEM,
      orderType: "production", qty: "134", orderDate: "2026-11-02", deliveryDate: "2026-11-30", delayDays: 0, lotRule: "lot-for-lot", status: "unprocessed", version: 1,
    };
    const fetchMock = stubApi(["/planned-orders", () => json({ items: [order] })]);
    renderAt("/planning/planned-orders?plan=MAIN&version=3");
    expect(await screen.findByText(/Version 3 of plan MAIN, read only/)).toBeInTheDocument();
    expect(await screen.findByText("PLN-000001")).toBeInTheDocument();
    expect(String(fetchMock.mock.calls[0][0])).toContain("version=3");
    expect(screen.queryByLabelText("Select PLN-000001 for firming")).not.toBeInTheDocument();
  });

  it("marks simulation versions in the run history", async () => {
    const v = { id: "00000000-0000-0000-0000-0000000000d1", planCode: "MAIN", number: 3, status: "complete", asOf: "2026-11-02", itemsPlanned: 1, plannedOrders: 1, messages: 0, durationMs: 2, planHash: "abcdef0123456789abcd" };
    stubApi(["/versions", () => json({ items: [{ ...v, kind: "simulation", scenarioCode: "UP20" }, { ...v, id: "00000000-0000-0000-0000-0000000000d2", number: 2, status: "current", kind: "static" }] })]);
    renderAt("/planning/runs");
    const table = await findTable({ name: "Plan runs" });
    await waitFor(() => expect(within(table).getByText("Simulation UP20")).toBeInTheDocument());
    expect(within(table).getByText("Operational")).toBeInTheDocument();
  });
});
