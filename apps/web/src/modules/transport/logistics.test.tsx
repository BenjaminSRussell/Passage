import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { LoadTracking, Tender, TrackingEvent, Voyage, VoyageSummary } from "./api/client";
import transport from "./module";

const LOAD = "0195f0a0-0000-7000-8000-0000000000a1";
const TENDER = "0195f0a0-0000-7000-8000-0000000000f1";
const VOYAGE = "0195f0a0-0000-7000-8000-0000000000f2";
const C1 = "0195f0a0-0000-7000-8000-0000000000c1";
const C2 = "0195f0a0-0000-7000-8000-0000000000c2";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const violations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

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

function Where() {
  const location = useLocation();
  return <p data-testid="location">{location.pathname + location.search}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {transport.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const problem = (status: number, code: string, detail: string, errors?: Record<string, string>) => json(status, { code, detail, status, errors });

const tender = (extra: Partial<Tender> = {}): Tender => ({
  id: TENDER,
  loadId: LOAD,
  status: "PENDING",
  carriers: [C1, C2],
  currency: "USD",
  timeoutMin: 120,
  attempts: [{ rank: 1, carrierId: C1, sentAt: "2026-06-01T08:00:00Z", expiresAt: "2026-06-01T10:00:00Z", response: "PENDING", rateQuoted: "412.50" }],
  plannerTask: false,
  pendingCarrierId: C1,
  pendingExpiresAt: "2026-06-01T10:00:00Z",
  overdue: false,
  version: 1,
  createdAt: "2026-06-01T08:00:00Z",
  ...extra,
});

const accepted = (): Tender =>
  tender({
    status: "ACCEPTED",
    acceptedCarrierId: C1,
    pendingCarrierId: undefined,
    pendingExpiresAt: undefined,
    attempts: [{ ...tender().attempts[0], response: "ACCEPTED" }],
    version: 2,
  });

describe("Tender board", () => {
  it("lists tenders with the pending carrier and flags overdue ones", async () => {
    handler = (url) => (url.pathname === "/api/v1/transport/tenders" ? json(200, { items: [tender({ overdue: true })] }) : undefined);
    const { container } = renderAt("/transport/tenders");
    const table = await findTable({ name: "Tenders" });
    await waitFor(() => expect(table).toHaveTextContent("Pending"));
    expect(table).toHaveTextContent("Overdue");
    expect(table).toHaveTextContent("1 of 2");
    expect(screen.getByRole("link", { name: "Start tender" })).toHaveAttribute("href", "/transport/tenders/new");
    expect(violations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: TENDER.slice(0, 8) + "…" }));
    expect(screen.getByTestId("location")).toHaveTextContent(`/transport/tenders/${TENDER}`);
    expect(await screen.findByText("Could not load the tender")).toBeInTheDocument();
  });

  it("filters by status and shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/transport/tenders");
    expect(await screen.findByText("No tenders yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "TENDER_FAILED" } });
    expect(await screen.findByText("No tender failed tenders")).toBeInTheDocument();
    expect(calls.at(-1)?.url.searchParams.get("status")).toBe("TENDER_FAILED");
  });

  it("runs the expiry sweep and reloads", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/transport/tenders:expire" && init.method === "POST") return json(200, { items: [tender()] });
      if (url.pathname === "/api/v1/transport/tenders") return json(200, { items: [] });
      return undefined;
    };
    renderAt("/transport/tenders");
    await screen.findByText("No tenders yet");
    fireEvent.click(screen.getByRole("button", { name: "Cascade overdue tenders" }));
    expect(await screen.findByText("1 overdue tender was passed to the next carrier or failed.")).toBeInTheDocument();
    expect(posts("/tenders:expire")).toHaveLength(1);
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/v1/transport/tenders")).toHaveLength(2));
  });

  it("shows a failed load with a retry", async () => {
    handler = () => problem(503, "COM-0500", "service unavailable");
    renderAt("/transport/tenders");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load tenders");
    expect(alert).toHaveTextContent("COM-0500");
    handler = () => json(200, { items: [] });
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No tenders yet")).toBeInTheDocument();
  });
});

describe("Start tender", () => {
  it("sends the carriers in rank order with one Idempotency-Key and opens the tender", async () => {
    handler = (url, init) => (url.pathname === `/api/v1/transport/loads/${LOAD}:tender` && init.method === "POST" ? json(201, tender()) : json(200, tender()));
    const { container } = renderAt(`/transport/tenders/new?loadId=${LOAD}`);
    expect(screen.getByLabelText("Load id")).toHaveValue(LOAD);
    fill("Carrier 1 carrier id", C1);
    fill("Carrier 1 rate quoted", "412.50");
    fireEvent.click(screen.getByRole("button", { name: "Add carrier" }));
    fill("Carrier 2 carrier id", C2);
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Start tender" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent(`/transport/tenders/${TENDER}`));
    const [call] = posts(`${LOAD}:tender`);
    expect(call.body).toEqual({ carriers: [{ carrierId: C1, rateQuoted: "412.50" }, { carrierId: C2 }], currency: "USD", timeoutMin: 120 });
    expect(header(call, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("shows TMS-0114 when the load already has an open tender", async () => {
    handler = () => problem(409, "TMS-0114", "load already has pending tender x");
    renderAt(`/transport/tenders/new?loadId=${LOAD}`);
    fill("Carrier 1 carrier id", C1);
    fireEvent.click(screen.getByRole("button", { name: "Start tender" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not start the tender");
    expect(alert).toHaveTextContent("TMS-0114");
  });

  it("checks the form before calling the service", () => {
    renderAt("/transport/tenders/new");
    fireEvent.click(screen.getByRole("button", { name: "Start tender" }));
    expect(screen.getByText("Enter the load id as a lower-case UUID.")).toBeInTheDocument();
    expect(screen.getByText(/Carrier 1: Enter the carrier id/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("Tender detail", () => {
  it("shows the cascade and records the pending carrier's acceptance", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/transport/tenders/${TENDER}:respond` && init.method === "POST") return json(200, accepted());
      if (url.pathname === `/api/v1/transport/tenders/${TENDER}`) return json(200, tender());
      return undefined;
    };
    const { container } = renderAt(`/transport/tenders/${TENDER}`);
    const attempts = await findTable({ name: "Tender attempts" });
    expect(attempts).toHaveTextContent("412.50 USD");
    expect(screen.getByLabelText("Responding carrier id")).toHaveValue(C1);
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Record acceptance" }));
    await waitFor(() => expect(screen.getAllByText("Accepted").length).toBeGreaterThan(0));
    expect(posts(":respond")[0].body).toEqual({ carrierId: C1, accept: true });
    expect(screen.queryByRole("button", { name: "Record acceptance" })).not.toBeInTheDocument();
  });

  it("shows a late acceptance as TMS-0105", async () => {
    handler = (url, init) =>
      init.method === "POST" ? problem(409, "TMS-0105", "tender expired for this carrier; the acceptance is rejected") : url.pathname.endsWith(TENDER) ? json(200, tender({ overdue: true })) : undefined;
    renderAt(`/transport/tenders/${TENDER}`);
    expect(await screen.findByText(/The deadline has passed/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Record acceptance" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("TMS-0105");
  });

  it("explains a failed tender", async () => {
    handler = () => json(200, tender({ status: "TENDER_FAILED", plannerTask: true, pendingCarrierId: undefined, pendingExpiresAt: undefined }));
    renderAt(`/transport/tenders/${TENDER}`);
    expect(await screen.findByText(/All carriers declined or timed out/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Record decline" })).not.toBeInTheDocument();
  });

  it("shows a missing tender", async () => {
    handler = () => problem(404, "TMS-0100", "tender not found");
    renderAt(`/transport/tenders/${TENDER}`);
    expect(await findAlert()).toHaveTextContent("TMS-0100");
  });
});

const event = (extra: Partial<TrackingEvent> = {}): TrackingEvent => ({
  id: "0195f0a0-0000-7000-8000-0000000000e1",
  loadId: LOAD,
  stopNo: 2,
  code: "D1",
  occurredAt: "2026-06-01T15:00:00Z",
  location: "Dallas TX",
  source: "EDI214",
  ref: "ISA-1",
  receivedAt: "2026-06-01T15:01:00Z",
  ...extra,
});

describe("Tracking", () => {
  it("lists the latest reports and opens a load timeline", async () => {
    handler = (url) => (url.pathname === "/api/v1/transport/tracking-events" ? json(200, { items: [event()] }) : undefined);
    const { container } = renderAt("/transport/tracking");
    const table = await findTable({ name: "Latest status reports" });
    await waitFor(() => expect(table).toHaveTextContent("D1 delivered"));
    expect(table).toHaveTextContent("Dallas TX");
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Open timeline" }));
    expect(screen.getByText("Enter the load id as a lower-case UUID.")).toBeInTheDocument();
    fill("Load id", LOAD);
    fireEvent.click(screen.getByRole("button", { name: "Open timeline" }));
    expect(screen.getByTestId("location")).toHaveTextContent(`/transport/tracking/${LOAD}`);
    expect(await screen.findByText("Could not load the timeline")).toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/transport/tracking");
    expect(await screen.findByText("No status reports yet")).toBeInTheDocument();
  });

  it("shows a load timeline with an inferred pickup (BR-34.9)", async () => {
    const timeline: LoadTracking = {
      loadId: LOAD,
      events: [event()],
      stops: [{ stopNo: 2, status: "DELIVERED", pickedUpAt: "2026-06-01T15:00:00Z", deliveredAt: "2026-06-01T15:00:00Z", pickupInferred: true }],
    };
    handler = (url) => (url.pathname === `/api/v1/transport/loads/${LOAD}/tracking` ? json(200, timeline) : undefined);
    const { container } = renderAt(`/transport/tracking/${LOAD}`);
    const stops = await findTable({ name: "Stops" });
    expect(stops).toHaveTextContent("Delivered");
    expect(stops).toHaveTextContent("Inferred");
    expect(screen.getByRole("table", { name: "Status reports" })).toHaveTextContent("ISA-1");
    expect(violations(container)).toEqual([]);
  });

  it("shows a load without reports and a failed timeline", async () => {
    handler = () => json(200, { loadId: LOAD, events: [], stops: [] });
    const { unmount } = renderAt(`/transport/tracking/${LOAD}`);
    expect(await screen.findByText("No status reports for this load")).toBeInTheDocument();
    unmount();
    handler = () => problem(400, "COM-0001", "request failed validation");
    renderAt(`/transport/tracking/${LOAD}`);
    expect(await findAlert()).toHaveTextContent("Could not load the timeline");
  });

  it("records a status report and opens the load", async () => {
    handler = (url, init) => (url.pathname === "/api/v1/transport/tracking-events" && init.method === "POST" ? json(201, event()) : json(200, { loadId: LOAD, events: [], stops: [] }));
    renderAt(`/transport/tracking/new?loadId=${LOAD}`);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "D1" } });
    fill("Stop number", "2");
    fill("Occurred at (UTC)", "2026-06-01T15:00");
    fill("Reference", "call-7");
    fireEvent.click(screen.getByRole("button", { name: "Record status" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent(`/transport/tracking/${LOAD}`));
    expect(posts("/tracking-events")[0].body).toEqual({ loadId: LOAD, stopNo: 2, code: "D1", occurredAt: "2026-06-01T15:00:00Z", source: "DRIVER", ref: "call-7" });
  });

  it("shows the service's field errors", async () => {
    handler = () => problem(422, "idempotency_key_reuse", "source DRIVER and ref call-7 were already used for a different status report");
    renderAt(`/transport/tracking/new?loadId=${LOAD}`);
    fill("Occurred at (UTC)", "2026-06-01T15:00");
    fill("Reference", "call-7");
    fireEvent.click(screen.getByRole("button", { name: "Record status" }));
    expect(await findAlert()).toHaveTextContent("idempotency_key_reuse");
  });
});

const summary: VoyageSummary = {
  id: VOYAGE,
  reference: "MAEU-2611E",
  currency: "USD",
  status: "ESTIMATED",
  costTotal: "1000",
  containers: 1,
  receiptLines: 3,
  createdAt: "2026-06-01T08:00:00Z",
};

const voyage = (extra: Partial<Voyage> = {}): Voyage => ({
  id: VOYAGE,
  reference: "MAEU-2611E",
  currency: "USD",
  status: "ESTIMATED",
  legs: [{ seq: 1, from: "CNSHA", to: "USLAX", mode: "OCEAN" }],
  containers: [{ number: "CSQU3054383", type: "40HC", seal: "778812" }],
  folios: [{ folioNo: "F-1", forwarder: "Kuehne", containers: ["CSQU3054383"] }],
  costLines: [{ costType: "FREIGHT", amount: "1000", allocationMethod: "WEIGHT" }],
  receiptLines: [
    { line: 1, value: "100", weight: "1", volume: "0", quantity: "0" },
    { line: 2, value: "100", weight: "1", volume: "0", quantity: "0" },
    { line: 3, value: "250", weight: "1", volume: "0", quantity: "0" },
  ],
  costAllocations: [],
  allocations: [],
  version: 1,
  createdAt: "2026-06-01T08:00:00Z",
  ...extra,
});

const posted = (): Voyage =>
  voyage({
    status: "POSTED",
    postedAt: "2026-06-02T08:00:00Z",
    version: 2,
    costAllocations: [
      {
        costType: "FREIGHT",
        allocationMethod: "WEIGHT",
        amount: "1000",
        shares: [
          { line: 1, amount: "333.33" },
          { line: 2, amount: "333.33" },
          { line: 3, amount: "333.34" },
        ],
        residual: "0.01",
        residualLine: 3,
      },
    ],
    allocations: [
      { line: 1, amount: "333.33" },
      { line: 2, amount: "333.33" },
      { line: 3, amount: "333.34" },
    ],
  });

describe("Landed cost voyages", () => {
  it("lists voyages with their landed cost", async () => {
    handler = (url) => (url.pathname === "/api/v1/transport/voyages" ? json(200, { items: [summary] }) : undefined);
    const { container } = renderAt("/transport/voyages");
    const table = await findTable({ name: "Voyages" });
    await waitFor(() => expect(table).toHaveTextContent("1000.00 USD"));
    expect(table).toHaveTextContent("Estimated");
    expect(violations(container)).toEqual([]);
  });

  it("shows the empty and error states", async () => {
    handler = () => json(200, { items: [] });
    const { unmount } = renderAt("/transport/voyages");
    expect(await screen.findByText("No voyages yet")).toBeInTheDocument();
    unmount();
    handler = () => problem(403, "AUTH-0003", "missing duty transport.landed-cost.maintain");
    renderAt("/transport/voyages");
    expect(await findAlert()).toHaveTextContent("AUTH-0003");
  });

  it("allocates and posts a voyage (AC-34.6)", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/transport/landed-cost/voyages/${VOYAGE}:allocate` && init.method === "POST") return json(200, posted());
      if (url.pathname === `/api/v1/transport/voyages/${VOYAGE}`) return json(200, voyage());
      return undefined;
    };
    const { container } = renderAt(`/transport/voyages/${VOYAGE}`);
    expect(await findTable({ name: "Containers" })).toHaveTextContent("CSQU3054383");
    // The header total and the freight cost line.
    expect(screen.getAllByText("1000.00 USD")).toHaveLength(2);
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Allocate and post" }));
    expect(await screen.findByText("Landed cost posted: 3 receipt lines updated for costing.")).toBeInTheDocument();
    const allocation = screen.getByRole("table", { name: "Receipt line allocation" });
    expect(allocation).toHaveTextContent("333.34 USD");
    expect(within(allocation).getAllByText("333.33 USD")).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Allocate and post" })).not.toBeInTheDocument();
  });

  it("creates a voyage with one Idempotency-Key and opens it", async () => {
    handler = (url, init) => (url.pathname === "/api/v1/transport/voyages" && init.method === "POST" ? json(201, voyage()) : json(200, voyage()));
    const { container } = renderAt("/transport/voyages/new");
    fill("Reference", "MAEU-2611E");
    fill("Leg 1 from", "cnsha");
    fill("Leg 1 to", "uslax");
    fill("Container 1 number", "csqu3054383");
    fill("Cost line 1 amount", "1000.00");
    fill("Receipt line 1 weight", "1");
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create voyage" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent(`/transport/voyages/${VOYAGE}`));
    const [call] = posts("/voyages");
    expect(call.body).toMatchObject({
      reference: "MAEU-2611E",
      legs: [{ from: "CNSHA", to: "USLAX", mode: "OCEAN" }],
      containers: [{ number: "CSQU3054383", type: "40HC" }],
      costLines: [{ costType: "FREIGHT", amount: "1000.00", allocationMethod: "WEIGHT" }],
      receiptLines: [{ line: 1, weight: "1", value: "0" }],
    });
    expect(header(call, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("shows TMS-0124 on the container that fails the check digit (AC-34.10)", async () => {
    handler = () => problem(422, "TMS-0124", "a container number fails the ISO 6346 check digit", { "containers[0].number": "container number CSQU3054384 fails the ISO 6346 check digit" });
    renderAt("/transport/voyages/new");
    fill("Reference", "MAEU-2611E");
    fill("Leg 1 from", "CNSHA");
    fill("Leg 1 to", "USLAX");
    fill("Container 1 number", "CSQU3054384");
    fill("Cost line 1 amount", "1000.00");
    fill("Receipt line 1 weight", "1");
    fireEvent.click(screen.getByRole("button", { name: "Create voyage" }));
    expect(await screen.findByText(/Container 1: container number CSQU3054384 fails/)).toBeInTheDocument();
    expect(screen.getByText(/Could not create the voyage/)).toBeInTheDocument();
  });
});

describe("Transportation home", () => {
  it("links the slice-2 areas", () => {
    renderAt("/transport");
    const areas = screen.getByRole("list", { name: "Transportation areas" });
    expect(within(areas).getByRole("link", { name: "Open tenders" })).toHaveAttribute("href", "/transport/tenders");
    expect(within(areas).getByRole("link", { name: "Open tracking" })).toHaveAttribute("href", "/transport/tracking");
    expect(within(areas).getByRole("link", { name: "Open landed cost voyages" })).toHaveAttribute("href", "/transport/voyages");
  });
});
