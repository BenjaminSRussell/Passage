import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { DGCheck, FreightBillDetail, Load, LoadDetail } from "./api/client";
import { validateBuild, type BuildValues } from "./BuildLoadsPage";
import { validateDG } from "./LoadDetailPage";
import transport from "./module";

const LOAD = "0195f0a0-0000-7000-8000-0000000000a1";
const LOAD2 = "0195f0a0-0000-7000-8000-0000000000a2";
const SITE = "0195f0a0-0000-7000-8000-0000000000b1";
const SHIP1 = "0195f0a0-0000-7000-8000-0000000000d1";
const SHIP2 = "0195f0a0-0000-7000-8000-0000000000d2";
const BILL = "0195f0a0-0000-7000-8000-0000000000e1";
const CARRIER = "0195f0a0-0000-7000-8000-0000000000c1";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const violations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const problem = (status: number, code: string, detail: string, errors?: Record<string, string>) => json(status, { code, detail, status, errors });

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

const load = (extra: Partial<Load> = {}): Load => ({
  id: LOAD,
  mode: "TL",
  status: "PLANNED",
  shipFromSiteId: SITE,
  shipToRegion: "US-SE",
  pickupDate: "2026-06-01",
  totalWeightKg: "19200",
  totalVolumeM3: "24",
  pallets: 12,
  capacity: { maxWeightKg: "20000", maxVolumeM3: "90", maxPallets: 26 },
  shipmentIds: [SHIP1, SHIP2],
  version: 1,
  createdAt: "2026-06-01T08:00:00Z",
  ...extra,
});

const failedCheck: DGCheck = {
  id: "0195f0a0-0000-7000-8000-0000000000f9",
  loadId: LOAD,
  regulation: "49CFR",
  result: "FAIL",
  adrPoints: "0",
  findings: [{ ruleId: "49CFR-177.848", severity: "FAIL", code: "TMS-0130", detail: "Dangerous goods check failed: 49CFR-177.848 (1.1D with 3)" }],
  shippingPapers: [
    { unNumber: "UN0081", properShippingName: "Explosive, blasting, type A", class: "1.1D", packingGroup: "II", quantity: "25", limitedQuantity: false, emergencyResponsePhone: "+18005551234" },
    { unNumber: "UN1203", properShippingName: "Gasoline", class: "3", packingGroup: "II", quantity: "200", limitedQuantity: false, emergencyResponsePhone: "+18005551234" },
  ],
  checkedBy: "planner-1",
  checkedAt: "2026-06-01T09:00:00Z",
};

const detail = (checks: DGCheck[] = []): LoadDetail => ({ ...load(), dgChecks: checks });

describe("loads list", () => {
  it("lists loads with their fill against capacity and filters by status", async () => {
    handler = (url) => (url.pathname === "/api/v1/transport/loads" ? json(200, { items: [load(), load({ id: LOAD2, status: "TENDERED", shipmentIds: [SHIP1], totalWeightKg: "1600", pallets: 1 })] }) : undefined);
    const { container } = renderAt("/transport/loads");
    const table = await screen.findByRole("table", { name: "Loads" });
    expect(within(table).getByText("19200 / 20000 kg")).toBeInTheDocument();
    expect(within(table).getByText("12 / 26")).toBeInTheDocument();
    expect(within(table).getByText("Tendered")).toBeInTheDocument();
    expect(violations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "PLANNED" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("PLANNED"));
  });

  it("shows the empty state and the error state with a retry", async () => {
    handler = () => json(200, { items: [] });
    const first = renderAt("/transport/loads");
    expect(await screen.findByText("No loads yet")).toBeInTheDocument();
    first.unmount();

    handler = () => problem(503, "COM-0503", "transport is down");
    renderAt("/transport/loads");
    expect(await screen.findByText(/transport is down/)).toBeInTheDocument();
    handler = () => json(200, { items: [load()] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("table", { name: "Loads" })).toBeInTheDocument();
  });
});

describe("build loads", () => {
  const values = (extra: Partial<BuildValues> = {}): BuildValues => ({
    maxWeightKg: "20000",
    maxVolumeM3: "90",
    maxPallets: "26",
    shipments: [{ id: SHIP1, shipFromSiteId: SITE, shipToRegion: "us-se", mode: "TL", pickupDate: "2026-06-01", weightKg: "1600", volumeM3: "2", pallets: "1" }],
    ...extra,
  });

  it("validates like the service and sends decimal strings", () => {
    const ok = validateBuild(values());
    expect(ok.errors).toEqual({});
    expect(ok.input?.capacity).toEqual({ maxWeightKg: "20000", maxVolumeM3: "90", maxPallets: 26 });
    expect(ok.input?.shipments[0]).toMatchObject({ shipToRegion: "US-SE", weightKg: "1600", pallets: 1 });

    const bad = validateBuild(
      values({
        maxWeightKg: "0",
        maxPallets: "0",
        shipments: [
          { id: SHIP1, shipFromSiteId: SITE, shipToRegion: "US", mode: "TL", pickupDate: "1 June", weightKg: "1", volumeM3: "1", pallets: "1" },
          { id: SHIP1, shipFromSiteId: SITE, shipToRegion: "US", mode: "TL", pickupDate: "2026-06-01", weightKg: "1", volumeM3: "1", pallets: "1" },
        ],
      }),
    );
    expect(bad.input).toBeUndefined();
    expect(bad.errors.maxWeightKg).toMatch(/greater than zero/);
    expect(bad.errors.maxPallets).toMatch(/at least 1/);
    expect(bad.errors["shipments[0]"]).toMatch(/YYYY-MM-DD/);
    expect(bad.errors["shipments[1]"]).toMatch(/once/);
  });

  it("builds loads with one idempotency key and shows what was planned", async () => {
    handler = (url, init) =>
      url.pathname === "/api/v1/transport/loads:build" && init.method === "POST"
        ? json(201, { items: [load(), load({ id: LOAD2, shipmentIds: [SHIP2], totalWeightKg: "800" })] })
        : undefined;
    const { container } = renderAt("/transport/loads/build");
    fill("Shipment 1 shipment id", SHIP1);
    fill("Shipment 1 ship-from site id", SITE);
    fill("Shipment 1 ship-to region", "us-se");
    fill("Shipment 1 pickup date", "2026-06-01");
    fill("Shipment 1 weight kg", "1600");
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Build loads" }));

    expect(await screen.findByText("2 loads planned")).toBeInTheDocument();
    const [call] = posts("/loads:build");
    expect(header(call, "Idempotency-Key")).toBeTruthy();
    expect(call.body).toMatchObject({ capacity: { maxWeightKg: "20000", maxPallets: 26 }, shipments: [{ id: SHIP1, shipToRegion: "US-SE", weightKg: "1600" }] });
    fireEvent.click(within(screen.getByRole("table", { name: "Loads built" })).getAllByRole("row")[1]);
    expect(screen.getByTestId("location").textContent).toBe(`/transport/loads/${LOAD}`);
  });

  it("shows a shipment that is too large or already planned", async () => {
    handler = () => problem(409, "TMS-0116", `shipment ${SHIP1} is already on load ${LOAD2}`);
    renderAt("/transport/loads/build");
    fill("Shipment 1 shipment id", SHIP1);
    fill("Shipment 1 ship-from site id", SITE);
    fill("Shipment 1 ship-to region", "US-SE");
    fill("Shipment 1 pickup date", "2026-06-01");
    fill("Shipment 1 weight kg", "1600");
    fireEvent.click(screen.getByRole("button", { name: "Build loads" }));
    expect(await screen.findByText(/already on load/)).toBeInTheDocument();
    expect(screen.getByText(/TMS-0116/)).toBeInTheDocument();
  });
});

describe("load detail", () => {
  it("shows capacity, shipments and a failed DG check that blocks tendering", async () => {
    handler = (url) => (url.pathname === `/api/v1/transport/loads/${LOAD}` ? json(200, detail([failedCheck])) : undefined);
    const { container } = renderAt(`/transport/loads/${LOAD}`);
    expect(await screen.findByText("Tendering is blocked (TMS-0130)")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Start tender" })).toBeNull();
    expect(screen.getByText("19200 of 20000 kg")).toBeInTheDocument();
    expect(screen.getByText(SHIP2)).toBeInTheDocument();
    const papers = screen.getByRole("table", { name: "DG shipping papers" });
    expect(within(papers).getByText("Gasoline")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "DG findings" })).getByText("49CFR-177.848")).toBeInTheDocument();
    expect(violations(container)).toEqual([]);
  });

  it("offers a tender when there is no DG check", async () => {
    handler = () => json(200, detail());
    renderAt(`/transport/loads/${LOAD}`);
    expect(await screen.findByText("No DG check yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Start tender" })).toHaveAttribute("href", `/transport/tenders/new?loadId=${LOAD}`);
  });

  it("runs a DG check and reloads the load", async () => {
    let checked = false;
    handler = (url, init) => {
      if (url.pathname.endsWith(":dg-check") && init.method === "POST") {
        checked = true;
        return json(200, { ...failedCheck, result: "WARN", regulation: "ADR", adrPoints: "1200", findings: [] });
      }
      return json(200, detail(checked ? [{ ...failedCheck, result: "WARN", regulation: "ADR", adrPoints: "1200", findings: [{ ruleId: "ADR-1.1.3.6", severity: "WARN", code: "TMS-0131", detail: "ADR 1.1.3.6 points 1200 exceed 1,000" }] }] : []));
    };
    renderAt(`/transport/loads/${LOAD}`);
    await screen.findByText("No DG check yet");
    fill("24-hour emergency phone", "+1 800 555 1234");
    fill("Line 1 UN number", "un1993");
    fill("Line 1 proper shipping name", "Flammable liquid, n.o.s.");
    fill("Line 1 class", "3");
    fill("Line 1 quantity", "400");
    fill("Line 1 transport category", "2");
    fireEvent.click(screen.getByRole("button", { name: "Run DG check" }));

    expect(await screen.findByText("Latest DG check: Warning")).toBeInTheDocument();
    expect(screen.getByText(/ADR 1.1.3.6 points 1200\. Checked/)).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "DG findings" })).getByText("TMS-0131")).toBeInTheDocument();
    const [call] = posts(":dg-check");
    expect(call.body).toEqual({
      regulation: "ADR",
      emergencyResponsePhone: "+18005551234",
      lines: [{ unNumber: "UN1993", properShippingName: "Flammable liquid, n.o.s.", class: "3", packingGroup: "", quantity: "400", transportCategory: 2, limitedQuantity: false }],
    });
  });

  it("validates DG lines before sending", () => {
    const bad = validateDG({ regulation: "ADR", emergencyResponsePhone: "555", lines: [{ unNumber: "1203", properShippingName: "x", class: "3", packingGroup: "", quantity: "1", transportCategory: "3", limitedQuantity: "No" }] });
    expect(bad.input).toBeUndefined();
    expect(bad.errors.emergencyResponsePhone).toMatch(/E.164/);
    expect(bad.errors["lines[0]"]).toMatch(/UN followed by four digits/);
    expect(validateDG({ regulation: "49CFR", emergencyResponsePhone: "", lines: [{ unNumber: "UN0081", properShippingName: "x", class: "1.1d", packingGroup: "II", quantity: "5", transportCategory: "1", limitedQuantity: "Yes" }] }).input?.lines[0]).toMatchObject({
      class: "1.1D",
      limitedQuantity: true,
    });
  });

  it("screens parties and shows a hold", async () => {
    handler = (url, init) =>
      url.pathname.endsWith(":screen") && init.method === "POST"
        ? json(200, { loadId: LOAD, crossBorder: true, decision: "HOLD", parties: [{ role: "SHIP_TO", name: "Northwind", status: "POTENTIAL_MATCH", code: "TMS-0133" }] })
        : json(200, detail());
    renderAt(`/transport/loads/${LOAD}`);
    await screen.findByText("No DG check yet");
    fill("Party 1 name", "Northwind");
    fill("Party 1 country", "us");
    fireEvent.click(screen.getByRole("button", { name: "Screen parties" }));
    expect(await screen.findByText("Load held for trade compliance (TMS-0133)")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Screening results" })).getByText("Potential match")).toBeInTheDocument();
    expect(posts(":screen")[0].body).toEqual({ crossBorder: false, parties: [{ role: "SHIP_TO", name: "Northwind", country: "US" }] });
  });

  it("shows an error for an unknown load", async () => {
    handler = () => problem(404, "TMS-0100", "load not found");
    renderAt(`/transport/loads/${LOAD}`);
    expect(await screen.findByText(/load not found/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to loads" })).toBeInTheDocument();
  });
});

describe("freight bill decisions", () => {
  const bill = (extra: Partial<FreightBillDetail> = {}): FreightBillDetail => ({
    id: BILL,
    loadId: LOAD,
    carrierId: CARRIER,
    currency: "USD",
    expectedLines: [{ chargeCode: "BASE", amount: "412.50" }],
    expectedTotal: "412.50",
    matchStatus: "VARIANCE",
    carrierInvoiceId: "0195f0a0-0000-7000-8000-0000000000e9",
    varianceTotal: "5.50",
    version: 2,
    createdAt: "2026-06-01T08:00:00Z",
    decisions: [],
    ...extra,
  });

  it("disputes a variance with If-Match and shows the decision history after reload", async () => {
    let current = bill();
    handler = (url, init) => {
      if (url.pathname.endsWith(":dispute") && init.method === "POST") {
        current = bill({
          matchStatus: "DISPUTED",
          version: 3,
          decisions: [{ id: "d1", freightBillId: BILL, action: "DISPUTE", fromStatus: "VARIANCE", toStatus: "DISPUTED", reason: "fuel charged twice", decidedBy: "ap-1", decidedAt: "2026-06-02T08:00:00Z" }],
        });
        return json(200, current);
      }
      return url.pathname === `/api/v1/transport/freight-bills/${BILL}` ? json(200, current) : undefined;
    };
    const { container } = renderAt(`/transport/freight-bills/${BILL}`);
    await screen.findByRole("button", { name: "Dispute with carrier" });
    expect(violations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Dispute with carrier" }));
    expect(await screen.findByText("Tell the carrier what is disputed.")).toBeInTheDocument();
    expect(posts(":dispute")).toHaveLength(0);

    fill("Reason", "fuel charged twice");
    fireEvent.click(screen.getByRole("button", { name: "Dispute with carrier" }));
    const table = await screen.findByRole("table", { name: "Approvals and disputes" });
    expect(within(table).getByText("fuel charged twice")).toBeInTheDocument();
    const [call] = posts(":dispute");
    expect(header(call, "If-Match")).toBe('"2"');
    expect(header(call, "Idempotency-Key")).toBeTruthy();
    expect(call.body).toEqual({ reason: "fuel charged twice" });
    expect(screen.queryByRole("button", { name: "Dispute with carrier" })).toBeNull();
  });

  it("approves a matched bill without a reason and reports a stale version", async () => {
    handler = (url, init) =>
      url.pathname.endsWith(":approve") && init.method === "POST"
        ? problem(412, "conflict_version", "the freight bill changed since it was read")
        : json(200, bill({ matchStatus: "MATCHED", varianceTotal: "0" }));
    renderAt(`/transport/freight-bills/${BILL}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve for payment" }));
    expect(await screen.findByText("The bill changed since you opened it")).toBeInTheDocument();
    expect(posts(":approve")[0].body).toEqual({});
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });

  it("offers no decision on an unmatched or approved bill", async () => {
    handler = () => json(200, bill({ matchStatus: "APPROVED" }));
    renderAt(`/transport/freight-bills/${BILL}`);
    await screen.findByText("Freight bill 0195f0a0");
    expect(screen.queryByRole("button", { name: "Approve for payment" })).toBeNull();
  });
});
