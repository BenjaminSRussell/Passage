import { findTable } from "../../test-utils";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { StrategySetup } from "./api/client";
import planning from "./module";
import { draftOf, validateStrategy } from "./StrategyPages";

const defaults = { consumptionMode: "backward", backwardDays: 30, forwardDays: 0, segregatedStock: false } as const;
const six: StrategySetup[] = [
  { code: "mts-net", configured: false, usesMarking: false, ...defaults },
  { code: "mts-gross", configured: false, usesMarking: false, ...defaults },
  { code: "mto", configured: false, usesMarking: true, ...defaults },
  { code: "assemble-to-order-final", configured: true, id: "00000000-0000-0000-0000-0000000000d1", version: 2, usesMarking: false, ...defaults, consumptionMode: "backward-forward", backwardDays: 14, forwardDays: 7 },
  { code: "plan-without-final-assembly", configured: false, usesMarking: true, ...defaults },
  { code: "subassembly", configured: false, usesMarking: false, ...defaults },
];
const ato = six[3];
const mto = six[2];

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

describe("validateStrategy", () => {
  it("accepts the defaults and returns numbers", () => {
    const { errors, settings } = validateStrategy(draftOf(mto));
    expect(errors).toEqual({});
    expect(settings).toEqual({ consumptionMode: "backward", backwardDays: 30, forwardDays: 0, usesMarking: true, segregatedStock: false });
  });

  it("names every rule the service enforces", () => {
    const d = { ...draftOf(mto), backwardDays: "400", forwardDays: "-1" };
    expect(Object.keys(validateStrategy(d).errors).sort()).toEqual(["backwardDays", "forwardDays"]);
    const empty = { ...draftOf(mto), consumptionMode: "backward-forward" as const, backwardDays: "0", forwardDays: "0" };
    expect(validateStrategy(empty).errors.consumptionMode).toMatch(/at least 1 day/);
  });
});

describe("Planning strategies page", () => {
  it("lists the six strategies with stored setup or defaults and opens one", async () => {
    stubApi(["/strategies/mto", () => json(mto)], ["/strategies", () => json({ items: six })]);
    renderAt("/planning/strategies");
    expect(screen.getByRole("heading", { level: 2, name: "Planning strategies" })).toBeInTheDocument();
    const table = await findTable({ name: "Planning strategies" });
    await waitFor(() => expect(within(table).getByText("assemble-to-order-final")).toBeInTheDocument());
    expect(within(table).getByText("Stored, v2")).toBeInTheDocument();
    expect(within(table).getAllByText("Defaults")).toHaveLength(5);
    expect(within(table).getByText("Backward, then forward, 14 d back, 7 d forward")).toBeInTheDocument();
    fireEvent.click(within(table).getByRole("button", { name: "mto" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/planning/strategies/mto");
    expect(await screen.findByLabelText("Backward window (days)")).toHaveValue("30");
    expect(screen.getByLabelText(/Mark pegging/)).toBeChecked();
  });

  it("shows the empty state", async () => {
    stubApi(["/strategies", () => json({ items: [] })]);
    renderAt("/planning/strategies");
    expect(await screen.findByText("No planning strategies")).toBeInTheDocument();
  });

  it("shows the service error instead of the table", async () => {
    stubApi(["/strategies", () => problem(403, "AUTH-0003", "missing duty planning.read")]);
    renderAt("/planning/strategies");
    expect(await screen.findByText("Could not load planning strategies")).toBeInTheDocument();
    expect(screen.getByText(/AUTH-0003/)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});

describe("Planning strategy page", () => {
  it("stores the first setup of an unconfigured strategy with an Idempotency-Key", async () => {
    const stored: StrategySetup = { ...mto, configured: true, id: "00000000-0000-0000-0000-0000000000d2", version: 1, backwardDays: 10 };
    const fetchMock = stubApi(["/strategies", (_url, init) => (init?.method === "POST" ? json(stored, 201) : json(mto))]);
    renderAt("/planning/strategies/mto");
    const back = await screen.findByLabelText("Backward window (days)");
    await act(async () => {});
    fireEvent.change(back, { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Save strategy setup" }));
    expect(await screen.findByText("Enter a whole number from 0 to 365.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
    fireEvent.change(back, { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save strategy setup" }));
    expect(await screen.findByText("Saved. Version 1.")).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(post?.[0]).toBe("/api/v1/planning/strategies");
    expect((post?.[1]?.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ code: "mto", consumptionMode: "backward", backwardDays: 10, forwardDays: 0, usesMarking: true, segregatedStock: false });
  });

  it("edits a stored strategy under If-Match", async () => {
    const fetchMock = stubApi(["/strategies/assemble-to-order-final", (_url, init) => (init?.method === "PUT" ? json({ ...ato, version: 3, forwardDays: 9 }) : json(ato))]);
    renderAt("/planning/strategies/assemble-to-order-final");
    const forward = await screen.findByLabelText("Forward window (days)");
    await act(async () => {});
    expect(forward).toHaveValue("7");
    fireEvent.change(forward, { target: { value: "9" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Saved. Version 3.")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT");
    expect(put?.[0]).toBe("/api/v1/planning/strategies/assemble-to-order-final");
    expect((put?.[1]?.headers as Record<string, string>)["If-Match"]).toBe('"2"');
    expect(JSON.parse(String(put?.[1]?.body))).toMatchObject({ consumptionMode: "backward-forward", backwardDays: 14, forwardDays: 9 });
  });

  it("offers a reload when someone else changed the strategy", async () => {
    let gets = 0;
    stubApi([
      "/strategies/assemble-to-order-final",
      (_url, init) => {
        if (init?.method === "PUT") return problem(412, "conflict_version", "the planning strategy was changed by another user; current version is 3");
        gets += 1;
        return json(gets === 1 ? ato : { ...ato, version: 3 });
      },
    ]);
    renderAt("/planning/strategies/assemble-to-order-final");
    fireEvent.click(await screen.findByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Someone else changed this strategy")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload strategy" }));
    await waitFor(() => expect(gets).toBe(2));
    await waitFor(() => expect(screen.queryByText("Someone else changed this strategy")).not.toBeInTheDocument());
  });

  it("offers a reload when someone else stored the setup first", async () => {
    let gets = 0;
    stubApi([
      "/strategies",
      (_url, init) => {
        if (init?.method === "POST") return problem(409, "PLAN-STRATEGY-EXISTS", "strategy mto already has setup");
        gets += 1;
        return json(gets === 1 ? mto : { ...mto, configured: true, id: "00000000-0000-0000-0000-0000000000d2", version: 1 });
      },
    ]);
    renderAt("/planning/strategies/mto");
    fireEvent.click(await screen.findByRole("button", { name: "Save strategy setup" }));
    expect(await screen.findByText("Someone else stored this strategy first")).toBeInTheDocument();
    expect(screen.getByText(/PLAN-STRATEGY-EXISTS/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload strategy" }));
    expect(await screen.findByRole("button", { name: "Save changes" })).toBeInTheDocument();
    expect(gets).toBe(2);
    expect(screen.queryByText("Someone else stored this strategy first")).not.toBeInTheDocument();
  });

  it("shows an unknown strategy", async () => {
    stubApi(["/strategies/nope", () => problem(404, "PLAN-STRATEGY-NOT-FOUND", "unknown planning strategy")]);
    renderAt("/planning/strategies/nope");
    expect(await screen.findByText("Could not load planning strategy nope")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Save/ })).not.toBeInTheDocument();
  });
});
