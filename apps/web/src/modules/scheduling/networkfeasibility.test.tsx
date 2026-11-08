import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Network, NetworkFeasibility } from "./api/client";
import scheduling from "./module";

const NET = "7e000000-0000-4000-8000-000000000001";
const RUN = "5e000000-0000-4000-8000-000000000001";
const OP = "3f1c0000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };
type Handler = (url: URL) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

let handler: Handler;
let calls: URL[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input, "http://localhost");
      calls.push(url);
      const reply = handler(url) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
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
        {scheduling.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const network = { id: NET, code: "MIX-FILL", name: "Mixing to filling", activeFrom: "2027-01-01", activeTo: null, edges: [], version: 1, createdAt: "2027-01-01T00:00:00Z" } as Network;

const result: NetworkFeasibility = {
  items: [
    {
      runId: RUN,
      runScope: "site",
      runAt: "2027-03-01T06:00:00Z",
      code: "SCHED-0009",
      flag: "NETWORK_INFEASIBLE",
      operationId: OP,
      orderId: "b1000000-0000-4000-8000-000000000001",
      operNumber: 20,
      networkId: NET,
      networkCode: "MIX-FILL",
      detail: "SCHED-0009 NETWORK_INFEASIBLE: no allowed transition",
    },
    {
      runId: RUN,
      runScope: "site",
      runAt: "2027-03-01T06:00:00Z",
      code: "SCHED-0010",
      flag: "WAIT_EXCEEDED",
      operationId: "3f1c0000-0000-4000-8000-000000000002",
      orderId: null,
      operNumber: null,
      networkId: null,
      networkCode: null,
      detail: "waits 400 min, maximum 120",
    },
  ],
  truncated: true,
  infeasible: 5,
  infeasibleOrders: 2,
  waitViolations: 1,
};

function serve(url: URL): Reply | undefined {
  if (url.pathname.endsWith("/networks")) return { status: 200, body: { items: [network] } };
  if (url.pathname.endsWith("/network-feasibility")) return { status: 200, body: result };
  return undefined;
}

describe("Network feasibility", () => {
  it("lists infeasible orders and wait violations with totals, and filters by network and day", async () => {
    handler = serve;
    const { container } = renderAt("/scheduling/network-feasibility");
    const table = await findTable({ name: "Network findings" });
    await waitFor(() => expect(table).toHaveTextContent("Network infeasible"));
    expect(table).toHaveTextContent("Wait over maximum");
    expect(table).toHaveTextContent("MIX-FILL");
    expect(table).toHaveTextContent("Operation 20");
    expect(table).toHaveTextContent("Deleted");
    expect(table).toHaveTextContent("waits 400 min, maximum 120");
    expect(screen.getByLabelText("Totals")).toHaveTextContent("2 orders network infeasible (5 operations)");
    expect(screen.getByLabelText("Totals")).toHaveTextContent("1 wait over maximum");
    expect(screen.getByText("Showing the first 200 findings")).toBeInTheDocument();
    expect(calls.find((c) => c.pathname.endsWith("/network-feasibility"))?.searchParams.get("networkId")).toBeNull();

    fireEvent.change(await screen.findByRole("option", { name: "MIX-FILL Mixing to filling" }).then(() => screen.getByLabelText("Network")), { target: { value: NET } });
    fireEvent.change(screen.getByLabelText("Runs from"), { target: { value: "2027-03-01" } });
    fireEvent.change(screen.getByLabelText("Runs to"), { target: { value: "2027-03-03" } });
    await waitFor(() => {
      const last = calls.filter((c) => c.pathname.endsWith("/network-feasibility")).at(-1);
      expect(last?.searchParams.get("networkId")).toBe(NET);
      expect(last?.searchParams.get("from")).toBe("2027-03-01T00:00:00Z");
      expect(last?.searchParams.get("to")).toBe("2027-03-04T00:00:00Z");
    });
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("explains an empty list, an inverted range without asking the service, and a failure with retry", async () => {
    handler = (url) => (url.pathname.endsWith("/network-feasibility") ? { status: 200, body: { items: [], truncated: false, infeasible: 0, infeasibleOrders: 0, waitViolations: 0 } } : serve(url));
    const first = renderAt("/scheduling/network-feasibility");
    expect(await screen.findByText("No network findings")).toBeInTheDocument();
    const before = calls.length;
    fireEvent.change(screen.getByLabelText("Runs from"), { target: { value: "2027-03-05" } });
    fireEvent.change(screen.getByLabelText("Runs to"), { target: { value: "2027-03-01" } });
    expect(await screen.findByText("The range is empty")).toBeInTheDocument();
    expect(calls.slice(before).every((c) => !(c.pathname.endsWith("/network-feasibility") && c.searchParams.get("to") === "2027-03-02T00:00:00Z"))).toBe(true);
    first.unmount();

    handler = (url) => (url.pathname.endsWith("/network-feasibility") ? { status: 500, body: { code: "COM-0500", detail: "internal error" } } : serve(url));
    renderAt("/scheduling/network-feasibility");
    expect(await findAlert()).toHaveTextContent("COM-0500");
    handler = serve;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Network findings" })).toBeInTheDocument();
  });
});
