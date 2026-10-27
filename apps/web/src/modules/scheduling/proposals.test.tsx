import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CampaignProposals, ProposedCampaign, Resource } from "./api/client";
import { proposalToCampaign } from "./CampaignProposalsPage";
import scheduling from "./module";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const FILL = "3f1c0000-0000-4000-8000-000000000002";
const ORDER_B = "b1000000-0000-4000-8000-00000000000b";
const ORDER_C = "c1000000-0000-4000-8000-00000000000c";
const OP_B = "0b000000-0000-4000-8000-00000000000b";
const OP_C = "0c000000-0000-4000-8000-00000000000c";
const CAMPAIGN = "ca000000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
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

const shifts = [1, 2, 3, 4, 5].map((weekday) => ({ weekday, startMinute: 480, endMinute: 960 }));
const resources: Resource[] = [
  { id: MIX, code: "MIX-1", name: "Mixing vessel", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" },
  { id: FILL, code: "FILL-1", name: "Filler", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" },
];

const proposal: ProposedCampaign = {
  campaignKey: "WHITE",
  resourceId: MIX,
  plannedStart: "2027-03-01T08:00:00Z",
  runMinutes: 150,
  orders: [
    { orderId: ORDER_B, operationId: OP_B, operNumber: 10, resourceId: MIX, setupGroup: "WHITE", runMinutes: 60, routeSetupMinutes: 15, start: "2027-03-01T08:00:00Z", dueAt: "2027-03-03T00:00:00Z", firmed: false },
    { orderId: ORDER_C, operationId: OP_C, operNumber: 20, resourceId: MIX, setupGroup: "WHITE", runMinutes: 90, routeSetupMinutes: 0, start: "2027-03-01T09:00:00Z", firmed: true },
  ],
};

function answer(items: ProposedCampaign[]): CampaignProposals {
  return { from: "2027-03-01T00:00:00Z", to: "2027-03-15T00:00:00Z", windowDays: 14, items };
}

function standard(items: ProposedCampaign[]): Handler {
  return (url) => {
    if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: resources } };
    if (url.pathname.endsWith("/campaign-proposals")) return { status: 200, body: answer(items) };
    return undefined;
  };
}

describe("proposalToCampaign", () => {
  it("builds the campaign request from the proposal and the typed quantities", () => {
    const { errors, input } = proposalToCampaign(proposal, { [ORDER_B]: "100", [ORDER_C]: " 2.5 " }, "cp-sat");
    expect(errors).toEqual({});
    expect(input).toEqual({
      resourceId: MIX,
      campaignKey: "WHITE",
      plannedStart: "2027-03-01T08:00:00Z",
      sequenceRule: "cp-sat",
      orders: [
        { orderId: ORDER_B, setupGroup: "WHITE", quantity: "100", runMinutes: 60, routeSetupMinutes: 15, dueAt: "2027-03-03T00:00:00Z" },
        { orderId: ORDER_C, setupGroup: "WHITE", quantity: "2.5", runMinutes: 90, routeSetupMinutes: 0 },
      ],
    });
  });

  it("refuses missing, zero, negative and over-precise quantities per order", () => {
    for (const bad of ["", "0", "0.000", "-1", "1.1234567", "abc"]) {
      const { errors, input } = proposalToCampaign(proposal, { [ORDER_B]: "5", [ORDER_C]: bad }, "nearest-neighbor");
      expect(input).toBeUndefined();
      expect(Object.keys(errors)).toEqual([ORDER_C]);
    }
  });
});

describe("CampaignProposalsPage", () => {
  it("asks for the default 14-day window from today and lists each proposal with its orders", async () => {
    handler = standard([proposal]);
    const { container } = renderAt("/scheduling/campaign-proposals");
    expect(await screen.findByRole("heading", { name: "WHITE on MIX-1" })).toBeInTheDocument();
    const req = calls.find((c) => c.url.pathname.endsWith("/campaign-proposals"));
    expect(req?.url.searchParams.get("windowDays")).toBe("14");
    expect(req?.url.searchParams.get("from")).toMatch(/^\d{4}-\d{2}-\d{2}T00:00:00Z$/);
    expect(req?.url.searchParams.has("resourceId")).toBe(false);
    const table = screen.getByRole("table", { name: "Orders proposed for WHITE on MIX-1" });
    expect(within(table).getByText("b1000000")).toBeInTheDocument();
    expect(within(table).getByText("Firmed")).toBeInTheDocument();
    expect(within(table).getByText("1 h 30 min")).toBeInTheDocument();
    expect(screen.getByText(/2 orders · 2 h 30 min run time/)).toBeInTheDocument();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("sends the chosen window, start date and resource", async () => {
    handler = standard([]);
    renderAt("/scheduling/campaign-proposals");
    await screen.findByText("No campaigns to propose");
    fireEvent.change(screen.getByLabelText("Window"), { target: { value: "30" } });
    fireEvent.change(screen.getByLabelText("From (UTC)"), { target: { value: "2027-03-01" } });
    fireEvent.change(screen.getByLabelText("Resource"), { target: { value: FILL } });
    await waitFor(() => {
      const last = calls.filter((c) => c.url.pathname.endsWith("/campaign-proposals")).at(-1);
      expect(last?.url.searchParams.get("windowDays")).toBe("30");
      expect(last?.url.searchParams.get("from")).toBe("2027-03-01T00:00:00Z");
      expect(last?.url.searchParams.get("resourceId")).toBe(FILL);
    });
  });

  it("shows an empty state when nothing shares a key", async () => {
    handler = standard([]);
    renderAt("/scheduling/campaign-proposals");
    expect(await screen.findByText("No campaigns to propose")).toBeInTheDocument();
  });

  it("shows the error with a retry that loads again", async () => {
    let fail = true;
    handler = (url) => {
      if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: resources } };
      if (url.pathname.endsWith("/campaign-proposals")) {
        return fail ? { status: 400, body: { code: "validation_failed", detail: "request failed validation" } } : { status: 200, body: answer([proposal]) };
      }
      return undefined;
    };
    renderAt("/scheduling/campaign-proposals");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load campaign proposals");
    expect(alert).toHaveTextContent("validation_failed");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { name: "WHITE on MIX-1" })).toBeInTheDocument();
  });

  it("does not create a campaign until every order has a quantity", async () => {
    handler = standard([proposal]);
    renderAt("/scheduling/campaign-proposals");
    await screen.findByRole("heading", { name: "WHITE on MIX-1" });
    fireEvent.change(screen.getByLabelText("Quantity for order b1000000"), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "Create campaign" }));
    expect(await screen.findByText("Enter a quantity for every order.")).toBeInTheDocument();
    expect(screen.getByLabelText("Quantity for order c1000000")).toHaveAttribute("aria-invalid", "true");
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("creates the campaign with an idempotency key and opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/campaigns")) {
        return { status: 201, body: { id: CAMPAIGN, resourceId: MIX, campaignKey: "WHITE", status: "proposed", version: 1, orders: [] } };
      }
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) {
        return { status: 200, body: { id: CAMPAIGN, resourceId: MIX, campaignKey: "WHITE", status: "proposed", plannedStart: "2027-03-01T08:00:00Z", sequenceRule: "manual", orderCount: 2, runMinutes: 0, changeoverMinutes: 0, cleanoutMinutes: 0, plan: null, version: 1, createdAt: "2027-02-20T00:00:00Z", orders: [] } };
      }
      return standard([proposal])(url, init);
    };
    renderAt("/scheduling/campaign-proposals");
    await screen.findByRole("heading", { name: "WHITE on MIX-1" });
    fireEvent.change(screen.getByLabelText("Quantity for order b1000000"), { target: { value: "100" } });
    fireEvent.change(screen.getByLabelText("Quantity for order c1000000"), { target: { value: "200" } });
    fireEvent.change(screen.getByLabelText("Sequence rule"), { target: { value: "manual" } });
    fireEvent.click(screen.getByRole("button", { name: "Create campaign" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith(`/campaigns/${CAMPAIGN}`))).toBe(true));
    const post = calls.find((c) => c.method === "POST");
    expect(new Headers(post?.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toMatchObject({
      resourceId: MIX,
      campaignKey: "WHITE",
      sequenceRule: "manual",
      orders: [
        { orderId: ORDER_B, quantity: "100", runMinutes: 60 },
        { orderId: ORDER_C, quantity: "200", runMinutes: 90 },
      ],
    });
  });

  it("explains an order that joined another campaign meanwhile, and maps quantity errors to their row", async () => {
    let reply: Reply = { status: 409, body: { code: "SCHED-0114", detail: "an order already belongs to a campaign" } };
    handler = (url, init) => (init.method === "POST" ? reply : standard([proposal])(url, init));
    renderAt("/scheduling/campaign-proposals");
    await screen.findByRole("heading", { name: "WHITE on MIX-1" });
    fireEvent.change(screen.getByLabelText("Quantity for order b1000000"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Quantity for order c1000000"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Create campaign" }));
    expect(await screen.findByText(/joined another campaign/)).toBeInTheDocument();

    reply = { status: 400, body: { code: "validation_failed", detail: "request failed validation", errors: { "orders[1].quantity": "must be at most 6 decimals" } } };
    fireEvent.click(screen.getByRole("button", { name: "Create campaign" }));
    expect(await screen.findByText("must be at most 6 decimals")).toBeInTheDocument();
    expect(screen.getByLabelText("Quantity for order c1000000")).toHaveAttribute("aria-invalid", "true");
  });

  it("is reachable from the campaigns list", async () => {
    handler = (url) => (url.pathname.endsWith("/campaigns") ? { status: 200, body: { items: [] } } : standard([])(url, {}));
    renderAt("/scheduling/campaigns");
    fireEvent.click(await screen.findByRole("link", { name: "Proposals" }));
    expect(await screen.findByRole("heading", { name: "Campaign proposals" })).toBeInTheDocument();
  });
});
